"""The grading engine: executes a capability spec against a running app and
returns a credit in [0.0, 1.0] plus evidence.

Datastore-agnostic by construction: the only primitive this module has for
"did the write take effect" is `observe()`, a ranked HTTP read-back fallback
(direct read -> audit-trail search -> operations-state read) adapted from the
proven `durability_grade.py` / `durability_probe.py` prototype. It never
queries a database.

Capability spec shape
----------------------
A capability module exposes a module-level `CAPABILITIES` list of `Capability`
instances (see below). Each `Capability.run(engine)` is a plain Python
function -- not a declarative list of dict steps -- because several
requirements in this system are inherently sequential/conditional
(discover which worker an offer landed on, discover the plan version before
editing it, branch on whether a request came back serviceable) and a static
list of `$ref`-capture steps cannot express "ask the app, then decide" without
turning into an ad hoc interpreter anyway. Writing `run` as code is the
same amount of logic with far less indirection, and it is still exactly the
signature the prompt's spec sketch implies (setup -> operation -> observe ->
assert -> probes) -- `run` simply performs those five phases in order and
returns a `Verdict`. This is documented as a deliberate, justified deviation
in engine/README.md.
"""
from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from typing import Any, Callable, Optional

from client import Session, World

# --------------------------------------------------------------------- verdict


@dataclass
class Verdict:
    """The result of exercising ONE probe dimension (positive / negative /
    idempotency / durability) within a capability."""

    ok: bool
    detail: str = ""
    evidence: dict[str, Any] = field(default_factory=dict)


@dataclass
class Capability:
    id: str  # must equal the pinned contract's operationId, exactly
    kind: str  # "write" | "read" | "lifecycle"
    description: str
    run: Callable[["Engine"], "CapabilityReport"]
    priority: bool = False  # True for the session's highest-value/highest-risk domains
    # Optional durability probe, split across two process lifetimes because a
    # real durability check spans an application restart:
    #   durability_write(engine)  -> JSON-serialisable sentinel dict, called
    #       BEFORE the restart (usually piggybacks on the same setup the
    #       positive probe already built).
    #   durability_verify(engine, sentinel) -> Verdict, called AFTER the
    #       restart, using ONLY read-back HTTP calls (engine.observe()).
    # See run.py's `durability-write` / `durability-verify` subcommands and
    # engine/README.md ("Durability probes") for how these plug in.
    durability_write: Optional[Callable[["Engine"], dict[str, Any]]] = None
    durability_verify: Optional[Callable[["Engine", dict[str, Any]], Verdict]] = None
    # ISOLATION GROUPING. Capabilities sharing a group MUST run against the same
    # app instance because they deliberately share setup: the lifecycle
    # step-capabilities each grade a DIFFERENT step of ONE cached
    # `_drive_happy_path` journey (`engine.shared["lc_happy"]`), so a reset
    # between them would destroy the journey they are grading. Everything else
    # is independent and forms a group of one (see `effective_group`).
    #
    # Declared here rather than inferred, deliberately: this started as a
    # hand-written list in the run wrapper and rotted immediately -- the first
    # version missed BOTH `edit_active_plan` (calls the drive directly) and
    # `confirm_laboratory_receipt` (a multi-line `_make_step_capability` call a
    # single-line grep didn't catch), silently leaving them on shared state.
    # Keeping it on the capability puts it where an author adding a
    # step-capability will actually see it.
    group: str = ""

    def effective_group(self) -> str:
        """The isolation unit this capability belongs to: its declared group,
        or itself when independent."""
        return self.group or self.id


def credit_from_oks(oks: list[bool]) -> float:
    """Equal-weight credit across whichever dimensions are present. Pulled out
    of `CapabilityReport.credit()` so run.py's isolated-mode merge step can
    fold in a durability Verdict obtained from a SEPARATE process (durability
    spans an application restart, so it cannot be known within the same
    `cap.run(engine)` call `credit()` is normally computed from) using this
    exact formula, rather than a second, drift-prone copy of it."""
    if not oks:
        return 0.0
    return round(sum(1 for ok in oks if ok) / len(oks), 4)


