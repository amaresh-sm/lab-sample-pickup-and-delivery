"""read_sample_state, read_sample_lifecycle, read_journey_plan,
read_operations_state, search_audit_trail, list_role_work, read_ui_bootstrap,
stream_realtime_events, system_readiness.

Priority-7 domain (audit) plus the rest of the read/tracking surface.
Ground truth mined from golden-mongo's audit_integration_test.go /
work_views_integration_test.go / ac_tracking_test.go:

  - Every audit entry carries exactly: actorId, role, action, entityType,
    entityId, an outcome (`success`), and `details` (the "relevant changes")
    -- AC-71's seven fields. A REJECTED action is still audited, with
    success:false (AC-72) -- exercised here by triggering one genuine 403.
  - list_role_work's buckets are exactly "pending" / "inProgress" /
    "completed" (AC-3: current work AND past/completed work, both visible).
  - A requester must see only their own sample (AC-2).
"""
from __future__ import annotations

import base64
import os
import socket
import time as _time
from urllib.parse import urlsplit

from client import engine_wait_seconds, now_utc, parse_rfc3339
from oracle import Capability, CapabilityReport, Verdict, planned_journey_ok, planning_failure_report


def _try_websocket_upgrade(session, operation_id: str, timeout: float = 5.0) -> bool:
    """True iff a real WebSocket handshake against this op's path returns
    `101 Switching Protocols`. The pinned contract's own response set for
    `stream_realtime_events` includes BOTH `101` (a real upgrade) and `200`
    (plain long-poll -- golden-mongo's own `events()` handler branches on
    `websocket.IsWebSocketUpgrade(r)` and serves plain JSON when the request
    is NOT an upgrade attempt). Testing only the plain-GET path would fail a
    candidate that implemented only the WebSocket path, and vice versa --
    both are equally contract-legal, so this performs a real upgrade attempt
    so either implementation is recognized. Never raises; a candidate with no
    WS support just returns False here, and the caller falls back to its
    existing plain-GET check."""
    method, path_template = session.ops[operation_id]
    if method != "get":
        return False
    parts = urlsplit(session.base_url)
    host, port = parts.hostname, parts.port or (443 if parts.scheme == "https" else 80)
    key = base64.b64encode(os.urandom(16)).decode()
    lines = [
        f"GET {path_template} HTTP/1.1",
        f"Host: {host}:{port}",
        "Connection: Upgrade",
        "Upgrade: websocket",
        "Sec-WebSocket-Version: 13",
        f"Sec-WebSocket-Key: {key}",
    ]
    if session.token:
        lines.append(f"Authorization: Bearer {session.token}")
    lines += ["", ""]
    try:
        with socket.create_connection((host, port), timeout=timeout) as sock:
            sock.sendall("\r\n".join(lines).encode())
            sock.settimeout(timeout)
            status_line = sock.recv(512).split(b"\r\n", 1)[0].decode(errors="replace")
        return " 101 " in f" {status_line} "
    except Exception:
        return False


def _seed_and_plan(engine, tag: str):
    world = engine.new_world(tag)
    net = world.basic_network()
    world.user("owner", ["requester"])
    world.user("other", ["requester"])
    owner_sess = world.session("owner")
    body = world.request_body(net, world.key("aud"), requesterId=world.user_id("owner"))
    # De-amplification (matches custody_lifecycle.py's _plan_serviceable): a
    # single unretried create_collection_request races a candidate's async
    # serviceability publication (AC-40 permits it), so a request planned
    # immediately after seeding the network can read a transient
    # `unserviceable` on an otherwise-serviceable network -- cascading a false
    # zero into every read capability built on this seed. world.plan_journey
    # awaits publication and retries with a fresh idempotencyKey before
    # conceding; a genuinely-unplannable network still returns unserviceable.
    created = world.plan_journey(body, session=owner_sess)
    return world, net, created


