"""HTTP client for the pinned-contract grading engine.

Everything here speaks ONLY HTTP, addressed by `operationId` as declared in
`tasks/public/contracts/openapi.contract.json` (the frozen, pinned contract) rather
than by hand-written URLs. This means a capability spec cannot silently drift
from the pinned contract, and grading never depends on how a candidate app's
own (possibly buggy) `app-setup/openapi.json` describes itself.

Adapted from the reference app's `app-setup/api_client.py` (same idea: an
operationId-keyed `Client` plus a `World` seeder), but repointed at the pinned
contract instead of a per-app-generated one, and extended with the
`World`/`Session` helpers this engine's capability specs need.

No SQL, no pymongo, no datastore driver of any kind is imported anywhere in
this package -- grep it if in doubt. The only thing this engine ever talks to
is the application's own HTTP API.
"""
from __future__ import annotations

import json
import os
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Callable, Optional

def _default_contract_path() -> Path:
    """Resolve the canonical public contract from the repository root.

    Falls back to the bare filename (resolved relative to the current working
    directory) when this file has been copied somewhere shallower than the
    repo it came from (e.g. into a container for an isolated test run) --
    callers running from an unusual location should just pass `--contract`
    explicitly.
    """
    here = Path(__file__).resolve()
    parents = here.parents
    if len(parents) > 3:
        candidate = parents[3] / "tasks" / "public" / "contracts" / "openapi.contract.json"
        if candidate.exists():
            return candidate
    return Path("tasks/public/contracts/openapi.contract.json")


CONTRACT_PATH = _default_contract_path()


class ContractLoadError(RuntimeError):
    """Raised by `load_contract()` when the PINNED contract itself -- this
    engine's OWN input, not a candidate's file -- cannot be read or parsed.

    Every capability probe's operationId -> (method, path) routing table
    (`_index_operations()`) is built from this file, so a malformed pinned
    contract silently breaks EVERY probe, not just one. This exists purely to
    turn a raw `OSError`/`json.JSONDecodeError` (whose message says nothing
    about *why* this particular file matters) into something that names the
    file, says what depends on it, and hints at the fix -- it does not
    change what gets validated.
    """


def load_contract(path: Path = CONTRACT_PATH) -> dict[str, Any]:
    try:
        raw = path.read_text()
    except OSError as exc:
        raise ContractLoadError(
            f"could not read the pinned contract at {path!s} ({exc.__class__.__name__}: {exc}). "
            "This is the engine's OWN input (normally "
            "tasks/public/contracts/openapi.contract.json), not a candidate's file -- every capability "
            "probe's operationId routing table depends on it. If this engine has been copied "
            "somewhere shallower than the repo it came from, pass --contract explicitly."
        ) from exc
    try:
        contract = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ContractLoadError(
            f"the pinned contract at {path!s} is not valid JSON "
            f"(line {exc.lineno}, column {exc.colno}: {exc.msg}). This is the engine's OWN input, "
            "not a candidate's file, and must never be malformed -- check for a bad hand-edit."
        ) from exc
    if not isinstance(contract, dict):
        raise ContractLoadError(
            f"the pinned contract at {path!s} parsed to a {type(contract).__name__}, not a JSON "
            "object -- every capability probe's operationId routing table expects a top-level "
            "object with a 'paths' key."
        )
    return contract


def _index_operations(contract: dict[str, Any]) -> dict[str, tuple[str, str]]:
    """operationId -> (method, path-template), taken from the pinned contract."""
    ops: dict[str, tuple[str, str]] = {}
    for path, item in contract["paths"].items():
        for method, operation in item.items():
            if method.lower() not in {"get", "post", "put", "patch", "delete"}:
                continue
            op_id = operation.get("operationId")
            if op_id:
                ops[op_id] = (method.lower(), path)
    return ops


# --------------------------------------------------- contract conformance
# Populated once by make_admin_session(); read by Session.raw() to check
# EVERY response it ever sees against the pinned schema for that
# operationId+status. Zero changes needed at any capability call site --
# conformance evidence comes for free from HTTP traffic every capability
# already generates, the same "reuse existing traffic" idea durability's
# restart cycle is built on. See run.py's `contract-report` command for how
# this gets read back out.
_RESPONSE_SCHEMAS: dict[str, dict[str, dict]] = {}
_SCHEMA_COMPONENTS: dict[str, dict] = {}
CONTRACT_REPORT: dict[str, dict] = {}  # operationId -> {"calls": int, "violations": [...]}


