"""create_location, update_location, set_location_status, set_location_function_status,
create_connection, set_connection_status.

Ground truth: pinned contract enums (functions in
{collection_point, transfer_point, hub, laboratory}) plus AC-20..26. An
unknown function name is rejected 400 invalid_location_function (this IS
pinned: the contract's own 400 response enumerates exactly that code). What
happens when a function name is valid but the location does not currently
provide it is NOT pinned by instruction.md or the contract to any specific
status/code (AC-26 in ACCEPTANCE.md deliberately asserts only "rejected/
fails", no specific status) -- a prior version of this file mined golden's
own test suite for its 404 pick and unfairly failed candidates that reject
the same request a different, equally valid way (e.g. 400/409), so this
engine only checks that the request is refused (any 4xx), not which one.
"""
from __future__ import annotations

from oracle import Capability, CapabilityReport, Verdict


def _enabled_functions(functions) -> set[str]:
    """Normalize a location's `functions` field to the set of ENABLED names,
    regardless of which contract-legal shape the candidate chose: a sparse
    list/array of enabled names (golden's convention), or the complete
    object-of-booleans the schema itself declares (LocationFunction enum ->
    bool for all four names, `additionalProperties: false`). `set(dict)`
    yields KEYS, not truthy values -- wrong for the complete-object shape,
    where a disabled function is still a present key with value False."""
    if isinstance(functions, dict):
        return {k for k, v in functions.items() if v}
    return set(functions or [])


def _find_location(session, location_id: str):
    """A location's live record, read the only way this HTTP-only engine
    ever reads anything: via read_operations_state's locations[], never the
    write's own echoed response. Mirrors capacity.py's identically-named
    helper (kept local rather than shared to avoid a cross-module import for
    six lines)."""
    status, payload = session.raw("read_operations_state", {"filters": {}})
    data = session.data(status, payload)
    for loc in data.get("locations", []) or []:
        if loc.get("locationId") == location_id:
            return loc
    return None


def _run_create_location(engine) -> CapabilityReport:
    world = engine.new_world("loc")
    created = world.location("combo", ["hub", "laboratory"], 20.0, 20.0)  # AC-20: one location, two functions
    via = engine.observe(engine.admin, created["locationId"], entity_type="location")
    # read_operations_state has no path param; also check the locations list directly.
    status, payload = engine.admin.raw("read_operations_state", {"filters": {}})
    data = engine.admin.data(status, payload)
    found = any(loc.get("locationId") == created["locationId"] for loc in data.get("locations", []))
    positive = Verdict(
        ok=_enabled_functions(created.get("functions")) == {"hub", "laboratory"} and (found or via is not None),
        detail=f"functions={created.get('functions')} found_in_ops_state={found} via={via}",
    )

    bad_status, bad_payload = engine.admin.raw(
        "create_location",
        {
            "name": world.key("bad-loc"),
            "active": True,
            "coordinate": {"latitude": 1.0, "longitude": 1.0},
            "functions": ["not_a_real_function"],
        },
    )
    negative = Verdict(
        ok=bad_status == 400 and engine.admin.code(bad_payload) == "invalid_location_function",
        detail=f"status={bad_status} code={engine.admin.code(bad_payload)!r}",
    )
    return CapabilityReport(positive=positive, negative=negative)


def _run_update_location(engine) -> CapabilityReport:
    world = engine.new_world("uloc")
    created = world.location("orig", ["collection_point"], 21.0, 21.0)
    new_name = world.key("renamed")
    updated = engine.admin.call(
        "update_location",
        {"locationId": created["locationId"], "name": new_name, "coordinate": {"latitude": 21.5, "longitude": 21.5}},
    )
    via = engine.observe(engine.admin, new_name, direct=None, entity_type="location")
    # VB2.6: the OR-fallback made this a tautology -- once the first clause
    # (the update RESPONSE echoes the new name back) held, the second clause
    # was identical to the first and always true, so `via` was decorative and
    # never actually gated credit. Require the independent confirmation.
    positive = Verdict(ok=updated.get("name") == new_name and via is not None, detail=f"name={updated.get('name')} via={via}")

    # A non-zero coordinate: {0,0} is schema-valid but some candidates treat
    # it as a zero-value sentinel ("no coordinate given") and reject it on
    # THAT ground, which would conflate coordinate validation with the
    # unknown-id check this negative test is actually meant to isolate.
    bad_status, bad_payload = engine.admin.raw(
        "update_location", {"locationId": "00000000-0000-0000-0000-000000000000", "name": "x", "coordinate": {"latitude": 1.0, "longitude": 1.0}}
    )
    negative = Verdict(ok=bad_status == 404, detail=f"status={bad_status} code={engine.admin.code(bad_payload)!r}")
    return CapabilityReport(positive=positive, negative=negative)


