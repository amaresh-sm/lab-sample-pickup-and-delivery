"""HTTP seeders for the UI scenarios.

Every scenario seeds KNOWN STATE OVER HTTP first, then opens the browser --
the same determinism discipline the API engine uses. All seeding reuses the
HTTP engine's ``client.World`` (contract-exact request bodies) and
``oracle.Engine.park_field_workers`` (fairness parking), so a seed here can
never drift from the pinned contract.

If the candidate BACKEND cannot produce the state a scenario needs (e.g. its
planner returns unserviceable for a network golden plans fine), the seeder
returns a ``SeedResult`` with ``ok=False`` and a diagnostic -- mirroring the
API engine's ``planning_failure_report`` -- so a broken backend surfaces as
"UI not exercisable", NOT as a false UI failure.
"""
from __future__ import annotations

import json
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Optional

# The UI package sits next to client.py / oracle.py inside engine/; make sure
# that directory is importable whether run as a module or a script.
_ENGINE_DIR = Path(__file__).resolve().parent.parent
if str(_ENGINE_DIR) not in sys.path:
    sys.path.insert(0, str(_ENGINE_DIR))

from client import load_contract, make_admin_session, World, PASSWORD  # noqa: E402
from oracle import Engine  # noqa: E402


def _await_serviceable(admin, body, *, tries: int = 60, delay: float = 0.25) -> bool:
    """Poll check_serviceability (a dry run -- idempotencyKey stripped, creates
    nothing) until the just-configured network is PUBLISHED as serviceable,
    bounded (~15s). Some candidates recompute/publish serviceability
    asynchronously (AC-40 permits it), so a request planned immediately after
    seeding a node legitimately reads `unserviceable` for a brief window. This
    mirrors the backend engine's World.await_serviceable."""
    probe = {k: v for k, v in body.items() if k != "idempotencyKey"}
    for _ in range(max(1, tries)):
        try:
            status, payload = admin.raw("check_serviceability", probe)
            data = admin.data(status, payload) if status == 200 else {}
        except Exception:
            status, data = 0, {}
        if status == 200 and (data.get("serviceable") is True or data.get("reason") == "serviceable"):
            return True
        time.sleep(delay)
    return False


def _plan_serviceable(admin, world, body, *, tries: int = 8, delay: float = 1.0) -> dict:
    """Create a request that SHOULD be serviceable, tolerating a candidate's
    intermittent async serviceability publication -- the SAME de-amplification
    the backend engine applies (World.plan_journey): await serviceability, then
    create; if no real journey formed, retry with a FRESH idempotencyKey (a
    forced re-plan, not an idempotent replay) up to `tries`. Returns as soon as a
    genuine journey forms; otherwise returns the last attempt so a candidate that
    genuinely cannot plan still surfaces honestly (the scenario then reports "UI
    not exercisable"). This removes FLAKINESS, never real inability -- a broken
    backend still yields no journey here."""
    attempt = dict(body)
    created: dict = {}
    for _ in range(max(1, tries)):
        _await_serviceable(admin, attempt)
        created = admin.data(*admin.raw("create_collection_request", attempt))
        if (created.get("serviceability") == "serviceable"
                and created.get("journeyId")
                and (created.get("activeAssignmentIds") or [])):
            return created
        attempt = dict(body, idempotencyKey=world.key("replan"))
        time.sleep(delay)
    return created


@dataclass
class SeedResult:
    ok: bool
    detail: str = ""
    sample_id: str = ""
    journey_id: str = ""
    owner_username: str = ""
    owner_password: str = PASSWORD
    courier_username: str = ""
    courier_password: str = PASSWORD
    intruder_username: str = ""
    intruder_password: str = PASSWORD
    # AC-3 (role work buckets): one worker who holds BOTH an active/current
    # assignment and a completed/past one, so a role-work view can be checked
    # for rendering both buckets.
    worker_username: str = ""
    worker_password: str = PASSWORD
    active_sample_id: str = ""
    completed_sample_id: str = ""
    # AC-76 (monitor dashboard) / AC-88 (audit): a dispatcher/admin viewer for a
    # scenario whose surface is not the requester's own view. Defaults to admin.
    dispatcher_username: str = ""
    dispatcher_password: str = PASSWORD
    # AC-5/AC-7 (role switching): ONE account holding >= 2 roles, so a role-switch
    # control can be checked for listing and applying a different role WITHOUT
    # adding/removing roles on the account.
    switcher_username: str = ""
    switcher_password: str = PASSWORD
    roles: list[str] = field(default_factory=list)
    # Real-time (spec line 219 "live"): a scenario that must observe the UI update
    # WITHOUT a page reload needs a way to trigger ONE more backend change AFTER
    # the page is already open. ``mutate`` closes over the live seeding
    # World/sessions and performs that step; ``mutate_expect`` is a token/regex a
    # correct live UI should surface as a result. Both default to None so every
    # existing seeder is unaffected.
    mutate: Optional[Callable[[], None]] = None
    mutate_expect: Optional[str] = None
    # Write-through confirmation (accept-assignment AC-31 et al.): a scenario
    # that exercises a WRITE through the UI needs to re-read server-side state
    # AFTER the browser has acted, without touching client.py directly (the
    # established convention is that scenarios only ever see a SeedResult).
    # ``recheck(alias)`` closes over the seeding World/sessions and returns a
    # dict describing where a given assignment currently sits from that
    # alias's OWN list_role_work -- called both before and after a UI action
    # so the assertion is grounded in a genuine HTTP re-read, never the UI's
    # own claim. None for every seeder that has no write-through to confirm.
    recheck: Optional[Callable[..., Any]] = None
    # Side-effect awareness (AC-37/AC-38 dry run): a callable that counts a
    # requester's OWN total work items (pending+inProgress+completed, via the
    # SAME list_role_work operation every other seeder already uses) over
    # HTTP. Called before and after a UI action so "the dry run created
    # nothing" is confirmed from the backend, never from the UI's own claim.
    request_count: Optional[Callable[[], int]] = None
    evidence: dict[str, Any] = field(default_factory=dict)


def _prefix_seq():
    import random
    import string

    run_id = "".join(random.choices(string.ascii_lowercase + string.digits, k=5))
    counter = {"n": 0}

    def _next() -> str:
        counter["n"] += 1
        return f"{run_id}{counter['n']:04d}"

    return _next


def _find_owner(world: World, assignment_id: str, aliases: list[str]) -> Optional[str]:
    for alias in aliases:
        status, payload = world.session(alias).raw("list_role_work", {})
        if status == 200 and assignment_id in json.dumps(payload, default=str):
            return alias
    return None


