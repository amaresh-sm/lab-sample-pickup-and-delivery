"""The 11-step custody lifecycle (accept_assignment, collect_and_seal_sample,
initiate_handoff, confirm_handoff, update_assignment_progress,
record_handling_checkpoint, record_worker_location, confirm_laboratory_receipt,
record_laboratory_exception, report_disruption, edit_active_plan) plus
decline_assignment.

THE CASCADE PROBLEM AND ITS FIX
--------------------------------
The old verifier graded this chain as one unbroken sequence keyed by
`activeAssignmentIds[0]` = collection leg, `[1]` = transport leg. That is a
harness ASSUMPTION about plan shape, not a fact about the contract -- a
correct app is free to plan 3 assignments (an extra hub leg), or to offer the
collection leg to whichever worker its own fairness algorithm picks. When
that assumption was wrong, one index-out-of-range or wrong-worker-offer
silently zeroed every downstream step, even though the app had done nothing
wrong.

The fix (`_classify_assignments` below): never index into `activeAssignmentIds`.
Instead, ask the app itself which worker each assignment was offered to, by
scanning `list_role_work` across every worker we seeded for the relevant
field roles (mirrors golden-mongo's own verification helper,
`World.find_offered` in app-setup/api_client.py) -- then drive the chain
using whichever ids and aliases the app actually produced. A step still gets
0 credit if it genuinely fails (e.g. a real upstream planning bug) -- the fix
only removes FALSE zeros caused by the harness's own shape assumptions, per
the task's explicit requirement.

`record_laboratory_exception`, `report_disruption`, `decline_assignment` and
`accept_assignment` each get their OWN freshly-built, self-contained scenario
rather than reusing the happy-path drive above, because they are
terminal/alternate branches (or, for `accept_assignment`, need to run a
negative probe BEFORE the shared drive's own internal accept happens) that
would otherwise collide with (and corrupt) the happy path's shared sample
(you cannot both receive AND except the same sample; disrupting the happy
path's only connection would reroute -- or expire -- the very sample the
other 8 steps depend on; and the shared drive's `_drive_happy_path` call is
monolithic -- by the time it returns, the real collector has already
accepted, so there is no "before accept" moment left to probe from inside
it).

`accept_assignment`'s negative probe closes a gap found by this session's
mutant-testing pass (`anchors/mutants/assignment-stealing`, mirroring a real
production bug in the sibling golden-postgres app): AC-31 requires that "an
assignment cannot be accepted by anyone other than the offered worker,
including when no worker was pre-assigned" -- a same-role bystander who was
NEVER offered the collection leg must be rejected 409 `offer_not_available`
while the assignment is still genuinely pending (offered to someone else),
not merely after it has already been taken (that would exercise a
different code path, `assignment_taken`, and would not prove the
offer-identity check this AC actually protects).
"""
from __future__ import annotations

import json

from oracle import Capability, CapabilityReport, Verdict, planned_journey_ok, planning_failure_report, retry_durability_write

WORKER_ALIASES = ["collector", "collector2", "courier", "courier2"]

# Isolation group for every capability that consumes the SHARED
# `_drive_happy_path` journey cached in `engine.shared["lc_happy"]`. They each
# grade a DIFFERENT step of that one drive, so an app reset between them would
# wipe the journey they are grading -- they must share one app instance, and the
# instance must be pristine relative to the OTHER ~44 capabilities (leftover
# unparked field workers from predecessors steal the offer and collapse the
# whole group to 0: measured 0.857 pristine vs 0.000 with 20 leftovers).
# Members: the 6 built by `_make_step_capability` below, plus `confirm_handoff`
# and `edit_active_plan`, which call the drive directly.
LIFECYCLE_DRIVE_GROUP = "lifecycle_drive"


def _read_capacity(engine, resource_id: str) -> dict:
    """A resource's live `Capacity` object (capacityUnits/reservedUnits/...),
    read the only way this HTTP-only engine ever reads anything: via
    `read_operations_state`'s `locations[]`/`connections[]`, never a direct
    datastore query. Returns `{}` if the resource isn't found in either list.
    """
    status, payload = engine.admin.raw("read_operations_state", {"filters": {}})
    data = engine.admin.data(status, payload)
    for loc in data.get("locations", []) or []:
        if loc.get("locationId") == resource_id:
            return loc.get("capacity") or {}
    for conn in data.get("connections", []) or []:
        if conn.get("connectionId") == resource_id:
            return conn.get("capacity") or {}
    return {}


def _discover_owner(world, assignment_id: str, aliases: list[str]) -> str | None:
    for alias in aliases:
        sess = world.session(alias)
        status, payload = sess.raw("list_role_work", {})
        data = sess.data(status, payload)
        for bucket in ("pending", "inProgress", "completed"):
            for item in data.get(bucket, []) or []:
                if item.get("assignmentId") == assignment_id:
                    return alias
    return None


def _classify_assignments(world, assignment_ids: list[str], aliases: list[str]) -> dict[str, str | None]:
    """id -> alias, discovered from the app's own list_role_work -- never a
    hardcoded position. See module docstring."""
    return {aid: _discover_owner(world, aid, aliases) for aid in assignment_ids}


def _seed_lifecycle_world(engine, tag: str):
    world = engine.new_world(tag)
    st = world.sample_type("LC-ST")
    pickup = world.location("pickup", ["collection_point"], 71.0, 71.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 71.01, 71.01)
    conn = world.connection(pickup["locationId"], lab["locationId"], travelMinutes=10, costUnits=1, cooled=True)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for r in (pickup["locationId"], lab["locationId"], conn["connectionId"]):
        world.capacity(r, units=20, cooled_units=20)
    world.user("collector", ["collection_professional"], worker=True)
    world.user("collector2", ["collection_professional"], worker=True)
    world.user("courier", ["delivery_partner"], worker=True)
    world.user("courier2", ["delivery_partner"], worker=True)
    world.user("lab_op", ["laboratory_team"], worker=True, location_id=lab["locationId"])
    world.user("dispatcher", ["dispatcher"])
    # Fairness offers the collection/transport legs to the longest-waiting
    # eligible worker across the WHOLE app, not just this world's seed -- so
    # every field worker this engine created in an earlier capability must be
    # parked, or our freshly-seeded collector/courier can never win the offer.
    ours = {world.user_id(a) for a in ("collector", "collector2", "courier", "courier2", "lab_op")}
    engine.park_field_workers(exclude=ours)
    return world, st, pickup, lab, conn


def _plan_request_body(world, st, pickup) -> dict:
    return {
        "requesterId": world.default_requester_id(),
        "origin": {"latitude": 71.0, "longitude": 71.0},
        "originType": "home",
        # requestedAt must be EARLIER than earliestCollectionAt so the
        # window strictly FOLLOWS the request. A candidate that requires
        # earliestCollectionAt > requestedAt (a reading the contract is silent
        # on) rejects the old requestedAt=now / earliest=now-5min (earliest
        # BEFORE request) with 400 invalid_collection_window, cascading to
        # every lifecycle capability that plans a request. Pushing requestedAt
        # to now-10min keeps golden (lenient) passing and lets a strict-after
        # candidate through; window still contains now, ends now+4h, deadline
        # now+10h. (World.__init__/request_body carries the same fix.)
        "requestedAt": world.at(minutes=-10),
        "deliveryDeadline": world.at(hours=10),
        "earliestCollectionAt": world.at(minutes=-5),
        "latestCollectionAt": world.at(hours=4),
        "sampleTypeId": st["sampleTypeId"],
        "containerType": "sealed-tube",
        "temperatureMinC": 2,
        "temperatureMaxC": 8,
        "incompatibleSampleTypes": [],
        "idempotencyKey": world.key("lc"),
    }


def _plan_serviceable(engine, world, st, pickup) -> dict:
    """Plan the happy-path request, but first wait for the just-added pickup to
    be reflected in the published serviceability calculation (see
    World.await_serviceable) so an async/versioned candidate isn't raced into a
    false `unserviceable` that would cascade to every lifecycle capability."""
    return world.plan_journey(_plan_request_body(world, st, pickup))


