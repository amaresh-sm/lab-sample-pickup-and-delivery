"""AC-77: no undocumented field the application returns.

verification/smoke-contract.sh already cross-checks the ROUTE REGISTRY against
verification/openapi.json in both directions. The one clause it cannot check
statically is whether the payloads the application actually sends match the
response schemas the document publishes.

This check drives every documented operation against a running application and
validates each response body against the schema openapi.json declares for that
exact status. Every response object in the spec carries
`additionalProperties: false`, so a field the application returns but the spec
does not document is reported as a failure -- as is a missing required field, a
wrong type, a value outside an enum, and an undocumented status code.

Timestamps are derived from the wall clock at run time, so the check does not
expire, and every seeded name is unique, so it can be re-run against the same
database.
"""
from __future__ import annotations

import os
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from api_client import Client, World, default_base_url  # noqa: E402

TYPE_MAP = {
    "object": dict,
    "array": list,
    "string": str,
    "boolean": bool,
    "number": (int, float),
    "integer": int,
}


class Recorder:
    """Validates one response against the documented schema for its status."""

    def __init__(self, spec: dict):
        self.spec = spec
        self.schemas = spec["components"]["schemas"]
        self.operations = {}
        for path, item in spec["paths"].items():
            for method, operation in item.items():
                if method.lower() in {"get", "post", "put", "patch", "delete"}:
                    self.operations[operation["operationId"]] = operation
        self.failures: list[str] = []
        self.responses: list[tuple[str, int, bool]] = []

    # ------------------------------------------------------------------ schema
    def resolve(self, schema):
        seen = 0
        while isinstance(schema, dict) and "$ref" in schema:
            schema = self.schemas[schema["$ref"].split("/")[-1]]
            seen += 1
            if seen > 20:
                raise RuntimeError("$ref loop in openapi.json")
        return schema

    def validate(self, value, schema, trail, errors):
        schema = self.resolve(schema)
        if not isinstance(schema, dict):
            return
        if "oneOf" in schema:
            branches = []
            for index, branch in enumerate(schema["oneOf"]):
                local: list[str] = []
                self.validate(value, branch, trail, local)
                if not local:
                    return
                branches.append((index, local))
            errors.append(
                f"{trail}: matched none of the oneOf branches -> "
                + "; ".join(f"[{index}] {reasons[0]}" for index, reasons in branches)
            )
            return
        if "allOf" in schema:
            for branch in schema["allOf"]:
                self.validate(value, branch, trail, errors)
        expected = schema.get("type")
        if expected:
            want = TYPE_MAP[expected]
            if expected in {"number", "integer"} and isinstance(value, bool):
                errors.append(f"{trail}: expected {expected}, got bool")
                return
            if not isinstance(value, want):
                errors.append(f"{trail}: expected {expected}, got {type(value).__name__}")
                return
        if "enum" in schema and value not in schema["enum"]:
            errors.append(f"{trail}: value {value!r} is not one of {schema['enum']}")
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            if "minimum" in schema and value < schema["minimum"]:
                errors.append(f"{trail}: {value} is below the documented minimum {schema['minimum']}")
            if "maximum" in schema and value > schema["maximum"]:
                errors.append(f"{trail}: {value} is above the documented maximum {schema['maximum']}")
        if isinstance(value, dict):
            properties = schema.get("properties", {})
            for name in schema.get("required", []):
                if name not in value:
                    errors.append(f"{trail}: missing required field {name!r}")
            if schema.get("additionalProperties") is False:
                for name in value:
                    if name not in properties:
                        errors.append(f"{trail}: UNDOCUMENTED field {name!r}")
            for name, item in value.items():
                if name in properties:
                    self.validate(item, properties[name], f"{trail}.{name}", errors)
        if isinstance(value, list) and "items" in schema:
            for index, item in enumerate(value):
                self.validate(item, schema["items"], f"{trail}[{index}]", errors)

    # ------------------------------------------------------------------ record
    def record(self, operation_id: str, status: int, payload) -> None:
        operation = self.operations[operation_id]
        documented = operation["responses"].get(str(status))
        if documented is None:
            self.failures.append(
                f"{operation_id}: HTTP {status} is not documented in openapi.json ({payload})"
            )
            self.responses.append((operation_id, status, False))
            return
        schema = documented.get("content", {}).get("application/json", {}).get("schema")
        errors: list[str] = []
        if schema is not None:
            self.validate(payload, schema, f"{operation_id}({status})", errors)
        self.failures.extend(errors)
        self.responses.append((operation_id, status, not errors))