def _run_read_sample_state(engine) -> CapabilityReport:
    world, net, created = _seed_and_plan(engine, "rss")
    sample_id = created["sampleId"]
    owner_id = world.user_id("owner")
    owner_sess = world.session("owner")
    status, payload = owner_sess.raw("read_sample_state", path_param=sample_id)
    data = owner_sess.data(status, payload)
    # VB2.6: `data.get("sampleId") == sample_id` is a trivial path-param
    # echo -- ANY GET-by-id handler returns the id it was asked for, real or
    # fabricated (see anchors/hollow/server.py's h_read_sample_state).
    # requesterId is NOT derivable from the path parameter alone; it can
    # only be correct if the app actually persisted who created this sample.
    positive = Verdict(
        ok=status == 200 and data.get("sampleId") == sample_id and data.get("requesterId") == owner_id
        and "timeline" in data and "locationStale" in data,
        detail=f"status={status} requesterId={data.get('requesterId')} (expected {owner_id}) locationStale={data.get('locationStale')}",
    )

    other_sess = world.session("other")
    bad_status, bad_payload = other_sess.raw("read_sample_state", path_param=sample_id)
    negative = Verdict(ok=bad_status in (403, 404), detail=f"AC-2: another requester reading it -> status={bad_status} code={other_sess.code(bad_payload)!r}")
    return CapabilityReport(positive=positive, negative=negative)


def _run_read_sample_lifecycle(engine) -> CapabilityReport:
    world, net, created = _seed_and_plan(engine, "rsl")
    # A candidate whose planner failed for this network returns no journeyId --
    # indexing created["journeyId"] would raise KeyError. Clean-FAIL instead.
    if not planned_journey_ok(created):
        return planning_failure_report("read_sample_lifecycle", created)
    sample_id = created["sampleId"]
    journey_id = created["journeyId"]
    status, payload = engine.admin.raw("read_sample_lifecycle", {"sampleId": sample_id})
    data = engine.admin.data(status, payload)
    timeline = data.get("timeline", [])
    ordered = all(timeline[i].get("occurredAt", "") <= timeline[i + 1].get("occurredAt", "") for i in range(len(timeline) - 1))
    # VB2.6: `data.get("sampleId") == sample_id` is a trivial echo of the
    # BODY field we just sent this same call (see
    # anchors/hollow/server.py's h_read_sample_lifecycle). activeJourneyId
    # is app-COMPUTED state that was never part of this call's own input --
    # a stateless echo cannot produce the correct value here, only a real
    # persistence layer that actually planned this sample's journey can.
    journey_confirmed = data.get("activeJourneyId") == journey_id
    positive = Verdict(
        ok=status == 200 and data.get("sampleId") == sample_id and journey_confirmed and "activeReplacementPlanIds" in data and ordered,
        detail=f"status={status} sealed={data.get('sealed')} activeJourneyId={data.get('activeJourneyId')} (expected {journey_id}) timeline_len={len(timeline)} ordered={ordered}",
    )

    bad_status, bad_payload = engine.admin.raw("read_sample_lifecycle", {"sampleId": "00000000-0000-0000-0000-000000000000"})
    negative = Verdict(ok=bad_status == 404, detail=f"status={bad_status} code={engine.admin.code(bad_payload)!r}")
    return CapabilityReport(positive=positive, negative=negative)


def _run_read_journey_plan(engine) -> CapabilityReport:
    world, net, created = _seed_and_plan(engine, "rjp")
    # No journeyId on an unplanned request -> indexing would raise KeyError.
    if not planned_journey_ok(created):
        return planning_failure_report("read_journey_plan", created)
    journey_id = created["journeyId"]
    status, payload = engine.admin.raw("read_journey_plan", {"journeyId": journey_id})
    data = engine.admin.data(status, payload)
    legs = data.get("legs", []) or []
    # VB2.6: "legs"/"orderedStops" merely being PRESENT keys is satisfiable
    # by a fixed, always-empty fabrication (see anchors/hollow/server.py's
    # h_read_journey_plan, hardcoded legs=[]/orderedStops=[] regardless of
    # any real plan). A journey planned against our own just-created
    # pickup/lab network must have at least one REAL leg referencing one of
    # those two locations -- a stateless handler with no route-planning
    # logic behind it cannot produce that.
    leg_locations = {leg.get("fromLocationId") for leg in legs} | {leg.get("toLocationId") for leg in legs}
    references_our_network = bool({net.pickup["locationId"], net.laboratory["locationId"]} & leg_locations)
    positive = Verdict(
        ok=status == 200 and data.get("planVersion", 0) >= 1 and len(legs) >= 1 and references_our_network and "orderedStops" in data,
        detail=f"status={status} planVersion={data.get('planVersion')} routeMode={data.get('routeMode')} legs={len(legs)} references_our_network={references_our_network}",
    )

    world.user("plainrequester", ["requester"])
    bad_status, bad_payload = world.session("plainrequester").raw("read_journey_plan", {"journeyId": journey_id})
    negative = Verdict(ok=bad_status in (403, 404), detail=f"unrelated requester -> status={bad_status} code={world.session('plainrequester').code(bad_payload)!r}")
    return CapabilityReport(positive=positive, negative=negative)


