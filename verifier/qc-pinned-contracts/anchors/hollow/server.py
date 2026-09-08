#!/usr/bin/env python3
"""The hollow anchor: a minimal fake app that serves every one of the 45
pinned operations at their correct path+method, returns a plausible-looking
2xx with fabricated ids for every write, and PERSISTS NOTHING across
requests -- no database, no file, not even an in-process dict keyed by id.
Every handler is a pure function of the ONE incoming request; nothing a
previous request did is ever visible to a later one (a bearer token is
accepted as a face-value credential, never looked up).

It deliberately does NOT enforce any business rule: named-sample-type
policy, RBAC-by-role, optimistic-concurrency (expectedPlanVersion), capacity
limits, serviceability geometry, idempotency-key replay -- none of it. Every
authenticated request that isn't a hard 404 (unknown path) gets a 2xx.

This is the floor the grading engine (verifier/qc-pinned-contracts/engine)
must sit near zero on. See anchors/hollow/RUNBOOK.md.
"""
from __future__ import annotations

import json
import sys
import threading
import uuid
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse

REPO_ROOT = Path(__file__).resolve().parents[4]
CONTRACT_PATH = REPO_ROOT / "tasks" / "lab-sample-pickup-and-delivery" / "openapi.contract.json"

ALL_ROLES = [
    "administrator",
    "dispatcher",
    "requester",
    "collection_professional",
    "delivery_partner",
    "laboratory_team",
    "node_handler",
]
ALL_FUNCTIONS = ["collection_point", "transfer_point", "hub", "laboratory"]


def now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def new_id() -> str:
    return str(uuid.uuid4())


def _load_ops() -> dict[str, tuple[str, str]]:
    """operationId -> (method, path-template), taken straight from the pinned
    contract -- the hollow app claims to speak exactly this contract."""
    contract = json.loads(CONTRACT_PATH.read_text())
    ops: dict[str, tuple[str, str]] = {}
    for path, item in contract["paths"].items():
        for method, operation in item.items():
            if method.lower() not in {"get", "post", "put", "patch", "delete"}:
                continue
            op_id = operation.get("operationId")
            if op_id:
                ops[op_id] = (method.lower(), path)
    return ops


OPS = _load_ops()
# (method, path-prefix-before-{param}, has_param) -> operationId, for routing.
ROUTES: list[tuple[str, str, bool, str]] = []
for op_id, (method, path_template) in OPS.items():
    if "{" in path_template:
        prefix = path_template.split("{")[0]
        ROUTES.append((method, prefix, True, op_id))
    else:
        ROUTES.append((method, path_template, False, op_id))
# Longest prefix first so e.g. "/api/read-user/" (with param) isn't shadowed.
ROUTES.sort(key=lambda r: -len(r[1]))

NO_AUTH_REQUIRED = {"authenticate_user", "system_readiness"}


def _wrap(data: dict) -> dict:
    return {"data": data}


def _err(status: int, code: str, message: str) -> tuple[int, dict]:
    return status, {"error": message, "code": code}


# --------------------------------------------------------------------------
# Per-operation fabricators. Every one is a pure function of (body, path_param)
# -- no dict/db is consulted or written. Each returns (status, response_body).
# --------------------------------------------------------------------------


def h_authenticate_user(body, path_param):
    return 200, _wrap(
        {
            "sessionToken": "hollow-" + new_id(),
            "userId": "hollow-user-" + new_id(),
            "roles": list(ALL_ROLES),
            "activeRole": "administrator",
        }
    )


def h_select_active_role(body, path_param):
    return 200, _wrap({"activeRole": body.get("role", "administrator")})


def h_create_user(body, path_param):
    return 201, _wrap(
        {
            "userId": new_id(),
            "username": body.get("username"),
            "email": body.get("email"),
            "fullName": body.get("fullName"),
            "phone": body.get("phone"),
            "roles": body.get("roles", []),
            "qualifications": body.get("qualifications", []),
            "active": body.get("active", True),
        }
    )


def h_update_user_access(body, path_param):
    return 201, _wrap(
        {
            "userId": body.get("userId"),
            "roles": body.get("roles", []),
            "qualifications": body.get("qualifications", []),
            "active": body.get("active", True),
        }
    )


def h_read_user(body, path_param):
    return 200, _wrap(
        {
            "userId": path_param,
            "username": "hollow-user",
            "fullName": "Hollow User",
            "email": "hollow@example.test",
            "phone": "555-0000",
            "roles": list(ALL_ROLES),
            "qualifications": [],
            "active": True,
            "available": True,
            "capacityUnits": 200,
        }
    )