class ConformanceClient(Client):
    """A Client that validates every response it sees."""

    recorder: Recorder

    def new_session(self, token: str) -> "ConformanceClient":
        session = ConformanceClient(self.base_url, token, self.spec)
        session.recorder = self.recorder
        return session

    def raw(self, operation_id, body=None, path_value=None, query=""):
        status, payload = super().raw(operation_id, body, path_value, query)
        self.recorder.record(operation_id, status, payload)
        return status, payload

    def expect(self, operation_id, body=None, status=None, path_value=None, query=""):
        """Drive an operation whose interesting outcome is a refusal."""
        got, payload = self.raw(operation_id, body, path_value, query)
        if status is not None and got != status:
            self.recorder.failures.append(
                f"{operation_id}: HTTP {got}, expected {status} ({payload})"
            )
        return got, payload


def main() -> int:
    base_url = os.environ.get("APP_BASE_URL") or default_base_url()
    admin_username = os.environ.get("ADMIN_USERNAME", "")
    admin_password = os.environ.get("ADMIN_PASSWORD", "")
    if not admin_username or not admin_password:
        print("ADMIN_USERNAME and ADMIN_PASSWORD are required", file=sys.stderr)
        return 2

    anonymous = ConformanceClient(base_url)
    recorder = Recorder(anonymous.spec)
    anonymous.recorder = recorder
    prefix = f"rc{int(time.time())}"

    # ------------------------------------------------------------------ sign in
    admin = anonymous.login(admin_username, admin_password)
    anonymous.expect("authenticate_user", {"username": admin_username, "password": "wrong"}, 401)

    world = World(admin, prefix).build()
    requester = world.session("requester")
    collector = world.session("collector")
    courier = world.session("courier")
    handler = world.session("handler")
    laboratory = world.session("lab")
    dispatcher = world.session("dispatcher")

    admin.call("read_user", path_value=world.user_id("requester"))
    admin.call(
        "update_user_access",
        {
            "userId": world.user_id("requester"),
            "roles": ["requester"],
            "qualifications": [],
            "certifications": [],
            "active": True,
        },
    )

    # ------------------------------------------------------- role selection paths
    world.make_user("dual", ["dispatcher", "requester"])
    dual = anonymous.login(f"{prefix}-dual", "verification-password-123456", auto_select=False)
    dual.expect("read_operations_state", {}, 409)
    dual.expect("select_active_role", {"role": "laboratory_team"}, 403)
    dual.expect("select_active_role", {"role": ""}, 400)
    dual.expect("select_active_role", {"role": "requester"}, 200)
    dual.expect("read_operations_state", {}, 403)
    dual.expect("select_active_role", {"role": "dispatcher"}, 200)
    dual.expect("read_operations_state", {}, 200)

    # ------------------------------------------------------- network refusals
    # Frost is a named policy type: an uncooled container for it is refused.
    admin.expect(
        "create_sample_type",
        {
            "name": "Frost",
            "containerType": "ambient-bag",
            "temperatureMinC": -80,
            "temperatureMaxC": -20,
        },
        400,
    )
    admin.expect(
        "create_sample_type",
        {
            "name": f"{prefix} inverted",
            "containerType": "sealed-tube",
            "temperatureMinC": 8,
            "temperatureMaxC": 2,
        },
        400,
    )
    admin.call(
        "update_sample_type",
        {
            "sampleTypeId": world.sample_type,
            "containerType": "sealed-tube",
            "temperatureMinC": 2,
            "temperatureMaxC": 8,
            "incompatibleSampleTypes": [],
            "batchingAllowed": True,
        },
    )
    admin.expect(
        "create_location",
        {
            "name": f"{prefix} no functions",
            "active": True,
            "coordinate": {"latitude": 12.95, "longitude": 77.57},
            "functions": [],
        },
        400,
    )
    admin.expect(
        "create_location",
        {
            "name": f"{prefix} bad function",
            "active": True,
            "coordinate": {"latitude": 12.95, "longitude": 77.57},
            "functions": ["warehouse"],
        },
        400,
    )
    admin.call(
        "update_location",
        {
            "locationId": world.pickup,
            "name": f"{prefix} collection point",
            "coordinate": {"latitude": 12.9500, "longitude": 77.5700},
            "coverageKm": 5,
            "handlingMinutes": 0,
            "functions": ["collection_point", "transfer_point"],
        },
    )
    admin.expect(
        "update_location",
        {
            "locationId": world.pickup,
            "name": f"{prefix} collection point",
            "coordinate": {"latitude": 12.9500, "longitude": 77.5700},
            "functions": [],
        },
        400,
    )
    admin.call("set_location_status", {"locationId": world.pickup, "active": True})
    admin.call(
        "set_location_function_status",
        {"locationId": world.pickup, "function": "collection_point", "active": True},
    )
    admin.expect(
        "set_location_function_status",
        {"locationId": world.pickup, "function": "laboratory", "active": False},
        404,
    )
    admin.expect(
        "set_location_function_status",
        {"locationId": world.pickup, "function": "warehouse", "active": False},
        400,
    )
    admin.call("set_connection_status", {"connectionId": world.connection, "active": True})
    admin.expect(
        "configure_laboratory_acceptance",
        {"laboratoryId": world.pickup, "sampleTypeId": world.sample_type, "accepts": True},
        400,
    )
    admin.expect(
        "configure_capacity",
        {"resourceId": world.pickup, "capacityUnits": 5, "cooledCapacityUnits": 9},
        400,
    )

    # --------------------------------------------------------- serviceability
    serviceability_body = {
        "origin": {"latitude": 12.9500, "longitude": 77.5700},
        "originType": "home",
        "sampleTypeId": world.sample_type,
        "requestedAt": world.at(minutes=-5),
        "deliveryDeadline": world.at(hours=10),
        "earliestCollectionAt": world.at(minutes=-5),
        "latestCollectionAt": world.at(hours=4),
    }
    admin.call("check_serviceability", serviceability_body)
    admin.call(
        "check_serviceability",
        {**serviceability_body, "origin": {"latitude": 40.0, "longitude": 0.0}},
    )
    admin.expect(
        "check_serviceability",
        {**serviceability_body, "deliveryDeadline": world.at(minutes=-5)},
        400,
    )
    admin.expect("check_serviceability", {**serviceability_body, "originType": "spaceship"}, 400)
    admin.expect(
        "check_serviceability",
        {
            **serviceability_body,
            "earliestCollectionAt": world.at(hours=4),
            "latestCollectionAt": world.at(minutes=-5),
        },
        400,
    )
    collector.expect("check_serviceability", serviceability_body, 403)
    admin.call("read_serviceability_snapshot", {})
    collector.expect("read_serviceability_snapshot", {}, 403)

    # ------------------------------------------------------ request and journey
    created = world.create_request("conformance-request-1")
    replay = world.create_request("conformance-request-1")
    if replay["sampleId"] != created["sampleId"]:
        recorder.failures.append("an idempotent replay created a second sample")
    unserviceable = world.create_request(
        "conformance-unserviceable-1", origin={"latitude": 40.0, "longitude": 0.0}, originNodeId=""
    )
    if "serviceabilityReason" not in unserviceable:
        recorder.failures.append("an unserviceable request carries no serviceabilityReason")
    admin.expect(
        "create_collection_request",
        world.request_body("conformance-bad-container", containerType="wrong-tube"),
        400,
    )
    presealed = admin.call(
        "ingest_presealed_sample",
        {
            **world.request_body("conformance-presealed-1"),
            "originType": "node",
            "initialHolderId": world.user_id("handler"),
        },
    )

    sample_id, journey_id = created["sampleId"], created["journeyId"]

    # ------------------------------------------------------------------ execution
    operations = admin.call("read_operations_state", {"filters": {}})
    if "locations" not in operations:
        recorder.failures.append("read_operations_state has no unified locations array")
    for legacy in ("nodes", "laboratories"):
        if legacy in operations:
            recorder.failures.append(f"read_operations_state still returns {legacy}")

    plan = admin.call("read_journey_plan", {"journeyId": journey_id})
    collection_assignment = plan["collectionAssignmentId"]
    transport_assignment = plan["finalTransportAssignmentId"]
    laboratory_assignment = plan["laboratoryAssignmentIds"][0]

    collector_alias = world.find_offered(collection_assignment, ["collector", "collector2"])
    collector = world.session(collector_alias)
    other_collector = world.session(
        "collector2" if collector_alias == "collector" else "collector"
    )
    # An eligible worker who was not offered the work cannot take it.
    other_collector.expect(
        "accept_assignment",
        {"assignmentId": collection_assignment, "idempotencyKey": f"{prefix}-steal"},
        409,
    )
    collector.call(
        "accept_assignment",
        {"assignmentId": collection_assignment, "idempotencyKey": f"{prefix}-accept-collection"},
    )
    collector.expect(
        "collect_and_seal_sample",
        {
            "sampleId": sample_id,
            "assignmentId": collection_assignment,
            "collectedAt": world.at(hours=5),
            "containerType": "sealed-tube",
            "observedTemperatureC": 5,
            "idempotencyKey": f"{prefix}-collect-late",
        },
        409,
    )
    collector.call(
        "collect_and_seal_sample",
        {
            "sampleId": sample_id,
            "assignmentId": collection_assignment,
            "collectedAt": world.at(minutes=1),
            "containerType": "sealed-tube",
            "observedTemperatureC": 5,
            "idempotencyKey": f"{prefix}-collect",
        },
    )

    courier_alias = world.find_offered(transport_assignment, ["courier"])
    courier = world.session(courier_alias)
    courier.call(
        "accept_assignment",
        {"assignmentId": transport_assignment, "idempotencyKey": f"{prefix}-accept-transport"},
    )
    handoff = collector.call(
        "initiate_handoff",
        {
            "sampleId": sample_id,
            "fromAssignmentId": collection_assignment,
            "toAssignmentId": transport_assignment,
            "receiverId": world.user_id(courier_alias),
            "initiatedAt": world.at(minutes=5),
            "idempotencyKey": f"{prefix}-handoff",
        },
    )
    collector.call(
        "confirm_handoff",
        {
            "handoffId": handoff["handoffId"],
            "confirmationSide": "sender",
            "confirmedAt": world.at(minutes=6),
            "idempotencyKey": f"{prefix}-handoff-sender",
        },
    )
    courier.call(
        "confirm_handoff",
        {
            "handoffId": handoff["handoffId"],
            "confirmationSide": "receiver",
            "confirmedAt": world.at(minutes=7),
            "idempotencyKey": f"{prefix}-handoff-receiver",
        },
    )
    courier.call(
        "record_worker_location",
        {
            "assignmentId": transport_assignment,
            "coordinate": {"latitude": 12.9600, "longitude": 77.5800},
            "sequence": 1,
            "recordedAt": world.at(minutes=8),
        },
    )
    courier.expect(
        "record_worker_location",
        {
            "assignmentId": transport_assignment,
            "coordinate": {"latitude": 12.9600, "longitude": 77.5800},
            "sequence": 1,
            "recordedAt": world.at(minutes=9),
        },
        409,
    )
    courier.call(
        "record_handling_checkpoint",
        {
            "sampleId": sample_id,
            "assignmentId": transport_assignment,
            "locationId": world.pickup,
            "containerType": "sealed-tube",
            "observedTemperatureC": 5,
            "recordedAt": world.at(minutes=10),
            "idempotencyKey": f"{prefix}-checkpoint",
        },
    )
    for index, status in enumerate(
        ("at_pickup", "picked_up", "in_transit", "arrived", "delivered")
    ):
        courier.call(
            "update_assignment_progress",
            {
                "assignmentId": transport_assignment,
                "status": status,
                "occurredAt": world.at(minutes=20 + index),
                "idempotencyKey": f"{prefix}-progress-{status}",
            },
        )

    laboratory_alias = world.find_offered(laboratory_assignment, ["lab"])
    laboratory = world.session(laboratory_alias)
    laboratory.call(
        "accept_assignment",
        {"assignmentId": laboratory_assignment, "idempotencyKey": f"{prefix}-accept-laboratory"},
    )
    laboratory.call(
        "confirm_laboratory_receipt",
        {
            "sampleId": sample_id,
            "laboratoryId": world.laboratory,
            "receivedAt": world.at(minutes=40),
            "idempotencyKey": f"{prefix}-receipt",
        },
    )
    laboratory.expect(
        "decline_assignment",
        {"assignmentId": laboratory_assignment, "idempotencyKey": f"{prefix}-late-decline"},
        409,
    )

    # ------------------------------------------------------------ reads and views
    admin.call("read_sample_state", path_value=sample_id)
    requester.call("read_sample_state", path_value=sample_id)
    admin.expect(
        "read_sample_state", path_value="00000000-0000-4000-8000-000000000000", status=404
    )
    admin.call("read_sample_lifecycle", {"sampleId": sample_id})
    requester.call("read_sample_lifecycle", {"sampleId": presealed["sampleId"]})
    for session in (requester, courier, dispatcher, collector):
        session.call("read_journey_plan", {"journeyId": journey_id})
    worker_plan = collector.call("read_journey_plan", {"journeyId": journey_id})
    if worker_plan.get("scope") != "assigned_legs":
        recorder.failures.append(f"field-worker plan scope = {worker_plan.get('scope')!r}")
    for leaked in (
        "totalCostUnits",
        "capacityUsed",
        "capacityAvailable",
        "collectionAssignmentId",
        "firstTransportAssignmentId",
        "middleTransportAssignmentId",
        "finalTransportAssignmentId",
    ):
        if leaked in worker_plan:
            recorder.failures.append(f"the field-worker plan leaks {leaked}")

    for session, role in (
        (admin, "administrator"),
        (dispatcher, "dispatcher"),
        (requester, "requester"),
        (collector, "collection_professional"),
        (courier, "delivery_partner"),
        (handler, "node_handler"),
        (laboratory, "laboratory_team"),
    ):
        work = session.call("list_role_work", {"role": role})
        for bucket in ("pending", "inProgress", "completed"):
            for item in work[bucket]:
                if "history" not in item:
                    recorder.failures.append(
                        f"{role} {bucket} work item has no history: {sorted(item)}"
                    )

    # ------------------------------------------------------------- plan changes
    second = world.create_request("conformance-request-2")
    third = world.create_request("conformance-request-3")
    dispatcher.expect(
        "edit_active_plan",
        {
            "journeyId": second["journeyId"],
            "expectedPlanVersion": 1,
            "changeType": "not_a_change",
            "changes": {},
            "idempotencyKey": f"{prefix}-bad-change",
        },
        400,
    )
    dispatcher.expect(
        "edit_active_plan",
        {
            "journeyId": second["journeyId"],
            "expectedPlanVersion": 99,
            "changeType": "cancel_work",
            "changes": {},
            "idempotencyKey": f"{prefix}-stale-version",
        },
        409,
    )
    dispatcher.call(
        "edit_active_plan",
        {
            "journeyId": second["journeyId"],
            "expectedPlanVersion": 1,
            "changeType": "add_stop",
            "changes": {"locationId": world.transfer, "position": 1, "dwellMinutes": 0},
            "idempotencyKey": f"{prefix}-edit",
        },
    )
    version = dispatcher.call("read_journey_plan", {"journeyId": second["journeyId"]})["planVersion"]
    merged_status, _ = dispatcher.expect(
        "merge_journeys",
        {
            "targetJourneyId": second["journeyId"],
            "expectedPlanVersion": version,
            "sourceJourneyIds": [third["journeyId"]],
            "idempotencyKey": f"{prefix}-merge",
        },
    )
    if merged_status == 201:
        version = dispatcher.call("read_journey_plan", {"journeyId": second["journeyId"]})[
            "planVersion"
        ]
        split_status, _ = dispatcher.expect(
            "split_journey",
            {
                "journeyId": second["journeyId"],
                "expectedPlanVersion": version,
                "sampleIds": [third["sampleId"]],
                "idempotencyKey": f"{prefix}-split",
            },
        )
        if split_status == 201:
            version = dispatcher.call("read_journey_plan", {"journeyId": second["journeyId"]})[
                "planVersion"
            ]
    dispatcher.expect(
        "reroute_journey",
        {
            "journeyId": second["journeyId"],
            "expectedPlanVersion": version,
            "reason": "conformance probe",
            "idempotencyKey": f"{prefix}-reroute",
        },
    )
    fourth = world.create_request("conformance-request-4")
    dispatcher.call(
        "cancel_journey",
        {
            "journeyId": fourth["journeyId"],
            "expectedPlanVersion": 1,
            "reason": "conformance probe",
            "idempotencyKey": f"{prefix}-cancel",
        },
    )
    laboratory.expect(
        "record_laboratory_exception",
        {
            "sampleId": presealed["sampleId"],
            "laboratoryId": world.laboratory,
            "reason": "conformance probe",
            "observedAt": world.at(minutes=50),
            "idempotencyKey": f"{prefix}-exception",
        },
    )

    # --------------------------------- every documented plan-change branch
    # edit_active_plan documents ONE success schema for all of its changeType
    # branches, so each branch's real payload has to satisfy it.
    rerouted = world.create_request("conformance-request-5")
    dispatcher.expect(
        "edit_active_plan",
        {
            "journeyId": rerouted["journeyId"],
            "expectedPlanVersion": 1,
            "changeType": "reroute",
            "changes": {"reason": "conformance probe"},
            "idempotencyKey": f"{prefix}-edit-reroute",
        },
    )
    cancelled_by_edit = world.create_request("conformance-request-6")
    dispatcher.expect(
        "edit_active_plan",
        {
            "journeyId": cancelled_by_edit["journeyId"],
            "expectedPlanVersion": 1,
            "changeType": "cancel_work",
            "changes": {"reason": "conformance probe"},
            "idempotencyKey": f"{prefix}-edit-cancel",
        },
    )
    # A changeType the document publishes must not be refused as unsupported:
    # the request schema is part of "openapi.json matches the running app".
    change_types = recorder.operations["edit_active_plan"]["requestBody"]["content"][
        "application/json"
    ]["schema"]["properties"]["changeType"]["enum"]
    for change_type in change_types:
        probe = world.create_request(f"conformance-change-{change_type}")
        status, payload = dispatcher.raw(
            "edit_active_plan",
            {
                "journeyId": probe["journeyId"],
                "expectedPlanVersion": 1,
                "changeType": change_type,
                "changes": {"reason": "conformance probe"},
                "idempotencyKey": f"{prefix}-changetype-{change_type}",
            },
        )
        if status == 400 and isinstance(payload, dict) and payload.get("code") == "invalid_change_type":
            recorder.failures.append(
                f"edit_active_plan: openapi.json documents changeType {change_type!r} "
                "but the application refuses it with 400 invalid_change_type"
            )

    # Disruption comes last: it deactivates a network resource, so anything that
    # still needs to be planned has to happen before this point.
    dispatcher.call(
        "report_disruption",
        {
            "resourceType": "location",
            "resourceId": world.transfer,
            "reason": "conformance probe",
            "occurredAt": world.at(minutes=45),
            "idempotencyKey": f"{prefix}-disruption",
        },
    )
    dispatcher.expect(
        "report_disruption",
        {
            "resourceType": "unknown",
            "resourceId": world.transfer,
            "reason": "conformance probe",
            "occurredAt": world.at(minutes=45),
            "idempotencyKey": f"{prefix}-bad-disruption",
        },
        400,
    )

    # ------------------------------------------------------------------- audit
    audit = admin.call("search_audit_trail", {})
    if not audit["entries"]:
        recorder.failures.append("the audit search returned nothing")
    admin.call("search_audit_trail", {"entityType": "sample", "entityId": sample_id})
    admin.call("search_audit_trail", {"role": "dispatcher", "success": False})
    admin.call("search_audit_trail", {"limit": 3, "offset": 1, "order": "oldest_first"})
    admin.expect("search_audit_trail", {"limit": 0}, 400)
    admin.expect("search_audit_trail", {"offset": -1}, 400)
    admin.expect("search_audit_trail", {"order": "sideways"}, 400)
    collector.expect("search_audit_trail", {}, 403)

    # ---------------------------------------------------------- infra endpoints
    anonymous.expect("system_readiness", status=200)
    admin.call("read_ui_bootstrap")
    requester.call("read_ui_bootstrap")
    anonymous.expect("read_ui_bootstrap", status=401)
    admin.call("stream_realtime_events", query="?since=0")
    anonymous.expect("stream_realtime_events", status=401, query="?since=0")

    # ------------------------------------------------------------------- report
    covered = {name for name, _, _ in recorder.responses}
    uncovered = sorted(set(recorder.operations) - covered)
    statuses = sorted({status for _, status, _ in recorder.responses})
    print(
        f"AC-77 response conformance: {len(recorder.responses)} responses validated across "
        f"{len(covered)}/{len(recorder.operations)} documented operations; "
        f"statuses exercised: {statuses}"
    )
    if uncovered:
        recorder.failures.append(
            "operations never exercised, so their response schemas are unproven: "
            + ", ".join(uncovered)
        )
    if recorder.failures:
        counted: dict[str, int] = {}
        for failure in recorder.failures:
            counted[failure] = counted.get(failure, 0) + 1
        print(
            f"\nAC-77 FAILED: {len(counted)} distinct findings "
            f"across {len(recorder.failures)} occurrences:"
        )
        for failure, occurrences in counted.items():
            suffix = f"  [x{occurrences}]" if occurrences > 1 else ""
            print("  -", failure + suffix)
        return 1
    print("AC-77 PASSED: every live response matches openapi.json, with no undocumented field")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
