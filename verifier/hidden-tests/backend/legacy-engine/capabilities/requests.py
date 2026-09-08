"""create_collection_request, ingest_presealed_sample.

Priority-1 domain, together with serviceability.py. Ground truth mined from
golden-mongo's ac_cheap_test.go / service_integration_test.go:

  - A structurally-unserviceable request is NOT an error: it is 201 with
    `serviceability:"unserviceable"` and a `serviceabilityReason`. Only
    request-level validation failures (bad window, bad origin type, min>max
    temperature, ...) are actual 400s with no state created.
  - Replaying the same `idempotencyKey` returns the identical
    requestId/sampleId/journeyId (AC-74).
  - A pre-sealed sample (ingest_presealed_sample) must NOT create a
    home-collection assignment: its custody starts at the registering
    facility (instruction.md, "Node handler" section) -- so its
    `activeAssignmentIds` must not include a collection leg, and its
    `alreadySealed` flag must be true.
"""
from __future__ import annotations

from oracle import Capability, CapabilityReport, Verdict


def _ops_sample_count(session) -> int:
    status, payload = session.raw("read_operations_state", {"filters": {}})
    data = session.data(status, payload)
    return len(data.get("samples", []))


def _run_create_collection_request(engine) -> CapabilityReport:
    world = engine.new_world("ccr")
    net = world.basic_network()
    requester = world.requester_session()

    key = world.key("idem")
    body = world.request_body(net, key)
    created = requester.call("create_collection_request", body)
    via = engine.observe(engine.admin, created["sampleId"], direct=("read_sample_state", created["sampleId"]), entity_type="sample")
    window_echoed = created.get("earliestCollectionAt") == body["earliestCollectionAt"] and created.get("latestCollectionAt") == body["latestCollectionAt"]
    positive = Verdict(
        ok=created.get("serviceability") == "serviceable" and bool(created.get("journeyId")) and len(created.get("activeAssignmentIds", [])) >= 1
        and via is not None and window_echoed,
        detail=f"serviceability={created.get('serviceability')} assignments={len(created.get('activeAssignmentIds', []))} via={via} window_echoed={window_echoed}",
        evidence={"sampleId": created.get("sampleId"), "journeyId": created.get("journeyId")},
    )

    before_count = _ops_sample_count(engine.admin)
    bad_body = world.request_body(net, world.key("bad"), earliestCollectionAt=world.at(hours=5), latestCollectionAt=world.at(hours=1))
    bad_status, bad_payload = requester.raw("create_collection_request", bad_body)
    after_count = _ops_sample_count(engine.admin)
    negative = Verdict(
        ok=bad_status == 400 and requester.code(bad_payload) == "invalid_collection_window" and before_count == after_count,
        detail=f"inverted window -> status={bad_status} code={requester.code(bad_payload)!r}; sample_count unchanged={before_count == after_count}",
    )

    replay = requester.call("create_collection_request", body)  # same idempotencyKey, same body
    idem_ok = replay.get("sampleId") == created.get("sampleId") and replay.get("journeyId") == created.get("journeyId")
    idempotency = Verdict(ok=idem_ok, detail=f"replay sampleId={replay.get('sampleId')} vs original {created.get('sampleId')}")

    return CapabilityReport(positive=positive, negative=negative, idempotency=idempotency)


def _durability_write_ccr(engine) -> dict:
    world = engine.new_world("ccrdur")
    net = world.basic_network()
    created = world.requester_session().call("create_collection_request", world.request_body(net, world.key("dur")))
    return {"sample_id": created.get("sampleId")}


