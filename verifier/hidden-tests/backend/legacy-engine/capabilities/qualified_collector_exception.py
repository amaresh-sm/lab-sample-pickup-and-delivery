"""qualified_collector_exhaustion_exception (AC-131/AC-132).

instruction.md (Sample types, and Journey and assignment rules) adds two
rules this session:

  - All four named types (Amber/Indigo/Frost/Pulse) "require a qualified
    collector -- only a certified collection professional holding the
    matching qualification may collect it."
  - "If no qualified worker is ever available for an assignment that
    requires one, and none becomes available within a reasonable time, the
    sample must be flagged as an exception for that specific reason --
    distinguishable from a missed delivery deadline."

This is the real black-box scenario for the second rule, chosen for Indigo
(not Pulse, which already has its own audit probe in tracking_audit.py):
a requester requests to collect an Indigo sample at a location that IS within
5km of an active collection point and can reach an accepting laboratory (so
the request is genuinely serviceable on every OTHER clause), but zero
collection_professional workers holding Indigo's own required qualification
exist anywhere -- so the collection assignment is created unassigned and
never offered to anyone. golden-mongo's own two-path mechanism for this
(`backend/internal/service/maintenance.go`,
`expireUnassignedQualifiedCollectionSearches`, and the shared
`recordNoQualifiedCollectorException`) is read here purely as ground truth
for what "a reasonable time" and "that specific reason" mean operationally;
nothing below asserts golden's own internal naming:

  - The requiredQualification code (golden happens to use "LX-I" for
    Indigo) is discovered from `create_sample_type`'s own response, exactly
    as `tracking_audit.py`'s pulse-exception probe already does -- never
    assumed or hardcoded.
  - The exception reason string (golden happens to use
    "no_qualified_collector_available") is discovered from
    `read_sample_lifecycle`'s own `exception.reason` field, never assumed.
  - The "reasonable time" bound is golden's own configurable
    UNASSIGNED_SEARCH_TIMEOUT_SECONDS (default 3600s, per
    `operational_timers.go`'s `unassignedSearchTimeout()`), read through
    `engine_wait_seconds` -- the same env-var-overridable-timer pattern
    `fairness.py` and `tracking_audit.py` already use, so this probe is
    correct whether the app under test runs with golden's out-of-the-box
    default or a faster test-only override.

"Distinguishable from a missed delivery deadline" is proven structurally,
not by string-matching an assumed reason: the request's own deliveryDeadline
is set far beyond the full wait, so at the moment the sample is read back
the deadline has demonstrably NOT yet elapsed -- only the qualified-collector
search-exhaustion path could have produced the exception, whatever the app
calls it, and its status must be "exception", never "expired" (the status
the SAME app uses for a genuinely missed deadline, per
`tracking_audit.py`'s own orphan-assignment probe).

Positive-only (no separate negative probe): a second full wait to prove the
contrapositive ("a collector who legitimately becomes available in time
prevents the exception") would double this capability's already-real
wall-clock cost for a fact already covered qualitatively by every OTHER
capability that plans an Indigo/Amber/Frost/Pulse journey successfully with
a seeded qualified worker (e.g. `sample_types.py`, `tracking_audit.py`'s own
pulse probe). `fairness.py`'s three timer-driven capabilities follow the same
positive-only convention for the same reason.
"""
from __future__ import annotations

import time as _time

from client import engine_wait_seconds
from oracle import Capability, CapabilityReport, Verdict