def _index_response_schemas(contract: dict[str, Any]) -> dict[str, dict[str, dict]]:
    """operationId -> {status_code_str: response_schema}, taken from the
    pinned contract's own `responses` blocks -- the schema for the FULL
    envelope actually returned (e.g. `{"data": Location}` for a 2xx, the
    flat `{"error","code"}` ErrorEnvelope for a 4xx/5xx), not an inner,
    unwrapped shape."""
    out: dict[str, dict[str, dict]] = {}
    for _path, item in contract["paths"].items():
        for method, operation in item.items():
            if method.lower() not in {"get", "post", "put", "patch", "delete"}:
                continue
            op_id = operation.get("operationId")
            if not op_id:
                continue
            per_status: dict[str, dict] = {}
            for status_str, resp in (operation.get("responses") or {}).items():
                schema = ((resp.get("content") or {}).get("application/json") or {}).get("schema")
                if schema:
                    per_status[status_str] = schema
            if per_status:
                out[op_id] = per_status
    return out


def _record_contract_check(operation_id: str, status: int, payload: Any) -> None:
    schema = _RESPONSE_SCHEMAS.get(operation_id, {}).get(str(status))
    # No pinned schema for this exact status (e.g. a transport-error 0, or a
    # status the contract simply never enumerated for this op) -- nothing to
    # check, and silently skipping is correct: this must never penalise a
    # status the contract itself doesn't describe.
    if schema is None or not isinstance(payload, (dict, list)):
        return
    from schema_validate import validate  # local import: keeps this optional for callers that never touch it
    errors = validate(payload, schema, _SCHEMA_COMPONENTS)
    entry = CONTRACT_REPORT.setdefault(operation_id, {"calls": 0, "violations": []})
    entry["calls"] += 1
    if errors:
        entry["violations"].append({"status": status, "errors": errors})


def rfc3339(value: datetime) -> str:
    return value.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def now_utc() -> datetime:
    return datetime.now(timezone.utc).replace(microsecond=0)


def parse_rfc3339(value: str) -> Optional[datetime]:
    """Inverse of `rfc3339()`. golden-mongo's `formatTime` uses Go's
    `time.RFC3339` with no fractional seconds (`service.go:formatTime`), so
    every timestamp this engine reads back from the app is exactly
    `%Y-%m-%dT%H:%M:%SZ`. Returns None rather than raising on anything else,
    since a timer-driven probe using this to size a `time.sleep()` must never
    crash the whole run over a parse hiccup."""
    if not value:
        return None
    try:
        return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
    except ValueError:
        try:
            return datetime.fromisoformat(value.replace("Z", "+00:00"))
        except ValueError:
            return None


def engine_wait_seconds(env_var: str, default: float) -> float:
    """A wall-clock wait parameter for a timer-driven capability probe (e.g.
    how long past a discovered expiry to sleep before checking the app's
    background maintenance pass ran). Defaults to golden-mongo's own
    out-of-the-box default, so a default-configured app is still graded
    correctly -- just slower. Override when the app under test was started
    with a faster test-only interval (the pattern this session's mutant
    RUNBOOKs establish: `MAINTENANCE_INTERVAL_MILLISECONDS`,
    `ASSIGNMENT_OFFER_TTL_SECONDS`) so the same probe runs fast in CI.
    """
    try:
        return max(0.1, float(os.environ.get(env_var, "") or default))
    except (TypeError, ValueError):
        return default


class ApiError(RuntimeError):
    """Raised by `Session.call` (the raise-on-failure convenience wrapper).

    Capability code that WANTS to see failures (negative probes, RBAC checks)
    should use `Session.raw` instead, which never raises.
    """

    def __init__(self, operation_id: str, status: int, payload: Any):
        self.operation_id = operation_id
        self.status = status
        self.payload = payload
        code = payload.get("code") if isinstance(payload, dict) else None
        super().__init__(f"{operation_id} -> HTTP {status} ({code}): {payload}")


