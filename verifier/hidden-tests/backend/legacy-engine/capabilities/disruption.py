"""cancel_journey, reroute_journey, split_journey, merge_journeys.

These are dispatcher-driven plan mutations, siblings of edit_active_plan and
report_disruption (graded in custody_lifecycle.py). Each builds its own
small, self-contained journey (or pair of journeys, for merge) rather than
sharing state with the custody-lifecycle happy path, for the same
non-collision reason documented there.

All four share the optimistic-concurrency contract proven in golden's
ac_cheap_test.go: an `expectedPlanVersion` that does not match the journey's
current planVersion is refused 409 `stale_plan`.
"""
from __future__ import annotations

from oracle import Capability, CapabilityReport, Verdict, retry_durability_write


def _read_capacity_disr(session, resource_id: str) -> dict:
    """Read a resource's live Capacity object via read_operations_state.
    Mirrors the identical helper in custody_lifecycle.py and concurrency.py --
    this engine never queries a datastore directly, only the app's HTTP reads.
    """
    status, payload = session.raw("read_operations_state", {"filters": {}})
    data = session.data(status, payload)
    for loc in data.get("locations", []) or []:
        if loc.get("locationId") == resource_id:
            return loc.get("capacity") or {}
    for conn in data.get("connections", []) or []:
        if conn.get("connectionId") == resource_id:
            return conn.get("capacity") or {}
    return {}


def _seed_network(engine, tag: str):
    world = engine.new_world(tag)
    st = world.sample_type("ST")
    pickup = world.location("pickup", ["collection_point"], 72.0, 72.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 72.01, 72.01)
    conn = world.connection(pickup["locationId"], lab["locationId"], travelMinutes=10, costUnits=1, cooled=True)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for r in (pickup["locationId"], lab["locationId"], conn["connectionId"]):
        world.capacity(r, units=20, cooled_units=20)
    # Plan mutation (cancel/reroute/split/merge) is a Dispatcher
    # responsibility per instruction.md -- an administrator account holds
    # NO dispatcher role by default and gets 403 forbidden on these, so every
    # mutating call in this module goes through a dispatcher session, not
    # engine.admin directly.
    world.user("dispatcher", ["dispatcher"])
    return world, st, pickup, lab, conn


def _plan(world, st, pickup, **overrides) -> dict:
    body = {
        "requesterId": world.default_requester_id(),
        "origin": {"latitude": 72.0, "longitude": 72.0},
        "originType": "home",
        "requestedAt": world.at(),
        "deliveryDeadline": world.at(hours=10),
        "sampleTypeId": st["sampleTypeId"],
        "containerType": "sealed-tube",
        "temperatureMinC": 2,
        "temperatureMaxC": 8,
        "incompatibleSampleTypes": [],
        "idempotencyKey": world.key("plan"),
    }
    body.update(overrides)
    # await + bounded retry so an app's intermittent planner race doesn't
    # cascade to every disruption/replanning capability (see World.plan_journey)
    return world.plan_journey(body)