def _drive_happy_path(engine) -> dict:
    """Build one journey and drive edit_active_plan -> accept -> collect ->
    checkpoint -> worker-location -> handoff(initiate+confirm both sides) ->
    progress -> laboratory receipt. Memoised so the 9 capability specs below
    all grade the SAME drive."""
    if "lc_happy" in engine.shared:
        return engine.shared["lc_happy"]

    world, st, pickup, lab, conn = _seed_lifecycle_world(engine, "lc")
    steps: dict[str, dict] = {}
    result = {"world": world, "steps": steps, "driven": False, "pickup": pickup, "laboratory": lab}
    engine.shared["lc_happy"] = result

    # capacity-leak-on-receipt: capacity.py's own docstring claims the
    # reserve/release lifecycle (AC-47/48/49 -- reserved on plan, released
    # on EVERY terminal path, never overbooked) is checked here, in
    # custody_lifecycle.py, where a journey is actually driven to
    # completion. Snapshot each resource this journey will use BEFORE
    # planning so the confirm_laboratory_receipt capability below can prove
    # reservedUnits actually returns to baseline after the terminal
    # confirm_laboratory_receipt call, not just that the call succeeded.
    capacity_resource_ids = (pickup["locationId"], lab["locationId"], conn["connectionId"])
    result["capacity_before"] = {rid: _read_capacity(engine, rid) for rid in capacity_resource_ids}

    created = _plan_serviceable(engine, world, st, pickup)
    sample_id, journey_id = created.get("sampleId"), created.get("journeyId")
    assignment_ids = created.get("activeAssignmentIds") or []
    result.update(sample_id=sample_id, journey_id=journey_id, created=created)
    # Only a genuinely planned journey (serviceable + journeyId + >=1
    # assignment) can drive the chain below. A candidate whose planner failed
    # for this network (unserviceable / no journey / 0 assignments) would send
    # `_classify_assignments` into iterating an empty/None list and every step
    # into "step never attempted" -- surface that as a single clean, diagnostic
    # planning-failure instead (the step caps read `planning_ok`). Golden
    # always plans here, so `planning_ok` is True and the drive is unchanged.
    result["planning_ok"] = planned_journey_ok(created)
    if not result["planning_ok"]:
        return result
    result["driven"] = True

    # ---- edit_active_plan: reassign the transport leg BEFORE anyone accepts
    # it (a safe, low-collateral dispatcher override), then re-discover who
    # holds it afterwards -- the discovery step, not an assumed index, is
    # what makes the rest of the drive correct even if this edit changes who
    # the transport leg belongs to.
    owners_before = _classify_assignments(world, assignment_ids, WORKER_ALIASES)
    transport_aid_guess = next((aid for aid, a in owners_before.items() if a in ("courier", "courier2")), None)
    plan_status, plan_payload = world.session("dispatcher").raw("read_journey_plan", {"journeyId": journey_id})
    plan_version = world.session("dispatcher").data(plan_status, plan_payload).get("planVersion", 1)
    result["plan_version_seen"] = plan_version
    if transport_aid_guess:
        alt_courier = "courier2" if owners_before.get(transport_aid_guess) == "courier" else "courier"
        edit_body = {
            "journeyId": journey_id,
            "expectedPlanVersion": plan_version,
            "changeType": "replace_worker",
            "changes": {"assignmentId": transport_aid_guess, "workerId": world.user_id(alt_courier), "reason": "capability probe"},
            "idempotencyKey": world.key("edit"),
        }
        dispatcher_sess = world.session("dispatcher")
        est, epayload = dispatcher_sess.raw("edit_active_plan", edit_body)
        steps["edit_active_plan"] = {"status": est, "payload": epayload, "data": dispatcher_sess.data(est, epayload), "code": dispatcher_sess.code(epayload)}
        # AC-63: a stale replay of the SAME version, with a genuinely
        # DIFFERENT key+body, must now be refused.
        stale_status, stale_payload = dispatcher_sess.raw(
            "edit_active_plan",
            {
                "journeyId": journey_id,
                "expectedPlanVersion": plan_version,
                "changeType": "replace_worker",
                "changes": {"assignmentId": transport_aid_guess, "workerId": world.user_id(alt_courier), "reason": "stale replay"},
                "idempotencyKey": world.key("edit-stale"),
            },
        )
        result["edit_stale_probe"] = {"status": stale_status, "code": dispatcher_sess.code(stale_payload)}
        # Idempotency: SAME edit_body (SAME idempotencyKey, captured once
        # above) replayed -- distinct from `edit_stale_probe` above, which
        # deliberately uses a DIFFERENT key/body to test the stale-version
        # guard. Only run this when the original actually succeeded.
        if isinstance(est, int) and 200 <= est < 300:
            replay_status, replay_payload = dispatcher_sess.raw("edit_active_plan", edit_body)
            result["edit_active_plan_replay"] = {"status": replay_status, "data": dispatcher_sess.data(replay_status, replay_payload)}
    else:
        steps["edit_active_plan"] = {"status": 0, "payload": None, "code": "no_transport_leg_discovered"}

    owners = _classify_assignments(world, assignment_ids, WORKER_ALIASES)
    collection_aid = next((aid for aid, a in owners.items() if a in ("collector", "collector2")), None)
    transport_aid = next((aid for aid, a in owners.items() if a in ("courier", "courier2")), None)
    collector_alias, courier_alias = owners.get(collection_aid), owners.get(transport_aid)
    result.update(collection_aid=collection_aid, transport_aid=transport_aid, owners=owners)
    if not (collection_aid and transport_aid and collector_alias and courier_alias):
        result["note"] = "could not classify collection/transport legs from the app's own list_role_work -- steps below stay undriven"
        return result

    collector, courier = world.session(collector_alias), world.session(courier_alias)

    def do(cap_id, sess, op, body, replay=False):
        status, payload = sess.raw(op, body)
        steps[cap_id] = {"status": status, "payload": payload, "data": sess.data(status, payload), "code": sess.code(payload)}
        if replay and isinstance(status, int) and 200 <= status < 300:
            # SAME body dict (same idempotencyKey, same everything) as the
            # call just made -- world.key()/world.moment() both mint a NEW
            # value per call, so re-deriving either for "the replay" would
            # silently test two DIFFERENT requests, not a true replay (see
            # the confirm_handoff fix above for the real bug this caused
            # when done wrong).
            replay_status, replay_payload = sess.raw(op, body)
            result[f"{cap_id}_replay"] = {"status": replay_status, "data": sess.data(replay_status, replay_payload)}
        return status, payload

    do("accept_assignment", collector, "accept_assignment", {"assignmentId": collection_aid, "idempotencyKey": world.key("acc")})
    # A checkpoint is an en-route observation, so it must be recorded WHILE
    # the collection assignment is still active -- collect_and_seal_sample
    # completes that assignment, so a checkpoint recorded afterwards against
    # the now-inactive assignment is correctly refused (assignment_inactive).
    do(
        "record_handling_checkpoint",
        collector,
        "record_handling_checkpoint",
        {"sampleId": sample_id, "assignmentId": collection_aid, "locationId": pickup["locationId"], "containerType": "sealed-tube", "observedTemperatureC": 5, "recordedAt": world.moment(), "idempotencyKey": world.key("chk")},
        replay=True,
    )
    do(
        "collect_and_seal_sample",
        collector,
        "collect_and_seal_sample",
        {"sampleId": sample_id, "assignmentId": collection_aid, "collectedAt": world.moment(), "containerType": "sealed-tube", "observedTemperatureC": 5, "idempotencyKey": world.key("col")},
        replay=True,
    )
    do(
        "record_worker_location",
        collector,
        "record_worker_location",
        {"assignmentId": collection_aid, "coordinate": {"latitude": 71.0, "longitude": 71.0}, "sequence": 1, "recordedAt": world.moment()},
    )
    courier.raw("accept_assignment", {"assignmentId": transport_aid, "idempotencyKey": world.key("acc2")})

    hstatus, hpayload = do(
        "initiate_handoff",
        collector,
        "initiate_handoff",
        {"sampleId": sample_id, "fromAssignmentId": collection_aid, "toAssignmentId": transport_aid, "receiverId": world.user_id(courier_alias), "initiatedAt": world.moment(), "idempotencyKey": world.key("iho")},
        replay=True,
    )
    handoff_id = collector.data(hstatus, hpayload).get("handoffId")
    result["handoff_id"] = handoff_id
    if handoff_id:
        s_status, s_payload = collector.raw("confirm_handoff", {"handoffId": handoff_id, "confirmationSide": "sender", "confirmedAt": world.moment(), "idempotencyKey": world.key("cs")})
        result["one_sided_confirm"] = collector.data(s_status, s_payload)
        # Captured ONCE into a variable and reused verbatim below for the
        # replay -- world.key()/world.moment() both mint a NEW value on every
        # call (a fresh sequence number, the real wall clock), so calling
        # either again for "the same" call would silently test two DIFFERENT
        # requests, not a true idempotency-key replay. (This was a real,
        # pre-existing bug: the replay below used to call world.key("cr")
        # again, producing a different key than the original -- so AC-54's
        # replay-safety was never actually being exercised, only "does
        # confirming an already-confirmed handoff again, with a genuinely
        # NEW key, still work" -- a different and weaker property.)
        confirm_receiver_body = {"handoffId": handoff_id, "confirmationSide": "receiver", "confirmedAt": world.moment(), "idempotencyKey": world.key("cr")}
        r_status, r_payload = courier.raw("confirm_handoff", confirm_receiver_body)
        steps["confirm_handoff"] = {"status": r_status, "payload": r_payload, "code": courier.code(r_payload)}
        result["both_sided_confirm"] = courier.data(r_status, r_payload)
        # AC-54: replay the receiver confirmation -- SAME body, SAME
        # idempotencyKey as above -- must not double-transfer.
        replay_status, replay_payload = courier.raw("confirm_handoff", confirm_receiver_body)
        result["handoff_replay"] = courier.data(replay_status, replay_payload)

    do(
        "update_assignment_progress",
        courier,
        "update_assignment_progress",
        {"assignmentId": transport_aid, "status": "delivered", "occurredAt": world.moment(), "idempotencyKey": world.key("prg")},
        replay=True,
    )
    lab_op = world.session("lab_op")
    do(
        "confirm_laboratory_receipt",
        lab_op,
        "confirm_laboratory_receipt",
        {"sampleId": sample_id, "laboratoryId": lab["locationId"], "receivedAt": world.moment(), "idempotencyKey": world.key("recv")},
        replay=True,
    )
    # AC-47/48/49, the receipt-side half: a successful confirm_laboratory_receipt
    # is a terminal path (journey.Status -> "completed") and must release its
    # reservations exactly like every OTHER terminal path already does
    # (cancelJourneyState, expireSample, expirePulseWithoutWorker,
    # replaceJourneyRoute all call releaseReservations -- confirmReceipt is
    # the one path this session's mutation testing flagged as unchecked).
    receipt_step = steps.get("confirm_laboratory_receipt", {})
    if isinstance(receipt_step.get("status"), int) and 200 <= receipt_step["status"] < 300:
        result["capacity_after"] = {rid: _read_capacity(engine, rid) for rid in capacity_resource_ids}
    return result


def _accepted(steps: dict, cap_id: str) -> Verdict:
    step = steps.get(cap_id)
    if not step:
        return Verdict(ok=False, detail="step never attempted (upstream planning did not produce a driveable journey)")
    ok = isinstance(step.get("status"), int) and 200 <= step["status"] < 300
    return Verdict(ok=ok, detail=f"status={step.get('status')} code={step.get('code')!r}")


# --------------------------------------------------------------- durability
# DUR4: the 8 capabilities below all grade a DIFFERENT step of ONE drive
# (`_drive_happy_path`). durability_write reuses that SAME memoized helper --
# the drive runs once per process (durability-write's own, separate from
# whatever `score` already did), and every one of the 8 durability_write
# calls after the first gets the already-completed drive back for free, same
# as the 8 positive probes already do. Each still gets its OWN read-back
# target, not a copy-pasted identical check, because a mid-drive step
# leaving stale/wrong evidence behind is a real, distinct failure mode from
# a later step's -- see the per-function docstrings below for what each one
# specifically proves.


