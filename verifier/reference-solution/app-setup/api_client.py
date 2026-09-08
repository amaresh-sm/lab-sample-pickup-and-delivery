"""Shared, dependency-free HTTP client and world seeder for the app-setup
checks under app-setup/.

Every request is addressed by the operationId documented in
app-setup/openapi.json rather than by a hard-coded URL, so a check cannot
drift away from the published contract without failing loudly.
"""
from __future__ import annotations

import json
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

VERIFICATION_DIR = Path(__file__).resolve().parent
WORKSPACE_ROOT = VERIFICATION_DIR.parent


def load_manifest() -> dict[str, Any]:
    return json.loads((VERIFICATION_DIR / "manifest.json").read_text())


def load_openapi() -> dict[str, Any]:
    return json.loads((VERIFICATION_DIR / "openapi.json").read_text())


def default_base_url() -> str:
    """The application origin, taken from the manifest rather than hard-coded."""
    return load_manifest()["urls"]["frontend"].rstrip("/")


class ApiError(RuntimeError):
    def __init__(self, operation: str, status: int, payload: Any):
        self.operation = operation
        self.status = status
        self.payload = payload
        code = ""
        if isinstance(payload, dict):
            code = str(payload.get("code", ""))
        super().__init__(f"{operation} -> HTTP {status} {code or payload}")


def rfc3339(value: datetime) -> str:
    return value.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


class Client:
    """One authenticated (or anonymous) session against the application."""

    def __init__(self, base_url: str, token: str = "", spec: dict[str, Any] | None = None):
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.spec = spec if spec is not None else load_openapi()
        self.operations: dict[str, tuple[str, str, dict[str, Any]]] = {}
        for path, item in self.spec["paths"].items():
            for method, operation in item.items():
                if method.lower() in {"get", "post", "put", "patch", "delete"}:
                    self.operations[operation["operationId"]] = (method.lower(), path, operation)

    # ------------------------------------------------------------------ plumbing
    def raw(
        self,
        operation_id: str,
        body: dict[str, Any] | None = None,
        path_value: str | None = None,
        query: str = "",
    ) -> tuple[int, Any]:
        method, path, _ = self.operations[operation_id]
        if path_value is not None:
            path = path.split("{")[0] + path_value
        url = self.base_url + path + query
        data = None
        headers: dict[str, str] = {}
        if method == "post":
            data = json.dumps(body or {}).encode()
            headers["Content-Type"] = "application/json"
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        request = urllib.request.Request(url, data=data, headers=headers, method=method.upper())
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                status, raw = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read()
        payload = json.loads(raw) if raw else None
        return status, payload

    def call(
        self,
        operation_id: str,
        body: dict[str, Any] | None = None,
        path_value: str | None = None,
        query: str = "",
    ) -> Any:
        """Perform one operation and return its `data`, raising on any failure."""
        status, payload = self.raw(operation_id, body, path_value, query)
        if status >= 400 or not isinstance(payload, dict) or "data" not in payload:
            raise ApiError(operation_id, status, payload)
        return payload["data"]

    def new_session(self, token: str) -> "Client":
        """A same-configuration client bound to another bearer token."""
        return type(self)(self.base_url, token, self.spec)

    def login(self, username: str, password: str, auto_select: bool = True) -> "Client":
        data = self.call("authenticate_user", {"username": username, "password": password})
        session = self.new_session(data["sessionToken"])
        session.user_id = data["userId"]
        session.roles = data["roles"]
        session.active_role = data["activeRole"]
        if auto_select and not data["activeRole"] and data["roles"]:
            session.call("select_active_role", {"role": data["roles"][0]})
            session.active_role = data["roles"][0]
        return session

    def select_role(self, role: str) -> None:
        self.call("select_active_role", {"role": role})
        self.active_role = role


PASSWORD = "app-setup-password-123456"

# Every location function the application recognises for a seeded network.
PICKUP_FUNCTIONS = ["collection_point", "transfer_point"]