def _run_cancel_journey(engine) -> CapabilityReport:
    world, st, pickup, lab, conn = _seed_network(engine, "cancel")
    dispatcher = world.session("dispatcher")

    # AC-47/48/49 (Item 2a): snapshot reservedUnits on every route resource
    # BEFORE planning so we can prove cancel_journey releases them.
    resource_ids = (pickup["locationId"], lab["locationId"], conn["connectionId"])
    cap_before = {rid: _read_capacity_disr(engine.admin, rid) for rid in resource_ids}

    created = _plan(world, st, pickup)
    journey_id = created.get("journeyId")
    if not journey_id:
        return CapabilityReport(positive=Verdict(ok=False, detail="planning did not produce a journey to cancel"))

    cancel_body = {"journeyId": journey_id, "expectedPlanVersion": 1, "reason": "capability probe", "idempotencyKey": world.key("cxl")}
    status, payload = dispatcher.raw("cancel_journey", cancel_body)
    data = dispatcher.data(status, payload)
    via = engine.observe(engine.admin, journey_id, entity_type="journey")
    base_ok = status == 201 and data.get("status") == "cancelled" and via is not None

    # Item 2a: capacity release check -- cancel must release all reservations
    # this journey held. Compare reservedUnits/cooledReservedUnits after cancel
    # against the pre-plan baseline: any field that is HIGHER than baseline is
    # a leak (mirrors _confirm_laboratory_receipt_extra's identical check).
    cap_after = {rid: _read_capacity_disr(engine.admin, rid) for rid in resource_ids}
    cap_leaks = {}
    for rid, before_cap in cap_before.items():
        after_cap = cap_after.get(rid) or {}
        for field in ("reservedUnits", "cooledReservedUnits"):
            b, a = before_cap.get(field), after_cap.get(field)
            if isinstance(b, (int, float)) and isinstance(a, (int, float)) and a > b:
                cap_leaks[f"{rid}.{field}"] = {"before": b, "after": a}
    positive = Verdict(
        ok=base_ok and not cap_leaks,
        detail=(
            f"status={status} journeyStatus={data.get('status')} via={via}; "
            f"capacity reserve/release: before={cap_before} after={cap_after} leaks={cap_leaks or 'none'}"
        ),
        evidence={"capacity_before": cap_before, "capacity_after": cap_after},
    )

    stale_status, stale_payload = dispatcher.raw("cancel_journey", {"journeyId": journey_id, "expectedPlanVersion": 1, "reason": "double cancel", "idempotencyKey": world.key("cxl2")})
    negative = Verdict(
        ok=stale_status == 409 and dispatcher.code(stale_payload) in ("stale_plan", "inactive_plan"),
        detail=f"cancel an already-cancelled journey -> status={stale_status} code={dispatcher.code(stale_payload)!r}",
    )
    # Idempotency: a REPLAY (SAME idempotencyKey+body as the original
    # successful call) hits the idempotency cache and must return the SAME
    # response, unlike `negative` above which deliberately uses a DIFFERENT
    # key against the same (now-cancelled) journey, so business logic
    # actually re-runs and correctly finds it inactive. Different keys test
    # different things: this is the replay-safety contract (AC-74), not the
    # stale/inactive-plan guard.
    replay_status, replay_payload = dispatcher.raw("cancel_journey", cancel_body)
    replay_data = dispatcher.data(replay_status, replay_payload)
    idempotency = Verdict(
        ok=(replay_status, replay_data.get("status")) == (status, data.get("status")),
        detail=f"replay cancel_journey (SAME idempotencyKey+body) -> status={replay_status} journeyStatus={replay_data.get('status')} (vs original status={status} journeyStatus={data.get('status')})",
    )
    return CapabilityReport(positive=positive, negative=negative, idempotency=idempotency)


def _run_reroute_journey(engine) -> CapabilityReport:
    world, st, pickup, lab, _conn = _seed_network(engine, "reroute")
    dispatcher = world.session("dispatcher")
    hub = world.location("hub", ["transfer_point", "hub"], 72.02, 72.02)
    alt1 = world.connection(pickup["locationId"], hub["locationId"], travelMinutes=6, costUnits=3, cooled=True)
    alt2 = world.connection(hub["locationId"], lab["locationId"], travelMinutes=6, costUnits=3, cooled=True)
    for r in (hub["locationId"], alt1["connectionId"], alt2["connectionId"]):
        world.capacity(r, units=20, cooled_units=20)
    created = _plan(world, st, pickup)
    journey_id = created.get("journeyId")
    if not journey_id:
        return CapabilityReport(positive=Verdict(ok=False, detail="planning did not produce a journey to reroute"))

    reroute_body = {"journeyId": journey_id, "expectedPlanVersion": 1, "reason": "capability probe", "idempotencyKey": world.key("rr")}
    status, payload = dispatcher.raw("reroute_journey", reroute_body)
    data = dispatcher.data(status, payload)
    # VB2.6: status==201 alone is satisfiable by a pure fabrication (see
    # anchors/hollow/server.py's h_reroute_journey, which always answers
    # replanStatus="replaced" regardless of any real state) -- require an
    # INDEPENDENT confirmation that the journey is genuinely discoverable
    # (audit-trail/ops-state), never touched by this call's own response.
    via = engine.observe(engine.admin, journey_id, entity_type="journey")
    positive = Verdict(ok=status == 201 and via is not None, detail=f"status={status} replanStatus={data.get('replanStatus')} code={dispatcher.code(payload)!r} via={via}")

    # A first reroute REPLACES the journey (replanStatus="replaced") rather
    # than bumping its version in place, so a stale replay against the now
    # inactive original id is refused as "inactive_plan" every bit as much as
    # "stale_plan" would be -- both are the same guarantee (an outdated
    # handle on the plan cannot mutate it further).
    stale_status, stale_payload = dispatcher.raw("reroute_journey", {"journeyId": journey_id, "expectedPlanVersion": 1, "reason": "stale", "idempotencyKey": world.key("rr-stale")})
    negative = Verdict(
        ok=stale_status == 409 and dispatcher.code(stale_payload) in ("stale_plan", "inactive_plan"),
        detail=f"stale expectedPlanVersion -> status={stale_status} code={dispatcher.code(stale_payload)!r}",
    )
    # Idempotency: replay the SAME body+key as the ORIGINAL successful call
    # (not `negative`'s deliberately-different key) -- must hit the
    # idempotency cache and return byte-identical replanStatus/journeyId,
    # never a second replacement journey minted on replay.
    replay_status, replay_payload = dispatcher.raw("reroute_journey", reroute_body)
    replay_data = dispatcher.data(replay_status, replay_payload)
    idempotency = Verdict(
        ok=(replay_status, replay_data.get("replanStatus"), replay_data.get("replacementPlanId")) == (status, data.get("replanStatus"), data.get("replacementPlanId")),
        detail=(
            f"replay reroute_journey (SAME idempotencyKey+body) -> status={replay_status} "
            f"replanStatus={replay_data.get('replanStatus')} replacementPlanId={replay_data.get('replacementPlanId')} "
            f"(vs original status={status} replanStatus={data.get('replanStatus')} replacementPlanId={data.get('replacementPlanId')})"
        ),
    )
    return CapabilityReport(positive=positive, negative=negative, idempotency=idempotency)