def _durability_write_lifecycle_drive(engine) -> dict:
    drive = _drive_happy_path(engine)
    if not drive.get("planning_ok"):
        return {}
    return {
        "sample_id": drive.get("sample_id"),
        "journey_id": drive.get("journey_id"),
        "handoff_id": drive.get("handoff_id"),
        "collection_aid": drive.get("collection_aid"),
        "transport_aid": drive.get("transport_aid"),
        "steps": {cap_id: step.get("status") for cap_id, step in drive.get("steps", {}).items()},
    }


def _durability_verify_collect_and_seal_sample(engine, sentinel: dict) -> Verdict:
    """collect_and_seal_sample's durable fact: the sample itself, sealed, is
    still discoverable -- checked via the same direct-read + audit-trail
    fallback every other create-shaped check in this pass uses."""
    sample_id = sentinel.get("sample_id")
    if not (sample_id and sentinel.get("steps", {}).get("collect_and_seal_sample") in range(200, 300)):
        return Verdict(ok=False, detail="no sealed sample_id recorded at write time (step never succeeded)")
    via = engine.observe(engine.admin, sample_id, direct=("read_sample_state", sample_id), entity_type="sample")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _durability_verify_record_handling_checkpoint(engine, sentinel: dict) -> Verdict:
    """The checkpoint is an audit/history event, not a standalone resource
    with its own id -- its only observable trace is the sample's own
    audit-trail entry for the `record_handling_checkpoint` action, so check
    THAT specifically rather than just "the sample still exists" (which
    every other step in this drive would also satisfy)."""
    sample_id = sentinel.get("sample_id")
    if not (sample_id and sentinel.get("steps", {}).get("record_handling_checkpoint") in range(200, 300)):
        return Verdict(ok=False, detail="no sample_id recorded at write time (step never succeeded)")
    status, payload = engine.admin.raw("search_audit_trail", {"entityType": "sample", "limit": 200, "order": "oldest_first"})
    data = engine.admin.data(status, payload)
    entries = data.get("entries") or []
    hit = any(
        e.get("success") is not False and e.get("entityId") == sample_id and "checkpoint" in json.dumps(e.get("action", ""), default=str).lower()
        for e in entries
    )
    via = engine.observe(engine.admin, sample_id, entity_type="sample") if not hit else "audit-trail"
    return Verdict(ok=hit or via is not None, detail=f"post-restart checkpoint-specific audit hit={hit}; fallback via={via}")


def _durability_verify_record_worker_location(engine, sentinel: dict) -> Verdict:
    """No standalone id either -- the collection assignment itself is the
    thing this call attaches location history to, so checking IT is still
    observable is the closest available proxy."""
    collection_aid = sentinel.get("collection_aid")
    if not (collection_aid and sentinel.get("steps", {}).get("record_worker_location") in range(200, 300)):
        return Verdict(ok=False, detail="no collection_aid recorded at write time (step never succeeded)")
    via = engine.observe(engine.admin, collection_aid, entity_type="assignment")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _durability_verify_initiate_handoff(engine, sentinel: dict) -> Verdict:
    """initiate_handoff CREATES the handoff object -- its id is the natural,
    specific sentinel (distinct from confirm_handoff's, which needs the
    object to have moved to a confirmed state, not just exist)."""
    handoff_id = sentinel.get("handoff_id")
    if not handoff_id:
        return Verdict(ok=False, detail="no handoff_id recorded at write time (initiate never produced one)")
    via = engine.observe(engine.admin, handoff_id, entity_type="handoff")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _durability_verify_confirm_handoff(engine, sentinel: dict) -> Verdict:
    """Stronger than initiate's: the handoff must still show as CONFIRMED
    (custody moved), not merely still exist -- read_journey_plan's legs
    reflect the transport worker now holding the sample; fall back to plain
    existence if that specific field isn't independently readable."""
    handoff_id, journey_id = sentinel.get("handoff_id"), sentinel.get("journey_id")
    if not (handoff_id and sentinel.get("steps", {}).get("confirm_handoff") in range(200, 300)):
        return Verdict(ok=False, detail="no confirmed handoff_id recorded at write time")
    via = engine.observe(engine.admin, handoff_id, entity_type="handoff")
    plan_ok = False
    if journey_id:
        status, payload = engine.admin.raw("read_journey_plan", {"journeyId": journey_id})
        plan_ok = status == 200
    return Verdict(ok=(via is not None) or plan_ok, detail=f"post-restart observe(handoff)={via}; journey still readable={plan_ok}")


def _durability_verify_update_assignment_progress(engine, sentinel: dict) -> Verdict:
    """The transport assignment should still be observable in its progressed
    (delivered) state -- checked against the assignment id itself, distinct
    from confirm_laboratory_receipt's stronger sample-level terminal check
    below."""
    transport_aid = sentinel.get("transport_aid")
    if not (transport_aid and sentinel.get("steps", {}).get("update_assignment_progress") in range(200, 300)):
        return Verdict(ok=False, detail="no transport_aid recorded at write time (step never succeeded)")
    via = engine.observe(engine.admin, transport_aid, entity_type="assignment")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _durability_verify_confirm_laboratory_receipt(engine, sentinel: dict) -> Verdict:
    """The strongest check in this group: this is the TERMINAL step of the
    whole drive -- the sample should still read back as received (or, at
    minimum, still discoverable at all) after the restart, not reverted to
    an earlier state."""
    sample_id = sentinel.get("sample_id")
    if not (sample_id and sentinel.get("steps", {}).get("confirm_laboratory_receipt") in range(200, 300)):
        return Verdict(ok=False, detail="no received sample_id recorded at write time (step never succeeded)")
    status, payload = engine.admin.raw("read_sample_state", path_param=sample_id)
    data = engine.admin.data(status, payload)
    # `state` is a NESTED object ({"status": "..."}) per the pinned
    # SampleState schema, not a top-level `status` field -- checked directly
    # against the contract after this came back empty on golden's own
    # correctly-received sample and only passed via the audit-trail fallback.
    sample_status = str((data.get("state") or {}).get("status", "")).lower()
    still_received = status == 200 and sample_status in ("received", "delivered", "completed")
    via = engine.observe(engine.admin, sample_id, entity_type="sample") if not still_received else "direct-read"
    return Verdict(ok=still_received or via is not None, detail=f"post-restart read_sample_state status={status} sample.state.status={sample_status!r}; fallback via={via}")


def _durability_write_edit_active_plan(engine) -> dict:
    drive = _drive_happy_path(engine)
    if not drive.get("planning_ok"):
        return {}
    edit_step = drive.get("steps", {}).get("edit_active_plan", {})
    if edit_step.get("status") not in range(200, 300):
        return {}
    return {"journey_id": drive.get("journey_id")}


def _durability_verify_edit_active_plan(engine, sentinel: dict) -> Verdict:
    journey_id = sentinel.get("journey_id")
    if not journey_id:
        return Verdict(ok=False, detail="no journey_id recorded at write time (edit never succeeded)")
    status, payload = engine.admin.raw("read_journey_plan", {"journeyId": journey_id})
    return Verdict(ok=status == 200, detail=f"post-restart read_journey_plan status={status}")


def _durability_write_accept_assignment_once(engine) -> dict:
    world, st, pickup, lab, conn = _seed_lifecycle_world(engine, "accdur")
    created = _plan_serviceable(engine, world, st, pickup)
    if not planned_journey_ok(created):
        return {}
    assignment_ids = created.get("activeAssignmentIds") or []
    owners = _classify_assignments(world, assignment_ids, WORKER_ALIASES)
    collection_aid = next((aid for aid, a in owners.items() if a in ("collector", "collector2")), None)
    if not collection_aid:
        return {}
    owner_sess = world.session(owners[collection_aid])
    status, payload = owner_sess.raw("accept_assignment", {"assignmentId": collection_aid, "idempotencyKey": world.key("accdur-real")})
    if status != 201:
        return {}
    return {"assignment_id": collection_aid}


def _durability_write_accept_assignment(engine) -> dict:
    return retry_durability_write(_durability_write_accept_assignment_once, engine)


def _durability_verify_accept_assignment(engine, sentinel: dict) -> Verdict:
    assignment_id = sentinel.get("assignment_id")
    if not assignment_id:
        return Verdict(ok=False, detail="no assignment_id recorded at write time")
    via = engine.observe(engine.admin, assignment_id, entity_type="assignment")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _step_idempotency(cap_id: str):
    """Generic idempotency check for a `do(cap_id, ..., replay=True)`-captured
    step in `_drive_happy_path`: the replay (SAME idempotencyKey+body,
    captured once and reused verbatim -- see `do`'s own comment on why that
    matters) must return the exact same status and full response body as
    the original call. Full-body equality rather than cherry-picked fields
    is deliberate -- it doesn't require knowing which specific fields matter
    for a given operation, and it's a STRICTER check than field-picking
    would be (any drift at all is caught, not just drift in fields someone
    thought to check)."""
    def check(drive: dict) -> Verdict:
        step = drive["steps"].get(cap_id, {})
        replay = drive.get(f"{cap_id}_replay")
        if not replay:
            return Verdict(ok=False, detail=f"no replay captured for {cap_id} (original call may not have succeeded)")
        identical = replay.get("status") == step.get("status") and replay.get("data") == step.get("data")
        return Verdict(
            ok=identical,
            detail=f"replay {cap_id} (SAME idempotencyKey+body) -> status={replay.get('status')} data={replay.get('data')} (vs original status={step.get('status')} data={step.get('data')})",
        )
    return check


