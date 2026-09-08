"""Fairness OUTCOME probes for the worker-offer algorithm (AC-128/129/130).

instruction.md (Journey and assignment rules) requires that offering logic
"consider each worker's current workload and recent assignments" -- but it
deliberately does not prescribe a formula. A naive test that assumed a
specific weighting would unfairly fail a candidate who implements a
different, equally reasonable algorithm. These three capabilities test the
OUTCOME instead of the MECHANISM: is fairness genuinely happening, not which
formula produced it. Any implementation that does not ignore workload/
recency entirely should pass; only a broken one (always offering the same
worker, or a rule that looks only at role/qualification/location and never
at load or history) should fail.

golden-mongo's own `fairWorker` (backend/internal/service/planning.go, around
line 780) is read here purely as a REFERENCE for realistic behaviour --
sorting candidates by active workload ascending, then same-role recent-offer
count ascending, then last-offered time (never-offered first, then oldest
first), then deterministic tiebreakers. Nothing below asserts that exact
ordering; each scenario is deliberately extreme (or, for the third,
deliberately loosely bounded) so that ANY reasonable fairness algorithm
agrees with the assertion, per the task that produced this file.

Three scenarios, one capability each:

  fairness_workload_disparity   -- AC-128. Two node handlers assigned to the
      SAME node (location-matched so workload is the only differing
      variable); one built up to ~18 accepted, uncompleted ("active") node
      assignments, the other with zero and never offered anything. A new
      node assignment at that node must go to the zero-workload worker.

  fairness_recency_disparity    -- AC-129. Two otherwise-identical eligible
      workers, each given exactly one assignment which they accept AND
      complete (so both return to zero ACTIVE workload), separated by a real
      wall-clock gap. A further eligible assignment must go to whichever one
      has been idle longer, isolating "recent assignments" from workload.

  fairness_offer_distribution   -- AC-130. 3-4 identical, always-available
      workers with no distinguishing signal; 8-12 sequential eligible
      assignments, each accepted+completed immediately by whoever receives
      it before the next is created. The resulting distribution must be
      loosely even (no worker's share > ~2x the average) -- deliberately
      loose so it only catches "fairness is being ignored entirely", not a
      specific expected split.

Discovery, never an assumed index or position: which worker an offer landed
on is the app's own choice, so every scenario below asks the app itself (via
`list_role_work`) rather than assuming a slot in `activeAssignmentIds` --
same idea as `custody_lifecycle.py`'s `_classify_assignments`/
`_discover_owner`, reimplemented locally here to keep this module
self-contained.
"""
from __future__ import annotations

import math
import time as _time
from typing import Optional

from client import engine_wait_seconds
from oracle import Capability, CapabilityReport, Verdict


# --------------------------------------------------------------- discovery


def _owner_among(world, assignment_ids: set[str], aliases: list[str]) -> tuple[Optional[str], Optional[str]]:
    """Which of `aliases` currently shows one of `assignment_ids` as PENDING
    (i.e. just offered, not yet accepted) in their own `list_role_work` --
    returns (alias, assignmentId), or (None, None) if none of them do.
    Never assumes a position in `activeAssignmentIds`; asks the app."""
    for alias in aliases:
        sess = world.session(alias)
        status, payload = sess.raw("list_role_work", {})
        data = sess.data(status, payload)
        pending_ids = {item.get("assignmentId") for item in data.get("pending", []) or []}
        hit = pending_ids & assignment_ids
        if hit:
            return alias, next(iter(hit))
    return None, None


def _basic_collection_network(world, tag: str) -> tuple[dict, dict, dict, dict]:
    """One collection point + one laboratory + one direct cooled connection,
    ample capacity -- the smallest serviceable world for the two
    collection-leg-only scenarios (recency disparity, distribution)."""
    st = world.sample_type(f"{tag}-ST")
    pickup = world.location("pickup", ["collection_point"], 61.0, 61.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 61.01, 61.01)
    conn = world.connection(pickup["locationId"], lab["locationId"], travelMinutes=10, costUnits=1, cooled=True)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for resource_id in (pickup["locationId"], lab["locationId"], conn["connectionId"]):
        world.capacity(resource_id, units=80, cooled_units=80)
    return st, pickup, lab, conn