def _run_read_operations_state(engine) -> CapabilityReport:
    world, net, created = _seed_and_plan(engine, "ros")
    sample_id = created["sampleId"]
    status, payload = engine.admin.raw("read_operations_state", {"filters": {}})
    data = engine.admin.data(status, payload)
    required = ("samples", "workers", "locations", "connections", "routes", "handoffs", "delays", "exceptions")
    # VB2.6: the required keys merely being PRESENT is satisfiable by fixed,
    # always-empty arrays (see anchors/hollow/server.py's
    # h_read_operations_state). Require the sample we JUST created to
    # actually show up in the aggregate -- a stateless handler with no
    # memory of any previous write cannot make that true.
    sample_ids_seen = {s.get("sampleId") for s in data.get("samples", []) or []}
    contains_our_sample = sample_id in sample_ids_seen
    positive = Verdict(
        ok=status == 200 and all(k in data for k in required) and contains_our_sample,
        detail=f"status={status} keys_present={[k for k in required if k in data]} contains_our_sample={contains_our_sample}",
    )

    world.user("plainrequester", ["requester"])
    bad_status, bad_payload = world.session("plainrequester").raw("read_operations_state", {"filters": {}})
    negative = Verdict(ok=bad_status == 403 and world.session("plainrequester").code(bad_payload) == "forbidden", detail=f"requester -> status={bad_status}")
    return CapabilityReport(positive=positive, negative=negative)