@dataclass
class Session:
    """One authenticated (or anonymous) session against the application under test."""

    base_url: str
    ops: dict[str, tuple[str, str]]
    token: str = ""
    user_id: str = ""
    roles: list[str] = field(default_factory=list)
    active_role: str = ""
    username: str = ""

    # ---------------------------------------------------------------- plumbing
    def raw(
        self,
        operation_id: str,
        body: dict[str, Any] | None = None,
        path_param: str | None = None,
        query: str = "",
        timeout: float = 20.0,
    ) -> tuple[int, Any]:
        """Perform one call, addressed by pinned operationId. Never raises on 4xx/5xx."""
        if operation_id not in self.ops:
            raise KeyError(f"{operation_id!r} is not an operationId in the pinned contract")
        method, path_template = self.ops[operation_id]
        path = path_template
        if path_param is not None:
            # Replace the single {xxxId} placeholder with the supplied value.
            prefix = path_template.split("{")[0]
            path = prefix + str(path_param)
        url = self.base_url.rstrip("/") + path + query
        data = None
        headers: dict[str, str] = {}
        if method in ("post", "put", "patch"):
            data = json.dumps(body if body is not None else {}).encode()
            headers["Content-Type"] = "application/json"
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        request = urllib.request.Request(url, data=data, headers=headers, method=method.upper())
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                status, raw_body = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, raw_body = error.code, error.read()
        except Exception as exc:  # network-level failure -- never crash the engine
            return 0, {"error": str(exc), "code": "transport_error"}
        try:
            payload = json.loads(raw_body) if raw_body else None
        except json.JSONDecodeError:
            payload = {"raw": raw_body.decode(errors="replace")}
        if status:  # 0 is this method's own transport-error sentinel, never a real HTTP status
            _record_contract_check(operation_id, status, payload)
        return status, payload

    def call(
        self,
        operation_id: str,
        body: dict[str, Any] | None = None,
        path_param: str | None = None,
        query: str = "",
    ) -> Any:
        """Perform a call expected to SUCCEED; return `data`, raising ApiError otherwise."""
        status, payload = self.raw(operation_id, body, path_param, query)
        if status >= 400 or not isinstance(payload, dict) or "data" not in payload:
            raise ApiError(operation_id, status, payload)
        return payload["data"]

    def data(self, status: int, payload: Any) -> dict[str, Any]:
        if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
            return payload["data"]
        return {}

    def code(self, payload: Any) -> str:
        if isinstance(payload, dict):
            return str(payload.get("code", ""))
        return ""

    def new_session(self, token: str = "") -> "Session":
        return Session(base_url=self.base_url, ops=self.ops, token=token)

    def login(self, username: str, password: str, auto_select: bool = True) -> "Session":
        data = self.call("authenticate_user", {"username": username, "password": password})
        session = self.new_session(data.get("sessionToken", ""))
        session.user_id = data.get("userId", "")
        session.roles = data.get("roles", [])
        session.active_role = data.get("activeRole", "")
        session.username = username
        if auto_select and not session.active_role and session.roles:
            session.select_role(session.roles[0])
        return session

    def select_role(self, role: str) -> tuple[int, Any]:
        status, payload = self.raw("select_active_role", {"role": role})
        if status == 200:
            self.active_role = role
        return status, payload


def make_admin_session(base_url: str, contract: dict[str, Any], username: str, password: str) -> Session:
    ops = _index_operations(contract)
    global _RESPONSE_SCHEMAS, _SCHEMA_COMPONENTS
    _RESPONSE_SCHEMAS = _index_response_schemas(contract)
    _SCHEMA_COMPONENTS = contract.get("components", {}).get("schemas", {})
    CONTRACT_REPORT.clear()  # fresh per process invocation, never carried over from a prior load_contract call
    anon = Session(base_url=base_url, ops=ops)
    return anon.login(username, password)


PASSWORD = "Engine-Pass-w0rd1!"