def seed_driven_journey(
    base_url: str, contract_path: str, admin_user: str, admin_pass: str
) -> SeedResult:
    """Plan a serviceable journey, drive it far enough that the sample has a
    RECORDED LOCATION (accept -> checkpoint -> collect -> record_worker_location),
    and hand back the requester who OWNS it, the courier on its transport leg,
    and an unrelated requester (the non-owner control)."""
    contract = load_contract(Path(contract_path))
    admin = make_admin_session(base_url, contract, admin_user, admin_pass)
    if not admin.token:
        return SeedResult(ok=False, detail="could not authenticate admin for seeding; UI not exercisable")

    engine = Engine(admin, _prefix_seq())
    world = World(admin, engine._prefix_seq() + "-uiseed")

    st = world.sample_type("ST")
    pickup = world.location("pickup", ["collection_point"], 71.0, 71.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 71.01, 71.01)
    conn = world.connection(pickup["locationId"], lab["locationId"], travelMinutes=10, costUnits=1, cooled=True)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for rid in (pickup["locationId"], lab["locationId"], conn["connectionId"]):
        world.capacity(rid, units=20, cooled_units=20)

    world.user("collector", ["collection_professional"], worker=True)
    world.user("courier", ["delivery_partner"], worker=True)
    world.user("lab_op", ["laboratory_team"], worker=True, location_id=lab["locationId"])
    owner = world.user("owner", ["requester"])
    intruder = world.user("intruder", ["requester"])

    # Fairness offers legs to the longest-waiting eligible worker across the
    # WHOLE app; park everyone except the workers we just seeded so our
    # collector/courier actually win the offers on a re-run against a live app.
    ours = {world.user_id(a) for a in ("collector", "courier", "lab_op")}
    engine.park_field_workers(exclude=ours)

    body = {
        "requesterId": owner["userId"],
        "origin": {"latitude": 71.0, "longitude": 71.0},
        "originType": "home",
        "requestedAt": world.at(minutes=-10),
        "deliveryDeadline": world.at(hours=10),
        "earliestCollectionAt": world.at(minutes=-5),
        "latestCollectionAt": world.at(hours=4),
        "sampleTypeId": st["sampleTypeId"],
        "containerType": "sealed-tube",
        "temperatureMinC": 2,
        "temperatureMaxC": 8,
        "incompatibleSampleTypes": [],
        "idempotencyKey": world.key("req"),
    }
    created = _plan_serviceable(admin, world, body)
    serviceable = created.get("serviceability")
    journey_id = created.get("journeyId")
    aids = created.get("activeAssignmentIds") or []
    if not (serviceable == "serviceable" and journey_id and len(aids) >= 1):
        return SeedResult(
            ok=False,
            detail=(
                "candidate produced no serviceable journey for a network golden plans "
                f"(serviceability={serviceable!r}, journeyId={bool(journey_id)}, assignments={len(aids)}); "
                "UI not exercisable -- candidate-side planning failure"
            ),
        )
    sample_id = created.get("sampleId")

    owners = {aid: _find_owner(world, aid, ["collector", "courier", "lab_op"]) for aid in aids}
    collection_aid = next((a for a, al in owners.items() if al == "collector"), None)
    transport_aid = next((a for a, al in owners.items() if al == "courier"), None)
    if not collection_aid:
        return SeedResult(
            ok=False,
            detail=f"could not classify the collection leg from the app's own list_role_work (owners={owners}); UI not exercisable",
        )

    collector = world.session("collector")
    collector.raw("accept_assignment", {"assignmentId": collection_aid, "idempotencyKey": world.key("a")})
    collector.raw("record_handling_checkpoint", {
        "sampleId": sample_id, "assignmentId": collection_aid, "locationId": pickup["locationId"],
        "containerType": "sealed-tube", "observedTemperatureC": 5, "recordedAt": world.moment(),
        "idempotencyKey": world.key("chk"),
    })
    collector.raw("collect_and_seal_sample", {
        "sampleId": sample_id, "assignmentId": collection_aid, "collectedAt": world.moment(),
        "containerType": "sealed-tube", "observedTemperatureC": 5, "idempotencyKey": world.key("col"),
    })
    collector.raw("record_worker_location", {
        "assignmentId": collection_aid, "coordinate": {"latitude": 71.0, "longitude": 71.0},
        "sequence": 1, "recordedAt": world.moment(),
    })

    # Confirm the owner can actually read a recorded location back -- this is
    # the value the UI freshness scenario asserts is rendered. If it never
    # populated, the scenario would fail for a data reason, not a UI reason;
    # capture it as evidence either way.
    st_status, st_payload = world.session("owner").raw("read_sample_state", path_param=sample_id)
    state = world.session("owner").data(st_status, st_payload)
    return SeedResult(
        ok=True,
        detail="seeded a driven, location-recorded journey",
        sample_id=sample_id,
        journey_id=journey_id,
        owner_username=owner["username"],
        courier_username=world.users["courier"]["username"],
        intruder_username=intruder["username"],
        evidence={
            "locationRecordedAt": state.get("locationRecordedAt"),
            "locationStale": state.get("locationStale"),
            "orderedLegs": len(aids),
        },
    )