@dataclass
class CapabilityReport:
    positive: Verdict
    negative: Optional[Verdict] = None
    idempotency: Optional[Verdict] = None
    durability: Optional[Verdict] = None
    notes: str = ""

    def credit(self) -> float:
        dims = [self.positive]
        for d in (self.negative, self.idempotency, self.durability):
            if d is not None:
                dims.append(d)
        return credit_from_oks([d.ok for d in dims])


# --------------------------------------------------------------------- engine


class Engine:
    """Carries the admin session + a fresh World per capability, and exposes
    the ranked observe() fallback every write-capability grades through."""

    def __init__(self, admin: Session, prefix_seq: Callable[[], str]):
        self.admin = admin
        self._prefix_seq = prefix_seq
        self.shared: dict[str, Any] = {}  # cross-capability cache (see lifecycle driver)

    def new_world(self, tag: str) -> World:
        return World(self.admin, self._prefix_seq() + "-" + tag)

    FIELD_ROLES = {"collection_professional", "delivery_partner", "node_handler", "laboratory_team"}

    def park_field_workers(self, exclude: set[str] | None = None) -> int:
        """Make every already-existing field worker unavailable, EXCEPT the
        ones in `exclude` (typically the worker ids this capability just
        seeded). Offers are allocated by the app's OWN fairness rules, which
        favour the longest-waiting eligible worker -- so on a live app that
        already carries workers from earlier capabilities in this same run,
        a freshly seeded worker can never win an offer unless the incumbents
        are parked first. Mirrors golden-mongo's own verification helper,
        `World.park_existing_field_workers` in app-setup/api_client.py.

        Parking serves TWO purposes, and app isolation only removes one of them:
          1. clear leftover workers from EARLIER capabilities -- a fresh app
             reset handles this, no parking needed;
          2. force a precondition among a capability's OWN workers -- still
             required even on a pristine app. `report_disruption` seeds
             standby + collector/collector2/courier/courier2/lab_op and parks
             all but `standby` so standby is the only eligible delivery_partner
             and MUST win the transport leg. Its docstring records that without
             this the AC-61 check is VACUOUS: mutation testing
             (`anchors/mutants/replan-copies-verbatim`) scored the mutant 1.0000
             identically to golden.
        An earlier version of this method short-circuited to 0 under
        `VERIFIER_ISOLATED=1` on the incorrect assumption that only (1) mattered.
        The golden gate caught it immediately -- report_disruption 1.0 -> 0,
        golden 1.0000 -> 0.9808 -- i.e. that shortcut silently re-opened a
        deliberately-closed mutant-detection gap. Parking therefore ALWAYS runs.

        DISCOVERY IS CONVENTION-FREE. The lookup used to filter
        `action="user_created"`, which is golden's audit-naming choice and is
        pinned NOWHERE in the contract (`action` is just `{"type":"string"}`,
        with no enum). Candidates naming audit actions after the operationId
        (`create_user` -- opus-max) matched nothing, so this parked ZERO workers
        on 8 of 8 candidates measured, while golden alone was parked correctly:
        a systematic bias in golden's favour. Now it pulls the trail unfiltered
        and selects on `entityType == "user"`, which IS contract-pinned, and
        pages with a contract-legal limit (sol-xhigh 409s and opus-max 400s on
        limit=5000 even though the contract permits it).
        """
        exclude = exclude or set()
        status, payload = self.admin.raw("search_audit_trail", {"limit": 500, "order": "oldest_first"})
        # `entries` may be absent OR present-but-null: a candidate that
        # serialises an empty result as JSON `null` (a Go nil slice, not `[]`)
        # yields None here, and iterating None would crash EVERY capability that
        # parks before planning. Coalesce to [].
        entries = (self.admin.data(status, payload).get("entries") or []) if status == 200 else []

        # Candidate worker ids: any audit entry about a `user` entity. entityType
        # IS contract-pinned; the action NAME is not, so we never filter on it.
        candidates: list[str] = []
        details_roles: dict[str, list] = {}
        for entry in entries:
            if entry.get("entityType") != "user":
                continue
            uid = entry.get("entityId")
            if not uid or uid in exclude:
                continue
            if uid not in details_roles:
                candidates.append(uid)
            # `details.roles` is a convenience when present, NOT a requirement:
            # nothing pins what an audit entry's `details` must contain, so a
            # missing/differently-shaped details block must not make a worker
            # invisible. Falls back to `read_user` below, which IS pinned.
            roles = (entry.get("details") or {}).get("roles")
            if roles and not details_roles.get(uid):
                details_roles[uid] = list(roles)

        parked = 0
        for uid in candidates:
            roles = details_roles.get(uid)
            if roles is None:
                st, pl = self.admin.raw("read_user", path_param=uid)
                roles = (self.admin.data(st, pl).get("roles") or []) if st == 200 else []
            if not self.FIELD_ROLES.intersection(roles):
                continue
            st, _ = self.admin.raw("set_worker_availability", {"workerId": uid, "available": False, "capacityUnits": 400})
            parked += int(st == 200)
        return parked

    # -------------------------------------------------------- ranked observe
    def observe(
        self,
        session: Session,
        sentinel: str,
        direct: tuple[str, str] | None = None,  # (operation_id, path_param)
        entity_type: str | None = None,
    ) -> Optional[str]:
        """Return the name of the first read surface that reflects `sentinel`
        (direct-read -> audit-trail -> operations-state), else None. Never
        touches a database -- only the app's own read endpoints."""
        if direct:
            op_id, path_param = direct
            status, payload = session.raw(op_id, path_param=path_param)
            # Guard against the trivial "id-echo" false positive (VB2.6): a
            # GET-by-id endpoint that always echoes the requested identifier
            # back in its response body -- real OR fabricated -- will ALWAYS
            # satisfy a naive substring search when the sentinel IS that same
            # identifier. That is not independent evidence of persistence:
            # ANY stateless handler that never looks anything up produces the
            # identical behaviour (see anchors/hollow/server.py's
            # h_read_user/h_read_sample_state, which echo `path_param` back
            # verbatim for ANY id, real or garbage, with a 200). Only trust
            # the direct-read tier when the sentinel is something OTHER than
            # the bare path parameter we just supplied -- a value the read
            # handler could only produce by having actually stored it.
            if status == 200 and _contains(payload, sentinel) and sentinel != path_param:
                return "direct-read"
        filters = [{"entityType": entity_type}] if entity_type else []
        filters += [{}, {"limit": 200, "order": "oldest_first"}]
        for body in filters:
            status, payload = session.raw("search_audit_trail", body)
            if status in (200, 201) and _successful_hit(session.data(status, payload), sentinel):
                return "audit-trail"
        status, payload = session.raw("read_operations_state", {"filters": {}})
        if status == 200 and _contains(payload, sentinel):
            return "ops-state"
        return None

    def audit_entries(self, session: Session, **filters: Any) -> list[dict[str, Any]]:
        status, payload = session.raw("search_audit_trail", filters)
        if status == 200 and isinstance(payload, dict):
            return payload.get("data", {}).get("entries") or []
        return []