def _run_qualified_collector_exhaustion_exception(engine) -> CapabilityReport:
    world = engine.new_world("qcx")

    # Indigo, not Pulse: Pulse's own qualified-collector-exhaustion audit
    # probe already lives in tracking_audit.py, and Pulse additionally 409s
    # synchronously (`pulse_no_wait`) when nobody is eligible at creation
    # time -- a mechanism specific to Pulse's own no-batching-delay rule
    # (instruction.md), not the general "flag it as an exception" rule this
    # capability targets. Indigo has no such synchronous gate, so it proves
    # the *general* rule (now generalised, per this session's own admin.go/
    # maintenance.go change, to all four named types) on a leg that is
    # actually created unassigned and left to expire.
    st = world.sample_type("Indigo", exact_name=True)
    required_qualification = st.get("requiredQualification")
    if not st.get("qualifiedCollectorRequired") or not required_qualification:
        if st.get("_unverifiable_canonical_policy"):
            # This "Indigo" is a SUBSTITUTE (see client.py's World.sample_type):
            # the candidate deduped the real canonical create and left no
            # audit provenance to discover it by, so its real policy is
            # genuinely unmeasurable via any pinned HTTP read path -- NOT
            # evidence the real Indigo lacks the policy (confirmed live
            # against opus-max: its actual reset-seeded Indigo has
            # qualifiedCollectorRequired=true). Raise rather than assert a
            # False this engine cannot actually back up; run.py reports this
            # as unassessable (credit 0, but honest) instead of a false
            # negative.
            raise RuntimeError(
                f"Indigo's real canonical policy is unmeasurable: create was deduped and has no audit "
                f"provenance, so only a substitute type (id={st.get('sampleTypeId')}) could be read back."
            )
        return CapabilityReport(
            positive=Verdict(
                ok=False,
                detail=(
                    "Indigo sample type did not come back with qualifiedCollectorRequired=true and a "
                    f"non-empty requiredQualification -- got qualifiedCollectorRequired={st.get('qualifiedCollectorRequired')!r} "
                    f"requiredQualification={required_qualification!r}; cannot construct the 'zero eligible "
                    "collector ever' precondition this capability depends on."
                ),
            )
        )

    # A genuinely serviceable network: a collection point co-located with
    # the request origin (well within the 5km radius) and a laboratory that
    # accepts Indigo, connected, with ample capacity -- every OTHER
    # serviceability clause (AC-34) satisfied, so the ONLY reason this
    # request can ever fail to progress is the missing qualified collector,
    # never distance/routing/capacity/handling.
    pickup = world.location("qcx-pickup", ["collection_point"], 40.0, 40.0, coverageKm=5)
    lab = world.location("qcx-lab", ["laboratory"], 40.001, 40.001)
    world.connection(pickup["locationId"], lab["locationId"], travelMinutes=5, costUnits=1, cooled=False)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for resource_id in (pickup["locationId"], lab["locationId"]):
        world.capacity(resource_id, units=20, cooled_units=20)

    # Zero collection_professional workers are ever seeded by this capability,
    # and every pre-existing field worker app-wide is parked (unavailable) --
    # mirrors `engine.park_field_workers()`'s existing use in
    # `fairness.py`/`tracking_audit.py`/`disruption.py`/`custody_lifecycle.py`
    # for the identical reason: a live, not-just-reset app under test may
    # already carry workers from earlier capabilities in this same run, and
    # this scenario requires that NONE of them is ever eligible to collect.
    engine.park_field_workers()

    origin = {"latitude": 40.0, "longitude": 40.0}
    deadline_offset_hours = 12  # far beyond the wait below, so the deadline demonstrably has NOT elapsed
    body = {
        "requesterId": world.default_requester_id(),
        "origin": origin,
        "originType": "home",
        "requestedAt": world.at(),
        "deliveryDeadline": world.at(hours=deadline_offset_hours),
        "sampleTypeId": st["sampleTypeId"],
        "containerType": "sealed-tube",
        "temperatureMinC": 2,
        "temperatureMaxC": 8,
        "idempotencyKey": world.key("qcx"),
    }

    # Confirm serviceable BEFORE creating the real request -- this must be
    # the "collection point within 5km, reachable, capacity available"
    # scenario the task's rule actually targets, not an unserviceable one
    # that would be flagged/rejected for an unrelated reason.
    svc_status, svc_payload = engine.admin.raw("check_serviceability", body)
    svc_data = engine.admin.data(svc_status, svc_payload)
    serviceable = svc_status == 200 and svc_data.get("serviceable") is True

    requester = world.requester_session()
    status, payload = requester.raw("create_collection_request", body)
    data = requester.data(status, payload)
    sample_id, journey_id = data.get("sampleId"), data.get("journeyId")
    if status != 201 or not (sample_id and journey_id):
        return CapabilityReport(
            positive=Verdict(
                ok=False,
                detail=(
                    f"serviceable_precheck={serviceable}; create_collection_request did not succeed with a "
                    f"serviceable-but-zero-qualified-collector world -- status={status} payload={payload}"
                ),
            )
        )

    # Discover the collection assignment's own id from the journey plan
    # (`JourneyPlan.collectionAssignmentId`) rather than assuming a position
    # in `activeAssignmentIds` -- same discovery discipline `fairness.py`'s
    # `_owner_among` and `custody_lifecycle.py`'s `_discover_owner` already
    # use for "which one is the app's own choice".
    plan_status, plan_payload = engine.admin.raw("read_journey_plan", {"journeyId": journey_id})
    plan_data = engine.admin.data(plan_status, plan_payload)
    collection_assignment_id = plan_data.get("collectionAssignmentId")
    if plan_status != 200 or not collection_assignment_id:
        return CapabilityReport(
            positive=Verdict(
                ok=False,
                detail=f"could not discover collectionAssignmentId from read_journey_plan -- status={plan_status} payload={plan_data}",
            )
        )

    # Confirm the "never offered to anyone" precondition BEFORE waiting:
    # any offer (even one that later expired) would itself be an audit event
    # against this exact assignment id -- an empty result here is direct
    # evidence nobody was ever eligible, not an assumption.
    pre_status, pre_payload = engine.admin.raw(
        "search_audit_trail", {"entityType": "assignment", "entityId": collection_assignment_id, "limit": 50}
    )
    pre_entries = engine.admin.data(pre_status, pre_payload).get("entries", []) if pre_status == 200 else []
    never_offered_yet = pre_status == 200 and len(pre_entries) == 0

    buffer_seconds = max(3 * engine_wait_seconds("ENGINE_MAINTENANCE_INTERVAL_SECONDS", 2.0), 5.0)
    wait_seconds = engine_wait_seconds("UNASSIGNED_SEARCH_TIMEOUT_SECONDS", 3600.0) + buffer_seconds
    _time.sleep(wait_seconds)

    life_status, life_payload = engine.admin.raw("read_sample_lifecycle", {"sampleId": sample_id})
    life_data = engine.admin.data(life_status, life_payload)
    status_value = life_data.get("status")
    exception = life_data.get("exception") or {}
    reason = exception.get("reason")
    became_exception = status_value == "exception" and bool(reason)
    # Structural proof of distinguishability, not a string-match against an
    # assumed reason value: the deadline we set is `deadline_offset_hours`
    # ahead of `world.now`, and the total wait is `wait_seconds` (bounded by
    # the search-timeout env var, which validates at most 604800s = 7 days
    # per `operational_timers.go`); as long as this deployment's own
    # deadline offset comfortably exceeds its own configured search timeout,
    # the deadline provably has not elapsed, so a status of "exception"
    # (never "expired", the status the same app uses for a genuinely missed
    # deadline) can only be this rule firing.
    deadline_not_elapsed = status_value != "expired"

    # The discovered reason string must itself be searchable in the audit
    # trail -- the task's own free-text `query` filter (`searchAudit`'s
    # `auditEventMatcher` lower-cases and substring-matches both the action
    # name and the JSON-encoded `details` of every event), confirmed against
    # OUR sample rather than assumed to exist anywhere.
    search_status, search_payload = (
        engine.admin.raw("search_audit_trail", {"query": reason, "limit": 200, "order": "oldest_first"}) if reason else (0, None)
    )
    search_entries = engine.admin.data(search_status, search_payload).get("entries", []) if search_status == 200 else []
    reason_searchable_for_our_sample = any(e.get("entityId") == sample_id for e in search_entries)

    ok = (
        serviceable
        and never_offered_yet
        and became_exception
        and deadline_not_elapsed
        and reason_searchable_for_our_sample
    )
    positive = Verdict(
        ok=ok,
        detail=(
            f"serviceable_precheck={serviceable} never_offered_before_wait={never_offered_yet} "
            f"waited {wait_seconds:.1f}s past assignment creation; sample.status={status_value!r} "
            f"discovered_reason={reason!r} deadline_not_elapsed(status!=expired)={deadline_not_elapsed} "
            f"reason_searchable_via_query={reason_searchable_for_our_sample}"
        ),
        evidence={
            "sampleId": sample_id,
            "journeyId": journey_id,
            "collectionAssignmentId": collection_assignment_id,
            "requiredQualification": required_qualification,
            "exception": exception,
            "search_entries": search_entries,
        },
    )
    return CapabilityReport(positive=positive, notes="AC-131/AC-132 outcome probe -- serviceable-but-zero-qualified-collector Indigo request, waited past the app's own unassigned-search timeout")