def seed_active_transport_leg(
    base_url: str, contract_path: str, admin_user: str, admin_pass: str
) -> SeedResult:
    """Plan a serviceable journey, drive the collection leg to sealed, and hand
    back a courier holding the transport leg genuinely ACCEPTED but with NO
    location recorded yet -- AC-113/AC-114 need the courier to record their
    OWN first location through the UI, not have one already seeded for them.

    ``recheck()`` re-reads both contract-approved observation surfaces via the
    admin session: the sample's ``read_sample_state`` and the courier's worker
    entry in ``read_operations_state`` (live-map data). AC-113/AC-114 explicitly
    permit either surface to reflect the update, so the scenario confirms real
    persistence without imposing one backend read-model design."""
    contract = load_contract(Path(contract_path))
    admin = make_admin_session(base_url, contract, admin_user, admin_pass)
    if not admin.token:
        return SeedResult(ok=False, detail="could not authenticate admin for seeding; UI not exercisable")

    engine = Engine(admin, _prefix_seq())
    world = World(admin, engine._prefix_seq() + "-uiseed")

    st = world.sample_type("ST")
    pickup = world.location("pickup", ["collection_point"], 71.0, 71.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 71.01, 71.01)
    conn = world.connection(pickup["locationId"], lab["locationId"], travelMinutes=10, costUnits=1, cooled=True)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for rid in (pickup["locationId"], lab["locationId"], conn["connectionId"]):
        world.capacity(rid, units=20, cooled_units=20)

    world.user("collector", ["collection_professional"], worker=True)
    world.user("courier", ["delivery_partner"], worker=True)

    ours = {world.user_id(a) for a in ("collector", "courier")}
    engine.park_field_workers(exclude=ours)

    body = {
        "requesterId": world.default_requester_id(),
        "origin": {"latitude": 71.0, "longitude": 71.0},
        "originType": "home",
        "requestedAt": world.at(minutes=-10),
        "deliveryDeadline": world.at(hours=10),
        "earliestCollectionAt": world.at(minutes=-5),
        "latestCollectionAt": world.at(hours=4),
        "sampleTypeId": st["sampleTypeId"],
        "containerType": "sealed-tube",
        "temperatureMinC": 2,
        "temperatureMaxC": 8,
        "incompatibleSampleTypes": [],
        "idempotencyKey": world.key("req"),
    }
    created = _plan_serviceable(admin, world, body)
    serviceable = created.get("serviceability")
    journey_id = created.get("journeyId")
    aids = created.get("activeAssignmentIds") or []
    if not (serviceable == "serviceable" and journey_id and len(aids) >= 2):
        return SeedResult(
            ok=False,
            detail=(
                "candidate produced no serviceable multi-leg journey for a network golden plans "
                f"(serviceability={serviceable!r}, journeyId={bool(journey_id)}, assignments={len(aids)}); "
                "UI not exercisable -- candidate-side planning failure"
            ),
        )
    sample_id = created.get("sampleId")

    owners = {aid: _find_owner(world, aid, ["collector", "courier"]) for aid in aids}
    collection_aid = next((a for a, al in owners.items() if al == "collector"), None)
    transport_aid = next((a for a, al in owners.items() if al == "courier"), None)
    if not (collection_aid and transport_aid):
        return SeedResult(
            ok=False,
            detail=f"could not classify collection/transport legs from the app's own list_role_work (owners={owners}); UI not exercisable",
        )

    collector = world.session("collector")
    courier = world.session("courier")
    collector.raw("accept_assignment", {"assignmentId": collection_aid, "idempotencyKey": world.key("a")})
    collector.raw("record_handling_checkpoint", {
        "sampleId": sample_id, "assignmentId": collection_aid, "locationId": pickup["locationId"],
        "containerType": "sealed-tube", "observedTemperatureC": 5, "recordedAt": world.moment(),
        "idempotencyKey": world.key("chk"),
    })
    collector.raw("collect_and_seal_sample", {
        "sampleId": sample_id, "assignmentId": collection_aid, "collectedAt": world.moment(),
        "containerType": "sealed-tube", "observedTemperatureC": 5, "idempotencyKey": world.key("col"),
    })
    courier.raw("accept_assignment", {"assignmentId": transport_aid, "idempotencyKey": world.key("acct")})

    pre_status, pre_payload = admin.raw("read_sample_state", path_param=sample_id)
    pre_state = admin.data(pre_status, pre_payload) if pre_status == 200 else {}

    courier_id = world.users["courier"]["userId"]

    def _worker_location() -> dict[str, Any]:
        status, payload = admin.raw("read_operations_state", {})
        operations = admin.data(status, payload) if status == 200 else {}
        for worker in operations.get("workers") or []:
            if worker.get("workerId") == courier_id or worker.get("worker_id") == courier_id:
                return {
                    "workerLatestLocation": worker.get("latestLocation") or worker.get("latest_location"),
                    "workerLocationRecordedAt": worker.get("locationRecordedAt"),
                }
        return {"workerLatestLocation": None, "workerLocationRecordedAt": None}

    pre_worker = _worker_location()

    def _recheck() -> dict[str, Any]:
        status, payload = admin.raw("read_sample_state", path_param=sample_id)
        data = admin.data(status, payload) if status == 200 else {}
        return {
            "latestLocation": data.get("latestLocation"),
            "locationRecordedAt": data.get("locationRecordedAt"),
            "locationStale": data.get("locationStale"),
            **_worker_location(),
        }

    return SeedResult(
        ok=True,
        detail="seeded an accepted, un-located transport leg for the courier",
        sample_id=sample_id,
        journey_id=journey_id,
        courier_username=world.users["courier"]["username"],
        recheck=_recheck,
        evidence={
            "transportAssignmentId": transport_aid,
            "preLatestLocation": pre_state.get("latestLocation"),
            "preLocationRecordedAt": pre_state.get("locationRecordedAt"),
            "preWorkerLatestLocation": pre_worker.get("workerLatestLocation"),
            "preWorkerLocationRecordedAt": pre_worker.get("workerLocationRecordedAt"),
        },
    )


def _buckets(world: World, alias: str) -> dict[str, list[dict[str, Any]]]:
    status, payload = world.session(alias).raw("list_role_work", {})
    data = world.session(alias).data(status, payload)
    return {b: (data.get(b) or []) for b in ("pending", "inProgress", "completed")}


def _collection_leg(world: World, aids: list[str], collector_alias: str) -> Optional[str]:
    for aid in aids:
        if _find_owner(world, aid, [collector_alias]) == collector_alias:
            return aid
    return None