def _run_pulse_exception_audit_probe(engine) -> Verdict:
    """pulse-exception-unaudited: when a Pulse assignment's offer expires and
    no OTHER eligible worker exists, the sample flips to `exception` -- but
    that outcome must be SEARCHABLE in the audit trail
    (`sample_exception_recorded`), not just visible on the sample record.
    Previously untested by any capability (no probe paid the real
    wall-clock cost of waiting past an offer's TTL); verified against
    `anchors/mutants/pulse-exception-unaudited`.

    Per the mutant's own RUNBOOK: build a Pulse-named sample type (forces
    qualifiedCollectorRequired + a requiredQualification code via
    `enforceNamedSamplePolicy`) with EXACTLY one qualified collector, one
    delivery_partner, and one laboratory_team worker -- every OTHER
    already-existing field worker app-wide is parked first, so Pulse's
    synchronous planning (an eligible worker is required for every leg or it
    409s `pulse_no_wait` up front) has no fallback once our one collector's
    offer expires without being accepted.

    The specific certification-code STRING that unlocks "Pulse" is an
    app-internal choice (golden-mongo happens to use "LX-P"); nothing in the
    pinned contract or instruction.md names a scheme. `create_sample_type`'s
    response exposes `requiredQualification` on `data` (required field per
    openapi.contract.json), so it is discovered dynamically from the app's
    own response and granted to the test worker -- this probe must work
    against ANY candidate's internal naming choice, not just golden's.
    """
    world = engine.new_world("pulseaud")
    st = world.sample_type("Pulse", exact_name=True)
    # A deduping candidate with no discoverable audit provenance hands back a
    # tagged SUBSTITUTE here -- a fresh, unrelated sample type, not the real
    # seeded Pulse -- because World.sample_type() has no other way to give
    # this call a working handle. Reading its policy fields as if they were
    # Pulse's own (they default to False/empty on the substitute, since
    # nothing forces named-type policy onto an arbitrary name) is what turned
    # "couldn't verify" into a false, hard failure here -- confirmed live
    # against opus-max/luna-xhigh/sol-xhigh, whose real seeded Pulse types are
    # unreachable through any pinned read path once dedup+no-audit-provenance
    # blocks both discovery routes. Match the same raise-and-report-
    # unassessable pattern qualified_collector_exhaustion_exception already
    # uses for the identical situation, rather than grading a stand-in.
    if st.get("_unverifiable_canonical_policy"):
        raise RuntimeError(
            "Pulse's real canonical policy is unmeasurable: create was deduped and has no audit "
            "provenance, so only a substitute type (not the real seeded Pulse) could be read back."
        )
    required_qualification = st.get("requiredQualification")
    if not st.get("qualifiedCollectorRequired") or not required_qualification:
        return Verdict(
            ok=False,
            detail=(
                "Pulse sample type did not come back with qualifiedCollectorRequired=true and a "
                f"non-empty requiredQualification -- got qualifiedCollectorRequired={st.get('qualifiedCollectorRequired')!r} "
                f"requiredQualification={required_qualification!r}; cannot construct the 'exactly one eligible "
                "collector' precondition this probe depends on."
            ),
        )
    pickup = world.location("pickup", ["collection_point"], 75.0, 75.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 75.001, 75.001)
    world.connection(pickup["locationId"], lab["locationId"], travelMinutes=5, costUnits=1, cooled=False)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for r in (pickup["locationId"], lab["locationId"]):
        world.capacity(r, units=20, cooled_units=20)
    world.user("collector", ["collection_professional"], worker=True, qualifications=[required_qualification])
    world.user("courier", ["delivery_partner"], worker=True)
    world.user("lab_op", ["laboratory_team"], worker=True, location_id=lab["locationId"])
    ours = {world.user_id(a) for a in ("collector", "courier", "lab_op")}
    engine.park_field_workers(exclude=ours)

    status, payload = engine.admin.raw(
        "create_collection_request",
        {
            "requesterId": world.default_requester_id(),
            "origin": {"latitude": 75.0, "longitude": 75.0},
            "originType": "home",
            "requestedAt": world.at(),
            "deliveryDeadline": world.at(hours=6),
            "sampleTypeId": st["sampleTypeId"],
            "containerType": "sealed-tube",
            "temperatureMinC": 2,
            "temperatureMaxC": 8,
            "incompatibleSampleTypes": [],
            "idempotencyKey": world.key("pulseaud"),
        },
    )
    data = engine.admin.data(status, payload)
    sample_id = data.get("sampleId")
    if status != 201 or not sample_id:
        return Verdict(ok=False, detail=f"Pulse planning did not succeed with the one seeded worker per role -- status={status} payload={payload}")

    # Discover the REAL configured offer TTL from the app's own response
    # (AssignmentWorkItem.offerExpiresAt) rather than assuming any particular
    # value -- this probe must be correct whether the app under test was
    # started with golden-mongo's out-of-the-box default or a fast
    # test-only override (see this session's established
    # ASSIGNMENT_OFFER_TTL_SECONDS pattern).
    collector_sess = world.session("collector")
    work_status, work_payload = collector_sess.raw("list_role_work", {})
    work_data = collector_sess.data(work_status, work_payload)
    offer_expires_raw = next((item.get("offerExpiresAt") for item in work_data.get("pending", []) or [] if item.get("sampleId") == sample_id), None)
    offer_expires_at = parse_rfc3339(offer_expires_raw) if offer_expires_raw else None
    if offer_expires_at is None:
        return Verdict(ok=False, detail=f"could not discover the collection assignment's offerExpiresAt from list_role_work: {work_data.get('pending')}")

    buffer_seconds = max(3 * engine_wait_seconds("ENGINE_MAINTENANCE_INTERVAL_SECONDS", 2.0), 5.0)
    wait_seconds = max(0.0, (offer_expires_at - now_utc()).total_seconds()) + buffer_seconds
    _time.sleep(wait_seconds)

    sample_status, sample_payload = engine.admin.raw("read_sample_state", path_param=sample_id)
    sample_data = engine.admin.data(sample_status, sample_payload)
    # SampleState nests status under `state.status` (not a top-level
    # `status` field) per the pinned contract's schema.
    sample_status_value = (sample_data.get("state") or {}).get("status")
    became_exception = sample_status_value == "exception"

    audit_status, audit_payload = engine.admin.raw("search_audit_trail", {"entityType": "sample", "entityId": sample_id, "limit": 200, "order": "oldest_first"})
    audit_data = engine.admin.data(audit_status, audit_payload)
    audited = any(e.get("action") == "sample_exception_recorded" for e in audit_data.get("entries", []) or [])

    ok = became_exception and audited
    return Verdict(
        ok=ok,
        detail=(
            f"waited {wait_seconds:.1f}s past discovered offerExpiresAt={offer_expires_raw}; "
            f"sample.state.status={sample_status_value} became_exception={became_exception} "
            f"sample_exception_recorded_in_audit_trail={audited} (entries={len(audit_data.get('entries', []) or [])})"
        ),
        evidence={"sample": sample_data, "audit_entries": audit_data.get("entries")},
    )