def _durability_write_qualified_collector_exhaustion_exception(engine) -> dict:
    """DUR4's one landmine-adjacent item (see DURABILITY.md): shares the SAME
    unverifiable-canonical-policy trap as create_sample_type -- if Indigo was
    deduped with no audit provenance, this raises RuntimeError exactly like
    the positive probe does, and run.py's durability-write already catches
    that safely (logs, skips, moves on) -- no special handling needed here
    beyond not assuming the happy path. Otherwise this is a trimmed copy of
    the positive probe's own setup: build the same zero-qualified-collector
    world, wait out the same configured timeout, and stop once the sample
    reads back as an exception -- skipping the extra searchable-reason check,
    which is that capability's own concern, not durability's."""
    world = engine.new_world("qcxdur")
    st = world.sample_type("Indigo", exact_name=True)
    if not st.get("qualifiedCollectorRequired") or not st.get("requiredQualification"):
        if st.get("_unverifiable_canonical_policy"):
            raise RuntimeError(
                f"Indigo's real canonical policy is unmeasurable: create was deduped and has no audit "
                f"provenance, so only a substitute type (id={st.get('sampleTypeId')}) could be read back."
            )
        return {}

    pickup = world.location("qcxdur-pickup", ["collection_point"], 41.0, 41.0, coverageKm=5)
    lab = world.location("qcxdur-lab", ["laboratory"], 41.001, 41.001)
    world.connection(pickup["locationId"], lab["locationId"], travelMinutes=5, costUnits=1, cooled=False)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for resource_id in (pickup["locationId"], lab["locationId"]):
        world.capacity(resource_id, units=20, cooled_units=20)
    engine.park_field_workers()

    body = {
        "requesterId": world.default_requester_id(),
        "origin": {"latitude": 41.0, "longitude": 41.0},
        "originType": "home",
        "requestedAt": world.at(),
        "deliveryDeadline": world.at(hours=12),
        "sampleTypeId": st["sampleTypeId"],
        "containerType": "sealed-tube",
        "temperatureMinC": 2,
        "temperatureMaxC": 8,
        "idempotencyKey": world.key("qcxdur"),
    }
    requester = world.requester_session()
    status, payload = requester.raw("create_collection_request", body)
    data = requester.data(status, payload)
    sample_id = data.get("sampleId")
    if status != 201 or not sample_id:
        return {}

    buffer_seconds = max(3 * engine_wait_seconds("ENGINE_MAINTENANCE_INTERVAL_SECONDS", 2.0), 5.0)
    wait_seconds = engine_wait_seconds("UNASSIGNED_SEARCH_TIMEOUT_SECONDS", 3600.0) + buffer_seconds
    _time.sleep(wait_seconds)

    life_status, life_payload = engine.admin.raw("read_sample_lifecycle", {"sampleId": sample_id})
    life_data = engine.admin.data(life_status, life_payload)
    if life_data.get("status") != "exception":
        return {}
    return {"sample_id": sample_id}