def _make_step_capability(cap_id: str, description: str, extra_check=None, idempotency_check=None, durability_write=None, durability_verify=None) -> Capability:
    def run(engine) -> CapabilityReport:
        drive = _drive_happy_path(engine)
        if not drive.get("planning_ok"):
            return planning_failure_report(cap_id, drive.get("created"))
        positive = _accepted(drive["steps"], cap_id)
        if positive.ok and extra_check:
            positive = extra_check(drive, positive)
        idempotency = idempotency_check(drive) if (positive.ok and idempotency_check) else None
        return CapabilityReport(positive=positive, idempotency=idempotency, notes="graded from the shared, discovery-driven happy-path lifecycle drive")

    return Capability(
        cap_id, "lifecycle", description, run,
        priority=cap_id in ("initiate_handoff", "confirm_handoff", "edit_active_plan"),
        group=LIFECYCLE_DRIVE_GROUP,
        durability_write=durability_write,
        durability_verify=durability_verify,
    )


def _confirm_handoff_extra(drive: dict, positive: Verdict) -> Verdict:
    # AC-51/52/53: one-sided confirm must not move custody; both-sided must.
    # (The replay/no-double-transfer check that used to live here is now its
    # own `idempotency` dimension -- see _confirm_handoff_idempotency --
    # since it tests a genuinely separate property, same as every other
    # capability's idempotency Verdict this session.)
    one_sided = drive.get("one_sided_confirm", {})
    both_sided = drive.get("both_sided_confirm", {})
    sender_still_holder = one_sided.get("status") == "pending" and one_sided.get("senderConfirmed") is True and one_sided.get("receiverConfirmed") is False
    flipped_on_both = both_sided.get("status") == "confirmed" and both_sided.get("senderConfirmed") is True and both_sided.get("receiverConfirmed") is True
    ok = positive.ok and sender_still_holder and flipped_on_both
    return Verdict(
        ok=ok,
        detail=(
            f"one-sided: status={one_sided.get('status')} sender={one_sided.get('senderConfirmed')} receiver={one_sided.get('receiverConfirmed')}; "
            f"both-sided: status={both_sided.get('status')} holder_flip_ok={flipped_on_both}"
        ),
        evidence={"one_sided": one_sided, "both_sided": both_sided},
    )


def _confirm_handoff_idempotency(drive: dict) -> Verdict:
    """AC-54: `_drive_happy_path` already replays the receiver confirmation
    with the SAME idempotencyKey (`handoff_replay`) -- this was previously
    folded silently into `positive` above; exposed here as its own
    dimension, same as every other capability's idempotency Verdict."""
    both_sided = drive.get("both_sided_confirm", {})
    replay = drive.get("handoff_replay", {})
    no_double_transfer = replay.get("currentHolderId") == both_sided.get("currentHolderId")
    return Verdict(
        ok=no_double_transfer,
        detail=f"replay confirm_handoff (SAME idempotencyKey) -> currentHolderId={replay.get('currentHolderId')} (vs original {both_sided.get('currentHolderId')})",
        evidence={"replay": replay, "both_sided": both_sided},
    )


def _confirm_laboratory_receipt_extra(drive: dict, positive: Verdict) -> Verdict:
    """AC-47/48/49 (the capacity RESERVE/RELEASE lifecycle): `capacity.py`'s
    own module docstring claims this is checked here, in
    custody_lifecycle.py, where a journey is actually driven to completion
    -- but this session's mutation testing (`anchors/mutants/
    capacity-leak-on-receipt`) found neither this file nor disruption.py
    actually read `reservedUnits`/`cooledReservedUnits` anywhere, so a real
    leak on the confirm_laboratory_receipt terminal path (the one path that
    is NOT covered by cancelJourneyState/expireSample/
    expirePulseWithoutWorker/replaceJourneyRoute, which all correctly call
    releaseReservations) would go completely unnoticed. Compares the
    pickup/laboratory/connection capacity snapshot taken before planning
    (`_drive_happy_path`'s `capacity_before`) against the snapshot taken
    right after this SAME journey's confirm_laboratory_receipt succeeded
    (`capacity_after`): every reservation this journey held must be back at
    (or below) its pre-plan baseline, not still pinned.
    """
    before = drive.get("capacity_before") or {}
    after = drive.get("capacity_after") or {}
    if not before or not after:
        return Verdict(
            ok=False,
            detail=f"{positive.detail}; capacity snapshots were not captured (see _drive_happy_path) -- cannot check the reserve/release lifecycle",
        )
    leaks = {}
    for rid, before_cap in before.items():
        after_cap = after.get(rid) or {}
        for field in ("reservedUnits", "cooledReservedUnits"):
            b, a = before_cap.get(field), after_cap.get(field)
            if isinstance(b, (int, float)) and isinstance(a, (int, float)) and a > b:
                leaks[f"{rid}.{field}"] = {"before": b, "after": a}
    ok = positive.ok and not leaks
    return Verdict(
        ok=ok,
        detail=f"{positive.detail}; capacity reserve/release (AC-47/48/49): before={before} after={after} leaks={leaks or 'none'}",
        evidence={"capacity_before": before, "capacity_after": after},
    )


def _run_record_laboratory_exception(engine) -> CapabilityReport:
    # golden-mongo's `laboratoryExceptionReady` requires the sample to
    # already be `delivered` (final transport leg completed AT the target
    # laboratory) before an exception can be recorded -- an exception is "the
    # lab found something wrong on arrival", not a substitute for the
    # collection step. So this drives the FULL happy path through delivery
    # (accept -> checkpoint -> collect -> handoff both sides -> transport
    # accept -> delivered) and only then diverges from confirm_laboratory_receipt.
    world, st, pickup, lab, conn = _seed_lifecycle_world(engine, "lcexc")
    created = _plan_serviceable(engine, world, st, pickup)
    if not planned_journey_ok(created):
        return planning_failure_report("record_laboratory_exception", created)
    sample_id = created.get("sampleId")
    assignment_ids = created.get("activeAssignmentIds") or []
    owners = _classify_assignments(world, assignment_ids, WORKER_ALIASES)
    collection_aid = next((aid for aid, a in owners.items() if a in ("collector", "collector2")), None)
    transport_aid = next((aid for aid, a in owners.items() if a in ("courier", "courier2")), None)
    if not (sample_id and collection_aid and transport_aid):
        return CapabilityReport(positive=Verdict(ok=False, detail="could not plan+classify a journey to drive to an exception"))

    collector, courier = world.session(owners[collection_aid]), world.session(owners[transport_aid])
    collector.raw("record_handling_checkpoint", {"sampleId": sample_id, "assignmentId": collection_aid, "locationId": pickup["locationId"], "containerType": "sealed-tube", "observedTemperatureC": 5, "recordedAt": world.moment(), "idempotencyKey": world.key("chk")})
    collector.raw("accept_assignment", {"assignmentId": collection_aid, "idempotencyKey": world.key("acc")})
    collector.raw(
        "collect_and_seal_sample",
        {"sampleId": sample_id, "assignmentId": collection_aid, "collectedAt": world.moment(), "containerType": "sealed-tube", "observedTemperatureC": 5, "idempotencyKey": world.key("col")},
    )
    courier.raw("accept_assignment", {"assignmentId": transport_aid, "idempotencyKey": world.key("acc2")})
    hstatus, hpayload = collector.raw(
        "initiate_handoff",
        {"sampleId": sample_id, "fromAssignmentId": collection_aid, "toAssignmentId": transport_aid, "receiverId": world.user_id(owners[transport_aid]), "initiatedAt": world.moment(), "idempotencyKey": world.key("iho")},
    )
    handoff_id = collector.data(hstatus, hpayload).get("handoffId")
    if handoff_id:
        collector.raw("confirm_handoff", {"handoffId": handoff_id, "confirmationSide": "sender", "confirmedAt": world.moment(), "idempotencyKey": world.key("cs")})
        courier.raw("confirm_handoff", {"handoffId": handoff_id, "confirmationSide": "receiver", "confirmedAt": world.moment(), "idempotencyKey": world.key("cr")})
    courier.raw("update_assignment_progress", {"assignmentId": transport_aid, "status": "delivered", "occurredAt": world.moment(), "idempotencyKey": world.key("prg")})

    lab_op = world.session("lab_op")
    exception_body = {"sampleId": sample_id, "laboratoryId": lab["locationId"], "reason": "capability probe: container compromised", "observedAt": world.moment(), "idempotencyKey": world.key("exc")}
    status, payload = lab_op.raw("record_laboratory_exception", exception_body)
    data = lab_op.data(status, payload)
    positive = Verdict(ok=status == 201 and bool(data.get("exceptionId")), detail=f"status={status} exceptionId={data.get('exceptionId')} sampleStatus={data.get('status')}")

    bad_status, bad_payload = lab_op.raw(
        "record_laboratory_exception",
        {"sampleId": sample_id, "laboratoryId": "00000000-0000-0000-0000-000000000000", "reason": "x", "observedAt": world.moment(), "idempotencyKey": world.key("exc2")},
    )
    # The pinned contract (openapi.contract.json) enumerates THREE valid
    # responses for this op's unknown/wrong laboratoryId case: 404 not_found,
    # 409 wrong_laboratory, AND 403 wrong_laboratory_operator. Which one an
    # app returns is a check-ordering/authorization-modeling choice it makes
    # (does it verify the laboratory exists first, that it's the sample's
    # target, or that THIS OPERATOR is authorized for it?) -- golden-mongo
    # happens to check ownership before existence (409 wrong_laboratory), but
    # neither instruction.md nor the contract mandates that order, so any of
    # the three pinned outcomes is accepted.
    bad_code = lab_op.code(bad_payload)
    negative = Verdict(
        ok=(bad_status == 409 and bad_code == "wrong_laboratory")
        or (bad_status == 404 and bad_code == "not_found")
        or (bad_status == 403 and bad_code == "wrong_laboratory_operator"),
        detail=f"unknown laboratoryId -> status={bad_status} code={bad_code!r}",
    )

    idempotency = None
    if status == 201:
        # SAME exception_body (SAME idempotencyKey) replayed -- must hit the
        # idempotency cache, not overwrite/duplicate the sample's SINGLE
        # `exception` slot (read_sample_lifecycle's own schema -- one object,
        # not a list, so "no duplicate" here means "unchanged", not "count
        # unchanged").
        status_before, payload_before = lab_op.raw("read_sample_lifecycle", {"sampleId": sample_id})
        exception_before = lab_op.data(status_before, payload_before).get("exception")
        replay_status, replay_payload = lab_op.raw("record_laboratory_exception", exception_body)
        replay_data = lab_op.data(replay_status, replay_payload)
        status_after, payload_after = lab_op.raw("read_sample_lifecycle", {"sampleId": sample_id})
        exception_after = lab_op.data(status_after, payload_after).get("exception")
        identical = (replay_status, replay_data.get("exceptionId")) == (status, data.get("exceptionId"))
        idempotency = Verdict(
            ok=identical and exception_before == exception_after,
            detail=(
                f"replay record_laboratory_exception (SAME idempotencyKey+body) -> status={replay_status} "
                f"exceptionId={replay_data.get('exceptionId')} (vs original status={status} exceptionId={data.get('exceptionId')}); "
                f"sample's exception record unchanged={exception_before == exception_after}"
            ),
        )
    return CapabilityReport(positive=positive, negative=negative, idempotency=idempotency)


