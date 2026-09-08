"""frost_cooled_route_enforcement: AC-16 + AC-17 -- Frost routing ENFORCEMENT.

The only pre-existing Frost check (`sample_types.py`) asserts the
`requiresCooledRoute` ATTRIBUTE on the seeded type record. It never plans a
Frost journey, so it cannot tell a real cooled-route engine from a buggy one
that stores the flag and then ignores it while routing. This capability closes
that gap: it exercises the BEHAVIOUR instruction.md demands.

  - AC-16: "Frost requires cooled storage AND transport throughout the journey"
    -- a route with a non-cooled leg is refused; an all-cooled route is planned.
  - AC-17: "Frost reserves cooled capacity at every location and leg" -- cooled
    reservations exist per leg and per location, not regular ones.

Three dimensions inside the one capability:
  * positive  (AC-16 all-cooled): a Frost request over a network whose every
    leg is a cooled connection with cooled capacity plans successfully
    (serviceable, a journey + assignment(s) created).
  * negative  (AC-16 non-cooled leg): a Frost request whose only path to the
    lab forces a NON-cooled leg comes back NOT serviceable, while the SAME
    network + request with a non-cooled-required type plans fine -- proving it
    is the cooling requirement causing the refusal, not a broken network.
  * idempotency slot, repurposed (AC-17 cooled reservations): CapabilityReport
    has no dedicated "third" field, so the third dimension rides the
    idempotency slot; its detail text names it clearly. After the positive
    plan, every leg's connection and every location the journey touches carries
    a COOLED reservation (cooledReservedUnits went up), discovered from the
    journey's own legs/stops. A non-cooled-required sample planned over the
    SAME cooled network is used as the discriminating contrast: it must reserve
    ONLY regular capacity and add NO cooled reservation -- proving the Frost
    cooled reservations exist because of the cooling requirement, not because
    the app reserves cooled capacity for everything. (golden reserves a regular
    slot too for a Frost sample -- a Frost unit physically occupies a slot AND
    needs it cooled -- so this dimension does NOT assert "regular untouched";
    that would contradict golden's genuine, correct behaviour. The cooled
    dimension moving for Frost and staying flat for the contrast is the real
    AC-17 signal.)

Anti-hardcoding discipline (a prior session found several golden-bias bugs
from hardcoded internal strings):
  * The refusal is asserted through the pinned contract's OWN public response
    enum -- create_collection_request.data.serviceability in
    ("serviceable","unserviceable") -- NEVER a golden-internal
    serviceabilityReason code (those are captured for the report only, not
    asserted).
  * Frost's forced attributes are DISCOVERED from the create response, never
    assumed to be any particular literal.
  * The set of legs/locations checked for cooled reservations is read back from
    the app's own JourneyPlan (legs[].from/toLocationId, orderedStops[]) and
    intersected with the resources this capability built, never a fixed index.
"""
from __future__ import annotations

from oracle import Capability, CapabilityReport, Verdict


class BasicNetworkLike:
    """Minimal stand-in for client.BasicNetwork so World.request_body (which
    only reads `.sample_type` and `.pickup`) can build a contract-exact request
    body without this capability hand-rolling one."""

    def __init__(self, sample_type: dict, pickup: dict):
        self.sample_type = sample_type
        self.pickup = pickup


def _capacity_map(engine) -> dict[str, dict]:
    """resource_id -> its live Capacity object, for every location AND
    connection, read the only way this HTTP-only engine reads anything:
    read_operations_state (never a datastore query)."""
    status, payload = engine.admin.raw("read_operations_state", {"filters": {}})
    data = engine.admin.data(status, payload)
    caps: dict[str, dict] = {}
    for loc in data.get("locations", []) or []:
        if loc.get("locationId"):
            caps[loc["locationId"]] = loc.get("capacity") or {}
    for conn in data.get("connections", []) or []:
        if conn.get("connectionId"):
            caps[conn["connectionId"]] = conn.get("capacity") or {}
    return caps