def _run_set_location_status(engine) -> CapabilityReport:
    world = engine.new_world("sls")
    created = world.location("toggle", ["collection_point"], 22.0, 22.0)
    updated = engine.admin.call("set_location_status", {"locationId": created["locationId"], "active": False})
    # VB2.6: `updated.get("active") is False` alone is a pure echo of the
    # request field (see anchors/hollow/server.py's h_set_location_status,
    # which returns exactly `body.get("active")`) -- no persistence is
    # needed to pass it. Require an INDEPENDENT read (read_operations_state's
    # own locations[]) to reflect the same change; a stateless handler with
    # no memory of what it just did cannot make that read agree.
    seen = _find_location(engine.admin, created["locationId"])
    reflected = bool(seen) and seen.get("active") is False
    positive = Verdict(
        ok=updated.get("active") is False and reflected,
        detail=f"active={updated.get('active')} (AC-24: no restart required); ops-state independent read reflects={reflected}",
    )

    bad_status, bad_payload = engine.admin.raw("set_location_status", {"locationId": "00000000-0000-0000-0000-000000000000", "active": False})
    negative = Verdict(ok=bad_status == 404, detail=f"status={bad_status}")
    return CapabilityReport(positive=positive, negative=negative)


def _run_set_location_function_status(engine) -> CapabilityReport:
    world = engine.new_world("slfs")
    created = world.location("dual", ["collection_point", "laboratory"], 23.0, 23.0)
    updated = engine.admin.call(
        "set_location_function_status", {"locationId": created["locationId"], "function": "collection_point", "active": False}
    )
    # AC-25: disabling ONE function must leave the other(s) usable. Checking
    # only the WRITE's own echoed response is not independent evidence (see
    # anchors/hollow/server.py's h_set_location_function_status, which
    # fabricates a full canonical function list with no memory of what this
    # location actually has) -- cross-check against an INDEPENDENT
    # read_operations_state read instead.
    seen = _find_location(engine.admin, created["locationId"])
    seen_functions = (seen or {}).get("functions") or {}
    # Plain `in` on the complete-object shape checks KEY presence, not the
    # boolean value -- true for "laboratory" here only because it happens to
    # still be True; the same check would wrongly pass a DISABLED laboratory
    # too. Use the shared truthy-aware helper instead.
    still_lab_independent = bool(seen) and "laboratory" in _enabled_functions(seen_functions)
    positive = Verdict(
        ok=updated.get("active") is False and still_lab_independent,
        detail=f"disabled collection_point, remaining functions={updated.get('functions')}; ops-state independent read functions={seen_functions}",
    )

    unknown_status, unknown_payload = engine.admin.raw(
        "set_location_function_status", {"locationId": created["locationId"], "function": "not_a_function", "active": False}
    )
    missing_status, missing_payload = engine.admin.raw(
        "set_location_function_status", {"locationId": created["locationId"], "function": "hub", "active": False}
    )
    # AC-26: an unknown function NAME is pinned to 400 invalid_location_function
    # (the contract's own enum). A valid function this location does not
    # currently provide (here: "hub", which "dual" was never given) has no
    # pinned status/code -- instruction.md and ACCEPTANCE.md both leave that
    # choice to the app, so only "rejected" (any 4xx) is required, not 404
    # specifically.
    negative = Verdict(
        ok=unknown_status == 400 and engine.admin.code(unknown_payload) == "invalid_location_function" and 400 <= missing_status < 500,
        detail=f"unknown-function->{unknown_status} ({engine.admin.code(unknown_payload)}); "
               f"function-not-held(hub)->{missing_status} ({engine.admin.code(missing_payload)}) (no specific status/code required)",
    )
    return CapabilityReport(positive=positive, negative=negative)


