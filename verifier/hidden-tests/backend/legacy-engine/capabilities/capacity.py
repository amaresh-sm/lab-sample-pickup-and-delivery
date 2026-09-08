"""configure_capacity, set_worker_availability.

The capacity RESERVE/RELEASE lifecycle itself (AC-47/48/49 -- reserved on
plan, released on every terminal path, never overbooked) is graded in
`custody_lifecycle.py`'s `confirm_laboratory_receipt` capability
(`_confirm_laboratory_receipt_extra`), which snapshots the journey's
pickup/laboratory/connection capacity before planning and again right after
the SAME journey's terminal `confirm_laboratory_receipt` call, and requires
`reservedUnits`/`cooledReservedUnits` to return to (or below) their pre-plan
baseline -- this is the fix for a real doc/reality mismatch this session's
mutation testing found (`anchors/mutants/capacity-leak-on-receipt`): this
docstring used to claim the check existed here AND in `disruption.py`, but
neither file actually read `reservedUnits` anywhere. `disruption.py` still
does NOT check it (cancel_journey/reroute_journey/split_journey/
merge_journeys each build their own short-lived journeys and don't drive
them to completion) -- that remains open for a further pass. This module
itself grades the two configuration primitives on their own terms only.

Ground truth: configure_capacity's two independent dimensions
(capacityUnits / cooledCapacityUnits) are both readable via
read_operations_state (dispatcher/administrator only -- 403 forbidden
otherwise), per ac_capacity_test.go.
"""
from __future__ import annotations

from oracle import Capability, CapabilityReport, Verdict


def _find_location(session, location_id: str):
    status, payload = session.raw("read_operations_state", {"filters": {}})
    data = session.data(status, payload)
    for loc in data.get("locations", []):
        if loc.get("locationId") == location_id:
            return loc
    return None


def _find_worker(session, worker_id: str):
    status, payload = session.raw("read_operations_state", {"filters": {}})
    data = session.data(status, payload)
    for worker in data.get("workers", []) or []:
        if worker.get("workerId") == worker_id:
            return worker
    return None


def _run_configure_capacity(engine) -> CapabilityReport:
    world = engine.new_world("cap")
    loc = world.location("capnode", ["collection_point"], 40.0, 40.0)
    result = engine.admin.call("configure_capacity", {"resourceId": loc["locationId"], "capacityUnits": 37, "cooledCapacityUnits": 11})
    seen = _find_location(engine.admin, loc["locationId"])
    seen_capacity = (seen or {}).get("capacity", {})
    reflected = bool(seen) and seen_capacity.get("capacityUnits") == 37 and seen_capacity.get("cooledCapacityUnits") == 11
    positive = Verdict(
        ok=result.get("capacityUnits") == 37 and result.get("cooledCapacityUnits") == 11 and reflected,
        detail=f"response capacityUnits={result.get('capacityUnits')} cooled={result.get('cooledCapacityUnits')}; ops-state reflects={reflected}",
    )

    bad_status, bad_payload = engine.admin.raw("configure_capacity", {"resourceId": loc["locationId"], "capacityUnits": -5})
    negative = Verdict(
        ok=bad_status == 400 and engine.admin.code(bad_payload) == "invalid_capacity",
        detail=f"negative capacityUnits -> status={bad_status} code={engine.admin.code(bad_payload)!r}",
    )

    world.user("plainrequester", ["requester"])
    req_sess = world.session("plainrequester")
    rbac_status, rbac_payload = req_sess.raw("read_operations_state", {"filters": {}})
    rbac_ok = rbac_status == 403 and req_sess.code(rbac_payload) == "forbidden"
    negative = Verdict(ok=negative.ok and rbac_ok, detail=negative.detail + f"; requester read_operations_state -> {rbac_status} (RBAC-gated capacity view)")

    return CapabilityReport(positive=positive, negative=negative)