def _contains(payload: Any, needle: str) -> bool:
    return needle in json.dumps(payload, default=str)


def _successful_hit(data: dict[str, Any], sentinel: str) -> bool:
    """True iff at least one entry in a search_audit_trail response both
    mentions `sentinel` AND has `success: true`. `success` is a REQUIRED
    field on every AuditEntry (pinned in the contract), so this is never a
    candidate-specific assumption.

    Without this, a REJECTED action naturally echoes back the id/value it
    failed to act on -- e.g. `read_sample_state` on an id that no longer
    exists gets logged as `action_rejected` with that same `entityId` -- and
    a bare substring search over the whole audit-trail response then reads
    that rejection as proof the entity EXISTS, exactly backwards. Found by
    deliberately wiping a durability sentinel's datastore before verifying:
    the direct-read tier correctly 404'd, but that very 404 got audit-logged
    and the audit-trail tier then "found" its own failure as evidence of
    survival. This is the audit-trail analogue of the id-echo guard already
    applied to the direct-read tier just above -- same failure shape, missing
    guard, in the OTHER fallback tier."""
    entries = data.get("entries")
    if not isinstance(entries, list):
        return False
    for entry in entries:
        if not isinstance(entry, dict) or entry.get("success") is False:
            continue
        if sentinel in json.dumps(entry, default=str):
            return True
    return False