def _run_create_connection(engine) -> CapabilityReport:
    world = engine.new_world("conn")
    a = world.location("a", ["collection_point"], 24.0, 24.0)
    b = world.location("b", ["laboratory"], 24.01, 24.01)
    created = world.connection(a["locationId"], b["locationId"], travelMinutes=12, costUnits=3, cooled=True)
    via = engine.observe(engine.admin, created["connectionId"], entity_type="connection")
    positive = Verdict(ok=via is not None and created.get("cooled") is True, detail=f"cooled={created.get('cooled')} via={via}")

    bad_status, bad_payload = engine.admin.raw(
        "create_connection", {"fromLocationId": a["locationId"], "toLocationId": b["locationId"], "travelMinutes": 0, "costUnits": 1, "active": True}
    )
    negative = Verdict(ok=bad_status == 400, detail=f"status={bad_status} code={engine.admin.code(bad_payload)!r}")
    return CapabilityReport(positive=positive, negative=negative)


def _run_set_connection_status(engine) -> CapabilityReport:
    world = engine.new_world("scs")
    a = world.location("a", ["collection_point"], 25.0, 25.0)
    b = world.location("b", ["laboratory"], 25.01, 25.01)
    created = world.connection(a["locationId"], b["locationId"])
    updated = engine.admin.call("set_connection_status", {"connectionId": created["connectionId"], "active": False})
    # VB2.6: as with set_location_status, the write's own echoed response is
    # not independent evidence. Cross-check via an independent
    # read_operations_state read.
    status, payload = engine.admin.raw("read_operations_state", {"filters": {}})
    data = engine.admin.data(status, payload)
    seen = next((c for c in data.get("connections", []) or [] if c.get("connectionId") == created["connectionId"]), None)
    reflected = bool(seen) and seen.get("active") is False
    positive = Verdict(ok=updated.get("active") is False and reflected, detail=f"active={updated.get('active')}; ops-state independent read reflects={reflected}")

    bad_status, bad_payload = engine.admin.raw("set_connection_status", {"connectionId": "00000000-0000-0000-0000-000000000000", "active": False})
    negative = Verdict(ok=bad_status == 404, detail=f"status={bad_status}")
    return CapabilityReport(positive=positive, negative=negative)


# --------------------------------------------------------------- durability
# DUR3: all six of this module's mutating calls already go through
# engine.admin directly (create/update-location/connection and their status
# toggles are administrator-only per instruction.md), so durability_write
# reuses that same session -- no role decision to make here, unlike
# disruption.py's dispatcher-scoped ones.


def _durability_write_create_location(engine) -> dict:
    world = engine.new_world("locdur")
    created = world.location("combodur", ["hub", "laboratory"], 26.0, 26.0)
    return {"location_id": created["locationId"]}


def _durability_verify_create_location(engine, sentinel: dict) -> Verdict:
    location_id = sentinel.get("location_id")
    if not location_id:
        return Verdict(ok=False, detail="no location_id recorded at write time")
    seen = _find_location(engine.admin, location_id)
    via = engine.observe(engine.admin, location_id, entity_type="location")
    return Verdict(ok=bool(seen) or via is not None, detail=f"post-restart ops-state found={bool(seen)} via={via}")


def _durability_write_update_location(engine) -> dict:
    world = engine.new_world("ulocdur")
    created = world.location("origdur", ["collection_point"], 27.0, 27.0)
    new_name = world.key("renameddur")
    engine.admin.call("update_location", {"locationId": created["locationId"], "name": new_name, "coordinate": {"latitude": 27.5, "longitude": 27.5}})
    return {"location_id": created["locationId"], "name": new_name}


def _durability_verify_update_location(engine, sentinel: dict) -> Verdict:
    location_id, name = sentinel.get("location_id"), sentinel.get("name")
    if not (location_id and name):
        return Verdict(ok=False, detail="no location_id/name recorded at write time")
    seen = _find_location(engine.admin, location_id)
    reflected = bool(seen) and seen.get("name") == name
    return Verdict(ok=reflected, detail=f"post-restart ops-state name={(seen or {}).get('name')!r} expected={name!r}")


def _durability_write_set_location_status(engine) -> dict:
    world = engine.new_world("slsdur")
    created = world.location("toggledur", ["collection_point"], 28.0, 28.0)
    engine.admin.call("set_location_status", {"locationId": created["locationId"], "active": False})
    return {"location_id": created["locationId"]}