def _run_search_audit_trail(engine) -> CapabilityReport:
    world = engine.new_world("aud2")
    world.user("plainrequester", ["requester"])
    req_sess = world.session("plainrequester")
    # Trigger one genuine rejection (AC-72: failures are audited too).
    denied_status, denied_payload = req_sess.raw("read_operations_state", {"filters": {}})

    status, payload = engine.admin.raw("search_audit_trail", {"limit": 200, "order": "oldest_first"})
    data = engine.admin.data(status, payload)
    entries = data.get("entries", [])
    required_fields = ("actorId", "role", "action", "entityType", "entityId", "success", "details")
    all_fields_present = all(all(f in e for f in required_fields) for e in entries[:50]) if entries else False
    failure_entry = next((e for e in entries if e.get("success") is False and e.get("actorId") == req_sess.user_id), None)

    pulse_probe = _run_pulse_exception_audit_probe(engine)
    positive = Verdict(
        ok=status == 200 and bool(entries) and all_fields_present and (failure_entry is not None or denied_status == 403) and pulse_probe.ok,
        detail=(
            f"status={status} total={data.get('total')} all_seven_fields_present={all_fields_present} failure_recorded={failure_entry is not None}; "
            f"pulse_offer_expiry_audit_probe: {pulse_probe.detail}"
        ),
        evidence={"failure_entry": failure_entry, "pulse_probe": pulse_probe.evidence},
    )

    bad_status, bad_payload = engine.admin.raw("search_audit_trail", {"limit": 0})
    negative = Verdict(ok=bad_status == 400 and engine.admin.code(bad_payload) == "invalid_limit", detail=f"limit=0 -> status={bad_status} code={engine.admin.code(bad_payload)!r}")
    return CapabilityReport(positive=positive, negative=negative)