def _collection_request_body(world, st: dict, pickup: dict, tag: str) -> dict:
    return {
        "requesterId": world.default_requester_id(),
        "origin": {"latitude": pickup["coordinate"]["latitude"], "longitude": pickup["coordinate"]["longitude"]},
        "originType": "home",
        # Same collection-window-edge handling as custody_lifecycle/World --
        # requestedAt (now-10min) must precede earliestCollectionAt (now-5min)
        # so the window strictly follows the request for a strict-after
        # candidate, while staying valid for lenient golden.
        "requestedAt": world.at(minutes=-10),
        "deliveryDeadline": world.at(hours=10),
        "earliestCollectionAt": world.at(minutes=-5),
        "latestCollectionAt": world.at(hours=4),
        "sampleTypeId": st["sampleTypeId"],
        "containerType": "sealed-tube",
        "temperatureMinC": 2,
        "temperatureMaxC": 8,
        "incompatibleSampleTypes": [],
        "idempotencyKey": world.key(tag),
    }


def _fair_plan(engine, world, st: dict, pickup: dict, tag: str) -> dict:
    """Plan a fairness request, waiting first for the just-added network to be
    published as serviceable (see World.await_serviceable) so an async/versioned
    candidate isn't raced into a false `unserviceable` that starves the offer
    setup this scenario measures. After the first call the check returns
    immediately, so the per-request cost is one dry-run round-trip."""
    return world.plan_journey(_collection_request_body(world, st, pickup, tag))


def _accept_and_complete(world, sess, sample_id: str, assignment_id: str, tag: str) -> tuple[int, int]:
    acc_status, acc_payload = sess.raw("accept_assignment", {"assignmentId": assignment_id, "idempotencyKey": world.key(f"{tag}-acc")})
    col_status, col_payload = sess.raw(
        "collect_and_seal_sample",
        {
            "sampleId": sample_id,
            "assignmentId": assignment_id,
            "collectedAt": world.moment(),
            "containerType": "sealed-tube",
            "observedTemperatureC": 5,
            "idempotencyKey": world.key(f"{tag}-col"),
        },
    )
    return acc_status, col_status


# ------------------------------------------------------- AC-128: workload


def _run_workload_disparity(engine) -> CapabilityReport:
    world = engine.new_world("fw-load")
    st = world.sample_type("FWLOAD-ST")
    pickup = world.location("pickup", ["collection_point"], 62.0, 62.0, coverageKm=5)
    hub = world.location("hub", ["transfer_point", "hub"], 62.01, 62.01)
    lab = world.location("lab", ["laboratory"], 62.02, 62.02)
    conn1 = world.connection(pickup["locationId"], hub["locationId"], travelMinutes=5, costUnits=1, cooled=True)
    conn2 = world.connection(hub["locationId"], lab["locationId"], travelMinutes=5, costUnits=1, cooled=True)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for resource_id in (pickup["locationId"], hub["locationId"], lab["locationId"], conn1["connectionId"], conn2["connectionId"]):
        world.capacity(resource_id, units=40, cooled_units=40)

    # A collection_professional and a delivery_partner must exist so every
    # journey below plans end to end -- fairness among THEM is not under
    # test here, only the node_handler offer at `hub`. Not location-scoped,
    # so one of each is enough regardless of how many legs the route has.
    world.user("collector", ["collection_professional"], worker=True)
    world.user("courier", ["delivery_partner"], worker=True)

    world.user("handler_loaded", ["node_handler"], worker=True, location_id=hub["locationId"])
    handler_sess = world.session("handler_loaded")

    n_setup = 18
    accepted_count = 0
    setup_failures: list[str] = []
    for i in range(n_setup):
        created = _fair_plan(engine, world, st, pickup, f"load{i}")
        assignment_ids = set(created.get("activeAssignmentIds", []) or [])
        winner, node_aid = _owner_among(world, assignment_ids, ["handler_loaded"])
        if not (winner and node_aid):
            setup_failures.append(f"setup#{i}: no pending node assignment discovered for handler_loaded among {sorted(assignment_ids)}")
            continue
        acc_status, acc_payload = handler_sess.raw("accept_assignment", {"assignmentId": node_aid, "idempotencyKey": world.key(f"load{i}-acc")})
        if acc_status == 201:
            accepted_count += 1
        else:
            setup_failures.append(f"setup#{i}: accept_assignment -> status={acc_status} code={handler_sess.code(acc_payload)!r}")

    if accepted_count < n_setup:
        return CapabilityReport(
            positive=Verdict(
                ok=False,
                detail=(
                    f"could only build {accepted_count}/{n_setup} accepted (active) node assignments for the "
                    f"loaded worker before running the actual probe, so the workload disparity would not be "
                    f"genuinely extreme -- first failures: {setup_failures[:5]}"
                ),
            )
        )

    # ONLY NOW create the zero-workload, never-offered worker at the SAME
    # node -- creation order must not be what decides the next offer here,
    # workload must.
    world.user("handler_fresh", ["node_handler"], worker=True, location_id=hub["locationId"])

    trigger = _fair_plan(engine, world, st, pickup, "load-trigger")
    trigger_ids = set(trigger.get("activeAssignmentIds", []) or [])
    winner, node_aid = _owner_among(world, trigger_ids, ["handler_loaded", "handler_fresh"])

    positive = Verdict(
        ok=winner == "handler_fresh",
        detail=(
            f"handler_loaded carried {accepted_count} accepted, uncompleted node assignments (active workload) "
            f"at node {hub['locationId']!r}; handler_fresh carried 0 and had never been offered anything at the "
            f"SAME node; the new node assignment there went to {winner!r} (assignmentId={node_aid}) -- expected "
            f"'handler_fresh'. This is an outcome check (AC-128): any workload-aware fairness algorithm, whatever "
            f"its exact weighting, must agree on this extreme case."
        ),
        evidence={"trigger_activeAssignmentIds": sorted(trigger_ids), "winner": winner, "loaded_active_count": accepted_count},
    )
    return CapabilityReport(positive=positive, notes="AC-128 outcome probe -- extreme active-workload disparity, location-matched to isolate it as the only differing variable")


