"""AC-74: a retried or duplicated request must not take effect twice.

Every operation that accepts an `idempotencyKey` is driven twice with a byte
identical body, and each replay must satisfy all of:

  * the same HTTP status as the first call
  * a byte-identical response body -- proof it was replayed from the stored
    result rather than executed again
  * exactly ONE effect in the audit trail for that action and that entity
  * no new audit record attributed to the acting user at all
  * unchanged derived state (for plan changes, the journey plan version, which
    increments on every real edit)

Where the operation carries a free-text `reason`, the same key is then reused
with a DIFFERENT body, which must be refused with 409 idempotency_conflict --
the other half of an exactly-once contract.

The set of operations swept here is the set that reaches
`Service.mutate(..., key, ...)` with a non-empty key in
backend/internal/service; `record_worker_location` is deliberately absent
because it is ordered by an explicit `sequence` instead of a key.
"""
from __future__ import annotations

import os
import sys
import time
from pathlib import Path
from typing import Any, Callable

sys.path.insert(0, str(Path(__file__).resolve().parent))

from api_client import Client, World, default_base_url  # noqa: E402


class Sweep:
    def __init__(self, admin: Client, world: World):
        self.admin = admin
        self.world = world
        self.results: list[tuple[str, str]] = []
        self.failures: list[str] = []

    def audit_total(self, **filters: Any) -> int:
        return self.admin.call("search_audit_trail", {**filters, "limit": 5000})["total"]

    def case(
        self,
        operation: str,
        client: Client,
        body: dict[str, Any],
        audit_filter: Callable[[dict[str, Any]], dict[str, Any]],
        expect_status: int = 201,
        fingerprint: Callable[[], Any] | None = None,
        conflict_body: dict[str, Any] | None = None,
        label: str = "",
    ) -> dict[str, Any]:
        name = label or operation
        problems: list[str] = []
        actor_before = self.audit_total(actorId=client.user_id)

        first_status, first = client.raw(operation, body)
        if first_status != expect_status:
            self.failures.append(
                f"{name}: first call returned HTTP {first_status}, expected {expect_status} ({first})"
            )
            self.results.append((name, f"SKIPPED (first call HTTP {first_status})"))
            return {}
        data = first["data"]
        effect = audit_filter(data)
        effects_once = self.audit_total(**effect)
        state_once = fingerprint() if fingerprint else None
        actor_once = self.audit_total(actorId=client.user_id)

        replay_status, replay = client.raw(operation, body)
        effects_twice = self.audit_total(**effect)
        state_twice = fingerprint() if fingerprint else None
        actor_twice = self.audit_total(actorId=client.user_id)

        if replay_status != first_status:
            problems.append(f"replay status {replay_status} != first status {first_status}")
        if replay != first:
            problems.append(
                "replay body differs from the first response, so it was re-executed:\n"
                f"      first  = {first}\n      replay = {replay}"
            )
        if effects_once != 1:
            problems.append(
                f"the first call produced {effects_once} audit records for {effect}, expected 1"
            )
        if effects_twice != effects_once:
            problems.append(
                f"the replay took effect again: {effect} went {effects_once} -> {effects_twice}"
            )
        if actor_twice != actor_once:
            problems.append(
                f"the replay wrote {actor_twice - actor_once} new audit record(s) for the actor"
            )
        if fingerprint and state_twice != state_once:
            problems.append(f"derived state changed on replay: {state_once} -> {state_twice}")
        if actor_once <= actor_before:
            problems.append("the first call wrote no audit record at all, so nothing was verified")

        conflict = ""
        if conflict_body is not None:
            conflict_status, conflict_payload = client.raw(operation, conflict_body)
            code = conflict_payload.get("code") if isinstance(conflict_payload, dict) else None
            if conflict_status != 409 or code != "idempotency_conflict":
                problems.append(
                    "reusing the key with a different body was not refused with "
                    f"409 idempotency_conflict (got {conflict_status} {code})"
                )
            else:
                conflict = ", key reuse with a different body -> 409 idempotency_conflict"

        if problems:
            for problem in problems:
                self.failures.append(f"{name}: {problem}")
            self.results.append((name, "FAILED"))
        else:
            self.results.append(
                (
                    name,
                    f"replay returned the original {first_status} response, "
                    f"exactly 1 effect{conflict}",
                )
            )
        return data