def _run_split_journey(engine) -> CapabilityReport:
    # split_journey requires "at least one but not all" samples on the
    # journey (golden-mongo: plan_changes.go's splitJourneyState), so a
    # single-sample journey can never be split -- merge two plans first to
    # get a 2-sample journey, then split one back out.
    world, st, pickup, lab, _conn = _seed_network(engine, "split")
    dispatcher = world.session("dispatcher")
    a = _plan(world, st, pickup)
    b = _plan(world, st, pickup, idempotencyKey=world.key("plan2"))
    target_id, source_id = a.get("journeyId"), b.get("journeyId")
    if not (target_id and source_id):
        return CapabilityReport(positive=Verdict(ok=False, detail="planning did not produce two journeys to merge before splitting"))
    dispatcher.raw("merge_journeys", {"targetJourneyId": target_id, "expectedPlanVersion": 1, "sourceJourneyIds": [source_id], "idempotencyKey": world.key("premerge")})
    plan_status, plan_payload = dispatcher.raw("read_journey_plan", {"journeyId": target_id})
    plan_version = dispatcher.data(plan_status, plan_payload).get("planVersion", 2)

    split_body = {"journeyId": target_id, "expectedPlanVersion": plan_version, "sampleIds": [b.get("sampleId")], "idempotencyKey": world.key("sp")}
    status, payload = dispatcher.raw("split_journey", split_body)
    data = dispatcher.data(status, payload)
    # VB2.6: as with reroute_journey, status==201 alone is satisfiable by a
    # pure fabrication (h_split_journey mints a fresh random journeyId with
    # no persistence behind it). Require the target journey to be
    # independently discoverable.
    via = engine.observe(engine.admin, target_id, entity_type="journey")
    positive = Verdict(ok=status == 201 and via is not None, detail=f"status={status} code={dispatcher.code(payload)!r} splitJourney={bool(data.get('splitJourney'))} via={via}")

    bad_status, bad_payload = dispatcher.raw("split_journey", {"journeyId": target_id, "expectedPlanVersion": 999, "sampleIds": [b.get("sampleId")], "idempotencyKey": world.key("sp-bad")})
    negative = Verdict(
        ok=bad_status == 409 and dispatcher.code(bad_payload) in ("stale_plan", "invalid_split", "inactive_plan"),
        detail=f"wrong expectedPlanVersion -> status={bad_status} code={dispatcher.code(bad_payload)!r}",
    )
    # Idempotency: replay the SAME body+key as the original successful split
    # -- must hit the idempotency cache, never mint a SECOND split (which
    # would try to split a sample already removed from the target on the
    # first call, a genuinely different and worse bug than a wrong status).
    replay_status, replay_payload = dispatcher.raw("split_journey", split_body)
    replay_data = dispatcher.data(replay_status, replay_payload)
    idempotency = Verdict(
        ok=(replay_status, bool(replay_data.get("splitJourney"))) == (status, bool(data.get("splitJourney"))),
        detail=(
            f"replay split_journey (SAME idempotencyKey+body) -> status={replay_status} "
            f"splitJourney_present={bool(replay_data.get('splitJourney'))} (vs original status={status} "
            f"splitJourney_present={bool(data.get('splitJourney'))})"
        ),
    )
    return CapabilityReport(positive=positive, negative=negative, idempotency=idempotency)