class World:
    """Shared network/people/timing fixture-builder for capability specs.

    Every method is a thin, contract-exact wrapper over one pinned write
    operation -- no capability spec should ever hand-build these bodies
    itself, so a body-shape mistake is fixed in exactly one place.
    """

    def __init__(self, admin: Session, prefix: str):
        self.admin = admin
        self.prefix = prefix
        now = now_utc()
        self.now = now
        # Keep the collection window strictly LATER than the request
        # instant. The pinned contract is SILENT on whether the window must
        # start strictly after `requestedAt`; golden accepts window_start ==
        # requested_at, but a reasonable candidate that requires the window to
        # open strictly AFTER the request would 400 (invalid_collection_window)
        # on every request, cascading to ~12 capabilities that were never
        # actually measured. Offsetting requested_at further into the past
        # (10 min) while window_start stays 5 min ago makes the fixture
        # accepted by BOTH lenient and strict-after implementations. The
        # window still contains `now` (so "collect now" is valid); window_end
        # stays now+4h and deadline stays now+10h.
        self.requested_at = now - timedelta(minutes=10)
        self.window_start = now - timedelta(minutes=5)
        self.window_end = now + timedelta(hours=4)
        self.deadline = now + timedelta(hours=10)
        self.users: dict[str, dict[str, Any]] = {}
        self.sessions: dict[str, Session] = {}
        self._seq = 0

    def at(self, **offset: float) -> str:
        return rfc3339(self.now + timedelta(**offset))

    def moment(self) -> str:
        """The REAL current wall-clock time, not `self.now` (frozen at World
        construction). Use this for event timestamps that must be causally
        AFTER a just-completed server-side action (collectedAt, confirmedAt,
        occurredAt, ...) -- a capability's setup phase (creating a dozen
        fixtures, scanning thousands of audit entries to park workers, ...)
        can burn several real seconds, and golden-mongo rejects an event
        timestamped before the action it follows (e.g. `invalid_collection_time`
        when `collectedAt` precedes the assignment's own `acceptedAt`).
        `self.at()` stays reserved for the REQUEST WINDOW itself
        (requestedAt/deliveryDeadline/earliestCollectionAt/latestCollectionAt),
        which must stay internally consistent relative to `self.now`, not to
        real time.

        A small forward buffer (2s) is added: `now_utc()` truncates to whole
        seconds, and this engine's own steps run back-to-back sub-second, so
        a truncated "now" can floor to just BEFORE a server-recorded
        timestamp (e.g. `assignment.AcceptedAt`) that has sub-second
        precision -- which golden-mongo correctly treats as "collected before
        accepted" and refuses. The buffer only ever pushes OUR events later
        in real wall-clock time, it never changes what the app is asked to
        validate.
        """
        return rfc3339(now_utc() + timedelta(seconds=2))

    def key(self, tag: str) -> str:
        self._seq += 1
        return f"{self.prefix}-{tag}-{self._seq}"

    # ------------------------------------------------------------- builders
    def sample_type(self, name: str, exact_name: bool = False, **overrides: Any) -> dict[str, Any]:
        """`exact_name=True` for the four canonical profiles (Amber/Indigo/
        Frost/Pulse): golden-mongo's `enforceNamedSamplePolicy` matches on
        `strings.ToLower(strings.TrimSpace(name))` -- an EXACT match, not a
        substring -- so a prefixed name like "e0016-cst Amber" silently gets
        NO policy applied at all. Every other sample type should stay
        prefixed so repeated capability runs against the same live app don't
        collide."""
        body = {
            "name": name if exact_name else f"{self.prefix} {name}",
            "containerType": "sealed-tube",
            "temperatureMinC": 2,
            "temperatureMaxC": 8,
            "qualifiedCollectorRequired": False,
            "incompatibleSampleTypes": [],
            "batchingAllowed": True,
        }
        body.update(overrides)
        if not exact_name:
            return self.admin.call("create_sample_type", body)
        # For the four canonical names (Amber/Indigo/Frost/Pulse) the
        # named types are pre-seeded at reset. golden-mongo does NOT dedupe by
        # name, so a second create returns 201 with a fresh id -- but nothing
        # in instruction.md requires that, and a candidate that rejects a
        # duplicate canonical name with a clean 4xx (e.g. 409
        # sample_type_exists) is equally valid. Rather than RAISE on that
        # conflict (which would fail setup and zero the capability before the
        # candidate's real behavior was ever measured), fall back to
        # DISCOVERING the pre-seeded canonical type via the audit trail and
        # return it. Against golden (dupes allowed -> 201) behavior is
        # unchanged; against a deduping candidate setup proceeds against the
        # seeded type.
        status, payload = self.admin.raw("create_sample_type", body)
        if status < 400 and isinstance(payload, dict) and isinstance(payload.get("data"), dict):
            return payload["data"]
        if status in (400, 409, 422):
            discovered = self._discover_seeded_named_type(name)
            if discovered is not None:
                return discovered
            # Last resort: a candidate that DEDUPES the canonical name (4xx)
            # yet records NO discoverable audit provenance for its boot-seeded
            # type leaves the pre-seeded id genuinely unreachable through any
            # PINNED, engine-visible read path (there is no read-sample-type
            # endpoint, and the audit trail is empty of the seed). Create a
            # substitute under a unique name so calls that only need SOME
            # valid, working sample-type handle to continue exercising
            # OTHER, unrelated behavior (e.g. frost_cooled_route_enforcement,
            # or update_sample_type's non-overridable-policy check) are not
            # crashed by this -- raising unconditionally here was tried and
            # reverted after it broke 3 previously-passing capabilities that
            # never asked about this substitute's policy fields at all.
            # BUT: the substitute's hardcoded defaults (qualifiedCollectorRequired
            # etc.) are NOT the real canonical type's actual policy -- reading
            # them back as if they were is what caused the false negative on
            # `qualified_collector_exhaustion_exception` (confirmed live
            # against opus-max: its real Indigo/Pulse DO have the correct
            # policy, invisible only because dedupe+no-audit-provenance blocks
            # both discovery paths). Tag the substitute so a caller that
            # SPECIFICALLY needs the real canonical policy can detect it and
            # report "unmeasurable" instead of grading the substitute's
            # unrelated defaults as if they were the real answer.
            fallback = dict(body)
            fallback["name"] = f"{self.prefix} {name}-dup-{self.key('stfb')}"
            result = self.admin.call("create_sample_type", fallback)
            result["_unverifiable_canonical_policy"] = True
            return result
        raise ApiError("create_sample_type", status, payload)

    def _discover_seeded_named_type(self, name: str) -> dict[str, Any] | None:
        """Discover a reset-seeded canonical sample type by name via the
        audit trail (mirrors sample_types.py::_seeded_named_types). Each
        `sample_type_created` audit entry embeds the FULL created record in
        `details`, so this is a genuine read, not a reconstruction. Used only
        as the fallback when a deduping candidate refuses a second
        create of an already-seeded canonical name.

        Was a SEPARATE, un-synced copy of the same bug fixed in
        sample_types.py's `_seeded_named_types` (commit f4e694a): filtered on
        `entityType=="sample_type"` and `action=="sample_type_created"`, both
        golden-mongo's own private naming choices, pinned nowhere in the
        contract. Fixing only the sibling copy and missing this one meant
        every caller that goes through `World.sample_type()`'s fallback --
        `qualified_collector_exhaustion_exception`, and `search_audit_trail`'s
        Pulse sub-probe -- never actually benefited from that fix. This alone
        does not rescue a candidate whose audit trail is genuinely silent
        about the seed (confirmed live: opus-max/luna-xhigh/sol-xhigh all have
        nothing before their own login) -- that case still correctly falls
        through to the tagged substitute below -- but it does mean a
        candidate that logs the event under a DIFFERENT name is no longer
        invisible to this path either.
        """
        target = name.strip().lower()
        status, payload = self.admin.raw(
            "search_audit_trail", {"order": "oldest_first", "limit": 50}
        )
        if status != 200 or not isinstance(payload, dict):
            return None
        entries = payload.get("data", {}).get("entries", [])
        for entry in entries:
            details = entry.get("details") or {}
            if str(details.get("name", "")).strip().lower() == target:
                return details
        return None

    def location(self, name: str, functions: list[str], lat: float, lon: float, **overrides: Any) -> dict[str, Any]:
        body = {
            "name": f"{self.prefix} {name}",
            "active": True,
            "coordinate": {"latitude": lat, "longitude": lon},
            "handlingMinutes": 0,
            "functions": functions,
        }
        body.update(overrides)
        return self.admin.call("create_location", body)

    def connection(self, from_id: str, to_id: str, **overrides: Any) -> dict[str, Any]:
        body = {
            "fromLocationId": from_id,
            "toLocationId": to_id,
            "travelMinutes": 15,
            "costUnits": 10,
            "cooled": False,
            "active": True,
        }
        body.update(overrides)
        return self.admin.call("create_connection", body)

    def lab_accepts(self, laboratory_id: str, sample_type_id: str, accepts: bool = True) -> dict[str, Any]:
        return self.admin.call(
            "configure_laboratory_acceptance",
            {"laboratoryId": laboratory_id, "sampleTypeId": sample_type_id, "accepts": accepts},
        )

    def capacity(self, resource_id: str, units: int = 400, cooled_units: int = 200) -> dict[str, Any]:
        return self.admin.call(
            "configure_capacity",
            {"resourceId": resource_id, "capacityUnits": units, "cooledCapacityUnits": cooled_units},
        )

    def user(
        self,
        alias: str,
        roles: list[str],
        worker: bool = False,
        capacity_units: int = 200,
        qualifications: list[str] | None = None,
        location_id: str | None = None,
    ) -> dict[str, Any]:
        username = f"{self.prefix}-{alias}-{self.key('u')}"
        body = {
            "username": username,
            "initialPassword": PASSWORD,
            "email": f"{username}@engine.test",
            "fullName": f"{self.prefix} {alias}",
            "phone": "555-0100",
            "roles": roles,
            "qualifications": qualifications or [],
            "active": True,
        }
        if location_id is not None:
            body["assignedLocationId"] = location_id
        created = self.admin.call("create_user", body)
        created["username"] = username
        self.users[alias] = created
        if worker:
            self.admin.call(
                "set_worker_availability",
                {"workerId": created["userId"], "available": True, "capacityUnits": capacity_units},
            )
        return created

    def session(self, alias: str) -> Session:
        if alias not in self.sessions:
            anon = self.admin.new_session("")
            self.sessions[alias] = anon.login(self.users[alias]["username"], PASSWORD)
        return self.sessions[alias]

    def user_id(self, alias: str) -> str:
        return self.users[alias]["userId"]

    def basic_network(self, cooled: bool = False) -> "BasicNetwork":
        """One collection point + one laboratory + one direct connection + a
        sample type that's accepted there, all with ample capacity. The
        smallest serviceable world a capability can plan a journey against.
        """
        st = self.sample_type("ST", requiresCooledRoute=cooled)
        pickup = self.location(
            "pickup", ["collection_point", "transfer_point"], 12.9500, 77.5700, coverageKm=5
        )
        lab = self.location("lab", ["hub", "laboratory"], 12.9300, 77.6000, handlingMinutes=5)
        conn = self.connection(pickup["locationId"], lab["locationId"], travelMinutes=30, costUnits=12.5, cooled=True)
        self.lab_accepts(lab["locationId"], st["sampleTypeId"])
        for resource in (pickup["locationId"], lab["locationId"], conn["connectionId"]):
            self.capacity(resource, units=50, cooled_units=50)
        return BasicNetwork(sample_type=st, pickup=pickup, laboratory=lab, connection=conn)

    def default_requester_id(self) -> str:
        """Lazily create (and cache) a `requester`-role user. create_collection_request's
        `requesterId` is optional in the pinned schema but golden-mongo 404s
        ("requester not found") if it is omitted and the caller isn't
        already a requester -- so every request body needs a real one."""
        if "requester" not in self.users:
            self.user("requester", ["requester"])
        return self.user_id("requester")

    def requester_session(self) -> Session:
        """The seeded `requester`-role user's own session -- the caller
        create_collection_request/ingest_presealed_sample should actually be
        exercised through. instruction.md lists "create a home or clinic
        collection request" ONLY under the Requester role's own abilities,
        never under Administrator's or Dispatcher's. golden-mongo happens to
        additionally permit an administrator caller
        (`require(p, "requester", "administrator")` in planning.go) -- its own
        choice, not something instruction.md asks for -- but candidates that
        scope this endpoint strictly to `requester` (the literal reading) are
        equally correct. Calling as admin (the old default) 403s on those
        candidates and cascades into every capability that plans a journey
        through this world, a false failure with nothing to do with the
        actual behaviour under test. Every implementation must accept its own
        primary, spec-mandated caller, so this is a strict widening, never a
        loss of coverage against a candidate that also tolerates admin."""
        self.default_requester_id()
        return self.session("requester")

    def request_body(self, network: "BasicNetwork", key: str, **overrides: Any) -> dict[str, Any]:
        body = {
            "requesterId": self.default_requester_id(),
            "origin": {"latitude": network.pickup["coordinate"]["latitude"], "longitude": network.pickup["coordinate"]["longitude"]},
            "originType": "home",
            "requestedAt": rfc3339(self.requested_at),
            "deliveryDeadline": rfc3339(self.deadline),
            "earliestCollectionAt": rfc3339(self.window_start),
            "latestCollectionAt": rfc3339(self.window_end),
            "sampleTypeId": network.sample_type["sampleTypeId"],
            "containerType": "sealed-tube",
            "temperatureMinC": 2,
            "temperatureMaxC": 8,
            "incompatibleSampleTypes": [],
            "idempotencyKey": key,
        }
        body.update(overrides)
        return body

    def plan_journey(self, body: dict[str, Any], *, tries: int = 6, delay: float = 0.5, session: "Session | None" = None) -> dict[str, Any]:
        """Plan a request that SHOULD be serviceable, tolerating a candidate's
        intermittent planner race.

        Some candidates return `unserviceable` / no journey from
        create_collection_request even when their own check_serviceability
        reports serviceable (an app-side recompute race). That is a real defect
        -- and it is still penalised in create_collection_request's OWN
        capability, which does NOT use this retry. But it must not be AMPLIFIED:
        a single racy plan otherwise cascades to ~23 downstream lifecycle
        capabilities that never get a journey to exercise.

        So: await serviceability, create, and if no real journey formed, retry
        with a FRESH idempotencyKey (a forced re-plan, not an idempotent replay)
        up to `tries`. Return as soon as a genuine journey forms; otherwise
        return the last attempt so the caller still sees -- and scores -- a
        genuine inability to plan. This removes FLAKINESS, never real inability:
        a hollow/broken app that can never plan still gets no journey here.
        """
        sess = session or self.requester_session()
        attempt = dict(body)
        created: dict[str, Any] = {}
        for i in range(max(1, tries)):
            # Wait up to ~15s for the published serviceability to settle: some
            # candidates recompute slowly (measured up to ~9s), so a short wait
            # races their recompute and reads a transient no_route/unserviceable.
            self.await_serviceable(attempt, tries=75)
            created = sess.call("create_collection_request", attempt)
            if (created.get("serviceability") == "serviceable"
                    and created.get("journeyId")
                    and (created.get("activeAssignmentIds") or [])):
                return created
            attempt = dict(body, idempotencyKey=self.key("replan"))
            time.sleep(delay)
        return created

    def await_serviceable(self, body: dict[str, Any], *, tries: int = 30, delay: float = 0.2) -> bool:
        """Wait until a just-configured network is actually reflected in the
        published serviceability calculation before planning against it.

        The instruction (AC-40) allows serviceability to be published as one
        COMPLETE version at a time, serving the previous complete version while
        a new one is computed. A collection node added a moment ago is therefore
        legitimately not-yet-serviceable for a brief window. If a seed plans a
        request in that window, a genuinely serviceable request reads
        `unserviceable`, no journey forms, and every downstream lifecycle
        capability cascades to 0 -- a verifier-side race that specifically
        penalises apps implementing the async/versioned model most faithfully.

        Poll check_serviceability with the SAME request shape (idempotencyKey
        stripped -- a dry run creates nothing) until it reports serviceable.
        Returns True once serviceable; False on timeout, in which case the
        caller proceeds anyway (so a genuinely-unserviceable case is still
        exercised, just after giving the recompute a fair chance)."""
        probe = {k: v for k, v in body.items() if k != "idempotencyKey"}
        for _ in range(max(1, tries)):
            status, payload = self.admin.raw("check_serviceability", probe)
            data = self.admin.data(status, payload) if status == 200 else {}
            if status == 200 and (data.get("serviceable") is True or data.get("reason") == "serviceable"):
                return True
            time.sleep(delay)
        return False


@dataclass
class BasicNetwork:
    sample_type: dict[str, Any]
    pickup: dict[str, Any]
    laboratory: dict[str, Any]
    connection: dict[str, Any]