def _run_set_worker_availability(engine) -> CapabilityReport:
    world = engine.new_world("swa")
    world.user("courier", ["delivery_partner"])
    worker_id = world.user_id("courier")
    result = engine.admin.call("set_worker_availability", {"workerId": worker_id, "available": True, "capacityUnits": 77})
    # VB2.6: `via` was computed but never actually gated `ok` -- decorative,
    # not a real requirement (see anchors/hollow/server.py's
    # h_set_worker_availability, a pure echo of the request body). Cross-
    # check via an INDEPENDENT read_operations_state read of the workers[]
    # list, mirroring configure_capacity's own already-correct pattern.
    seen = _find_worker(engine.admin, worker_id)
    reflected = bool(seen) and seen.get("capacityUnits") == 77 and seen.get("available") is True
    positive = Verdict(
        ok=result.get("available") is True and result.get("capacityUnits") == 77 and reflected,
        detail=f"available={result.get('available')} capacityUnits={result.get('capacityUnits')}; ops-state independent read reflects={reflected}",
    )

    bad_status, bad_payload = engine.admin.raw("set_worker_availability", {"workerId": worker_id, "available": True, "capacityUnits": -1})
    negative = Verdict(
        ok=bad_status == 400 and engine.admin.code(bad_payload) == "invalid_capacity",
        detail=f"status={bad_status} code={engine.admin.code(bad_payload)!r}",
    )
    return CapabilityReport(positive=positive, negative=negative)


# --------------------------------------------------------------- durability
# DUR3: both calls already go through engine.admin directly, matching AC-91
# (an administrator may configure capacity for any resource); reused as-is.


def _durability_write_configure_capacity(engine) -> dict:
    world = engine.new_world("capdur")
    loc = world.location("capnodedur", ["collection_point"], 40.5, 40.5)
    engine.admin.call("configure_capacity", {"resourceId": loc["locationId"], "capacityUnits": 41, "cooledCapacityUnits": 13})
    return {"resource_id": loc["locationId"]}


def _durability_verify_configure_capacity(engine, sentinel: dict) -> Verdict:
    resource_id = sentinel.get("resource_id")
    if not resource_id:
        return Verdict(ok=False, detail="no resource_id recorded at write time")
    seen = _find_location(engine.admin, resource_id)
    seen_capacity = (seen or {}).get("capacity", {})
    reflected = bool(seen) and seen_capacity.get("capacityUnits") == 41 and seen_capacity.get("cooledCapacityUnits") == 13
    return Verdict(ok=reflected, detail=f"post-restart ops-state capacity={seen_capacity}")


def _durability_write_set_worker_availability(engine) -> dict:
    world = engine.new_world("swadur")
    world.user("courierdur", ["delivery_partner"])
    worker_id = world.user_id("courierdur")
    engine.admin.call("set_worker_availability", {"workerId": worker_id, "available": True, "capacityUnits": 78})
    return {"worker_id": worker_id}


def _durability_verify_set_worker_availability(engine, sentinel: dict) -> Verdict:
    worker_id = sentinel.get("worker_id")
    if not worker_id:
        return Verdict(ok=False, detail="no worker_id recorded at write time")
    seen = _find_worker(engine.admin, worker_id)
    reflected = bool(seen) and seen.get("capacityUnits") == 78 and seen.get("available") is True
    return Verdict(ok=reflected, detail=f"post-restart ops-state worker={seen}")


CAPABILITIES = [
    Capability(
        "configure_capacity",
        "write",
        "Regular and cooled capacity are independent dimensions, visible to dispatcher/administrator only.",
        _run_configure_capacity,
        priority=True,
        durability_write=_durability_write_configure_capacity,
        durability_verify=_durability_verify_configure_capacity,
    ),
    Capability(
        "set_worker_availability", "write", "Mark a worker available/unavailable with a capacity contribution.", _run_set_worker_availability,
        durability_write=_durability_write_set_worker_availability, durability_verify=_durability_verify_set_worker_availability,
    ),
]