def _durability_verify_ccr(engine, sentinel: dict) -> Verdict:
    sample_id = sentinel.get("sample_id")
    if not sample_id:
        return Verdict(ok=False, detail="no sample_id recorded at write time")
    via = engine.observe(engine.admin, sample_id, direct=("read_sample_state", sample_id), entity_type="sample")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _run_ingest_presealed_sample(engine) -> CapabilityReport:
    world = engine.new_world("ips")
    st = world.sample_type("ST")
    # The node also carries collection_point coverage: instruction.md says
    # serviceability is measured from active collection points, and a
    # pre-sealed sample's origin is still checked against that same network
    # structure even though its custody starts at the facility, not via a
    # home-collection leg.
    node = world.location("node", ["collection_point", "transfer_point", "hub"], 30.0, 30.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 30.01, 30.01)
    world.connection(node["locationId"], lab["locationId"])
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for r in (node["locationId"], lab["locationId"]):
        world.capacity(r, units=20, cooled_units=20)
    # A candidate that requires node/lab workers to carry an assigned
    # location at creation returns 400 invalid_assignment for a locationless
    # node_handler; golden accepts it. Assign the node location built above so
    # BOTH implementations accept the handler (harmless to golden).
    world.user("handler", ["node_handler"], worker=True, location_id=node["locationId"])
    # A collection_professional exists and is available, but must NEVER be
    # offered anything for a pre-sealed sample -- that's the concrete,
    # HTTP-observable form of "must not create a home-collection assignment".
    world.user("bystander_collector", ["collection_professional"], worker=True)

    body = {
        "requesterId": world.default_requester_id(),
        "originType": "node",
        "originNodeId": node["locationId"],
        "origin": {"latitude": 30.0, "longitude": 30.0},
        "requestedAt": world.at(),
        "deliveryDeadline": world.at(hours=6),
        "sampleTypeId": st["sampleTypeId"],
        "containerType": "sealed-tube",
        "temperatureMinC": 2,
        "temperatureMaxC": 8,
        "idempotencyKey": world.key("ips"),
    }
    handler_sess = world.session("handler")
    status, payload = handler_sess.raw("ingest_presealed_sample", body)
    data = handler_sess.data(status, payload)
    # A pre-sealed sample must NOT create a home-collection assignment. The
    # concrete, HTTP-observable form: the standby collection_professional
    # (who is available and otherwise eligible) must never appear as the
    # owner of any assignment this call produced.
    bystander_status, bystander_payload = world.session("bystander_collector").raw("list_role_work", {})
    bystander_work = world.session("bystander_collector").data(bystander_status, bystander_payload)
    bystander_assignment_ids = {item.get("assignmentId") for bucket in ("pending", "inProgress", "completed") for item in bystander_work.get(bucket, []) or []}
    no_collection_leg = not (bystander_assignment_ids & set(data.get("activeAssignmentIds", [])))
    # VB2.6: `no_collection_leg` is vacuously true on an app that never
    # tracks assignments at all (bystander_assignment_ids is always empty,
    # so its intersection with anything is empty) -- that is not evidence
    # the sample was actually ingested, just evidence nothing is tracked.
    # Require the sample itself to be independently discoverable
    # (audit-trail/ops-state) too.
    sample_id = data.get("sampleId")
    via = engine.observe(engine.admin, sample_id, entity_type="sample") if sample_id else None
    positive = Verdict(
        ok=status == 201 and data.get("alreadySealed") is True and no_collection_leg and via is not None,
        detail=f"status={status} alreadySealed={data.get('alreadySealed')} activeAssignmentIds={data.get('activeAssignmentIds')} no_collector_offer={no_collection_leg} via={via}",
    )

    # ingest_presealed_sample is restricted to collection_professional,
    # delivery_partner, node_handler, and laboratory_team (planning.go's
    # require(...) call); a plain "requester" caller -- who holds no such
    # role -- must be rejected outright, never allowed to register custody.
    world.user("outsider", ["requester"])
    outsider_sess = world.session("outsider")
    bad_status, bad_payload = outsider_sess.raw(
        "ingest_presealed_sample",
        {**body, "idempotencyKey": world.key("ips-bad")},
    )
    negative = Verdict(
        ok=bad_status == 403 and outsider_sess.code(bad_payload) == "forbidden",
        detail=f"requester-role caller -> status={bad_status} code={outsider_sess.code(bad_payload)!r}",
    )

    idempotency = None
    if status == 201:
        # SAME body (SAME idempotencyKey) replayed -- must hit the
        # idempotency cache (same sampleId/journeyId back, no second sample
        # ingested), reusing the SAME `_ops_sample_count` helper the
        # negative-window check above already relies on.
        before_replay_count = _ops_sample_count(engine.admin)
        replay_status, replay_payload = handler_sess.raw("ingest_presealed_sample", body)
        replay_data = handler_sess.data(replay_status, replay_payload)
        after_replay_count = _ops_sample_count(engine.admin)
        identical = (replay_status, replay_data.get("sampleId")) == (status, data.get("sampleId"))
        idempotency = Verdict(
            ok=identical and before_replay_count == after_replay_count,
            detail=(
                f"replay ingest_presealed_sample (SAME idempotencyKey+body) -> status={replay_status} "
                f"sampleId={replay_data.get('sampleId')} (vs original status={status} sampleId={data.get('sampleId')}); "
                f"sample_count unchanged={before_replay_count == after_replay_count} ({before_replay_count} -> {after_replay_count})"
            ),
        )
    return CapabilityReport(positive=positive, negative=negative, idempotency=idempotency)


def _durability_write_ips(engine) -> dict:
    world = engine.new_world("ipsdur")
    st = world.sample_type("STdur")
    node = world.location("nodedur", ["collection_point", "transfer_point", "hub"], 30.5, 30.5, coverageKm=5)
    lab = world.location("labdur", ["laboratory"], 30.51, 30.51)
    world.connection(node["locationId"], lab["locationId"])
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for r in (node["locationId"], lab["locationId"]):
        world.capacity(r, units=20, cooled_units=20)
    world.user("handlerdur", ["node_handler"], worker=True, location_id=node["locationId"])
    body = {
        "requesterId": world.default_requester_id(),
        "originType": "node",
        "originNodeId": node["locationId"],
        "origin": {"latitude": 30.5, "longitude": 30.5},
        "requestedAt": world.at(),
        "deliveryDeadline": world.at(hours=6),
        "sampleTypeId": st["sampleTypeId"],
        "containerType": "sealed-tube",
        "temperatureMinC": 2,
        "temperatureMaxC": 8,
        "idempotencyKey": world.key("ipsdur"),
    }
    handler_sess = world.session("handlerdur")
    status, payload = handler_sess.raw("ingest_presealed_sample", body)
    data = handler_sess.data(status, payload)
    sample_id = data.get("sampleId")
    if not (status == 201 and sample_id):
        return {}
    return {"sample_id": sample_id}


def _durability_verify_ips(engine, sentinel: dict) -> Verdict:
    sample_id = sentinel.get("sample_id")
    if not sample_id:
        return Verdict(ok=False, detail="no sample_id recorded at write time")
    via = engine.observe(engine.admin, sample_id, direct=("read_sample_state", sample_id), entity_type="sample")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


CAPABILITIES = [
    Capability(
        "create_collection_request",
        "write",
        "Plans a serviceable journey (or records why not); replay of the same idempotencyKey is a no-op.",
        _run_create_collection_request,
        priority=True,
        durability_write=_durability_write_ccr,
        durability_verify=_durability_verify_ccr,
    ),
    Capability(
        "ingest_presealed_sample",
        "write",
        "A pre-sealed sample's custody starts at the registering facility -- no home-collection leg is created.",
        _run_ingest_presealed_sample,
        durability_write=_durability_write_ips,
        durability_verify=_durability_verify_ips,
    ),
]