def seed_role_work_buckets(
    base_url: str, contract_path: str, admin_user: str, admin_pass: str
) -> SeedResult:
    """AC-3: give ONE field worker (a collection professional) both a COMPLETED
    assignment and an ACTIVE one, so the role-work view can be checked for
    rendering BOTH the current and the past bucket.

    Two journeys are planned on the same network:
      * journey A is driven until its collection leg is genuinely COMPLETED,
        which is done in a BACKEND-AGNOSTIC way -- accept -> checkpoint ->
        collect -> hand the sample OFF to the transport courier (courier
        accept -> initiate_handoff -> both-sided confirm). Custody transfer is
        what terminates the sender's (collection) leg on every conformant
        backend; some complete it at collect (golden), some only at handoff
        (e.g. Sol), so driving through the handoff avoids a golden-specific
        "collect completes the leg" assumption.
      * journey B's collection leg is only ACCEPTED (stays in-progress/active).
    Every OTHER field worker is parked, so the one seeded collector is the only
    eligible collection professional and therefore wins both collection legs
    deterministically. The two buckets are then CONFIRMED over HTTP via the
    app's own ``list_role_work`` before any page opens -- if the candidate
    backend cannot produce both (planner unserviceable / no leg offered / leg
    never completes), the result is ``ok=False`` so the scenario reports "UI
    not exercisable" rather than a false UI failure.
    """
    contract = load_contract(Path(contract_path))
    admin = make_admin_session(base_url, contract, admin_user, admin_pass)
    if not admin.token:
        return SeedResult(ok=False, detail="could not authenticate admin for seeding; UI not exercisable")

    engine = Engine(admin, _prefix_seq())
    world = World(admin, engine._prefix_seq() + "-uiwork")

    st = world.sample_type("ST")
    pickup = world.location("pickup", ["collection_point"], 71.0, 71.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 71.01, 71.01)
    conn = world.connection(pickup["locationId"], lab["locationId"], travelMinutes=10, costUnits=1, cooled=True)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for rid in (pickup["locationId"], lab["locationId"], conn["connectionId"]):
        world.capacity(rid, units=20, cooled_units=20)

    world.user("collector", ["collection_professional"], worker=True)
    world.user("courier", ["delivery_partner"], worker=True)
    world.user("lab_op", ["laboratory_team"], worker=True, location_id=lab["locationId"])
    ours = {world.user_id(a) for a in ("collector", "courier", "lab_op")}
    engine.park_field_workers(exclude=ours)

    def _request_body() -> dict[str, Any]:
        return {
            "requesterId": world.default_requester_id(),
            "origin": {"latitude": 71.0, "longitude": 71.0},
            "originType": "home",
            "requestedAt": world.at(minutes=-10),
            "deliveryDeadline": world.at(hours=10),
            "earliestCollectionAt": world.at(minutes=-5),
            "latestCollectionAt": world.at(hours=4),
            "sampleTypeId": st["sampleTypeId"],
            "containerType": "sealed-tube",
            "temperatureMinC": 2,
            "temperatureMaxC": 8,
            "incompatibleSampleTypes": [],
            "idempotencyKey": world.key("req"),
        }

    def _plan() -> tuple[Optional[str], Optional[str], Optional[str]]:
        created = _plan_serviceable(admin, world, _request_body())
        if created.get("serviceability") != "serviceable" or not created.get("journeyId"):
            return None, None, None
        aids = created.get("activeAssignmentIds") or []
        collection_aid = _collection_leg(world, aids, "collector")
        transport_aid = next(
            (aid for aid in aids if _find_owner(world, aid, ["courier"]) == "courier"), None
        )
        return created.get("sampleId"), collection_aid, transport_aid

    # ---- journey A: drive the collection leg to COMPLETED (through handoff) ----
    collector = world.session("collector")
    courier = world.session("courier")
    sample_a, aid_a, transport_a = _plan()
    if not (sample_a and aid_a):
        return SeedResult(
            ok=False,
            detail="candidate produced no serviceable journey / no collection leg for journey A; UI not exercisable -- candidate-side planning failure",
        )
    collector.raw("accept_assignment", {"assignmentId": aid_a, "idempotencyKey": world.key("accA")})
    collector.raw("record_handling_checkpoint", {
        "sampleId": sample_a, "assignmentId": aid_a, "locationId": pickup["locationId"],
        "containerType": "sealed-tube", "observedTemperatureC": 5, "recordedAt": world.moment(),
        "idempotencyKey": world.key("chkA"),
    })
    collector.raw("collect_and_seal_sample", {
        "sampleId": sample_a, "assignmentId": aid_a, "collectedAt": world.moment(),
        "containerType": "sealed-tube", "observedTemperatureC": 5, "idempotencyKey": world.key("colA"),
    })
    # Hand the sample off to the transport courier -- custody transfer completes
    # the collection (sender) leg on every conformant backend.
    if transport_a:
        courier.raw("accept_assignment", {"assignmentId": transport_a, "idempotencyKey": world.key("accTA")})
        h_status, h_payload = collector.raw("initiate_handoff", {
            "sampleId": sample_a, "fromAssignmentId": aid_a, "toAssignmentId": transport_a,
            "receiverId": world.user_id("courier"), "initiatedAt": world.moment(),
            "idempotencyKey": world.key("ihoA"),
        })
        handoff_id = collector.data(h_status, h_payload).get("handoffId")
        if handoff_id:
            collector.raw("confirm_handoff", {"handoffId": handoff_id, "confirmationSide": "sender", "confirmedAt": world.moment(), "idempotencyKey": world.key("csA")})
            courier.raw("confirm_handoff", {"handoffId": handoff_id, "confirmationSide": "receiver", "confirmedAt": world.moment(), "idempotencyKey": world.key("crA")})

    # ---- journey B: leave the collection leg ACTIVE (accepted, in progress) ----
    sample_b, aid_b, _ = _plan()
    if not (sample_b and aid_b):
        return SeedResult(
            ok=False,
            detail="candidate produced no serviceable journey / no collection leg for journey B; UI not exercisable -- candidate-side planning failure",
        )
    collector.raw("accept_assignment", {"assignmentId": aid_b, "idempotencyKey": world.key("accB")})

    buckets = _buckets(world, "collector")
    completed_ids = {i.get("sampleId") for i in buckets["completed"]}
    active_ids = {i.get("sampleId") for i in buckets["pending"] + buckets["inProgress"]}
    if sample_a not in completed_ids or sample_b not in active_ids:
        return SeedResult(
            ok=False,
            detail=(
                "backend did not bucket the worker's assignments as expected "
                f"(completed={sorted(completed_ids)}, active={sorted(active_ids)}, "
                f"wanted completed={sample_a!r} + active={sample_b!r}); UI not exercisable"
            ),
            evidence={"buckets": {b: [i.get("status") for i in v] for b, v in buckets.items()}},
        )

    return SeedResult(
        ok=True,
        detail="seeded one worker with a completed leg and an active leg",
        worker_username=world.users["collector"]["username"],
        active_sample_id=sample_b,
        completed_sample_id=sample_a,
        evidence={
            "buckets": {b: [i.get("status") for i in v] for b, v in buckets.items()},
            "completedSample": sample_a,
            "activeSample": sample_b,
            "completedAssignmentId": aid_a,
            "activeAssignmentId": aid_b,
        },
    )


# ---------------------------------------------------------------------------
# Shared serviceable-network setup for the per-sample scenarios below. Mirrors
# the network the existing seeders build (one collection point + one laboratory
# + one cooled direct connection + a sample type accepted there, ample
# capacity), plus the three field workers and the fairness parking that let
# those workers win the offers deterministically on a live app. Factored out so
# the exception and live-update seeders stay contract-exact without re-deriving
# the fixture; the existing seeders are left untouched.
# ---------------------------------------------------------------------------
def _serviceable_network(world: World, engine: Engine) -> dict[str, Any]:
    st = world.sample_type("ST")
    pickup = world.location("pickup", ["collection_point"], 71.0, 71.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 71.01, 71.01)
    conn = world.connection(pickup["locationId"], lab["locationId"], travelMinutes=10, costUnits=1, cooled=True)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    for rid in (pickup["locationId"], lab["locationId"], conn["connectionId"]):
        world.capacity(rid, units=20, cooled_units=20)
    world.user("collector", ["collection_professional"], worker=True)
    world.user("courier", ["delivery_partner"], worker=True)
    world.user("lab_op", ["laboratory_team"], worker=True, location_id=lab["locationId"])
    ours = {world.user_id(a) for a in ("collector", "courier", "lab_op")}
    engine.park_field_workers(exclude=ours)
    return {"st": st, "pickup": pickup, "lab": lab, "conn": conn}


def _request_body(world: World, requester_id: str, sample_type_id: str) -> dict[str, Any]:
    return {
        "requesterId": requester_id,
        "origin": {"latitude": 71.0, "longitude": 71.0},
        "originType": "home",
        "requestedAt": world.at(minutes=-10),
        "deliveryDeadline": world.at(hours=10),
        "earliestCollectionAt": world.at(minutes=-5),
        "latestCollectionAt": world.at(hours=4),
        "sampleTypeId": sample_type_id,
        "containerType": "sealed-tube",
        "temperatureMinC": 2,
        "temperatureMaxC": 8,
        "incompatibleSampleTypes": [],
        "idempotencyKey": world.key("req"),
    }