# ------------------------------------------------------- AC-129: recency


def _run_recency_disparity(engine) -> CapabilityReport:
    world = engine.new_world("fw-rec")
    st, pickup, lab, conn = _basic_collection_network(world, "FWREC")

    world.user("worker_a", ["collection_professional"], worker=True)
    world.user("worker_b", ["collection_professional"], worker=True)
    ours = {world.user_id("worker_a"), world.user_id("worker_b")}
    # collection_professional is not location-scoped, so (unlike AC-128's
    # node-handler pair) any collection_professional left over from an
    # earlier capability in this same run would also be eligible here and
    # could win an offer instead of our two -- park everyone else, exactly
    # as custody_lifecycle.py/disruption.py already do for the same reason.
    engine.park_field_workers(exclude=ours)

    def drive_one(tag: str) -> tuple[Optional[str], dict]:
        created = _fair_plan(engine, world, st, pickup, tag)
        assignment_ids = set(created.get("activeAssignmentIds", []) or [])
        winner, aid = _owner_among(world, assignment_ids, ["worker_a", "worker_b"])
        if not (winner and aid):
            return None, {"created": created}
        acc_status, col_status = _accept_and_complete(world, world.session(winner), created.get("sampleId"), aid, tag)
        return winner, {"accept_status": acc_status, "collect_status": col_status}

    # A REAL wall-clock gap, not a client-supplied timestamp: LastOfferedAt
    # is set server-side at offer time from the app's own clock, so this
    # engine has no way to backdate/forward-date it directly (see
    # engine/client.py's `now_utc()`/`world.at()` -- `at()` only controls the
    # REQUEST WINDOW fields, never the app's own fairness bookkeeping). The
    # gap only needs to exceed the app's timestamp granularity (golden-mongo
    # truncates to whole seconds, per client.py's `moment()` docstring) --
    # `engine_wait_seconds` lets a slower/faster app under test override the
    # default via env var, same mechanism `tracking_audit.py` already uses
    # for timer-driven probes.
    gap_seconds = engine_wait_seconds("FAIRNESS_RECENCY_GAP_SECONDS", 2.5)

    first_winner, first_result = drive_one("rec1")
    _time.sleep(gap_seconds)
    second_winner, second_result = drive_one("rec2")

    if not first_winner or not second_winner or first_winner == second_winner:
        return CapabilityReport(
            positive=Verdict(
                ok=False,
                detail=(
                    f"setup did not produce exactly one completed assignment for EACH of the two workers "
                    f"(first_winner={first_winner!r} result={first_result}; second_winner={second_winner!r} "
                    f"result={second_result}) -- cannot isolate recency from workload unless both workers end up "
                    f"with one completed assignment each, at different real times"
                ),
            )
        )

    # Whoever won first is, by construction, the longer-idle one once the
    # gap has elapsed -- discovered from the app's own responses, not
    # assumed from which alias was created first.
    stale_alias, recent_alias = first_winner, second_winner

    trigger = _fair_plan(engine, world, st, pickup, "rec-trigger")
    trigger_ids = set(trigger.get("activeAssignmentIds", []) or [])
    trigger_winner, trigger_aid = _owner_among(world, trigger_ids, ["worker_a", "worker_b"])

    positive = Verdict(
        ok=trigger_winner == stale_alias,
        detail=(
            f"{stale_alias} was offered+completed a collection assignment first; after a {gap_seconds}s real-time "
            f"gap, {recent_alias} was offered+completed one too -- both now show zero ACTIVE workload but were "
            f"last given something at very different times. The next eligible assignment went to "
            f"{trigger_winner!r} (assignmentId={trigger_aid}), expected {stale_alias!r} (the longer-idle worker). "
            f"This is an outcome check (AC-129): any recency-aware fairness algorithm must agree on this extreme, "
            f"workload-equalised case."
        ),
        evidence={"stale_alias": stale_alias, "recent_alias": recent_alias, "trigger_winner": trigger_winner, "gap_seconds": gap_seconds},
    )
    return CapabilityReport(positive=positive, notes="AC-129 outcome probe -- extreme idle-time disparity with active workload held equal at zero for both workers")