def _run_deadline_expiry_orphan_probe(engine) -> Verdict:
    """orphan-assignments-on-expiry: when a sample's deliveryDeadline elapses
    before collection, the timer-driven `Maintain()` pass must not just mark
    the journey cancelled -- it must also cancel the sample's still-pending
    assignments, or a live, un-cancelled work item is left pointing at an
    already-dead journey (an "orphan" that jams later plan edits which
    assume every assignment on an inactive journey has been resolved).
    Previously untested by any capability (no probe paid the real
    wall-clock cost of waiting past a deadline); verified against
    `anchors/mutants/orphan-assignments-on-expiry`.

    A tiny (1-minute) network + a near-term deadline keeps the real wait
    bounded (~90s + a maintenance-tick buffer) instead of hours.
    """
    world = engine.new_world("orphan")
    st = world.sample_type("ORPHAN-ST")
    pickup = world.location("pickup", ["collection_point"], 76.0, 76.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 76.001, 76.001)
    world.connection(pickup["locationId"], lab["locationId"], travelMinutes=1, costUnits=1, cooled=False)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for r in (pickup["locationId"], lab["locationId"]):
        world.capacity(r, units=20, cooled_units=20)
    world.user("collector", ["collection_professional"], worker=True)
    collector_id = world.user_id("collector")
    engine.park_field_workers(exclude={collector_id})

    deadline_offset = 90.0
    body = {
        "requesterId": world.default_requester_id(),
        "origin": {"latitude": 76.0, "longitude": 76.0},
        "originType": "home",
        "requestedAt": world.moment(),
        "earliestCollectionAt": world.at(minutes=-5),
        "latestCollectionAt": world.at(seconds=30),
        "deliveryDeadline": world.at(seconds=deadline_offset),
        "sampleTypeId": st["sampleTypeId"],
        "containerType": "sealed-tube",
        "temperatureMinC": 2,
        "temperatureMaxC": 8,
        "incompatibleSampleTypes": [],
        "idempotencyKey": world.key("orphan"),
    }
    requester = world.requester_session()
    status, payload = requester.raw("create_collection_request", body)
    data = requester.data(status, payload)
    sample_id, journey_id = data.get("sampleId"), data.get("journeyId")
    assignment_ids = set(data.get("activeAssignmentIds", []) or [])
    if status != 201 or not (sample_id and journey_id and assignment_ids):
        return Verdict(ok=False, detail=f"planning did not produce a driveable journey for the deadline-expiry probe -- status={status} payload={payload}")

    collector_sess = world.session("collector")
    pre_status, pre_payload = collector_sess.raw("list_role_work", {})
    pre_data = collector_sess.data(pre_status, pre_payload)
    pre_pending_ids = {item.get("assignmentId") for item in pre_data.get("pending", []) or []}
    collection_aid = next(iter(assignment_ids & pre_pending_ids), None)
    if not collection_aid:
        return Verdict(
            ok=False,
            detail=(
                "precondition-forcing failed: the seeded collector was never offered the collection assignment "
                f"before waiting for expiry (pending={sorted(pre_pending_ids)}, journey assignments={sorted(assignment_ids)})"
            ),
        )

    # Deliberately do NOT accept/collect -- the assignment must still be
    # live and pending when the deadline elapses, or there is nothing left
    # to orphan.
    buffer_seconds = max(3 * engine_wait_seconds("ENGINE_MAINTENANCE_INTERVAL_SECONDS", 2.0), 5.0)
    wait_seconds = deadline_offset + buffer_seconds
    _time.sleep(wait_seconds)

    sample_status, sample_payload = engine.admin.raw("read_sample_state", path_param=sample_id)
    sample_data = engine.admin.data(sample_status, sample_payload)
    # SampleState nests status under `state.status`, not a top-level field.
    sample_status_value = (sample_data.get("state") or {}).get("status")
    expired = sample_status_value == "expired"

    journey_status, journey_payload = engine.admin.raw("read_journey_plan", {"journeyId": journey_id})
    journey_data = engine.admin.data(journey_status, journey_payload)
    journey_terminal = journey_data.get("status") == "cancelled"

    post_status, post_payload = collector_sess.raw("list_role_work", {})
    post_data = collector_sess.data(post_status, post_payload)
    still_pending = any(item.get("assignmentId") == collection_aid for item in post_data.get("pending", []) or [])
    completed_entry = next((item for item in post_data.get("completed", []) or [] if item.get("assignmentId") == collection_aid), None)
    cancelled_not_orphaned = (not still_pending) and completed_entry is not None and completed_entry.get("status") == "cancelled"

    ok = expired and journey_terminal and cancelled_not_orphaned
    return Verdict(
        ok=ok,
        detail=(
            f"waited {wait_seconds:.1f}s past a {deadline_offset:.0f}s deadline; sample.state.status={sample_status_value} "
            f"journey.status={journey_data.get('status')} still_pending={still_pending} "
            f"completed_entry_status={completed_entry.get('status') if completed_entry else None} (not-orphaned means the "
            "assignment left the pending bucket AND landed in completed with status=cancelled)"
        ),
        evidence={"sample": sample_data, "journey": journey_data, "post_list_role_work": post_data},
    )


def _run_list_role_work(engine) -> CapabilityReport:
    world, net, created = _seed_and_plan(engine, "lrw")
    owner_sess = world.session("owner")
    status, payload = owner_sess.raw("list_role_work", {})
    data = owner_sess.data(status, payload)
    buckets_present = all(b in data for b in ("pending", "inProgress", "completed"))

    orphan_probe = _run_deadline_expiry_orphan_probe(engine)
    positive = Verdict(
        ok=status == 200 and buckets_present and orphan_probe.ok,
        detail=f"status={status} buckets={list(data.keys())}; deadline_expiry_orphan_probe: {orphan_probe.detail}",
        evidence={"orphan_probe": orphan_probe.evidence},
    )

    world.user("otherrole", ["requester"])
    bad_status, bad_payload = world.session("otherrole").raw("list_role_work", {"role": "dispatcher"})
    negative = Verdict(ok=bad_status == 403, detail=f"requesting a role you don't hold -> status={bad_status} code={world.session('otherrole').code(bad_payload)!r}")
    return CapabilityReport(positive=positive, negative=negative)