def h_create_location(body, path_param):
    return 201, _wrap(
        {
            "locationId": new_id(),
            "name": body.get("name"),
            "active": body.get("active", True),
            "coordinate": body.get("coordinate"),
            "coverageKm": body.get("coverageKm"),
            "handlingMinutes": body.get("handlingMinutes", 0),
            "functions": body.get("functions", []),
        }
    )


def h_update_location(body, path_param):
    return 201, _wrap(
        {
            "locationId": body.get("locationId"),
            "name": body.get("name"),
            "coordinate": body.get("coordinate"),
            "coverageKm": body.get("coverageKm"),
            "handlingMinutes": body.get("handlingMinutes", 0),
            "functions": list(ALL_FUNCTIONS),
        }
    )


def h_set_location_status(body, path_param):
    return 200, _wrap({"locationId": body.get("locationId"), "active": body.get("active")})


def h_set_location_function_status(body, path_param):
    return 200, _wrap(
        {
            "locationId": body.get("locationId"),
            "function": body.get("function"),
            "active": body.get("active"),
            # A stateless handler has no memory of what functions this
            # location actually has, so it fabricates the full canonical set.
            "functions": list(ALL_FUNCTIONS),
        }
    )


def h_create_connection(body, path_param):
    return 201, _wrap(
        {
            "connectionId": new_id(),
            "fromLocationId": body.get("fromLocationId"),
            "toLocationId": body.get("toLocationId"),
            "travelMinutes": body.get("travelMinutes"),
            "costUnits": body.get("costUnits"),
            "cooled": body.get("cooled", False),
            "active": body.get("active", True),
        }
    )


def h_set_connection_status(body, path_param):
    return 200, _wrap({"connectionId": body.get("connectionId"), "active": body.get("active")})


def h_create_sample_type(body, path_param):
    return 201, _wrap(
        {
            "sampleTypeId": new_id(),
            "name": body.get("name"),
            "containerType": body.get("containerType"),
            "temperatureMinC": body.get("temperatureMinC"),
            "temperatureMaxC": body.get("temperatureMaxC"),
            "qualifiedCollectorRequired": body.get("qualifiedCollectorRequired", False),
            "requiredQualification": body.get("requiredQualification", ""),
            "requiresCooledRoute": body.get("requiresCooledRoute", False),
            "incompatibleSampleTypes": body.get("incompatibleSampleTypes", []),
            "batchingAllowed": body.get("batchingAllowed", True),
        }
    )


def h_update_sample_type(body, path_param):
    return 201, _wrap(
        {
            "sampleTypeId": body.get("sampleTypeId"),
            "containerType": body.get("containerType"),
            "temperatureMinC": body.get("temperatureMinC"),
            "temperatureMaxC": body.get("temperatureMaxC"),
            "incompatibleSampleTypes": body.get("incompatibleSampleTypes", []),
            "batchingAllowed": body.get("batchingAllowed", True),
        }
    )


def h_configure_laboratory_acceptance(body, path_param):
    return 200, _wrap(
        {
            "laboratoryId": body.get("laboratoryId"),
            "sampleTypeId": body.get("sampleTypeId"),
            "accepts": body.get("accepts", True),
        }
    )


def h_check_serviceability(body, path_param):
    # Always serviceable: no geometry, no window/deadline logic at all.
    return 200, _wrap({"serviceable": True, "reason": "serviceable"})


def h_read_serviceability_snapshot(body, path_param):
    return 200, _wrap({"version": 1, "regions": [], "activeCollectionPointCount": 0})


def h_create_collection_request(body, path_param):
    return 201, _wrap(
        {
            "requestId": new_id(),
            "sampleId": new_id(),
            "journeyId": new_id(),
            "serviceability": "serviceable",
            "activeAssignmentIds": [new_id(), new_id()],
            "earliestCollectionAt": body.get("earliestCollectionAt"),
            "latestCollectionAt": body.get("latestCollectionAt"),
            "status": "planned",
        }
    )


def h_ingest_presealed_sample(body, path_param):
    return 201, _wrap(
        {
            "sampleId": new_id(),
            "alreadySealed": True,
            "activeAssignmentIds": [],
            "status": "sealed",
        }
    )


def h_configure_capacity(body, path_param):
    return 200, _wrap(
        {
            "resourceId": body.get("resourceId"),
            "capacityUnits": body.get("capacityUnits"),
            "cooledCapacityUnits": body.get("cooledCapacityUnits"),
            "reservedUnits": 0,
            "cooledReserved": 0,
        }
    )