def _run_merge_journeys(engine) -> CapabilityReport:
    world, st, pickup, lab, _conn = _seed_network(engine, "merge")
    dispatcher = world.session("dispatcher")
    a = _plan(world, st, pickup)
    b = _plan(world, st, pickup, idempotencyKey=world.key("plan2"))
    target_id, source_id = a.get("journeyId"), b.get("journeyId")
    if not (target_id and source_id):
        return CapabilityReport(positive=Verdict(ok=False, detail="planning did not produce two journeys to merge"))

    merge_body = {"targetJourneyId": target_id, "expectedPlanVersion": 1, "sourceJourneyIds": [source_id], "idempotencyKey": world.key("mg")}
    status, payload = dispatcher.raw("merge_journeys", merge_body)
    data = dispatcher.data(status, payload)
    both_samples = set(data.get("sampleIds", [])) >= {a.get("sampleId"), b.get("sampleId")}
    positive = Verdict(ok=status == 201 and both_samples, detail=f"status={status} code={dispatcher.code(payload)!r} sampleIds={data.get('sampleIds')} contains_both={both_samples}")

    bad_status, bad_payload = dispatcher.raw(
        "merge_journeys", {"targetJourneyId": target_id, "expectedPlanVersion": 1, "sourceJourneyIds": [source_id], "idempotencyKey": world.key("mg-again")}
    )
    # A second merge naming the same (now-superseded) source is expected to
    # fail: either a stale plan version, or the source is no longer active.
    negative = Verdict(
        ok=bad_status >= 400,
        detail=f"re-merging an already-merged source -> status={bad_status} code={dispatcher.code(bad_payload)!r}",
    )
    # Idempotency: replay the SAME body+key as the original successful merge
    # -- must hit the idempotency cache, never attempt a SECOND merge of a
    # source journey that's already been merged away on the first call.
    replay_status, replay_payload = dispatcher.raw("merge_journeys", merge_body)
    replay_data = dispatcher.data(replay_status, replay_payload)
    idempotency = Verdict(
        ok=(replay_status, set(replay_data.get("sampleIds", []))) == (status, set(data.get("sampleIds", []))),
        detail=f"replay merge_journeys (SAME idempotencyKey+body) -> status={replay_status} sampleIds={replay_data.get('sampleIds')} (vs original status={status} sampleIds={data.get('sampleIds')})",
    )
    return CapabilityReport(positive=positive, negative=negative, idempotency=idempotency)


# --------------------------------------------------------------- durability
# DUR3 (see engine/../DURABILITY.md): each write reuses the SAME `dispatcher`
# session the capability's own positive probe already mutates through (never
# engine.admin directly -- see _seed_network's comment on why), then re-runs
# that SAME positive probe's own observe() check after the restart. Reusing
# the already-proven read-back rather than inventing a new one is
# deliberate: it is exactly as strict as the check that already caught
# `anchors/mutants/*` fabrications, no more, no less.


def _durability_write_cancel_journey(engine) -> dict:
    world, st, pickup, lab, _conn = _seed_network(engine, "canceldur")
    dispatcher = world.session("dispatcher")
    created = _plan(world, st, pickup)
    journey_id = created.get("journeyId")
    if not journey_id:
        return {}
    dispatcher.raw("cancel_journey", {"journeyId": journey_id, "expectedPlanVersion": 1, "reason": "durability probe", "idempotencyKey": world.key("cxldur")})
    return {"journey_id": journey_id}


def _durability_verify_cancel_journey(engine, sentinel: dict) -> Verdict:
    journey_id = sentinel.get("journey_id")
    if not journey_id:
        return Verdict(ok=False, detail="no journey_id recorded at write time")
    via = engine.observe(engine.admin, journey_id, entity_type="journey")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _durability_write_reroute_journey(engine) -> dict:
    world, st, pickup, lab, _conn = _seed_network(engine, "reroutedur")
    dispatcher = world.session("dispatcher")
    hub = world.location("hub", ["transfer_point", "hub"], 72.02, 72.02)
    alt1 = world.connection(pickup["locationId"], hub["locationId"], travelMinutes=6, costUnits=3, cooled=True)
    alt2 = world.connection(hub["locationId"], lab["locationId"], travelMinutes=6, costUnits=3, cooled=True)
    for r in (hub["locationId"], alt1["connectionId"], alt2["connectionId"]):
        world.capacity(r, units=20, cooled_units=20)
    created = _plan(world, st, pickup)
    journey_id = created.get("journeyId")
    if not journey_id:
        return {}
    dispatcher.raw("reroute_journey", {"journeyId": journey_id, "expectedPlanVersion": 1, "reason": "durability probe", "idempotencyKey": world.key("rrdur")})
    return {"journey_id": journey_id}