def _run_read_ui_bootstrap(engine) -> CapabilityReport:
    status, payload = engine.admin.raw("read_ui_bootstrap")
    data = engine.admin.data(status, payload)
    positive = Verdict(ok=status == 200 and "user" in data and "serverTime" in data, detail=f"status={status} keys={list(data.keys())}")

    anon = engine.admin.new_session("")
    bad_status, bad_payload = anon.raw("read_ui_bootstrap")
    negative = Verdict(ok=bad_status == 401, detail=f"anonymous -> status={bad_status} code={anon.code(bad_payload)!r}")
    return CapabilityReport(positive=positive, negative=negative)


def _run_stream_realtime_events(engine) -> CapabilityReport:
    status, payload = engine.admin.raw("stream_realtime_events")
    data = engine.admin.data(status, payload)
    plain_ok = status == 200 and "events" in data and "cursor" in data
    # The contract permits EITHER a real WebSocket upgrade (101) or plain
    # long-polling (200) for this op -- only try the handshake if the plain
    # path didn't already pass, so a normal golden-style run never pays for
    # the extra socket round-trip.
    ws_ok = False if plain_ok else _try_websocket_upgrade(engine.admin, "stream_realtime_events")
    positive = Verdict(
        ok=plain_ok or ws_ok,
        # `data.get('events', [])` only substitutes [] when the KEY is absent
        # -- a candidate whose events field serialises as a Go nil slice
        # (JSON `null`, key present) still yields None here and crashes len()
        # (confirmed live against sonnet-medium). `or []` coalesces both cases.
        detail=f"status={status} events={len(data.get('events') or [])} plain_get_ok={plain_ok} websocket_upgrade_ok={ws_ok}",
    )

    anon = engine.admin.new_session("")
    bad_status, bad_payload = anon.raw("stream_realtime_events")
    negative = Verdict(ok=bad_status == 401, detail=f"anonymous -> status={bad_status} code={anon.code(bad_payload)!r}")
    return CapabilityReport(positive=positive, negative=negative)


def _run_system_readiness(engine) -> CapabilityReport:
    status, payload = engine.admin.raw("system_readiness")
    positive = Verdict(ok=status == 200 and isinstance(payload, dict) and payload.get("status") == "ready", detail=f"status={status} payload={payload}")
    return CapabilityReport(positive=positive)


CAPABILITIES = [
    Capability("read_sample_state", "read", "A requester's own sample view; RBAC hides other requesters' samples (AC-2).", _run_read_sample_state),
    Capability("read_sample_lifecycle", "read", "Ordered status timeline plus custody/holder/staleness fields (AC-4, AC-65..69).", _run_read_sample_lifecycle),
    Capability("read_journey_plan", "read", "Full route/leg/plan-version view, RBAC-restricted to the roles that own it.", _run_read_journey_plan),
    Capability("read_operations_state", "read", "Dispatcher/administrator operational aggregate: samples, workers, capacity, delays, exceptions.", _run_read_operations_state),
    Capability(
        "search_audit_trail",
        "read",
        "Every audit entry carries actorId/role/action/entityType/entityId/success/details (AC-71); rejections are audited too (AC-72); "
        "a timer-driven Pulse offer-expiry exception is audited too (sample_exception_recorded), not just visible on the sample record.",
        _run_search_audit_trail,
        priority=True,
    ),
    Capability(
        "list_role_work",
        "read",
        "Per-role current (pending/inProgress) AND past (completed) work buckets (AC-3); a deadline-expired sample's pending "
        "assignment is properly cancelled by the timer-driven maintenance pass, not left orphaned on a dead journey.",
        _run_list_role_work,
        priority=True,
    ),
    Capability("read_ui_bootstrap", "read", "Authenticated bootstrap payload for the frontend; anonymous access is refused.", _run_read_ui_bootstrap),
    Capability("stream_realtime_events", "read", "Live event stream for the tracking map; anonymous access is refused.", _run_stream_realtime_events),
    Capability("system_readiness", "read", "`/ready` returns 200 {\"status\":\"ready\"} once the app can serve requests.", _run_system_readiness),
]