# ---------------------------------------------------- AC-130: distribution


def _run_offer_distribution(engine) -> CapabilityReport:
    world = engine.new_world("fw-dist")
    st, pickup, lab, conn = _basic_collection_network(world, "FWDIST")

    aliases = ["worker1", "worker2", "worker3", "worker4"]
    for alias in aliases:
        world.user(alias, ["collection_professional"], worker=True)
    ours = {world.user_id(alias) for alias in aliases}
    engine.park_field_workers(exclude=ours)

    n_trials = 10
    counts: dict[str, int] = {alias: 0 for alias in aliases}
    undiscovered = 0
    for i in range(n_trials):
        created = _fair_plan(engine, world, st, pickup, f"dist{i}")
        assignment_ids = set(created.get("activeAssignmentIds", []) or [])
        winner, aid = _owner_among(world, assignment_ids, aliases)
        if not (winner and aid):
            undiscovered += 1
            continue
        counts[winner] += 1
        # Accepted+completed BEFORE the next request is created, so every
        # worker's workload/recency genuinely updates in between trials --
        # per the task's own instruction, this is what makes the
        # distribution reflect the app's real fairness signal rather than a
        # pile of simultaneous pending offers.
        _accept_and_complete(world, world.session(winner), created.get("sampleId"), aid, f"dist{i}")

    total = sum(counts.values())
    average = total / len(aliases) if aliases else 0.0
    # Deliberately loose: catches "fairness ignored entirely" (one worker
    # gets everything, or a first-created-wins rule that never rebalances),
    # not any specific expected split -- different valid algorithms will
    # naturally produce different, all still-acceptable distributions.
    max_allowed = max(2, math.ceil(average * 2))
    worst_alias = max(counts, key=counts.get) if counts else None
    worst_count = counts.get(worst_alias, 0) if worst_alias else 0

    positive = Verdict(
        ok=total == n_trials and undiscovered == 0 and worst_count <= max_allowed,
        detail=(
            f"{n_trials} sequential eligible assignments across {len(aliases)} identical, always-available, "
            f"never-distinguished workers -> distribution={counts} (undiscovered={undiscovered}); "
            f"average={average:.2f}, loose bound (2x average, min 2)={max_allowed}; most-loaded worker "
            f"{worst_alias!r} received {worst_count}. This is an outcome check (AC-130): any workload/recency-"
            f"aware algorithm keeps every worker's share within this loose bound, while an always-same-worker or "
            f"workload/recency-blind rule concentrates offers on one candidate and would exceed it."
        ),
        evidence={"counts": counts, "max_allowed": max_allowed},
    )
    return CapabilityReport(positive=positive, notes="AC-130 outcome probe -- loosely-bounded distribution over repeated trials, not a specific expected split")


CAPABILITIES = [
    Capability(
        "fairness_workload_disparity",
        "fairness",
        "AC-128: among two node handlers assigned to the SAME node (location-matched so workload is the only "
        "differing variable), a new node assignment there must go to the one with zero active workload, not "
        "the one carrying ~18 accepted, uncompleted node assignments.",
        _run_workload_disparity,
        priority=True,
    ),
    Capability(
        "fairness_recency_disparity",
        "fairness",
        "AC-129: among two otherwise-identical eligible workers whose active workload is equalised at zero "
        "(each accepted-and-completed exactly one assignment, separated by a real time gap), a further eligible "
        "assignment must go to whichever one has been idle longer.",
        _run_recency_disparity,
        priority=True,
    ),
    Capability(
        "fairness_offer_distribution",
        "fairness",
        "AC-130: across 8-12 sequential eligible assignments among 3-4 identical, always-available workers with "
        "no distinguishing signal, the resulting distribution must be loosely even -- no worker's share exceeds "
        "roughly 2x the average.",
        _run_offer_distribution,
        priority=True,
    ),
]