def _durability_verify_reroute_journey(engine, sentinel: dict) -> Verdict:
    journey_id = sentinel.get("journey_id")
    if not journey_id:
        return Verdict(ok=False, detail="no journey_id recorded at write time")
    via = engine.observe(engine.admin, journey_id, entity_type="journey")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _durability_write_split_journey_once(engine) -> dict:
    world, st, pickup, lab, _conn = _seed_network(engine, "splitdur")
    dispatcher = world.session("dispatcher")
    a = _plan(world, st, pickup)
    b = _plan(world, st, pickup, idempotencyKey=world.key("plan2dur"))
    target_id, source_id = a.get("journeyId"), b.get("journeyId")
    if not (target_id and source_id):
        return {}
    merge_status, _ = dispatcher.raw("merge_journeys", {"targetJourneyId": target_id, "expectedPlanVersion": 1, "sourceJourneyIds": [source_id], "idempotencyKey": world.key("premergedur")})
    if merge_status != 201:
        return {}
    plan_status, plan_payload = dispatcher.raw("read_journey_plan", {"journeyId": target_id})
    if plan_status != 200:
        return {}
    plan_version = dispatcher.data(plan_status, plan_payload).get("planVersion", 2)
    split_status, _ = dispatcher.raw("split_journey", {"journeyId": target_id, "expectedPlanVersion": plan_version, "sampleIds": [b.get("sampleId")], "idempotencyKey": world.key("spdur")})
    if split_status != 201:
        return {}
    return {"journey_id": target_id}


def _durability_write_split_journey(engine) -> dict:
    return retry_durability_write(_durability_write_split_journey_once, engine)


def _durability_verify_split_journey(engine, sentinel: dict) -> Verdict:
    journey_id = sentinel.get("journey_id")
    if not journey_id:
        return Verdict(ok=False, detail="no journey_id recorded at write time")
    via = engine.observe(engine.admin, journey_id, entity_type="journey")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _durability_write_merge_journeys_once(engine) -> dict:
    world, st, pickup, lab, _conn = _seed_network(engine, "mergedur")
    dispatcher = world.session("dispatcher")
    a = _plan(world, st, pickup)
    b = _plan(world, st, pickup, idempotencyKey=world.key("plan2mdur"))
    target_id, source_id = a.get("journeyId"), b.get("journeyId")
    if not (target_id and source_id):
        return {}
    merge_status, _ = dispatcher.raw("merge_journeys", {"targetJourneyId": target_id, "expectedPlanVersion": 1, "sourceJourneyIds": [source_id], "idempotencyKey": world.key("mgdur")})
    if merge_status != 201:
        return {}
    return {"journey_id": target_id, "sample_ids": [a.get("sampleId"), b.get("sampleId")]}


def _durability_write_merge_journeys(engine) -> dict:
    return retry_durability_write(_durability_write_merge_journeys_once, engine)


def _durability_verify_merge_journeys(engine, sentinel: dict) -> Verdict:
    journey_id = sentinel.get("journey_id")
    if not journey_id:
        return Verdict(ok=False, detail="no journey_id recorded at write time")
    status, payload = engine.admin.raw("read_journey_plan", {"journeyId": journey_id})
    data = engine.admin.data(status, payload)
    both = set(data.get("sampleIds", []) or []) >= set(sentinel.get("sample_ids") or [])
    return Verdict(ok=status == 200 and both, detail=f"post-restart read_journey_plan status={status} sampleIds={data.get('sampleIds')} contains_both={both}")


CAPABILITIES = [
    Capability(
        "cancel_journey", "write", "Dispatcher cancels an active journey; a second cancel is refused (stale/inactive plan).", _run_cancel_journey,
        durability_write=_durability_write_cancel_journey, durability_verify=_durability_verify_cancel_journey,
    ),
    Capability(
        "reroute_journey", "write", "Dispatcher reroutes via an alternate hub path; stale expectedPlanVersion is refused (AC-63).", _run_reroute_journey,
        durability_write=_durability_write_reroute_journey, durability_verify=_durability_verify_reroute_journey,
    ),
    Capability(
        "split_journey", "write", "Dispatcher splits samples off an active journey into a new one.", _run_split_journey,
        durability_write=_durability_write_split_journey, durability_verify=_durability_verify_split_journey,
    ),
    Capability(
        "merge_journeys", "write", "Dispatcher merges two compatible journeys' samples into one plan.", _run_merge_journeys,
        durability_write=_durability_write_merge_journeys, durability_verify=_durability_verify_merge_journeys,
    ),
]