def seed_exception_sample(
    base_url: str, contract_path: str, admin_user: str, admin_pass: str
) -> SeedResult:
    """AC-70 / spec lines 158,202: drive a serviceable journey to the point of
    the collection checkpoint, record a handling checkpoint whose observed
    temperature is OUTSIDE the sample's required range so the backend FLAGS the
    sample as an ``exception`` (out-of-range handling non-compliance), and hand
    back the requester who owns it plus an unrelated requester (the non-owner
    control).

    The exception status is CONFIRMED over HTTP via ``read_sample_state`` before
    any page opens -- if the candidate backend does not flag an out-of-range
    checkpoint as an exception, the result is ``ok=False`` so the scenario
    reports "UI not exercisable" rather than a false UI failure.
    """
    contract = load_contract(Path(contract_path))
    admin = make_admin_session(base_url, contract, admin_user, admin_pass)
    if not admin.token:
        return SeedResult(ok=False, detail="could not authenticate admin for seeding; UI not exercisable")

    engine = Engine(admin, _prefix_seq())
    world = World(admin, engine._prefix_seq() + "-uiexc")
    net = _serviceable_network(world, engine)
    owner = world.user("owner", ["requester"])
    intruder = world.user("intruder", ["requester"])

    created = _plan_serviceable(admin, world, _request_body(world, owner["userId"], net["st"]["sampleTypeId"]))
    serviceable = created.get("serviceability")
    journey_id = created.get("journeyId")
    aids = created.get("activeAssignmentIds") or []
    if not (serviceable == "serviceable" and journey_id and len(aids) >= 1):
        return SeedResult(
            ok=False,
            detail=(
                "candidate produced no serviceable journey for a network golden plans "
                f"(serviceability={serviceable!r}, journeyId={bool(journey_id)}, assignments={len(aids)}); "
                "UI not exercisable -- candidate-side planning failure"
            ),
        )
    sample_id = created.get("sampleId")

    collection_aid = next(
        (aid for aid in aids if _find_owner(world, aid, ["collector"]) == "collector"), None
    )
    if not collection_aid:
        return SeedResult(
            ok=False,
            detail="could not classify the collection leg from the app's own list_role_work; UI not exercisable",
        )

    collector = world.session("collector")
    collector.raw("accept_assignment", {"assignmentId": collection_aid, "idempotencyKey": world.key("acc")})
    # An out-of-range temperature (well outside the request's 2-8 C window) with
    # the correct container isolates TEMPERATURE non-compliance as the sole
    # reason the backend flags the sample -- the exception path under test.
    def _checkpoint():
        return collector.raw("record_handling_checkpoint", {
            "sampleId": sample_id, "assignmentId": collection_aid, "locationId": net["pickup"]["locationId"],
            "containerType": "sealed-tube", "observedTemperatureC": 42, "recordedAt": world.moment(),
            "idempotencyKey": world.key("chk"),
        })

    # AC-88 permits a checkpoint only while the caller holds custody, but the
    # exact transfer point is not fixed: one conformant app transfers custody
    # when the collector accepts; another keeps it with the requester until
    # collect-and-seal. Try the checkpoint after acceptance. If refused, first
    # establish collector custody through a compliant collection, then retry
    # the checkpoint with a fresh idempotency key. This exercises the same
    # valid holder action without imposing either lifecycle interpretation.
    chk_status, chk_payload = _checkpoint()
    if chk_status != 201:
        collect_status, collect_payload = collector.raw("collect_and_seal_sample", {
            "sampleId": sample_id,
            "assignmentId": collection_aid,
            "collectedAt": world.moment(),
            "containerType": "sealed-tube",
            "observedTemperatureC": 5,
            "idempotencyKey": world.key("col"),
        })
        if collect_status != 201:
            collect_data = collector.data(collect_status, collect_payload)
            return SeedResult(
                ok=False,
                detail=(
                    "candidate refused the checkpoint before custody transfer and then did not let the "
                    "accepted collector establish custody via collect-and-seal "
                    f"(checkpoint status={chk_status}, collect status={collect_status}, "
                    f"collect response={collect_data!r}); UI not exercisable"
                ),
                evidence={"checkpointStatus": chk_status, "collectStatus": collect_status},
            )
        chk_status, chk_payload = _checkpoint()
    chk = collector.data(chk_status, chk_payload)

    # The pinned contract's record_handling_checkpoint (201) response carries the
    # resulting sample status as `sampleStatus`; an out-of-range checkpoint must
    # yield "exception" (spec line 202). This is the contract-canonical signal --
    # read_sample_state nests its status behind a $ref view (no top-level
    # `status` key), so we confirm the exception from the checkpoint response
    # itself, which is candidate-agnostic.
    if chk.get("sampleStatus") != "exception":
        return SeedResult(
            ok=False,
            detail=(
                "candidate did not flag an out-of-range handling checkpoint as an exception "
                f"(HTTP status={chk_status}, checkpoint sampleStatus={chk.get('sampleStatus')!r}, "
                f"compliant={chk.get('compliant')!r}); "
                "UI not exercisable -- candidate-side handling-compliance behaviour"
            ),
            evidence={"checkpoint": chk},
        )

    return SeedResult(
        ok=True,
        detail="seeded a sample flagged as an exception by an out-of-range handling checkpoint",
        sample_id=sample_id,
        journey_id=journey_id,
        owner_username=owner["username"],
        intruder_username=intruder["username"],
        evidence={"sampleStatus": chk.get("sampleStatus"), "checkpointCompliant": chk.get("compliant")},
    )


def seed_multirole_user(
    base_url: str, contract_path: str, admin_user: str, admin_pass: str
) -> SeedResult:
    """AC-5 / AC-7: create ONE account holding >= 2 roles that each have a real,
    reachable authenticated view -- ``requester`` (the "Requests" workspace) and
    ``dispatcher`` (the operations/monitor workspace) -- so a role-switch control
    can be checked for (a) listing >= 2 roles and (b) actually applying a
    different one, without a driven journey.

    The account's roles are confirmed straight from the contract-canonical
    ``create_user`` response (the ``roles`` array golden-mongo -- and any
    conformant backend -- must echo back) before any page opens, so a UI-only
    scenario never masquerades a backend gap as a false UI failure.
    """
    contract = load_contract(Path(contract_path))
    admin = make_admin_session(base_url, contract, admin_user, admin_pass)
    if not admin.token:
        return SeedResult(ok=False, detail="could not authenticate admin for seeding; UI not exercisable")

    engine = Engine(admin, _prefix_seq())
    world = World(admin, engine._prefix_seq() + "-uirole")
    roles = ["requester", "dispatcher"]
    user = world.user("switcher", roles)
    got_roles = user.get("roles") or []
    if not (set(roles) <= set(got_roles)):
        return SeedResult(
            ok=False,
            detail=(
                f"candidate did not create a multi-role account as requested (wanted {roles}, "
                f"got {got_roles!r}); UI not exercisable"
            ),
        )

    return SeedResult(
        ok=True,
        detail="seeded one account holding two roles (requester + dispatcher)",
        switcher_username=user["username"],
        roles=got_roles,
        evidence={"userId": user.get("userId"), "roles": got_roles},
    )