# --------------------------------------------------------------------- helpers
# used across capability modules to keep the five-phase shape consistent.


def retry_durability_write(fn: Callable[["Engine"], dict[str, Any]], engine: "Engine", tries: int = 3) -> dict[str, Any]:
    """Retry a WHOLE durability_write scenario, fresh world each attempt,
    rather than trying to pinpoint which exact intermediate HTTP call in a
    multi-step scenario (accept -> collect -> handoff -> deliver -> ...) hit
    a transient timing race. Mirrors `World.plan_journey`'s own existing
    tolerance for "a candidate's intermittent planner race" -- that helper
    retries the WHOLE create call rather than diagnosing the app's internal
    race either, for the identical reason: cheaper and just as sound as
    tracing the exact failing step, and every multi-step durability_write in
    this suite already builds on helpers (`_seed_lifecycle_world`,
    `_plan_serviceable`) that don't themselves retry an accept/collect/
    handoff call if IT individually hits a similar one-off timing hiccup.
    Measured on a live app: a bare single attempt returning empty (no
    sentinel) 1-2 times out of 5 consecutive runs; this makes that same
    scenario succeed reliably without inventing a new design."""
    result: dict[str, Any] = {}
    for _ in range(max(1, tries)):
        result = fn(engine) or {}
        if result:
            return result
    return result


def positive_from_status(status: int, ok_statuses: tuple[int, ...], detail: str, evidence: dict[str, Any]) -> Verdict:
    return Verdict(ok=status in ok_statuses, detail=detail, evidence=evidence)


def negative_probe(label: str, status: int, expected_statuses: tuple[int, ...], code: str, expected_codes: tuple[str, ...]) -> Verdict:
    ok = status in expected_statuses and (not expected_codes or code in expected_codes)
    return Verdict(
        ok=ok,
        detail=f"{label}: got status={status} code={code!r}, expected status in {expected_statuses} code in {expected_codes or '*'}",
    )


# --------------------------------------------------- planned-journey guard
# Shared by every capability whose probe can only run once a request has
# actually been PLANNED (a serviceable journey with at least one assignment).
# A candidate whose planner fails for a network golden plans fine -- the
# response comes back serviceability="unserviceable" with no journeyId and 0
# assignments -- must produce a CLEAN, diagnostic FAIL here, not crash
# downstream code that iterates a None assignment list or indexes a missing
# journeyId. Golden/mutants/hollow always return serviceable + journeyId +
# >=1 assignment on these fixtures, so this guard never fires for them and
# their behaviour is byte-for-byte identical.


def planned_journey_ok(created: Any) -> bool:
    """True iff a create_collection_request response reflects a genuinely
    planned journey: serviceable + a non-empty journeyId + >=1 active
    assignment. Tolerates a null/absent assignment list."""
    if not isinstance(created, dict):
        return False
    return (
        created.get("serviceability") == "serviceable"
        and bool(created.get("journeyId"))
        and len(created.get("activeAssignmentIds") or []) >= 1
    )


def planning_failure_detail(cap_id: str, created: Any) -> str:
    svc = created.get("serviceability") if isinstance(created, dict) else None
    jid = bool(created.get("journeyId")) if isinstance(created, dict) else False
    n = len(created.get("activeAssignmentIds") or []) if isinstance(created, dict) else 0
    return (
        "candidate produced no serviceable journey/assignments for a network golden plans "
        f"(serviceability={svc}, journeyId={jid}, assignments={n}); {cap_id} cannot be exercised "
        "-- candidate-side planning failure"
    )


def planning_failure_report(cap_id: str, created: Any) -> "CapabilityReport":
    """A clean, zero-credit CapabilityReport carrying the standard candidate-
    planning-failure diagnostic (used in place of proceeding into code that
    would crash on the absent journey/assignments)."""
    return CapabilityReport(positive=Verdict(ok=False, detail=planning_failure_detail(cap_id, created)))
