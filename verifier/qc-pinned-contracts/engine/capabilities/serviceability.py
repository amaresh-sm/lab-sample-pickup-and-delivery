"""check_serviceability, read_serviceability_snapshot.

Priority-1 domain (session finding: this is the highest-value/highest-risk
area -- the old shallow grading could not distinguish a correct serviceability
engine from a buggy one).

  - AC-35 (ACCEPTANCE.md): instruction.md's "within 5 km" never states
    whether the boundary itself (exactly 5.0km) is inclusive or exclusive --
    that is deliberately left unspecified, so this engine must NOT assert
    the exact-boundary case either way (a prior version of this file mined
    golden-mongo's own test suite for an inclusive-boundary assumption and
    unfairly failed any candidate that chose exclusive instead, a choice
    instruction.md never mandates). What IS clearly required: something
    meaningfully INSIDE the 5km radius is serviceable, and something clearly
    beyond a safety margin OUTSIDE it (6km -- well past any rounding
    ambiguity at the 5.0-5.05km boundary itself) is not, with reason
    "outside_service_coverage" (pinned contract's exact enum value).
  - It is a pure read: `TestCheckServiceabilityIsAPureReadAndMutatesNoState`
    shows a full state fingerprint is byte-identical before/after, even
    across repeated calls -- so this engine snapshots sample/journey counts
    via read_operations_state before and after, and requires them unchanged.
  - Collection window and delivery deadline are SEPARATE constraints: an
    inverted/late window is its own 400 (invalid_collection_window), distinct
    from a structurally-fine window that simply makes the deadline
    unreachable (200, reason deadline_unreachable).
"""
from __future__ import annotations

import math

from oracle import Capability, CapabilityReport, Verdict

EARTH_RADIUS_KM = 6371.0


def _km_offset_latitude(lat: float, km: float) -> float:
    """A latitude delta that is exactly `km` kilometres north of `lat`."""
    return lat + (km / EARTH_RADIUS_KM) * (180.0 / math.pi)


def _ops_sample_count(session) -> int:
    status, payload = session.raw("read_operations_state", {"filters": {}})
    data = session.data(status, payload)
    return len(data.get("samples", []))


def _run_check_serviceability(engine) -> CapabilityReport:
    world = engine.new_world("svc")
    net = world.basic_network()
    base_lat = net.pickup["coordinate"]["latitude"]
    base_lon = net.pickup["coordinate"]["longitude"]

    def body_at(km: float, **overrides):
        b = {
            "origin": {"latitude": _km_offset_latitude(base_lat, km), "longitude": base_lon},
            "originType": "home",
            "sampleTypeId": net.sample_type["sampleTypeId"],
            "requestedAt": world.at(),
            "deliveryDeadline": world.at(hours=6),
        }
        b.update(overrides)
        return b

    before_count = _ops_sample_count(engine.admin)
    # AC-35: exactly 5.0km is deliberately NOT probed -- instruction.md never
    # says whether the boundary is inclusive or exclusive, so this engine
    # only checks the two cases that ARE unambiguous: meaningfully inside the
    # radius, and clearly beyond it (6km, well past the 5.0-5.05km boundary
    # itself so the assertion holds regardless of which way the app rounds).
    inside_coverage = engine.admin.call("check_serviceability", body_at(3.0))
    outside_coverage = engine.admin.call("check_serviceability", body_at(6.0))
    # Re-run one of the calls again (a pure dry run must be repeatable with
    # no accumulating side effect).
    engine.admin.call("check_serviceability", body_at(3.0))
    after_count = _ops_sample_count(engine.admin)

    inside_ok = inside_coverage.get("serviceable") is True
    outside_ok = outside_coverage.get("serviceable") is False and outside_coverage.get("reason") == "outside_service_coverage"
    dry_run_ok = before_count == after_count

    # Window vs. deadline are separate: narrow the window so it is
    # structurally valid but pushes past a fixed, tight deadline.
    tight_deadline = world.at(hours=1)
    narrow_window = engine.admin.call(
        "check_serviceability",
        body_at(0, deliveryDeadline=tight_deadline, earliestCollectionAt=world.at(minutes=50), latestCollectionAt=world.at(minutes=55)),
    )
    deadline_ok = narrow_window.get("serviceable") is False and narrow_window.get("reason") == "deadline_unreachable"

    positive = Verdict(
        ok=inside_ok and outside_ok and dry_run_ok and deadline_ok,
        detail=(
            f"3km serviceable={inside_coverage.get('serviceable')}; 6km reason={outside_coverage.get('reason')}; "
            f"dry_run sample_count {before_count}->{after_count}; "
            f"tight-window-tight-deadline reason={narrow_window.get('reason')}; "
            "(exact 5.0km boundary intentionally not asserted either way, per AC-35)"
        ),
        evidence={"inside_3km": inside_coverage, "outside_6km": outside_coverage, "deadline_case": narrow_window},
    )

    bad_status, bad_payload = engine.admin.raw(
        "check_serviceability",
        body_at(0, earliestCollectionAt=world.at(hours=5), latestCollectionAt=world.at(hours=1)),  # inverted window
    )
    negative = Verdict(
        ok=bad_status == 400 and engine.admin.code(bad_payload) == "invalid_collection_window",
        detail=f"inverted window -> status={bad_status} code={engine.admin.code(bad_payload)!r}",
    )
    return CapabilityReport(positive=positive, negative=negative)