def _durability_write_record_laboratory_exception_once(engine) -> dict:
    world, st, pickup, lab, conn = _seed_lifecycle_world(engine, "lcexcdur")
    created = _plan_serviceable(engine, world, st, pickup)
    if not planned_journey_ok(created):
        return {}
    sample_id = created.get("sampleId")
    assignment_ids = created.get("activeAssignmentIds") or []
    owners = _classify_assignments(world, assignment_ids, WORKER_ALIASES)
    collection_aid = next((aid for aid, a in owners.items() if a in ("collector", "collector2")), None)
    transport_aid = next((aid for aid, a in owners.items() if a in ("courier", "courier2")), None)
    if not (sample_id and collection_aid and transport_aid):
        return {}
    collector, courier = world.session(owners[collection_aid]), world.session(owners[transport_aid])
    # Every step below chains on the PREVIOUS one having genuinely succeeded
    # -- bail out at the first sign of trouble instead of driving a doomed
    # sequence on top of a failed accept/collect/handoff, which is what
    # made the eventual "no exceptionId" failure impossible to distinguish
    # from a real app bug versus a transient offer-timing hiccup at any one
    # of these steps (see retry_durability_write's docstring).
    acc1_status, _ = collector.raw("accept_assignment", {"assignmentId": collection_aid, "idempotencyKey": world.key("accdur")})
    if acc1_status != 201:
        return {}
    col_status, _ = collector.raw(
        "collect_and_seal_sample",
        {"sampleId": sample_id, "assignmentId": collection_aid, "collectedAt": world.moment(), "containerType": "sealed-tube", "observedTemperatureC": 5, "idempotencyKey": world.key("coldur")},
    )
    if col_status != 201:
        return {}
    acc2_status, _ = courier.raw("accept_assignment", {"assignmentId": transport_aid, "idempotencyKey": world.key("acc2dur")})
    if acc2_status != 201:
        return {}
    hstatus, hpayload = collector.raw(
        "initiate_handoff",
        {"sampleId": sample_id, "fromAssignmentId": collection_aid, "toAssignmentId": transport_aid, "receiverId": world.user_id(owners[transport_aid]), "initiatedAt": world.moment(), "idempotencyKey": world.key("ihodur")},
    )
    if hstatus != 201:
        return {}
    handoff_id = collector.data(hstatus, hpayload).get("handoffId")
    if not handoff_id:
        return {}
    cs_status, _ = collector.raw("confirm_handoff", {"handoffId": handoff_id, "confirmationSide": "sender", "confirmedAt": world.moment(), "idempotencyKey": world.key("csdur")})
    cr_status, _ = courier.raw("confirm_handoff", {"handoffId": handoff_id, "confirmationSide": "receiver", "confirmedAt": world.moment(), "idempotencyKey": world.key("crdur")})
    if cs_status != 201 or cr_status != 201:
        return {}
    prg_status, _ = courier.raw("update_assignment_progress", {"assignmentId": transport_aid, "status": "delivered", "occurredAt": world.moment(), "idempotencyKey": world.key("prgdur")})
    if prg_status != 201:
        return {}
    lab_op = world.session("lab_op")
    status, payload = lab_op.raw(
        "record_laboratory_exception",
        {"sampleId": sample_id, "laboratoryId": lab["locationId"], "reason": "durability probe", "observedAt": world.moment(), "idempotencyKey": world.key("excdur")},
    )
    data = lab_op.data(status, payload)
    if not data.get("exceptionId"):
        return {}
    return {"sample_id": sample_id, "exception_id": data.get("exceptionId")}


def _durability_write_record_laboratory_exception(engine) -> dict:
    return retry_durability_write(_durability_write_record_laboratory_exception_once, engine)


def _durability_verify_record_laboratory_exception(engine, sentinel: dict) -> Verdict:
    sample_id, exception_id = sentinel.get("sample_id"), sentinel.get("exception_id")
    if not (sample_id and exception_id):
        return Verdict(ok=False, detail="no sample_id/exception_id recorded at write time")
    status, payload = engine.admin.raw("read_sample_state", path_param=sample_id)
    data = engine.admin.data(status, payload)
    # `state` is nested ({"status": "..."}) per the pinned SampleState schema
    # -- see _durability_verify_confirm_laboratory_receipt's identical fix.
    sample_status = (data.get("state") or {}).get("status")
    status_ok = status == 200 and sample_status == "exception"
    via = engine.observe(engine.admin, exception_id, entity_type="exception")
    return Verdict(ok=status_ok or via is not None, detail=f"post-restart read_sample_state status={status} sample.state.status={sample_status!r} via={via}")