def _durability_verify_qualified_collector_exhaustion_exception(engine, sentinel: dict) -> Verdict:
    sample_id = sentinel.get("sample_id")
    if not sample_id:
        return Verdict(ok=False, detail="no exception-flagged sample_id recorded at write time")
    status, payload = engine.admin.raw("read_sample_lifecycle", {"sampleId": sample_id})
    data = engine.admin.data(status, payload)
    ok = status == 200 and data.get("status") == "exception"
    return Verdict(ok=ok, detail=f"post-restart read_sample_lifecycle status={status} sampleStatus={data.get('status')!r}")


CAPABILITIES = [
    Capability(
        "qualified_collector_exhaustion_exception",
        "lifecycle",
        "AC-131/AC-132: an Indigo collection request that is serviceable in every other respect, but for which "
        "zero collection_professional workers hold the required qualification, has its collection assignment "
        "created unassigned and never offered; once the app's own configured search timeout elapses with nobody "
        "ever having become eligible, the sample is flagged as an exception with a discovered, searchable reason "
        "-- and remains distinguishable from a missed delivery deadline, whose own deadline was set far beyond "
        "the wait and therefore had not elapsed.",
        _run_qualified_collector_exhaustion_exception,
        priority=True,
        durability_write=_durability_write_qualified_collector_exhaustion_exception,
        durability_verify=_durability_verify_qualified_collector_exhaustion_exception,
    ),
]