def _durability_verify_set_location_status(engine, sentinel: dict) -> Verdict:
    location_id = sentinel.get("location_id")
    if not location_id:
        return Verdict(ok=False, detail="no location_id recorded at write time")
    seen = _find_location(engine.admin, location_id)
    reflected = bool(seen) and seen.get("active") is False
    return Verdict(ok=reflected, detail=f"post-restart ops-state active={(seen or {}).get('active')}")


def _durability_write_set_location_function_status(engine) -> dict:
    world = engine.new_world("slfsdur")
    created = world.location("dualdur", ["collection_point", "laboratory"], 29.0, 29.0)
    engine.admin.call("set_location_function_status", {"locationId": created["locationId"], "function": "collection_point", "active": False})
    return {"location_id": created["locationId"]}


def _durability_verify_set_location_function_status(engine, sentinel: dict) -> Verdict:
    location_id = sentinel.get("location_id")
    if not location_id:
        return Verdict(ok=False, detail="no location_id recorded at write time")
    seen = _find_location(engine.admin, location_id)
    enabled = _enabled_functions((seen or {}).get("functions"))
    reflected = bool(seen) and "collection_point" not in enabled and "laboratory" in enabled
    return Verdict(ok=reflected, detail=f"post-restart ops-state enabled_functions={enabled}")


def _durability_write_create_connection(engine) -> dict:
    world = engine.new_world("conndur")
    a = world.location("adur", ["collection_point"], 30.0, 30.0)
    b = world.location("bdur", ["laboratory"], 30.01, 30.01)
    created = world.connection(a["locationId"], b["locationId"], travelMinutes=12, costUnits=3, cooled=True)
    return {"connection_id": created["connectionId"]}


def _durability_verify_create_connection(engine, sentinel: dict) -> Verdict:
    connection_id = sentinel.get("connection_id")
    if not connection_id:
        return Verdict(ok=False, detail="no connection_id recorded at write time")
    via = engine.observe(engine.admin, connection_id, entity_type="connection")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _durability_write_set_connection_status(engine) -> dict:
    world = engine.new_world("scsdur")
    a = world.location("adur2", ["collection_point"], 31.0, 31.0)
    b = world.location("bdur2", ["laboratory"], 31.01, 31.01)
    created = world.connection(a["locationId"], b["locationId"])
    engine.admin.call("set_connection_status", {"connectionId": created["connectionId"], "active": False})
    return {"connection_id": created["connectionId"]}


def _durability_verify_set_connection_status(engine, sentinel: dict) -> Verdict:
    connection_id = sentinel.get("connection_id")
    if not connection_id:
        return Verdict(ok=False, detail="no connection_id recorded at write time")
    status, payload = engine.admin.raw("read_operations_state", {"filters": {}})
    data = engine.admin.data(status, payload)
    seen = next((c for c in data.get("connections", []) or [] if c.get("connectionId") == connection_id), None)
    reflected = bool(seen) and seen.get("active") is False
    return Verdict(ok=reflected, detail=f"post-restart ops-state active={(seen or {}).get('active')}")


CAPABILITIES = [
    Capability(
        "create_location", "write", "One location may combine collection/transfer/hub/laboratory functions (AC-20).", _run_create_location,
        durability_write=_durability_write_create_location, durability_verify=_durability_verify_create_location,
    ),
    Capability(
        "update_location", "write", "Update a location's name/coordinate/coverage/handling time.", _run_update_location,
        durability_write=_durability_write_update_location, durability_verify=_durability_verify_update_location,
    ),
    Capability(
        "set_location_status", "write", "Deactivate a whole location without restarting the app (AC-24).", _run_set_location_status,
        durability_write=_durability_write_set_location_status, durability_verify=_durability_verify_set_location_status,
    ),
    Capability(
        "set_location_function_status", "write", "Deactivate one function while others keep working (AC-25/26).", _run_set_location_function_status,
        durability_write=_durability_write_set_location_function_status, durability_verify=_durability_verify_set_location_function_status,
    ),
    Capability(
        "create_connection", "write", "Create a transport connection between two locations, optionally cooled.", _run_create_connection,
        durability_write=_durability_write_create_connection, durability_verify=_durability_verify_create_connection,
    ),
    Capability(
        "set_connection_status", "write", "Deactivate a connection without restarting the app.", _run_set_connection_status,
        durability_write=_durability_write_set_connection_status, durability_verify=_durability_verify_set_connection_status,
    ),
]