def _run_report_disruption(engine) -> CapabilityReport:
    """Own dedicated 2-route scenario (priority-5 domain): the replan must
    RECHECK eligibility/capacity/deadline, not copy the plan verbatim. We
    revoke the standby worker's eligibility (take away their role) before
    disrupting the primary route, then require the replacement to use
    neither the disrupted resource nor the now-ineligible worker -- mirrors
    golden's TestAC61ReplanRechecksWorkerEligibilityRatherThanCopyingThePlan.

    THIS CHECK WAS PREVIOUSLY VACUOUS (found by mutation testing against
    `anchors/mutants/replan-copies-verbatim`): `standby` was seeded but never
    guaranteed to be the worker the app's OWN fairness algorithm actually
    offered the transport leg to -- `courier`/`courier2`, seeded earlier in
    `_seed_lifecycle_world` and excluded from that call's own parking pass,
    were still available and, being longer-waiting, always won the offer
    instead. So `standby not in assignedWorkerIds` was already true BEFORE
    any revocation or disruption ran, and the assertion passed identically
    whether the replan was genuine (golden, clean) or a wholesale verbatim
    copy (the mutant) -- both scored 1.0000.

    Fix: park every other field worker (this scenario's own
    courier/courier2/collector/collector2/lab_op included) so `standby` is
    the ONLY eligible delivery_partner left, then explicitly assert the
    forced precondition held -- fail loudly, not silently, if `standby`
    somehow still isn't the assignee -- before revoking anything. Only once
    that is proven true does "not on the replacement plan" mean the replan
    actually re-validated eligibility.
    """
    world, st, pickup, lab, conn = _seed_lifecycle_world(engine, "dis")
    hub = world.location("hub", ["transfer_point", "hub"], 71.02, 71.02)
    alt1 = world.connection(pickup["locationId"], hub["locationId"], travelMinutes=8, costUnits=2, cooled=True)
    alt2 = world.connection(hub["locationId"], lab["locationId"], travelMinutes=8, costUnits=2, cooled=True)
    for r in (hub["locationId"], alt1["connectionId"], alt2["connectionId"]):
        world.capacity(r, units=20, cooled_units=20)
    world.user("standby", ["delivery_partner"], worker=True)
    standby_id = world.user_id("standby")

    # Force the precondition instead of hoping for it: park EVERY other field
    # worker app-wide (not just this world's collector/courier -- any earlier
    # capability's leftover workers too), so the app's fairness algorithm has
    # no eligible delivery_partner candidate left but `standby`.
    engine.park_field_workers(exclude={standby_id})

    created = _plan_serviceable(engine, world, st, pickup)
    if not planned_journey_ok(created):
        return planning_failure_report("report_disruption", created)
    sample_id, journey_id = created.get("sampleId"), created.get("journeyId")
    assignment_ids = created.get("activeAssignmentIds") or []

    # Discover which assignment the app itself offered to `standby` and have
    # them accept it -- never trust `assignedWorkerIds` in the window between
    # offer and acceptance. Whether a worker-id field on the assignment record
    # is populated at OFFER time (golden's own choice: planning.go sets
    # WorkerID when the offer is created) or only at ACCEPT time (an equally
    # valid design some candidates use, tracking pending offers in a separate
    # field) is an undocumented implementation detail the pinned contract
    # never specifies for `assignedWorkerIds`. Reading it pre-acceptance
    # baked golden's choice in as a hard requirement and falsely zeroed this
    # capability for every candidate using the other, equally valid design.
    # `accept_assignment` is unambiguous in every implementation, so forcing
    # acceptance first removes the ambiguity while still proving exactly what
    # this capability needs: that `standby` holds the transport leg before
    # the disruption fires.
    standby_sess = world.session("standby")
    owners = _classify_assignments(world, assignment_ids, ["standby"])
    standby_aid = next((aid for aid, alias in owners.items() if alias == "standby"), None)
    if not standby_aid:
        return CapabilityReport(
            positive=Verdict(
                ok=False,
                detail=(
                    "precondition-forcing failed: no assignment was discovered as offered to standby "
                    f"among {sorted(assignment_ids)} -- park_field_workers should have made standby the "
                    "only eligible delivery_partner -- the AC-61 recheck below would be vacuous if it ran "
                    "on top of this, so this capability refuses to run it and reports the broken "
                    "precondition instead"
                ),
                evidence={"assignment_ids": assignment_ids},
            )
        )
    acc_status, acc_payload = standby_sess.raw("accept_assignment", {"assignmentId": standby_aid, "idempotencyKey": world.key("dis-acc")})
    if not (acc_status == 201 or acc_status == 200):
        return CapabilityReport(
            positive=Verdict(
                ok=False,
                detail=(
                    f"precondition-forcing failed: standby's own accept_assignment on {standby_aid} was "
                    f"refused (status={acc_status} code={standby_sess.code(acc_payload)!r}) -- the AC-61 "
                    "recheck below would be vacuous if it ran on top of this, so this capability refuses "
                    "to run it and reports the broken precondition instead"
                ),
                evidence={"accept_response": acc_payload},
            )
        )

    # Explicitly assert the forced precondition held. Fail loudly here rather
    # than silently falling through to the (otherwise vacuous) after-check --
    # that silent fall-through is exactly how this capability went vacuous
    # last time.
    before_status, before_payload = engine.admin.raw("read_journey_plan", {"journeyId": journey_id})
    before_plan = engine.admin.data(before_status, before_payload)
    before_assigned = set(before_plan.get("assignedWorkerIds", []) or [])
    if standby_id not in before_assigned:
        return CapabilityReport(
            positive=Verdict(
                ok=False,
                detail=(
                    "precondition-forcing failed: standby was NOT the assigned transport worker "
                    f"after accepting {standby_aid} (assignedWorkerIds={sorted(before_assigned)}); "
                    "park_field_workers should have made standby the only eligible delivery_partner -- "
                    "the AC-61 recheck below would be vacuous if it ran on top of this, so this capability "
                    "refuses to run it and reports the broken precondition instead"
                ),
                evidence={"before": before_plan},
            )
        )

    # Revoke the standby delivery_partner's role -- if the replan blindly
    # copies the old plan it will still name a now-ineligible worker.
    engine.admin.call("update_user_access", {"userId": standby_id, "roles": ["requester"], "active": True})

    disruption_body = {"resourceType": "connection", "resourceId": conn["connectionId"], "reason": "capability probe: connection outage", "occurredAt": world.moment(), "idempotencyKey": world.key("dis")}
    status, payload = engine.admin.raw("report_disruption", disruption_body)
    data = engine.admin.data(status, payload)
    accepted = status == 201 and bool(data.get("disruptionId"))

    # `report_disruption` reports a "connection"-type disruption against
    # EVERY active journey app-wide that has ANY unfinished transport leg
    # (plan_changes.go's `journeyAffected`, deliberately: "invalidates the
    # route assumptions for every unfinished movement, even when the
    # unavailable connection was an alternate"), not just journeys that
    # actually used the disrupted connection. On a live app that already
    # carries pending transport work from earlier capabilities/probes in the
    # same run, `replacementPlanIds` can contain MANY unrelated journeys'
    # replacements in an unspecified order -- so neither `replacementPlanIds[0]`
    # nor the response's aggregate `replanStatus` (the HIGHEST-priority
    # outcome across every affected journey, not necessarily ours) safely
    # identifies OUR journey's own outcome. Ask the sample itself instead:
    # `read_sample_state`'s `activeJourneyId` names OUR sample's current
    # journey no matter how many other journeys the same disruption swept
    # in. (The ORIGINAL `journey_id` is also unsafe to re-read directly: it
    # is now "replaced" with its own assignments CANCELLED, and
    # views.go's journeyMap drops cancelled assignments from
    # `assignedWorkerIds` entirely -- re-reading it would show an EMPTY
    # `assignedWorkerIds`, making "standby not in assignedWorkerIds"
    # trivially true regardless of whether the replan was genuine or a
    # verbatim copy. This was a second, independent way the same check went
    # vacuous; both are fixed together here.)
    sample_status_code, sample_payload = engine.admin.raw("read_sample_state", path_param=sample_id)
    sample_state = engine.admin.data(sample_status_code, sample_payload)
    our_replacement_id = sample_state.get("activeJourneyId") or ""
    if not accepted or not our_replacement_id or our_replacement_id == journey_id:
        positive = Verdict(
            ok=False,
            detail=(
                f"expected OUR sample to land on a genuinely-replaced journey, got status={status} "
                f"accepted={accepted} sample.activeJourneyId={our_replacement_id!r} (original journey_id={journey_id}) "
                f"sample.status={sample_state.get('status')} -- the forced precondition scenario (an alternate hub "
                "route exists) should always produce a replacement for OUR sample; investigate before trusting this "
                "capability's credit"
            ),
            evidence={"before": before_plan, "disruption_response": data, "sample_state": sample_state},
        )
    else:
        replan_status_code, replan_payload = engine.admin.raw("read_journey_plan", {"journeyId": our_replacement_id})
        replan = engine.admin.data(replan_status_code, replan_payload)
        assigned_workers = set(replan.get("assignedWorkerIds", []) or [])
        revalidated = standby_id not in assigned_workers
        # A second, independent signal that the plan was actually recomputed
        # rather than cloned: the disrupted route was direct
        # (pickup->lab); a genuine replan must detour via the hub, which
        # changes totalCostUnits/estimatedDeliveryAt from the original.
        recomputed = (
            replan.get("totalCostUnits") != before_plan.get("totalCostUnits")
            or replan.get("estimatedDeliveryAt") != before_plan.get("estimatedDeliveryAt")
        )
        via = engine.observe(engine.admin, data.get("disruptionId", ""), entity_type="disruption") if data.get("disruptionId") else None
        positive = Verdict(
            ok=accepted and revalidated and recomputed,
            detail=(
                f"precondition forced: standby WAS the assignee before disruption (assignedWorkerIds={sorted(before_assigned)}); "
                f"status={status} disruptionId={data.get('disruptionId')} ourReplacementJourneyId={our_replacement_id} "
                f"ineligible_worker_excluded={revalidated} route_recomputed={recomputed} "
                f"(before: cost={before_plan.get('totalCostUnits')} eta={before_plan.get('estimatedDeliveryAt')}; "
                f"after: cost={replan.get('totalCostUnits')} eta={replan.get('estimatedDeliveryAt')}) via={via}"
            ),
            evidence={"before": before_plan, "replan": replan},
        )

    bad_status, bad_payload = engine.admin.raw(
        "report_disruption",
        {"resourceType": "not_a_real_resource_type", "resourceId": conn["connectionId"], "occurredAt": world.moment(), "idempotencyKey": world.key("dis-bad")},
    )
    negative = Verdict(ok=bad_status == 400, detail=f"invalid resourceType -> status={bad_status} code={engine.admin.code(bad_payload)!r}")

    idempotency = None
    if accepted:
        # SAME disruption_body (SAME idempotencyKey, captured once above) --
        # must hit the idempotency cache and return the identical
        # disruptionId, NOT re-sweep every affected journey a second time
        # (this call affects EVERY active journey with an unfinished
        # transport leg app-wide, per the module's own docstring -- a second,
        # genuine re-execution on replay would be a much worse bug than a
        # wrong status code).
        replay_status, replay_payload = engine.admin.raw("report_disruption", disruption_body)
        replay_data = engine.admin.data(replay_status, replay_payload)
        identical = (replay_status, replay_data.get("disruptionId")) == (status, data.get("disruptionId"))
        idempotency = Verdict(
            ok=identical,
            detail=f"replay report_disruption (SAME idempotencyKey+body) -> status={replay_status} disruptionId={replay_data.get('disruptionId')} (vs original status={status} disruptionId={data.get('disruptionId')})",
        )
    return CapabilityReport(positive=positive, negative=negative, idempotency=idempotency)


def _durability_write_report_disruption_once(engine) -> dict:
    """Simplified vs. the positive probe: durability only needs to prove a
    disruption record + its replacement journey survive a restart, not
    re-prove AC-61's eligibility recheck (that logic, and the
    park/standby/revoke precondition-forcing it requires, is exercised by
    the positive probe already) -- so this skips straight to firing the
    disruption against a real, in-use connection."""
    world, st, pickup, lab, conn = _seed_lifecycle_world(engine, "disdur")
    hub = world.location("hub", ["transfer_point", "hub"], 71.03, 71.03)
    alt1 = world.connection(pickup["locationId"], hub["locationId"], travelMinutes=8, costUnits=2, cooled=True)
    alt2 = world.connection(hub["locationId"], lab["locationId"], travelMinutes=8, costUnits=2, cooled=True)
    for r in (hub["locationId"], alt1["connectionId"], alt2["connectionId"]):
        world.capacity(r, units=20, cooled_units=20)
    created = _plan_serviceable(engine, world, st, pickup)
    if not planned_journey_ok(created):
        return {}
    sample_id, journey_id = created.get("sampleId"), created.get("journeyId")
    status, payload = engine.admin.raw(
        "report_disruption",
        {"resourceType": "connection", "resourceId": conn["connectionId"], "reason": "durability probe: connection outage", "occurredAt": world.moment(), "idempotencyKey": world.key("disdur")},
    )
    data = engine.admin.data(status, payload)
    disruption_id = data.get("disruptionId")
    if not (status == 201 and disruption_id):
        return {}
    return {"disruption_id": disruption_id, "sample_id": sample_id, "original_journey_id": journey_id}


def _durability_write_report_disruption(engine) -> dict:
    return retry_durability_write(_durability_write_report_disruption_once, engine)


def _durability_verify_report_disruption(engine, sentinel: dict) -> Verdict:
    disruption_id, sample_id, original_journey_id = sentinel.get("disruption_id"), sentinel.get("sample_id"), sentinel.get("original_journey_id")
    if not (disruption_id and sample_id):
        return Verdict(ok=False, detail="no disruption_id/sample_id recorded at write time")
    via = engine.observe(engine.admin, disruption_id, entity_type="disruption")
    status, payload = engine.admin.raw("read_sample_state", path_param=sample_id)
    data = engine.admin.data(status, payload)
    replaced = status == 200 and bool(data.get("activeJourneyId")) and data.get("activeJourneyId") != original_journey_id
    return Verdict(
        ok=(via is not None) or replaced,
        detail=f"post-restart observe(disruption)={via}; sample.activeJourneyId={data.get('activeJourneyId')} (original {original_journey_id})",
    )