def _connection_index(engine) -> dict[tuple[str, str], str]:
    """(fromLocationId, toLocationId) -> connectionId, from the app's own
    read_operations_state connections list -- so a JourneyLeg (which names
    endpoints, not always a connection id) can be mapped to the connection
    resource whose capacity backs that leg."""
    status, payload = engine.admin.raw("read_operations_state", {"filters": {}})
    data = engine.admin.data(status, payload)
    idx: dict[tuple[str, str], str] = {}
    for conn in data.get("connections", []) or []:
        f, t, cid = conn.get("fromLocationId"), conn.get("toLocationId"), conn.get("connectionId")
        if f and t and cid:
            idx[(f, t)] = cid
    return idx


def _cooled_up(before: dict, after: dict) -> bool:
    b = before.get("cooledReservedUnits")
    a = after.get("cooledReservedUnits")
    return isinstance(b, (int, float)) and isinstance(a, (int, float)) and a > b


def _regular_up(before: dict, after: dict) -> bool:
    b = before.get("reservedUnits")
    a = after.get("reservedUnits")
    return isinstance(b, (int, float)) and isinstance(a, (int, float)) and a > b


def _run_frost_cooled_route_enforcement(engine) -> CapabilityReport:
    world = engine.new_world("frost")

    # A Frost-named type: golden's enforceNamedSamplePolicy forces
    # requiresCooledRoute=true on the exact name; we also pass it explicitly so
    # the property under test is well-defined regardless of name-forcing. A
    # cooled container satisfies the generic container/temperature handling
    # rule for a cooled type (independent of the ROUTE enforcement under test
    # here); the exact literal is not asserted, only that the type/request are
    # internally consistent so planning is gated purely on the route.
    FROST_CONTAINER = "cooled-vial"
    frost = world.sample_type("Frost", exact_name=True, requiresCooledRoute=True, containerType=FROST_CONTAINER)
    # A contrast type with NO cooled-route requirement, to prove the negative
    # refusal is caused by the cooling requirement, not a broken network.
    noncooled = world.sample_type("NonCooled", requiresCooledRoute=False)

    # ---------------------------------------------------------------- checks 1 & 3
    # Network A: an all-cooled TWO-leg route (pickup -> hub -> lab) with NO
    # direct pickup->lab edge, so the plan must be genuinely multi-stop -- this
    # is what makes AC-17's "per leg AND per location" a non-trivial assertion
    # (two cooled legs, three locations), not a single-hop special case.
    pickup = world.location("pickupA", ["collection_point", "transfer_point"], 60.00, 60.00, coverageKm=5)
    hub = world.location("hubA", ["transfer_point", "hub"], 60.01, 60.01, handlingMinutes=2)
    lab = world.location("labA", ["hub", "laboratory"], 60.02, 60.02, handlingMinutes=2)
    conn1 = world.connection(pickup["locationId"], hub["locationId"], travelMinutes=10, costUnits=5, cooled=True)
    conn2 = world.connection(hub["locationId"], lab["locationId"], travelMinutes=10, costUnits=5, cooled=True)
    world.lab_accepts(lab["locationId"], frost["sampleTypeId"])
    world.lab_accepts(lab["locationId"], noncooled["sampleTypeId"])  # for the AC-17 contrast
    built_a = (pickup["locationId"], hub["locationId"], lab["locationId"], conn1["connectionId"], conn2["connectionId"])
    for r in built_a:
        world.capacity(r, units=50, cooled_units=50)

    before_caps = _capacity_map(engine)

    frost_req = world.request_body(
        BasicNetworkLike(frost, pickup), world.key("frost-ok"),
        origin={"latitude": 60.00, "longitude": 60.00},
        containerType=FROST_CONTAINER,
    )
    created = world.requester_session().call("create_collection_request", frost_req)
    planned_ok = (
        created.get("serviceability") == "serviceable"
        and bool(created.get("journeyId"))
        and len(created.get("activeAssignmentIds", []) or []) >= 1
    )
    positive = Verdict(
        ok=planned_ok,
        detail=(
            f"all-cooled Frost route: serviceability={created.get('serviceability')!r} "
            f"journeyId={bool(created.get('journeyId'))} assignments={len(created.get('activeAssignmentIds', []) or [])}"
        ),
        evidence={"created": created},
    )

    # ---- AC-17: cooled reservations per leg + per location, discovered from
    # the journey's own plan.
    plan_status, plan_payload = engine.admin.raw("read_journey_plan", {"journeyId": created.get("journeyId")})
    plan = engine.admin.data(plan_status, plan_payload)
    after_caps = _capacity_map(engine)
    conn_idx = _connection_index(engine)

    legs = plan.get("legs", []) or []
    stops = plan.get("orderedStops", []) or []
    leg_conn_ids: list[str] = []
    for leg in legs:
        f, t = leg.get("fromLocationId"), leg.get("toLocationId")
        cid = conn_idx.get((f, t))
        if cid in before_caps and cid not in leg_conn_ids:
            leg_conn_ids.append(cid)
    location_ids: list[str] = []
    for stop in stops:
        lid = stop.get("locationId")
        if lid in before_caps and lid not in location_ids:
            location_ids.append(lid)
    # Fallback: also fold in leg endpoints that are built locations, in case a
    # correct plan reports stops differently.
    for leg in legs:
        for lid in (leg.get("fromLocationId"), leg.get("toLocationId")):
            if lid in before_caps and lid not in location_ids:
                location_ids.append(lid)

    per_leg = {cid: _cooled_up(before_caps.get(cid, {}), after_caps.get(cid, {})) for cid in leg_conn_ids}
    per_location = {lid: _cooled_up(before_caps.get(lid, {}), after_caps.get(lid, {})) for lid in location_ids}
    frost_legs_cooled = bool(per_leg) and all(per_leg.values())
    frost_locs_cooled = bool(per_location) and all(per_location.values())

    # Discriminating contrast: the SAME cooled network, but a non-cooled-required
    # sample. It must reserve ONLY regular capacity and add NO cooled reservation
    # on the resources it touches -- so the Frost cooled reservations above exist
    # because of the cooling requirement, not because the app reserves cooled
    # capacity for every sample. Measured as a before/after delta around just
    # this request (reservations accumulate, so a delta is the sound measure).
    contrast_before = _capacity_map(engine)
    nc_req = world.request_body(
        BasicNetworkLike(noncooled, pickup), world.key("frostnet-noncooled"),
        origin={"latitude": 60.00, "longitude": 60.00},
    )
    nc_created = world.requester_session().call("create_collection_request", nc_req)
    nc_plan = engine.admin.data(*engine.admin.raw("read_journey_plan", {"journeyId": nc_created.get("journeyId")}))
    nc_conn_idx = _connection_index(engine)
    nc_resources: list[str] = []
    for leg in nc_plan.get("legs", []) or []:
        f, t = leg.get("fromLocationId"), leg.get("toLocationId")
        for r in (nc_conn_idx.get((f, t)), f, t):
            if r in contrast_before and r not in nc_resources:
                nc_resources.append(r)
    for stop in nc_plan.get("orderedStops", []) or []:
        lid = stop.get("locationId")
        if lid in contrast_before and lid not in nc_resources:
            nc_resources.append(lid)
    contrast_after = _capacity_map(engine)
    contrast_reserved_regular = bool(nc_resources) and all(
        _regular_up(contrast_before.get(r, {}), contrast_after.get(r, {})) for r in nc_resources
    )
    contrast_no_cooled = all(
        not _cooled_up(contrast_before.get(r, {}), contrast_after.get(r, {})) for r in nc_resources
    )

    reservations = Verdict(
        ok=(
            planned_ok and len(leg_conn_ids) >= 1 and len(location_ids) >= 2
            and frost_legs_cooled and frost_locs_cooled
            and nc_created.get("serviceability") == "serviceable"
            and contrast_reserved_regular and contrast_no_cooled
        ),
        detail=(
            f"AC-17 cooled reservations (NOT the idempotency check -- this slot carries the third dimension): "
            f"routeMode={plan.get('routeMode')} Frost cooled-reserved per_leg={per_leg} per_location={per_location}; "
            f"contrast (non-cooled type, same cooled network): resources={len(nc_resources)} "
            f"reserved_regular={contrast_reserved_regular} added_no_cooled={contrast_no_cooled}"
        ),
        evidence={
            "frost_before": {k: before_caps.get(k) for k in built_a},
            "frost_after": {k: after_caps.get(k) for k in built_a},
            "contrast_before": {k: contrast_before.get(k) for k in built_a},
            "contrast_after": {k: contrast_after.get(k) for k in built_a},
        },
    )

    # ---------------------------------------------------------------- check 2
    # Network B: the ONLY path to the lab is a NON-cooled connection, so a Frost
    # route is impossible. The contrast (non-cooled-required type) must plan.
    pickup_b = world.location("pickupB", ["collection_point", "transfer_point"], 61.00, 61.00, coverageKm=5)
    lab_b = world.location("labB", ["hub", "laboratory"], 61.01, 61.01, handlingMinutes=2)
    conn_b = world.connection(pickup_b["locationId"], lab_b["locationId"], travelMinutes=10, costUnits=5, cooled=False)
    world.lab_accepts(lab_b["locationId"], frost["sampleTypeId"])
    world.lab_accepts(lab_b["locationId"], noncooled["sampleTypeId"])
    for r in (pickup_b["locationId"], lab_b["locationId"], conn_b["connectionId"]):
        world.capacity(r, units=50, cooled_units=50)

    frost_b = world.request_body(
        BasicNetworkLike(frost, pickup_b), world.key("frost-bad"),
        origin={"latitude": 61.00, "longitude": 61.00},
        containerType=FROST_CONTAINER,
    )
    frost_b_res = world.requester_session().call("create_collection_request", frost_b)
    noncooled_b = world.request_body(
        BasicNetworkLike(noncooled, pickup_b), world.key("noncooled-ok"),
        origin={"latitude": 61.00, "longitude": 61.00},
    )
    noncooled_b_res = world.requester_session().call("create_collection_request", noncooled_b)

    frost_refused = (
        frost_b_res.get("serviceability") == "unserviceable"
        and not frost_b_res.get("journeyId")
    )
    contrast_planned = (
        noncooled_b_res.get("serviceability") == "serviceable"
        and bool(noncooled_b_res.get("journeyId"))
    )
    negative = Verdict(
        ok=frost_refused and contrast_planned,
        detail=(
            f"forced non-cooled leg: Frost serviceability={frost_b_res.get('serviceability')!r} "
            f"reason={frost_b_res.get('serviceabilityReason')!r} journey={bool(frost_b_res.get('journeyId'))}; "
            f"same-network non-cooled-required type serviceability={noncooled_b_res.get('serviceability')!r} "
            f"journey={bool(noncooled_b_res.get('journeyId'))} "
            f"(refusal asserted via the public serviceability enum, not any internal reason code)"
        ),
        evidence={"frost": frost_b_res, "contrast": noncooled_b_res},
    )

    return CapabilityReport(positive=positive, negative=negative, idempotency=reservations)


CAPABILITIES = [
    Capability(
        "frost_cooled_route_enforcement",
        "write",
        "AC-16/AC-17: Frost is ROUTED under a genuine cooled-route constraint (all-cooled route plans, a "
        "forced non-cooled leg is refused while a non-cooled-required type plans over the same network) and "
        "reserves COOLED capacity per leg and per location -- behaviour, not just the stored attribute. Not a "
        "pinned-contract operationId (like named_sample_types_present_after_reset): a deliberate exception that "
        "grades a cross-operation routing property rather than a single operation.",
        _run_frost_cooled_route_enforcement,
        priority=True,
    ),
]