def main() -> int:
    base_url = os.environ.get("APP_BASE_URL") or default_base_url()
    admin_username = os.environ.get("ADMIN_USERNAME", "")
    admin_password = os.environ.get("ADMIN_PASSWORD", "")
    if not admin_username or not admin_password:
        print("ADMIN_USERNAME and ADMIN_PASSWORD are required", file=sys.stderr)
        return 2

    prefix = f"idem{int(time.time())}"
    admin = Client(base_url).login(admin_username, admin_password)
    world = World(admin, prefix).build()
    dispatcher = world.session("dispatcher")
    sweep = Sweep(admin, world)

    def plan_version(journey_id: str) -> int:
        return int(admin.call("read_journey_plan", {"journeyId": journey_id})["planVersion"])

    def planned(data: dict[str, Any]) -> dict[str, Any]:
        return {"action": "journey_planned", "entityId": data["sampleId"]}

    # ------------------------------------------------------------- 1. requests
    main_request = sweep.case(
        "create_collection_request",
        admin,
        world.request_body(f"{prefix}-create"),
        planned,
    )
    sweep.case(
        "ingest_presealed_sample",
        admin,
        {
            **world.request_body(f"{prefix}-presealed"),
            "originType": "node",
            "initialHolderId": world.user_id("handler"),
        },
        planned,
    )

    # ------------------------------------------------------ 2. the happy path
    journey_id = main_request["journeyId"]
    sample_id = main_request["sampleId"]
    plan = admin.call("read_journey_plan", {"journeyId": journey_id})
    collection_assignment = plan["collectionAssignmentId"]
    transport_assignment = plan["finalTransportAssignmentId"]
    laboratory_assignment = plan["laboratoryAssignmentIds"][0]

    collector = world.session(world.find_offered(collection_assignment, ["collector", "collector2"]))
    sweep.case(
        "accept_assignment",
        collector,
        {"assignmentId": collection_assignment, "idempotencyKey": f"{prefix}-accept"},
        lambda data: {"action": "assignment_accepted", "entityId": collection_assignment},
    )
    sweep.case(
        "collect_and_seal_sample",
        collector,
        {
            "sampleId": sample_id,
            "assignmentId": collection_assignment,
            "collectedAt": world.at(minutes=1),
            "containerType": "sealed-tube",
            "observedTemperatureC": 5,
            "idempotencyKey": f"{prefix}-collect",
        },
        lambda data: {"action": "sample_collected_and_sealed", "entityId": sample_id},
        fingerprint=lambda: admin.call("read_sample_lifecycle", {"sampleId": sample_id})["sealed"],
    )

    courier = world.session(world.find_offered(transport_assignment, ["courier"]))
    courier.call(
        "accept_assignment",
        {"assignmentId": transport_assignment, "idempotencyKey": f"{prefix}-accept-transport"},
    )
    handoff = sweep.case(
        "initiate_handoff",
        collector,
        {
            "sampleId": sample_id,
            "fromAssignmentId": collection_assignment,
            "toAssignmentId": transport_assignment,
            "receiverId": courier.user_id,
            "initiatedAt": world.at(minutes=5),
            "idempotencyKey": f"{prefix}-handoff",
        },
        lambda data: {"action": "handoff_initiated", "entityId": data["handoffId"]},
    )
    handoff_id = handoff["handoffId"]
    sweep.case(
        "confirm_handoff",
        collector,
        {
            "handoffId": handoff_id,
            "confirmationSide": "sender",
            "confirmedAt": world.at(minutes=6),
            "idempotencyKey": f"{prefix}-handoff-sender",
        },
        lambda data: {"action": "handoff_confirmed_sender", "entityId": handoff_id},
        label="confirm_handoff (sender)",
    )
    courier.call(
        "confirm_handoff",
        {
            "handoffId": handoff_id,
            "confirmationSide": "receiver",
            "confirmedAt": world.at(minutes=7),
            "idempotencyKey": f"{prefix}-handoff-receiver",
        },
    )
    sweep.case(
        "record_handling_checkpoint",
        courier,
        {
            "sampleId": sample_id,
            "assignmentId": transport_assignment,
            "locationId": world.pickup,
            "containerType": "sealed-tube",
            "observedTemperatureC": 5,
            "recordedAt": world.at(minutes=10),
            "idempotencyKey": f"{prefix}-checkpoint",
        },
        lambda data: {"action": "handling_checkpoint_recorded", "entityId": sample_id},
    )
    sweep.case(
        "update_assignment_progress",
        courier,
        {
            "assignmentId": transport_assignment,
            "status": "at_pickup",
            "occurredAt": world.at(minutes=20),
            "idempotencyKey": f"{prefix}-progress-at-pickup",
        },
        lambda data: {"action": "assignment_progress_updated", "entityId": transport_assignment},
        label="update_assignment_progress (at_pickup)",
    )
    for index, status in enumerate(("picked_up", "in_transit", "arrived", "delivered")):
        courier.call(
            "update_assignment_progress",
            {
                "assignmentId": transport_assignment,
                "status": status,
                "occurredAt": world.at(minutes=21 + index),
                "idempotencyKey": f"{prefix}-progress-{status}",
            },
        )
    laboratory = world.session(world.find_offered(laboratory_assignment, ["lab"]))
    laboratory.call(
        "accept_assignment",
        {"assignmentId": laboratory_assignment, "idempotencyKey": f"{prefix}-accept-laboratory"},
    )
    sweep.case(
        "confirm_laboratory_receipt",
        laboratory,
        {
            "sampleId": sample_id,
            "laboratoryId": world.laboratory,
            "receivedAt": world.at(minutes=40),
            "idempotencyKey": f"{prefix}-receipt",
        },
        lambda data: {"action": "laboratory_receipt_confirmed", "entityId": sample_id},
    )

    # ----------------------------------------------------------- 3. a decline
    declined = world.create_request(f"{prefix}-decline-request")
    declined_assignment = admin.call(
        "read_journey_plan", {"journeyId": declined["journeyId"]}
    )["collectionAssignmentId"]
    decliner = world.session(world.find_offered(declined_assignment, ["collector", "collector2"]))
    sweep.case(
        "decline_assignment",
        decliner,
        {
            "assignmentId": declined_assignment,
            "reason": "already committed",
            "idempotencyKey": f"{prefix}-decline",
        },
        lambda data: {"action": "assignment_declined", "entityId": declined_assignment},
        conflict_body={
            "assignmentId": declined_assignment,
            "reason": "a different reason entirely",
            "idempotencyKey": f"{prefix}-decline",
        },
    )

    # ------------------------------------------------- 4. a laboratory exception
    spoiled = world.create_request(f"{prefix}-exception-request")
    spoiled_sample, spoiled_journey = spoiled["sampleId"], spoiled["journeyId"]
    spoiled_plan = admin.call("read_journey_plan", {"journeyId": spoiled_journey})
    spoiled_collection = spoiled_plan["collectionAssignmentId"]
    spoiled_transport = spoiled_plan["finalTransportAssignmentId"]
    spoiled_laboratory = spoiled_plan["laboratoryAssignmentIds"][0]
    spoiled_collector = world.session(
        world.find_offered(spoiled_collection, ["collector", "collector2"])
    )
    spoiled_collector.call(
        "accept_assignment",
        {"assignmentId": spoiled_collection, "idempotencyKey": f"{prefix}-exc-accept"},
    )
    spoiled_collector.call(
        "collect_and_seal_sample",
        {
            "sampleId": spoiled_sample,
            "assignmentId": spoiled_collection,
            "collectedAt": world.at(minutes=1),
            "containerType": "sealed-tube",
            "observedTemperatureC": 5,
            "idempotencyKey": f"{prefix}-exc-collect",
        },
    )
    spoiled_courier = world.session(world.find_offered(spoiled_transport, ["courier"]))
    spoiled_courier.call(
        "accept_assignment",
        {"assignmentId": spoiled_transport, "idempotencyKey": f"{prefix}-exc-accept-transport"},
    )
    spoiled_handoff = spoiled_collector.call(
        "initiate_handoff",
        {
            "sampleId": spoiled_sample,
            "fromAssignmentId": spoiled_collection,
            "toAssignmentId": spoiled_transport,
            "receiverId": spoiled_courier.user_id,
            "initiatedAt": world.at(minutes=5),
            "idempotencyKey": f"{prefix}-exc-handoff",
        },
    )["handoffId"]
    spoiled_collector.call(
        "confirm_handoff",
        {
            "handoffId": spoiled_handoff,
            "confirmationSide": "sender",
            "confirmedAt": world.at(minutes=6),
            "idempotencyKey": f"{prefix}-exc-sender",
        },
    )
    spoiled_courier.call(
        "confirm_handoff",
        {
            "handoffId": spoiled_handoff,
            "confirmationSide": "receiver",
            "confirmedAt": world.at(minutes=7),
            "idempotencyKey": f"{prefix}-exc-receiver",
        },
    )
    for index, status in enumerate(
        ("at_pickup", "picked_up", "in_transit", "arrived", "delivered")
    ):
        spoiled_courier.call(
            "update_assignment_progress",
            {
                "assignmentId": spoiled_transport,
                "status": status,
                "occurredAt": world.at(minutes=20 + index),
                "idempotencyKey": f"{prefix}-exc-progress-{status}",
            },
        )
    spoiled_lab = world.session(world.find_offered(spoiled_laboratory, ["lab"]))
    spoiled_lab.call(
        "accept_assignment",
        {"assignmentId": spoiled_laboratory, "idempotencyKey": f"{prefix}-exc-accept-lab"},
    )
    sweep.case(
        "record_laboratory_exception",
        spoiled_lab,
        {
            "sampleId": spoiled_sample,
            "laboratoryId": world.laboratory,
            "reason": "seal compromised on arrival",
            "observedAt": world.at(minutes=45),
            "idempotencyKey": f"{prefix}-exception",
        },
        lambda data: {"action": "laboratory_exception_recorded", "entityId": spoiled_sample},
        conflict_body={
            "sampleId": spoiled_sample,
            "laboratoryId": world.laboratory,
            "reason": "a different reason entirely",
            "observedAt": world.at(minutes=45),
            "idempotencyKey": f"{prefix}-exception",
        },
    )

    # --------------------------------------------------------- 5. plan changes
    edited = world.create_request(f"{prefix}-edit-request")
    edited_journey = edited["journeyId"]
    sweep.case(
        "edit_active_plan",
        dispatcher,
        {
            "journeyId": edited_journey,
            "expectedPlanVersion": plan_version(edited_journey),
            "changeType": "add_stop",
            "changes": {"locationId": world.transfer, "position": 1, "dwellMinutes": 0},
            "idempotencyKey": f"{prefix}-edit",
        },
        lambda data: {"action": "active_plan_edited", "entityId": edited_journey},
        fingerprint=lambda: plan_version(edited_journey),
    )

    merge_target = world.create_request(f"{prefix}-merge-target")
    merge_source = world.create_request(f"{prefix}-merge-source")
    target_journey = merge_target["journeyId"]
    sweep.case(
        "merge_journeys",
        dispatcher,
        {
            "targetJourneyId": target_journey,
            "expectedPlanVersion": plan_version(target_journey),
            "sourceJourneyIds": [merge_source["journeyId"]],
            "idempotencyKey": f"{prefix}-merge",
        },
        lambda data: {"action": "journeys_merged", "entityId": target_journey},
        fingerprint=lambda: plan_version(target_journey),
    )
    sweep.case(
        "split_journey",
        dispatcher,
        {
            "journeyId": target_journey,
            "expectedPlanVersion": plan_version(target_journey),
            "sampleIds": [merge_source["sampleId"]],
            "idempotencyKey": f"{prefix}-split",
        },
        lambda data: {"action": "journey_split", "entityId": target_journey},
        fingerprint=lambda: plan_version(target_journey),
    )

    rerouted = world.create_request(f"{prefix}-reroute-request")
    rerouted_journey = rerouted["journeyId"]
    sweep.case(
        "reroute_journey",
        dispatcher,
        {
            "journeyId": rerouted_journey,
            "expectedPlanVersion": plan_version(rerouted_journey),
            "reason": "road closure",
            "idempotencyKey": f"{prefix}-reroute",
        },
        lambda data: {"action": "journey_rerouted", "entityId": rerouted_journey},
        conflict_body={
            "journeyId": rerouted_journey,
            "expectedPlanVersion": 1,
            "reason": "a different reason entirely",
            "idempotencyKey": f"{prefix}-reroute",
        },
    )

    cancelled = world.create_request(f"{prefix}-cancel-request")
    cancelled_journey = cancelled["journeyId"]
    sweep.case(
        "cancel_journey",
        dispatcher,
        {
            "journeyId": cancelled_journey,
            "expectedPlanVersion": plan_version(cancelled_journey),
            "reason": "requester withdrew",
            "idempotencyKey": f"{prefix}-cancel",
        },
        lambda data: {"action": "journey_cancelled", "entityId": cancelled_journey},
        conflict_body={
            "journeyId": cancelled_journey,
            "expectedPlanVersion": 1,
            "reason": "a different reason entirely",
            "idempotencyKey": f"{prefix}-cancel",
        },
    )

    # Disruption last: it deactivates a network resource.
    sweep.case(
        "report_disruption",
        dispatcher,
        {
            "resourceType": "location",
            "resourceId": world.transfer,
            "reason": "flooding",
            "occurredAt": world.at(minutes=50),
            "idempotencyKey": f"{prefix}-disruption",
        },
        lambda data: {"action": "disruption_reported", "entityId": data["disruptionId"]},
        conflict_body={
            "resourceType": "location",
            "resourceId": world.transfer,
            "reason": "a different reason entirely",
            "occurredAt": world.at(minutes=50),
            "idempotencyKey": f"{prefix}-disruption",
        },
    )

    # -------------------------------------------------------------- the report
    width = max(len(name) for name, _ in sweep.results)
    print(f"AC-74 idempotency replay sweep: {len(sweep.results)} keyed operations\n")
    for name, outcome in sweep.results:
        print(f"  {name.ljust(width)}  {outcome}")
    covered = {name.split(" ")[0] for name, _ in sweep.results}
    print(f"\n  distinct keyed operations covered: {len(covered)}")
    if sweep.failures:
        print(f"\nAC-74 FAILED with {len(sweep.failures)} findings:")
        for failure in sweep.failures:
            print("  -", failure)
        return 1
    print("\nAC-74 PASSED: every keyed operation replays exactly once")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