def _run_accept_assignment(engine) -> CapabilityReport:
    """Own dedicated scenario, NOT the shared `_drive_happy_path` -- that
    drive is monolithic (by the time it returns, the real collector has
    already accepted), so there is no "still pending, not yet accepted"
    moment left to probe a bystander against. Mirrors `decline_assignment`'s
    existing negative-probe shape, closing the gap `anchors/mutants/
    assignment-stealing` found: a same-role bystander who was never offered
    this assignment must be rejected (AC-31) WHILE it is still genuinely
    pending, not merely after it has already been taken.
    """
    world, st, pickup, lab, conn = _seed_lifecycle_world(engine, "accsteal")
    created = _plan_serviceable(engine, world, st, pickup)
    if not planned_journey_ok(created):
        return planning_failure_report("accept_assignment", created)
    assignment_ids = created.get("activeAssignmentIds") or []
    owners = _classify_assignments(world, assignment_ids, WORKER_ALIASES)
    collection_aid = next((aid for aid, a in owners.items() if a in ("collector", "collector2")), None)
    if not collection_aid:
        return CapabilityReport(positive=Verdict(ok=False, detail="could not classify a collection leg to accept"))
    owner_alias = owners[collection_aid]
    owner_sess = world.session(owner_alias)

    # The bystander attempt runs FIRST, while the assignment is still
    # genuinely pending and offered to `owner_alias` -- not after it has
    # already been taken (that would exercise `assignment_taken` instead of
    # the offer-identity check AC-31 actually protects).
    world.user("bystander_accept", ["collection_professional"], worker=True)
    bystander = world.session("bystander_accept")
    bad_status, bad_payload = bystander.raw("accept_assignment", {"assignmentId": collection_aid, "idempotencyKey": world.key("acc-steal")})
    # accept_assignment's own 409 enum pins BOTH offer_not_available AND
    # offer_not_offered as valid for "this offer isn't yours" -- golden picks
    # the former, but the latter is equally contract-legal (unlike
    # decline_assignment's 409 enum, which only has offer_not_available).
    rejected = bad_status == 409 and bystander.code(bad_payload) in ("offer_not_available", "offer_not_offered")

    bystander_work_status, bystander_work_payload = bystander.raw("list_role_work", {})
    bystander_work = bystander.data(bystander_work_status, bystander_work_payload)
    bystander_touched = any(
        item.get("assignmentId") == collection_aid
        for bucket in ("pending", "inProgress", "completed")
        for item in bystander_work.get(bucket, []) or []
    )

    # The rejected attempt must not have disturbed the assignment: the
    # ACTUAL offeree can still accept it afterwards, untouched.
    accept_body = {"assignmentId": collection_aid, "idempotencyKey": world.key("acc-real")}
    status, payload = owner_sess.raw("accept_assignment", accept_body)
    data = owner_sess.data(status, payload)
    real_accept_ok = status == 201 and data.get("status") == "accepted" and data.get("workerId") == world.user_id(owner_alias)

    positive = Verdict(
        ok=real_accept_ok,
        detail=f"legitimate offeree ({owner_alias}) accepts after the steal attempt -> status={status} workerId={data.get('workerId')}",
    )
    negative = Verdict(
        ok=rejected and not bystander_touched,
        detail=(
            f"same-role bystander (never offered this assignment) attempts accept WHILE still pending -> "
            f"status={bad_status} code={bystander.code(bad_payload)!r}; bystander_work_shows_assignment={bystander_touched}"
        ),
    )
    # Idempotency (AC-74's replay contract, same as create_collection_request):
    # SAME body, SAME idempotencyKey -- accept_body is captured once above and
    # reused here verbatim, not re-derived, since world.key() mints a NEW key
    # on every call and reusing it would silently test something else
    # entirely (two DIFFERENT requests) instead of a true replay.
    replay_status, replay_payload = owner_sess.raw("accept_assignment", accept_body)
    replay_data = owner_sess.data(replay_status, replay_payload)
    identical = (replay_status, replay_data.get("workerId"), replay_data.get("status")) == (status, data.get("workerId"), data.get("status"))
    work_status, work_payload = owner_sess.raw("list_role_work", {})
    work_data = owner_sess.data(work_status, work_payload)
    occurrences = sum(
        1 for bucket in ("pending", "inProgress", "completed")
        for item in work_data.get(bucket, []) or [] if item.get("assignmentId") == collection_aid
    )
    idempotency = Verdict(
        ok=identical and occurrences == 1,
        detail=(
            f"replay accept_assignment (SAME idempotencyKey+body) -> status={replay_status} "
            f"workerId={replay_data.get('workerId')} status_field={replay_data.get('status')!r} (vs original "
            f"status={status} workerId={data.get('workerId')}); occurrences in {owner_alias}'s own "
            f"list_role_work={occurrences} (expected exactly 1, not a duplicate accept)"
        ),
    )
    return CapabilityReport(positive=positive, negative=negative, idempotency=idempotency)


def _run_decline_assignment(engine) -> CapabilityReport:
    world, st, pickup, lab, conn = _seed_lifecycle_world(engine, "dec")
    created = _plan_serviceable(engine, world, st, pickup)
    if not planned_journey_ok(created):
        return planning_failure_report("decline_assignment", created)
    assignment_ids = created.get("activeAssignmentIds") or []
    owners = _classify_assignments(world, assignment_ids, WORKER_ALIASES)
    collection_aid = next((aid for aid, a in owners.items() if a in ("collector", "collector2")), None)
    if not collection_aid:
        return CapabilityReport(positive=Verdict(ok=False, detail="could not classify a collection leg to decline"))
    decliner_alias = owners[collection_aid]
    decliner = world.session(decliner_alias)

    decline_body = {"assignmentId": collection_aid, "reason": "capability probe", "declinedAt": world.moment(), "idempotencyKey": world.key("dec")}
    status, payload = decliner.raw("decline_assignment", decline_body)
    data = decliner.data(status, payload)
    reoffered_to = data.get("reofferedTo")
    # AC-32: decline must not make the decliner unavailable, and the offer
    # must move to a DIFFERENT eligible worker.
    still_available_status, still_available_payload = engine.admin.raw("read_user", path_param=world.user_id(decliner_alias))
    still_active = engine.admin.data(still_available_status, still_available_payload).get("active") is True
    moved_on = bool(reoffered_to) and reoffered_to != world.user_id(decliner_alias)
    positive = Verdict(
        ok=status == 201 and data.get("status") == "pending" and moved_on and still_active,
        detail=f"status={status} reofferedTo={reoffered_to} decliner_still_active={still_active}",
    )

    # A genuinely uninvolved worker (never offered ANYTHING, including the
    # re-offer that just happened) -- NOT simply "the other seeded collector",
    # since after a decline the re-offer legitimately lands on them and their
    # decline would then legitimately succeed.
    world.user("bystander_decline", ["collection_professional"], worker=True)
    bystander = world.session("bystander_decline")
    bad_status, bad_payload = bystander.raw(
        "decline_assignment", {"assignmentId": collection_aid, "reason": "not mine", "declinedAt": world.moment(), "idempotencyKey": world.key("dec-wrong")}
    )
    negative = Verdict(ok=bad_status == 409 and bystander.code(bad_payload) == "offer_not_available", detail=f"uninvolved worker declines -> status={bad_status} code={bystander.code(bad_payload)!r}")

    # Idempotency: SAME decline_body (captured once above, reused verbatim)
    # replayed by the SAME decliner -- must NOT trigger a second re-offer
    # (the offer would otherwise churn to a THIRD worker on every replay).
    replay_status, replay_payload = decliner.raw("decline_assignment", decline_body)
    replay_data = decliner.data(replay_status, replay_payload)
    identical = (replay_status, replay_data.get("reofferedTo"), replay_data.get("status")) == (status, reoffered_to, data.get("status"))
    idempotency = Verdict(
        ok=identical,
        detail=(
            f"replay decline_assignment (SAME idempotencyKey+body) -> status={replay_status} "
            f"reofferedTo={replay_data.get('reofferedTo')} (vs original status={status} reofferedTo={reoffered_to}); "
            f"expected byte-identical, not a second re-offer churning to yet another worker"
        ),
    )
    return CapabilityReport(positive=positive, negative=negative, idempotency=idempotency)


def _durability_write_decline_assignment_once(engine) -> dict:
    world, st, pickup, lab, conn = _seed_lifecycle_world(engine, "decdur")
    created = _plan_serviceable(engine, world, st, pickup)
    if not planned_journey_ok(created):
        return {}
    assignment_ids = created.get("activeAssignmentIds") or []
    owners = _classify_assignments(world, assignment_ids, WORKER_ALIASES)
    collection_aid = next((aid for aid, a in owners.items() if a in ("collector", "collector2")), None)
    if not collection_aid:
        return {}
    decliner = world.session(owners[collection_aid])
    status, _ = decliner.raw("decline_assignment", {"assignmentId": collection_aid, "reason": "durability probe", "declinedAt": world.moment(), "idempotencyKey": world.key("decdur")})
    if status != 201:
        return {}
    return {"assignment_id": collection_aid}


def _durability_write_decline_assignment(engine) -> dict:
    return retry_durability_write(_durability_write_decline_assignment_once, engine)


def _durability_verify_decline_assignment(engine, sentinel: dict) -> Verdict:
    assignment_id = sentinel.get("assignment_id")
    if not assignment_id:
        return Verdict(ok=False, detail="no assignment_id recorded at write time")
    via = engine.observe(engine.admin, assignment_id, entity_type="assignment")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


# The pinned contract's JourneyPlan schema requires these fields at the TOP
# LEVEL of every edit_active_plan response, regardless of changeType --
# `anchors/mutants/edit-plan-wrong-shape` reverts the reroute/replace_route
# branch to answering with only {journeyId, status, valid, replacementJourney},
# which silently drops a caller's ability to read the plan version it needs
# for its next optimistic edit without an extra read_journey_plan call.
_JOURNEY_PLAN_REQUIRED_TOP_LEVEL = ("journeyId", "planVersion", "routeMode", "status", "sampleIds", "orderedStops", "legs")