def h_set_worker_availability(body, path_param):
    return 200, _wrap(
        {
            "workerId": body.get("workerId"),
            "available": body.get("available"),
            "capacityUnits": body.get("capacityUnits"),
        }
    )


def h_accept_assignment(body, path_param):
    return 201, _wrap({"assignmentId": body.get("assignmentId"), "status": "accepted", "acceptedAt": now_iso()})


def h_collect_and_seal_sample(body, path_param):
    return 201, _wrap({"sampleId": body.get("sampleId"), "status": "sealed", "sealedAt": now_iso()})


def h_record_handling_checkpoint(body, path_param):
    return 201, _wrap({"checkpointId": new_id(), "recorded": True})


def h_record_worker_location(body, path_param):
    return 201, _wrap({"recorded": True})


def h_initiate_handoff(body, path_param):
    return 201, _wrap({"handoffId": new_id(), "status": "pending", "senderConfirmed": False, "receiverConfirmed": False})


def h_confirm_handoff(body, path_param):
    side = body.get("confirmationSide")
    sender_confirmed = side == "sender"
    receiver_confirmed = side == "receiver"
    status = "confirmed" if receiver_confirmed else "pending"
    return 201, _wrap(
        {
            "handoffId": body.get("handoffId"),
            "status": status,
            "senderConfirmed": sender_confirmed,
            "receiverConfirmed": receiver_confirmed,
            # No memory of who actually held custody before this call --
            # fabricate a fresh holder each time.
            "currentHolderId": "hollow-holder-" + new_id(),
        }
    )


def h_update_assignment_progress(body, path_param):
    return 201, _wrap({"assignmentId": body.get("assignmentId"), "status": body.get("status")})


def h_confirm_laboratory_receipt(body, path_param):
    return 201, _wrap({"sampleId": body.get("sampleId"), "status": "received", "receivedAt": now_iso()})


def h_record_laboratory_exception(body, path_param):
    return 201, _wrap({"exceptionId": new_id(), "sampleId": body.get("sampleId"), "status": "exception"})


def h_report_disruption(body, path_param):
    return 201, _wrap(
        {
            "disruptionId": new_id(),
            "affectedSampleIds": [],
            "replacementPlanIds": [],
            "replanStatus": "not_required",
        }
    )


def h_decline_assignment(body, path_param):
    return 201, _wrap(
        {
            "assignmentId": body.get("assignmentId"),
            "status": "pending",
            "reofferedTo": "hollow-worker-" + new_id(),
        }
    )


def h_edit_active_plan(body, path_param):
    return 201, _wrap({"journeyId": body.get("journeyId"), "status": "active", "valid": True, "planVersion": 2})


def h_cancel_journey(body, path_param):
    return 201, _wrap({"journeyId": body.get("journeyId"), "status": "cancelled", "sampleIds": []})


def h_reroute_journey(body, path_param):
    return 201, _wrap({"journeyId": body.get("journeyId"), "replanStatus": "replaced"})


def h_split_journey(body, path_param):
    return 201, _wrap({"journeyId": body.get("journeyId"), "splitJourney": {"journeyId": new_id()}, "valid": True})


def h_merge_journeys(body, path_param):
    return 201, _wrap(
        {
            "journeyId": body.get("targetJourneyId"),
            "sampleIds": list({body.get("targetJourneyId"), *(body.get("sourceJourneyIds") or [])}),
        }
    )


def h_read_sample_state(body, path_param):
    return 200, _wrap({"sampleId": path_param, "status": "pending", "timeline": [], "locationStale": True})


def h_read_sample_lifecycle(body, path_param):
    return 200, _wrap(
        {
            "sampleId": body.get("sampleId"),
            "sealed": False,
            "timeline": [],
            "activeReplacementPlanIds": [],
        }
    )


def h_read_journey_plan(body, path_param):
    return 200, _wrap(
        {
            "journeyId": body.get("journeyId"),
            "planVersion": 1,
            "routeMode": "direct",
            "legs": [],
            "orderedStops": [],
            "assignedWorkerIds": [],
        }
    )


def h_read_operations_state(body, path_param):
    return 200, _wrap(
        {
            "samples": [],
            "workers": [],
            "routes": [],
            "locations": [],
            "connections": [],
            "handoffs": [],
            "delays": [],
            "exceptions": [],
        }
    )


def h_search_audit_trail(body, path_param):
    limit = body.get("limit", 50)
    if isinstance(limit, int) and limit <= 0:
        return _err(400, "invalid_limit", "limit must be positive")
    return 200, _wrap({"entries": [], "total": 0})


def h_list_role_work(body, path_param):
    return 200, _wrap({"pending": [], "inProgress": [], "completed": []})