def _run_read_serviceability_snapshot(engine) -> CapabilityReport:
    world = engine.new_world("svcsnap")
    before_status, before_payload = engine.admin.raw("read_serviceability_snapshot", {})
    before_data = engine.admin.data(before_status, before_payload)
    before_count = before_data.get("activeCollectionPointCount", 0)

    # VB2.6: "activeCollectionPointCount is present and is an int" is
    # satisfiable by a fixed, never-changing fabrication (see
    # anchors/hollow/server.py's h_read_serviceability_snapshot, hardcoded to
    # 0 forever). Create a genuinely active collection point and require the
    # count to actually move -- a snapshot that doesn't track the network
    # cannot make that happen.
    world.location("newcp", ["collection_point"], 27.0, 27.0, coverageKm=5)

    status, payload = engine.admin.raw("read_serviceability_snapshot", {})
    data = engine.admin.data(status, payload)
    after_count = data.get("activeCollectionPointCount", 0)
    count_reflects_creation = isinstance(after_count, int) and isinstance(before_count, int) and after_count >= before_count + 1
    positive = Verdict(
        ok=status == 200 and isinstance(data.get("version"), int) and data.get("version") >= 1 and "regions" in data and count_reflects_creation,
        detail=f"status={status} version={data.get('version')} activeCollectionPointCount {before_count}->{after_count}",
    )

    world.user("plainrequester", ["requester"])
    req_sess = world.session("plainrequester")
    bad_status, bad_payload = req_sess.raw("read_serviceability_snapshot", {})
    negative = Verdict(
        ok=bad_status == 403 and req_sess.code(bad_payload) == "forbidden",
        detail=f"requester role -> status={bad_status} code={req_sess.code(bad_payload)!r}",
    )
    return CapabilityReport(positive=positive, negative=negative)


CAPABILITIES = [
    Capability(
        "check_serviceability",
        "write",
        "Dry-run feasibility check: inside/outside the 5km radius (exact boundary unspecified per AC-35), pure (no state change), window/deadline are separate constraints.",
        _run_check_serviceability,
        priority=True,
    ),
    Capability(
        "read_serviceability_snapshot",
        "read",
        "Admin/dispatcher view of the active nodes and coverage regions used by the published calculation.",
        _run_read_serviceability_snapshot,
        priority=True,
    ),
]