def _run_edit_active_plan_reroute_shape(engine) -> Verdict:
    """Dedicated scenario for edit_active_plan's `reroute`/`replace_route`
    changeType branch -- previously NEVER exercised by this suite (every
    other edit_active_plan probe drives `replace_worker` exclusively via
    `_drive_happy_path`), so a shape regression specific to this branch
    (`anchors/mutants/edit-plan-wrong-shape`) was completely invisible.
    Mirrors `disruption.py`'s `_run_reroute_journey` network (an alternate
    hub route), but drives the reroute through `edit_active_plan` itself,
    not the standalone `reroute_journey` operation, since the two are
    different operationIds with independently-implemented response shapes.
    """
    world = engine.new_world("eap-rr")
    st = world.sample_type("EAP-RR-ST")
    pickup = world.location("pickup", ["collection_point"], 73.0, 73.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 73.01, 73.01)
    conn = world.connection(pickup["locationId"], lab["locationId"], travelMinutes=10, costUnits=1, cooled=True)
    hub = world.location("hub", ["transfer_point", "hub"], 73.02, 73.02)
    alt1 = world.connection(pickup["locationId"], hub["locationId"], travelMinutes=6, costUnits=3, cooled=True)
    alt2 = world.connection(hub["locationId"], lab["locationId"], travelMinutes=6, costUnits=3, cooled=True)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for r in (pickup["locationId"], lab["locationId"], conn["connectionId"], hub["locationId"], alt1["connectionId"], alt2["connectionId"]):
        world.capacity(r, units=20, cooled_units=20)
    world.user("dispatcher", ["dispatcher"])
    dispatcher = world.session("dispatcher")

    created = world.requester_session().call(
        "create_collection_request",
        {
            "requesterId": world.default_requester_id(),
            "origin": {"latitude": 73.0, "longitude": 73.0},
            "originType": "home",
            "requestedAt": world.at(),
            "deliveryDeadline": world.at(hours=10),
            "sampleTypeId": st["sampleTypeId"],
            "containerType": "sealed-tube",
            "temperatureMinC": 2,
            "temperatureMaxC": 8,
            "incompatibleSampleTypes": [],
            "idempotencyKey": world.key("eap-rr-plan"),
        },
    )
    journey_id = created.get("journeyId")
    if not journey_id:
        return Verdict(ok=False, detail="planning did not produce a journey to reroute via edit_active_plan")

    status, payload = dispatcher.raw(
        "edit_active_plan",
        {
            "journeyId": journey_id,
            "expectedPlanVersion": 1,
            "changeType": "reroute",
            "changes": {"reason": "capability probe: reroute shape"},
            "idempotencyKey": world.key("eap-rr"),
        },
    )
    data = dispatcher.data(status, payload)
    missing_top_level = [f for f in _JOURNEY_PLAN_REQUIRED_TOP_LEVEL if f not in data]
    # The pinned contract's 201 response schema lists `replacementJourney` as
    # a property but NOT in its `required[]` -- omitting it is contract-legal
    # (this reroute already fully answers via the top-level fields checked
    # above). Only hold it to the required-shape bar if the candidate chose
    # to include it: present-but-malformed is still the shape regression
    # `edit-plan-wrong-shape` is meant to catch; genuinely absent is not.
    replacement = data.get("replacementJourney")
    missing_in_replacement = (
        [f for f in _JOURNEY_PLAN_REQUIRED_TOP_LEVEL if f not in replacement] if replacement else []
    )
    ok = (
        status == 201
        and not missing_top_level
        and data.get("valid") is True
        and data.get("replanStatus") == "replaced"
        and not missing_in_replacement
    )
    return Verdict(
        ok=ok,
        detail=(
            f"reroute via edit_active_plan -> status={status} top_level_keys={sorted(data.keys())} "
            f"missing_required_top_level={missing_top_level} replanStatus={data.get('replanStatus')} valid={data.get('valid')} "
            f"missing_in_replacementJourney={missing_in_replacement}"
        ),
        evidence={"response": data},
    )


def _run_edit_active_plan(engine) -> CapabilityReport:
    drive = _drive_happy_path(engine)
    if not drive.get("planning_ok"):
        return planning_failure_report("edit_active_plan", drive.get("created"))
    replace_worker_positive = _accepted(drive["steps"], "edit_active_plan")
    reroute_shape = _run_edit_active_plan_reroute_shape(engine)
    positive = Verdict(
        ok=replace_worker_positive.ok and reroute_shape.ok,
        detail=f"replace_worker: {replace_worker_positive.detail}; reroute_shape: {reroute_shape.detail}",
        evidence={"reroute_shape": reroute_shape.evidence},
    )
    stale_probe = drive.get("edit_stale_probe", {})
    negative = Verdict(
        ok=stale_probe.get("status") == 409 and stale_probe.get("code") == "stale_plan",
        detail=f"stale replay -> {stale_probe}",
    )
    idempotency = None
    if replace_worker_positive.ok:
        original, replay = drive["steps"].get("edit_active_plan", {}), drive.get("edit_active_plan_replay")
        if replay:
            identical = replay.get("status") == original.get("status") and replay.get("data") == original.get("data")
            idempotency = Verdict(
                ok=identical,
                detail=f"replay edit_active_plan (SAME idempotencyKey+body) -> status={replay.get('status')} data={replay.get('data')} (vs original status={original.get('status')} data={original.get('data')})",
            )
    return CapabilityReport(positive=positive, negative=negative, idempotency=idempotency)


CAPABILITIES = [
    Capability(
        "accept_assignment",
        "lifecycle",
        "Discovered collection-leg worker accepts their offered assignment; a same-role bystander who was never "
        "offered it is rejected while it is still genuinely pending (AC-31, assignment-stealing).",
        _run_accept_assignment,
        priority=True,
        durability_write=_durability_write_accept_assignment,
        durability_verify=_durability_verify_accept_assignment,
    ),
    _make_step_capability(
        "collect_and_seal_sample", "Collector records collection+seal; sample transitions to sealed.",
        idempotency_check=_step_idempotency("collect_and_seal_sample"),
        durability_write=_durability_write_lifecycle_drive, durability_verify=_durability_verify_collect_and_seal_sample,
    ),
    _make_step_capability(
        "record_handling_checkpoint", "A handling checkpoint is recorded en route.",
        idempotency_check=_step_idempotency("record_handling_checkpoint"),
        durability_write=_durability_write_lifecycle_drive, durability_verify=_durability_verify_record_handling_checkpoint,
    ),
    _make_step_capability(
        "record_worker_location", "The collector's live location is recorded against their assignment.",
        durability_write=_durability_write_lifecycle_drive, durability_verify=_durability_verify_record_worker_location,
    ),
    _make_step_capability(
        "initiate_handoff", "Collector initiates a handoff to the discovered transport-leg worker.",
        idempotency_check=_step_idempotency("initiate_handoff"),
        durability_write=_durability_write_lifecycle_drive, durability_verify=_durability_verify_initiate_handoff,
    ),
    Capability(
        "confirm_handoff",
        "lifecycle",
        "Custody transfers only once BOTH sides confirm; a one-sided confirm must not move/duplicate custody (AC-51..53); a replay (AC-54) must not double-transfer.",
        lambda engine: (lambda d: (
            planning_failure_report("confirm_handoff", d.get("created")) if not d.get("planning_ok") else
            (lambda positive: CapabilityReport(positive=positive, idempotency=_confirm_handoff_idempotency(d) if positive.ok else None))
            (_confirm_handoff_extra(d, _accepted(d["steps"], "confirm_handoff")))
        ))(_drive_happy_path(engine)),
        priority=True,
        group=LIFECYCLE_DRIVE_GROUP,  # calls _drive_happy_path directly
        durability_write=_durability_write_lifecycle_drive,
        durability_verify=_durability_verify_confirm_handoff,
    ),
    _make_step_capability(
        "update_assignment_progress", "Transport-leg worker advances their assignment to delivered.",
        idempotency_check=_step_idempotency("update_assignment_progress"),
        durability_write=_durability_write_lifecycle_drive, durability_verify=_durability_verify_update_assignment_progress,
    ),
    _make_step_capability(
        "confirm_laboratory_receipt",
        "Laboratory operator confirms receipt, completing the journey; every resource this journey reserved "
        "(pickup/laboratory/connection) returns to its pre-plan capacity baseline -- no reservation leak (AC-47/48/49).",
        extra_check=_confirm_laboratory_receipt_extra,
        idempotency_check=_step_idempotency("confirm_laboratory_receipt"),
        durability_write=_durability_write_lifecycle_drive,
        durability_verify=_durability_verify_confirm_laboratory_receipt,
    ),
    Capability(
        "edit_active_plan",
        "lifecycle",
        "Dispatcher edits the active plan via replace_worker AND reroute; a stale expectedPlanVersion is refused (AC-63), and the reroute/replace_route branch must answer with the full JourneyPlan shape (AC-77).",
        _run_edit_active_plan,
        priority=True,
        group=LIFECYCLE_DRIVE_GROUP,  # calls _drive_happy_path directly (twice)
        durability_write=_durability_write_edit_active_plan,
        durability_verify=_durability_verify_edit_active_plan,
    ),
    Capability(
        "record_laboratory_exception", "write", "Laboratory operator records an exception instead of a normal receipt.", _run_record_laboratory_exception,
        durability_write=_durability_write_record_laboratory_exception, durability_verify=_durability_verify_record_laboratory_exception,
    ),
    Capability(
        "report_disruption", "write", "Replanning rechecks eligibility/capacity/deadlines rather than copying the plan verbatim (AC-61).", _run_report_disruption, priority=True,
        durability_write=_durability_write_report_disruption, durability_verify=_durability_verify_report_disruption,
    ),
    Capability(
        "decline_assignment", "write", "Declining is recorded, the worker stays available, and the offer moves to another eligible worker (AC-32).", _run_decline_assignment,
        durability_write=_durability_write_decline_assignment, durability_verify=_durability_verify_decline_assignment,
    ),
]