def seed_requester_ready(
    base_url: str, contract_path: str, admin_user: str, admin_pass: str
) -> SeedResult:
    """Form/business-rule violations must be surfaced, not silently dropped
    (grounded in the pinned contract's own ``invalid_collection_window`` rule,
    AC-9/AC-10 -- window and deadline are separate constraints): seed a
    serviceable network and ONE requester ready to submit the create-request
    form, plus the EXACT inverted-collection-window values
    (``earliestCollectionAt`` scheduled AFTER ``latestCollectionAt``) that the
    pinned contract's own negative probe
    (``engine/capabilities/requests.py``) uses to trigger
    ``invalid_collection_window``.

    The inversion is confirmed a 400 ``invalid_collection_window`` refusal over
    HTTP, against THIS candidate backend, before any page opens -- so the UI
    scenario is guaranteed to submit a body this backend actually rejects,
    never a hypothetical one.
    """
    contract = load_contract(Path(contract_path))
    admin = make_admin_session(base_url, contract, admin_user, admin_pass)
    if not admin.token:
        return SeedResult(ok=False, detail="could not authenticate admin for seeding; UI not exercisable")

    engine = Engine(admin, _prefix_seq())
    world = World(admin, engine._prefix_seq() + "-uiform")
    net = _serviceable_network(world, engine)
    owner = world.user("owner", ["requester"])

    body = _request_body(world, owner["userId"], net["st"]["sampleTypeId"])
    # Mirrors capabilities/requests.py's own negative probe exactly: earliest
    # scheduled AFTER latest -- the inverted-window shape the pinned contract
    # refuses with invalid_collection_window regardless of any other candidate
    # behaviour.
    bad_earliest = world.at(hours=5)
    bad_latest = world.at(hours=1)
    bad_body = dict(body, earliestCollectionAt=bad_earliest, latestCollectionAt=bad_latest)
    bad_status, bad_payload = admin.raw("create_collection_request", bad_body)
    code = admin.code(bad_payload) if bad_status == 400 else ""
    if not (bad_status == 400 and code == "invalid_collection_window"):
        return SeedResult(
            ok=False,
            detail=(
                "candidate backend did not refuse an inverted collection window with "
                f"invalid_collection_window (status={bad_status}, code={code!r}); UI not exercisable "
                "-- candidate-side validation behaviour"
            ),
        )

    return SeedResult(
        ok=True,
        detail="seeded a serviceable network + requester; confirmed the backend refuses an inverted collection window",
        owner_username=owner["username"],
        evidence={
            "requesterId": owner["userId"],
            "sampleTypeId": net["st"]["sampleTypeId"],
            "requestedAt": body["requestedAt"],
            "deliveryDeadline": body["deliveryDeadline"],
            "earliestCollectionAt": bad_earliest,
            "latestCollectionAt": bad_latest,
            "invalidCode": code,
        },
    )


def seed_live_update(
    base_url: str, contract_path: str, admin_user: str, admin_pass: str
) -> SeedResult:
    """Real-time (spec line 219 "live"): drive a serviceable journey to a stable
    mid-state (accept -> compliant checkpoint -> collect) so the sample already
    has a multi-entry status history, then hand back a DISPATCHER viewer plus a
    ``mutate`` callable that performs ONE further backend step which adds a NEW
    status-history entry.

    Viewer = a dispatcher (not the requester): the sample lifecycle events the
    backend streams are addressed to the ``[actor, "dispatcher"]`` audience, so a
    requester's tracking view would never receive them and could not live-update
    at all. The dispatcher's tracking view is the surface that genuinely
    exercises the realtime layer.

    ``mutate`` records a SECOND handling checkpoint on the same sample. That is a
    SAMPLE-entity audited action, so (a) it grows the sample's own timeline (the
    status history the tracking view renders) by one entry, and (b) it emits a
    ``sample.*`` event carrying the sampleId to the dispatcher audience, which is
    exactly what a correct live view re-fetches on. (A handoff, by contrast, is a
    HANDOFF-entity audit -- it neither grows the sample timeline nor names the
    sample in its event payload, so it would not drive a sample-scoped live
    refresh.) The checkpoint is intentionally out-of-range so it ALSO flips the
    sample to ``exception`` -- giving ``mutate_expect`` a distinct new token the
    view should surface, on top of the entry-count increase.
    """
    contract = load_contract(Path(contract_path))
    admin = make_admin_session(base_url, contract, admin_user, admin_pass)
    if not admin.token:
        return SeedResult(ok=False, detail="could not authenticate admin for seeding; UI not exercisable")

    engine = Engine(admin, _prefix_seq())
    world = World(admin, engine._prefix_seq() + "-uilive")
    net = _serviceable_network(world, engine)
    owner = world.user("owner", ["requester"])
    dispatcher = world.user("dispatcher", ["dispatcher"])

    created = _plan_serviceable(admin, world, _request_body(world, owner["userId"], net["st"]["sampleTypeId"]))
    serviceable = created.get("serviceability")
    journey_id = created.get("journeyId")
    aids = created.get("activeAssignmentIds") or []
    if not (serviceable == "serviceable" and journey_id and len(aids) >= 1):
        return SeedResult(
            ok=False,
            detail=(
                "candidate produced no serviceable journey for a network golden plans "
                f"(serviceability={serviceable!r}, journeyId={bool(journey_id)}, assignments={len(aids)}); "
                "UI not exercisable -- candidate-side planning failure"
            ),
        )
    sample_id = created.get("sampleId")

    collection_aid = next(
        (aid for aid in aids if _find_owner(world, aid, ["collector"]) == "collector"), None
    )
    if not collection_aid:
        return SeedResult(
            ok=False,
            detail="could not classify the collection leg from the app's own list_role_work; UI not exercisable",
        )

    collector = world.session("collector")
    collector.raw("accept_assignment", {"assignmentId": collection_aid, "idempotencyKey": world.key("acc")})
    collector.raw("record_handling_checkpoint", {
        "sampleId": sample_id, "assignmentId": collection_aid, "locationId": net["pickup"]["locationId"],
        "containerType": "sealed-tube", "observedTemperatureC": 5, "recordedAt": world.moment(),
        "idempotencyKey": world.key("chk1"),
    })
    collector.raw("collect_and_seal_sample", {
        "sampleId": sample_id, "assignmentId": collection_aid, "collectedAt": world.moment(),
        "containerType": "sealed-tube", "observedTemperatureC": 5, "idempotencyKey": world.key("col"),
    })

    def _mutate() -> None:
        # ONE more SAMPLE-entity step, run AFTER the browser has the sample open:
        # a further (out-of-range) handling checkpoint. Adds a new timeline entry
        # and streams a sample event to the dispatcher audience.
        collector.raw("record_handling_checkpoint", {
            "sampleId": sample_id, "assignmentId": collection_aid, "locationId": net["pickup"]["locationId"],
            "containerType": "sealed-tube", "observedTemperatureC": 42, "recordedAt": world.moment(),
            "idempotencyKey": world.key("chk2"),
        })

    return SeedResult(
        ok=True,
        detail="seeded a mid-journey sample with a dispatcher viewer and a live mutate step",
        sample_id=sample_id,
        journey_id=journey_id,
        owner_username=owner["username"],
        dispatcher_username=dispatcher["username"],
        mutate=_mutate,
        mutate_expect=r"\bexception|out[\s-]?of[\s-]?range|\bbreach|\bnon[\s-]?complian",
        evidence={"collectionAssignment": collection_aid, "orderedLegs": len(aids)},
    )