def h_read_ui_bootstrap(body, path_param):
    return 200, _wrap({"user": {"userId": "hollow-user", "roles": list(ALL_ROLES)}, "serverTime": now_iso()})


def h_stream_realtime_events(body, path_param):
    return 200, _wrap({"events": [], "cursor": "0"})


def h_system_readiness(body, path_param):
    # NOT wrapped in {"data": ...} -- the pinned contract's /ready is a bare
    # health probe, and the engine reads `payload["status"]` directly.
    return 200, {"status": "ready"}


HANDLERS = {
    "authenticate_user": h_authenticate_user,
    "select_active_role": h_select_active_role,
    "create_user": h_create_user,
    "update_user_access": h_update_user_access,
    "read_user": h_read_user,
    "create_location": h_create_location,
    "update_location": h_update_location,
    "set_location_status": h_set_location_status,
    "set_location_function_status": h_set_location_function_status,
    "create_connection": h_create_connection,
    "set_connection_status": h_set_connection_status,
    "create_sample_type": h_create_sample_type,
    "update_sample_type": h_update_sample_type,
    "configure_laboratory_acceptance": h_configure_laboratory_acceptance,
    "check_serviceability": h_check_serviceability,
    "read_serviceability_snapshot": h_read_serviceability_snapshot,
    "create_collection_request": h_create_collection_request,
    "ingest_presealed_sample": h_ingest_presealed_sample,
    "configure_capacity": h_configure_capacity,
    "set_worker_availability": h_set_worker_availability,
    "accept_assignment": h_accept_assignment,
    "collect_and_seal_sample": h_collect_and_seal_sample,
    "record_handling_checkpoint": h_record_handling_checkpoint,
    "record_worker_location": h_record_worker_location,
    "initiate_handoff": h_initiate_handoff,
    "confirm_handoff": h_confirm_handoff,
    "update_assignment_progress": h_update_assignment_progress,
    "confirm_laboratory_receipt": h_confirm_laboratory_receipt,
    "record_laboratory_exception": h_record_laboratory_exception,
    "report_disruption": h_report_disruption,
    "decline_assignment": h_decline_assignment,
    "edit_active_plan": h_edit_active_plan,
    "cancel_journey": h_cancel_journey,
    "reroute_journey": h_reroute_journey,
    "split_journey": h_split_journey,
    "merge_journeys": h_merge_journeys,
    "read_sample_state": h_read_sample_state,
    "read_sample_lifecycle": h_read_sample_lifecycle,
    "read_journey_plan": h_read_journey_plan,
    "read_operations_state": h_read_operations_state,
    "search_audit_trail": h_search_audit_trail,
    "list_role_work": h_list_role_work,
    "read_ui_bootstrap": h_read_ui_bootstrap,
    "stream_realtime_events": h_stream_realtime_events,
    "system_readiness": h_system_readiness,
}

assert set(HANDLERS) == set(OPS), f"missing handlers: {set(OPS) - set(HANDLERS)}"


def _match_route(method: str, path: str):
    for route_method, prefix, has_param, op_id in ROUTES:
        if route_method != method:
            continue
        if has_param:
            if path.startswith(prefix):
                return op_id, path[len(prefix):]
        else:
            if path == prefix:
                return op_id, None
    return None, None


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # quieter server log
        sys.stderr.write("[hollow] " + (fmt % args) + "\n")

    def _dispatch(self, method: str):
        parsed = urlparse(self.path)
        op_id, path_param = _match_route(method, parsed.path)
        if op_id is None:
            return self._send(404, {"error": "not found", "code": "not_found"})

        if op_id not in NO_AUTH_REQUIRED:
            auth = self.headers.get("Authorization", "")
            if not auth.strip():
                return self._send(401, {"error": "unauthorized", "code": "unauthorized"})

        body = {}
        length = int(self.headers.get("Content-Length", 0) or 0)
        if length:
            raw = self.rfile.read(length)
            try:
                body = json.loads(raw) if raw else {}
            except json.JSONDecodeError:
                body = {}

        status, payload = HANDLERS[op_id](body, path_param)
        self._send(status, payload)

    def _send(self, status: int, payload: dict):
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self._dispatch("get")

    def do_POST(self):
        self._dispatch("post")

    def do_PUT(self):
        self._dispatch("put")

    def do_PATCH(self):
        self._dispatch("patch")

    def do_DELETE(self):
        self._dispatch("delete")


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8099
    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    print(f"hollow anchor listening on http://127.0.0.1:{port} ({len(OPS)} operations, 0 bytes of persistent state)")
    server.serve_forever()


if __name__ == "__main__":
    main()