class World:
    """The network, people and timing one app-setup run works against.

    Timestamps are derived from the wall clock at seed time so a check stays
    valid whenever it is run, instead of expiring with hard-coded dates.
    """

    def __init__(self, admin: Client, prefix: str):
        self.admin = admin
        self.prefix = prefix
        now = datetime.now(timezone.utc).replace(microsecond=0)
        self.now = now
        self.requested_at = now - timedelta(minutes=5)
        self.window_start = now - timedelta(minutes=5)
        self.window_end = now + timedelta(hours=4)
        self.deadline = now + timedelta(hours=10)
        self.after_window = now + timedelta(hours=6)
        self.users: dict[str, dict[str, Any]] = {}
        self.sessions: dict[str, Client] = {}

    def at(self, **offset: float) -> str:
        return rfc3339(self.now + timedelta(**offset))

    # --------------------------------------------------------------------- seed
    def park_existing_field_workers(self) -> int:
        """Make every field worker already in the database unavailable.

        Offers are allocated by the application's fairness rules, which favour
        the longest-waiting eligible worker -- so on a database that already
        carries workers from an earlier run, a freshly seeded worker can never
        win an offer. Parking the incumbents first makes this run's offers land
        on this run's people without needing a reset. Users are enumerated from
        the immutable audit trail, which records every create_user with the
        roles it granted.
        """
        field_roles = {
            "collection_professional",
            "delivery_partner",
            "node_handler",
            "laboratory_team",
        }
        entries = self.admin.call(
            "search_audit_trail",
            {"action": "user_created", "limit": 5000, "order": "oldest_first"},
        )["entries"]
        parked = 0
        for entry in entries:
            roles = entry.get("details", {}).get("roles") or []
            if not field_roles.intersection(roles):
                continue
            status, _ = self.admin.raw(
                "set_worker_availability",
                {"workerId": entry["entityId"], "available": False, "capacityUnits": 400},
            )
            if status == 200:
                parked += 1
        return parked

    def build(self) -> "World":
        self.parked_workers = self.park_existing_field_workers()
        admin, prefix = self.admin, self.prefix
        self.sample_type = admin.call(
            "create_sample_type",
            {
                "name": f"{prefix} Standard",
                "containerType": "sealed-tube",
                "temperatureMinC": 2,
                "temperatureMaxC": 8,
                "qualifiedCollectorRequired": False,
                "incompatibleSampleTypes": [],
                "batchingAllowed": True,
            },
        )["sampleTypeId"]
        self.pickup = admin.call(
            "create_location",
            {
                "name": f"{prefix} collection point",
                "active": True,
                "coordinate": {"latitude": 12.9500, "longitude": 77.5700},
                "coverageKm": 5,
                "handlingMinutes": 0,
                "functions": PICKUP_FUNCTIONS,
            },
        )["locationId"]
        self.laboratory = admin.call(
            "create_location",
            {
                "name": f"{prefix} reference laboratory",
                "active": True,
                "coordinate": {"latitude": 12.9300, "longitude": 77.6000},
                "handlingMinutes": 5,
                "functions": ["hub", "laboratory"],
            },
        )["locationId"]
        # A transfer point/hub with its own, deliberately dearer, pair of
        # connections. The direct route stays the cheapest so planning is
        # predictable, while plan edits and reroutes have somewhere to go.
        self.transfer = admin.call(
            "create_location",
            {
                "name": f"{prefix} transfer hub",
                "active": True,
                "coordinate": {"latitude": 12.9400, "longitude": 77.5850},
                "handlingMinutes": 5,
                "functions": ["transfer_point", "hub"],
            },
        )["locationId"]
        self.connection = admin.call(
            "create_connection",
            {
                "fromLocationId": self.pickup,
                "toLocationId": self.laboratory,
                "travelMinutes": 30,
                "costUnits": 12.5,
                "cooled": True,
                "active": True,
            },
        )["connectionId"]
        self.connection_via_transfer = [
            admin.call(
                "create_connection",
                {
                    "fromLocationId": origin,
                    "toLocationId": destination,
                    "travelMinutes": 15,
                    "costUnits": 20,
                    "cooled": True,
                    "active": True,
                },
            )["connectionId"]
            for origin, destination in (
                (self.pickup, self.transfer),
                (self.transfer, self.laboratory),
            )
        ]
        admin.call(
            "configure_laboratory_acceptance",
            {"laboratoryId": self.laboratory, "sampleTypeId": self.sample_type, "accepts": True},
        )
        for resource in (
            self.pickup,
            self.transfer,
            self.laboratory,
            self.connection,
            *self.connection_via_transfer,
        ):
            admin.call(
                "configure_capacity",
                {"resourceId": resource, "capacityUnits": 400, "cooledCapacityUnits": 200},
            )

        self.make_user("requester", ["requester"])
        self.make_user("collector", ["collection_professional"], worker=True)
        self.make_user("collector2", ["collection_professional"], worker=True)
        self.make_user("courier", ["delivery_partner"], worker=True)
        self.make_user("handler", ["node_handler"], worker=True)
        self.make_user("lab", ["laboratory_team"], worker=True)
        self.make_user("dispatcher", ["dispatcher"])
        return self

    def make_user(self, alias: str, roles: list[str], worker: bool = False) -> dict[str, Any]:
        username = f"{self.prefix}-{alias}"
        created = self.admin.call(
            "create_user",
            {
                "username": username,
                "initialPassword": PASSWORD,
                "email": f"{username}@example.test",
                "fullName": f"{self.prefix} {alias}".replace("-", " ").title(),
                "phone": "555-0100",
                "roles": roles,
                "qualifications": [],
                "certifications": [],
                "active": True,
            },
        )
        created["username"] = username
        self.users[alias] = created
        if worker:
            self.admin.call(
                "set_worker_availability",
                {"workerId": created["userId"], "available": True, "capacityUnits": 200},
            )
        return created

    def find_offered(self, assignment_id: str, aliases: list[str]) -> str:
        """Which seeded worker the application actually offered an assignment to.

        Offers are allocated by the application's own fairness rules, so a check
        must discover the holder instead of assuming one. A holder outside this
        seed means the database still carries workers from an earlier run.
        """
        for alias in aliases:
            work = self.session(alias).call("list_role_work", {})
            for bucket in ("pending", "inProgress", "completed"):
                for item in work[bucket]:
                    if item.get("assignmentId") == assignment_id:
                        return alias
        raise RuntimeError(
            f"assignment {assignment_id} was offered to a worker outside this seed "
            f"(tried {aliases}); run app-setup/reset.sh first"
        )

    def session(self, alias: str) -> Client:
        if alias not in self.sessions:
            anonymous = self.admin.new_session("")
            self.sessions[alias] = anonymous.login(self.users[alias]["username"], PASSWORD)
        return self.sessions[alias]

    def user_id(self, alias: str) -> str:
        return self.users[alias]["userId"]

    # ------------------------------------------------------------------ requests
    def request_body(self, key: str, **overrides: Any) -> dict[str, Any]:
        body = {
            "requesterId": self.user_id("requester"),
            "origin": {"latitude": 12.9500, "longitude": 77.5700},
            "originType": "home",
            "originNodeId": self.pickup,
            "requestedAt": rfc3339(self.requested_at),
            "deliveryDeadline": rfc3339(self.deadline),
            "earliestCollectionAt": rfc3339(self.window_start),
            "latestCollectionAt": rfc3339(self.window_end),
            "sampleTypeId": self.sample_type,
            "containerType": "sealed-tube",
            "temperatureMinC": 2,
            "temperatureMaxC": 8,
            "incompatibleSampleTypes": [],
            "idempotencyKey": key,
        }
        body.update(overrides)
        return body

    def create_request(self, key: str, **overrides: Any) -> dict[str, Any]:
        return self.admin.call("create_collection_request", self.request_body(key, **overrides))