def seed_pending_offer(
    base_url: str, contract_path: str, admin_user: str, admin_pass: str
) -> SeedResult:
    """instruction.md lines 38/48/57/67 ("Accept or decline an offered ...
    assignment") / AC-31 ("An assignment cannot be accepted by anyone other
    than the offered worker"): plan a serviceable journey and hand back the
    COLLECTION leg while it is still genuinely PENDING (offered, not yet
    accepted) -- confirmed over HTTP via the collector's own ``list_role_work``
    before any page opens -- plus an unrelated, otherwise-eligible collection
    professional (the AC-31 control) who was never offered this assignment.

    ``recheck(alias)`` re-reads (alias's OWN) ``list_role_work`` AFTER the
    browser acts, so a scenario can confirm a UI accept actually persisted
    server-side (or that an intruder's accept attempt had no effect) from a
    genuine HTTP read, never from the UI's own success/error message.
    """
    contract = load_contract(Path(contract_path))
    admin = make_admin_session(base_url, contract, admin_user, admin_pass)
    if not admin.token:
        return SeedResult(ok=False, detail="could not authenticate admin for seeding; UI not exercisable")

    engine = Engine(admin, _prefix_seq())
    world = World(admin, engine._prefix_seq() + "-uioffer")
    net = _serviceable_network(world, engine)
    # AC-31 control: a second collection professional, eligible in every way
    # (same role, same qualifications), created BEFORE parking so they are
    # parked along with every other incumbent -- irrelevant to accept-time
    # ownership (that check is on WHO the assignment was offered to, not on
    # availability), and it keeps them from ever winning THIS journey's offer
    # instead of "collector".
    intruder = world.user("intruder", ["collection_professional"], worker=True)
    ours = {world.user_id(a) for a in ("collector", "courier", "lab_op")}
    engine.park_field_workers(exclude=ours)

    created = _plan_serviceable(admin, world, _request_body(world, world.default_requester_id(), net["st"]["sampleTypeId"]))
    serviceable = created.get("serviceability")
    journey_id = created.get("journeyId")
    aids = created.get("activeAssignmentIds") or []
    if not (serviceable == "serviceable" and journey_id and aids):
        return SeedResult(
            ok=False,
            detail=(
                "candidate produced no serviceable journey for a network golden plans "
                f"(serviceability={serviceable!r}, journeyId={bool(journey_id)}, assignments={len(aids)}); "
                "UI not exercisable -- candidate-side planning failure"
            ),
        )
    sample_id = created.get("sampleId")

    collection_aid = next(
        (aid for aid in aids if _find_owner(world, aid, ["collector"]) == "collector"), None
    )
    if not collection_aid:
        return SeedResult(
            ok=False,
            detail="could not classify the collection leg from the app's own list_role_work; UI not exercisable",
        )

    def _item_for(alias: str) -> tuple[Optional[str], dict[str, Any]]:
        status, payload = world.session(alias).raw("list_role_work", {})
        data = world.session(alias).data(status, payload)
        for bucket in ("pending", "inProgress", "completed"):
            for item in data.get(bucket) or []:
                if item.get("assignmentId") == collection_aid:
                    return bucket, item
        return None, {}

    def _recheck(alias: str = "collector") -> dict[str, Any]:
        bucket, item = _item_for(alias)
        return {"bucket": bucket, "status": item.get("status")}

    bucket0, item0 = _item_for("collector")
    if bucket0 != "pending":
        return SeedResult(
            ok=False,
            detail=(
                f"collector's own list_role_work does not show the collection leg as PENDING before any "
                f"UI action (bucket={bucket0!r}, status={item0.get('status')!r}); UI not exercisable"
            ),
            evidence={"bucket": bucket0, "item": item0},
        )

    return SeedResult(
        ok=True,
        detail="seeded a genuinely pending, offered collection assignment plus an unrelated eligible worker",
        sample_id=sample_id,
        journey_id=journey_id,
        worker_username=world.users["collector"]["username"],
        intruder_username=intruder["username"],
        recheck=_recheck,
        evidence={"collectionAssignmentId": collection_aid, "preBucket": bucket0},
    )


def seed_serviceability_dry_run(
    base_url: str, contract_path: str, admin_user: str, admin_pass: str
) -> SeedResult:
    """AC-37 ("A requester can run the check independently of creating a
    request") / AC-38 ("The dry run creates NO request, sample, journey,
    assignment or capacity reservation -- snapshot counts before/after are
    identical"): seed a serviceable network + a requester, confirm over HTTP
    that ``check_serviceability`` reports the network serviceable for THIS
    candidate (so the UI check is guaranteed to land on a genuine serviceable
    verdict, not a hypothetical one), and hand back a ``request_count``
    callable that counts the requester's OWN total work items via
    ``list_role_work`` (the same op every other seeder here already uses) --
    called before and after the UI's dry-run click confirms nothing was
    created, from a genuine HTTP read rather than the UI's own claim.
    """
    contract = load_contract(Path(contract_path))
    admin = make_admin_session(base_url, contract, admin_user, admin_pass)
    if not admin.token:
        return SeedResult(ok=False, detail="could not authenticate admin for seeding; UI not exercisable")

    engine = Engine(admin, _prefix_seq())
    world = World(admin, engine._prefix_seq() + "-uidry")
    net = _serviceable_network(world, engine)
    owner = world.user("owner", ["requester"])

    probe_body = {
        "originType": "home",
        "origin": {"latitude": 71.0, "longitude": 71.0},
        "sampleTypeId": net["st"]["sampleTypeId"],
        "requestedAt": world.at(minutes=0),
        "deliveryDeadline": world.at(hours=4),
    }
    if not _await_serviceable(admin, dict(probe_body, idempotencyKey="n/a")):
        return SeedResult(
            ok=False,
            detail="candidate never published this network as serviceable via check_serviceability; UI not exercisable",
        )

    owner_session = world.session("owner")

    def _count() -> int:
        status, payload = owner_session.raw("list_role_work", {})
        data = owner_session.data(status, payload)
        return sum(len(data.get(b) or []) for b in ("pending", "inProgress", "completed"))

    baseline = _count()

    return SeedResult(
        ok=True,
        detail="seeded a serviceable network + requester; confirmed check_serviceability reports serviceable",
        owner_username=owner["username"],
        request_count=_count,
        evidence={
            "sampleTypeId": net["st"]["sampleTypeId"],
            "latitude": probe_body["origin"]["latitude"],
            "longitude": probe_body["origin"]["longitude"],
            "baselineRequestCount": baseline,
        },
    )


def seed_lab_capacity_view(
    base_url: str, contract_path: str, admin_user: str, admin_pass: str
) -> SeedResult:
    """instruction.md line 68 ("View ... regular and cooled capacity at the
    laboratory they are assigned to") / AC-91 (view/configure capacity scoped
    to the worker's OWN assigned facility): configure the LAB's regular and
    cooled capacity to distinctive, asymmetric values (never golden's generic
    20/20 fixture default, so a hollow app that renders a fixed placeholder
    can never accidentally match), assign a laboratory_team worker to that
    lab, plan a serviceable journey so the worker genuinely holds a
    laboratory-kind assignment whose destination is their OWN lab, and read
    back the EXACT ``availableUnits``/``cooledAvailableUnits`` the pinned
    contract's own ``list_role_work`` (``AssignmentWorkItem.destinationCapacity``,
    a formally named, non-golden-specific contract schema) reports for that
    worker BEFORE any page opens -- so the UI assertion checks for whatever
    number this candidate's OWN backend actually reserved, never a number we
    merely configured and assumed survived capacity reservation untouched.
    """
    contract = load_contract(Path(contract_path))
    admin = make_admin_session(base_url, contract, admin_user, admin_pass)
    if not admin.token:
        return SeedResult(ok=False, detail="could not authenticate admin for seeding; UI not exercisable")

    engine = Engine(admin, _prefix_seq())
    world = World(admin, engine._prefix_seq() + "-uicap")

    st = world.sample_type("ST")
    pickup = world.location("pickup", ["collection_point"], 71.0, 71.0, coverageKm=5)
    lab = world.location("lab", ["laboratory"], 71.01, 71.01)
    conn = world.connection(pickup["locationId"], lab["locationId"], travelMinutes=10, costUnits=1, cooled=True)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"])
    world.capacity(pickup["locationId"], units=20, cooled_units=20)
    world.capacity(conn["connectionId"], units=20, cooled_units=20)
    # Distinctive, ASYMMETRIC capacity for the lab itself -- distinguishable
    # from both the 20/20 baseline above and from a hollow "0" placeholder.
    REGULAR_UNITS, COOLED_UNITS = 58, 34
    world.capacity(lab["locationId"], units=REGULAR_UNITS, cooled_units=COOLED_UNITS)

    world.user("collector", ["collection_professional"], worker=True)
    world.user("courier", ["delivery_partner"], worker=True)
    world.user("lab_op", ["laboratory_team"], worker=True, location_id=lab["locationId"])
    ours = {world.user_id(a) for a in ("collector", "courier", "lab_op")}
    engine.park_field_workers(exclude=ours)

    created = _plan_serviceable(admin, world, _request_body(world, world.default_requester_id(), st["sampleTypeId"]))
    serviceable = created.get("serviceability")
    journey_id = created.get("journeyId")
    aids = created.get("activeAssignmentIds") or []
    if not (serviceable == "serviceable" and journey_id and aids):
        return SeedResult(
            ok=False,
            detail=(
                "candidate produced no serviceable journey for a network golden plans "
                f"(serviceability={serviceable!r}, journeyId={bool(journey_id)}, assignments={len(aids)}); "
                "UI not exercisable -- candidate-side planning failure"
            ),
        )

    status, payload = world.session("lab_op").raw("list_role_work", {})
    data = world.session("lab_op").data(status, payload)
    lab_item = None
    for bucket in ("pending", "inProgress", "completed"):
        for item in data.get(bucket) or []:
            if item.get("toLocationId") == lab["locationId"] or item.get("laboratoryId") == lab["locationId"]:
                lab_item = item
                break
        if lab_item:
            break
    if not lab_item:
        return SeedResult(
            ok=False,
            detail=(
                "candidate did not offer the laboratory_team worker a laboratory-kind work item "
                "for their own assigned lab; UI not exercisable"
            ),
            evidence={"roleWork": data},
        )
    capacity = lab_item.get("destinationCapacity")
    if not isinstance(capacity, dict):
        return SeedResult(
            ok=False,
            detail=(
                "candidate's list_role_work did not include destinationCapacity (the pinned "
                "contract's AssignmentWorkItem.destinationCapacity) on the lab worker's own work "
                "item; UI not exercisable -- candidate-side response shape"
            ),
            evidence={"item": lab_item},
        )
    avail = capacity.get("availableUnits")
    cooled_avail = capacity.get("cooledAvailableUnits")
    if not (isinstance(avail, (int, float)) and isinstance(cooled_avail, (int, float))):
        return SeedResult(
            ok=False,
            detail=(
                f"destinationCapacity missing numeric availableUnits/cooledAvailableUnits "
                f"(got {capacity!r}); UI not exercisable"
            ),
            evidence={"capacity": capacity},
        )

    return SeedResult(
        ok=True,
        detail="seeded a laboratory worker with a distinctive, HTTP-confirmed capacity at their own assigned lab",
        worker_username=world.users["lab_op"]["username"],
        journey_id=journey_id,
        evidence={
            "laboratoryId": lab["locationId"],
            "configuredRegularUnits": REGULAR_UNITS,
            "configuredCooledUnits": COOLED_UNITS,
            "availableUnits": avail,
            "cooledAvailableUnits": cooled_avail,
        },
    )


def confirm_bad_login_refused(
    base_url: str, contract_path: str, admin_user: str, admin_pass: str
) -> SeedResult:
    """Implicit authentication requirement (no dedicated AC id -- mirrors
    ``sign_out_ends_authenticated_session``'s "session-lifecycle (implicit)"):
    an authentication mechanism must REJECT invalid credentials. No state is
    seeded (any account works); instead this confirms, over HTTP via a fresh
    anonymous session and the pinned contract's own ``authenticate_user``
    operation, that THIS specific bogus username/password pair this scenario
    is about to submit through the UI really is refused (a non-2xx status) by
    THIS candidate's backend -- so the UI submission is guaranteed to hit a
    genuine rejection path, never a hypothetical one, and a real account's
    credentials are never touched or put at risk of lockout.
    """
    contract = load_contract(Path(contract_path))
    admin = make_admin_session(base_url, contract, admin_user, admin_pass)
    if not admin.token:
        return SeedResult(ok=False, detail="could not authenticate admin for seeding; UI not exercisable")

    bogus_user = f"no-such-user-{admin.user_id[-6:] if admin.user_id else 'uiseed'}-{_prefix_seq()()}"
    bogus_pass = "Wr0ng-Pass-Not-Real!"
    anon = admin.new_session("")
    status, payload = anon.raw("authenticate_user", {"username": bogus_user, "password": bogus_pass})
    if status < 400:
        return SeedResult(
            ok=False,
            detail=(
                f"candidate backend ACCEPTED a fabricated, never-created username/password pair "
                f"(status={status}); UI not exercisable -- candidate-side authentication behaviour"
            ),
            evidence={"status": status, "payload": payload},
        )

    return SeedResult(
        ok=True,
        detail="confirmed the candidate backend refuses a fabricated username/password pair over HTTP",
        owner_username=bogus_user,
        owner_password=bogus_pass,
        evidence={"status": status, "code": admin.code(payload)},
    )
