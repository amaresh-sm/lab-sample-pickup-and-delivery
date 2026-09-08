"""AC-113 ("A certified collection professional can record their current
location") / AC-114 ("A relay courier can record location and progress
updates for their assigned leg") -- a genuine WRITE through the UI, not just
observation. The critical check is the same write-through discipline as
``accept_assignment_writes_through``: the action must actually persist
server-side, confirmed by a genuine HTTP re-read, never trusted from the UI's
own success message.

Presentation-layer, framework-agnostic:
  * ``seed.seed_active_transport_leg`` hands back a courier holding a
    genuinely ACCEPTED transport leg with NO location recorded yet (confirmed
    via the app's OWN ``read_sample_state`` before any page opens), plus a
    ``recheck()`` callable that re-reads both contract-approved surfaces AFTER
    the browser acts: sample location state and worker live-map state.
  * POSITIVE: the courier opens their work view and records a location. Two
    rendering shapes are supported without any golden-specific markup:
      - a single action button with no fields to fill (confirmed live: one
        candidate exposes a bare "Record location" button that auto-fills
        the assignment id/coordinate/sequence itself), or
      - a form with fillable latitude/longitude (and optionally an
        assignment id, sequence, and recorded-at) fields plus a submit
        button (confirmed live: golden's "Share location" panel).
    Either way, credit requires the write-through check: ``recheck()`` must
    show a location/timestamp changed on at least one allowed surface over HTTP
    -- NEVER the UI's own "recorded"/checkmark claim, which a hollow UI could
    show without the backend ever receiving anything.

Locators are broad location/coordinate vocabulary, not golden's exact "Share
location" copy -- "record"/"log"/"update"/"report" a location/position still
scores.
"""
from __future__ import annotations

import re
from datetime import datetime, timezone

from model import UiCapability, UiReport, UiVerdict
from seed import seed_active_transport_leg
from tracking import count, settle

_WORK_NAV = (
    r"\blocation(?:\s*(?:&|and)\s*status)?\b",
    r"\blive tracking\b",
    r"\bmy work\b",
    r"\bwork\b",
)
_LOCATION_ACTION = re.compile(
    r"record\s+location|share\s+location|save\s+location|log\s+location|"
    r"update\s+location|report\s+location|record\s+position",
    re.I,
)
_LAT_FIELD = re.compile(r"lat(itude)?\b", re.I)
_LNG_FIELD = re.compile(r"lon(gitude)?\b|\blng\b", re.I)
_SEQ_FIELD = re.compile(r"sequence", re.I)
_TIME_FIELD = re.compile(r"recorded\s*at|location.{0,15}time", re.I)
_ASSIGNMENT_FIELD = re.compile(r"location.{0,10}assignment|assignment.{0,10}id", re.I)

_TEST_LAT = "12.9716"
_TEST_LNG = "77.5946"


def _now_text() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _now_local() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M")


def _fill_time_field(field) -> None:
    try:
        input_type = field.first.get_attribute("type") or ""
    except Exception:
        input_type = ""
    value = _now_local() if input_type == "datetime-local" else _now_text()
    try:
        field.first.fill(value)
    except Exception:
        pass


def _attempt_record_location(page, assignment_id: str) -> tuple[bool, str]:
    """Try the form-fill shape first (lat/lng fields present); fall back to a
    bare action button with nothing to fill. Returns (clicked, shape)."""
    lat_field = page.get_by_label(_LAT_FIELD)
    lng_field = page.get_by_label(_LNG_FIELD)
    if count(lat_field) > 0 and count(lng_field) > 0:
        try:
            # Scope the OTHER fields (assignment id, sequence, recorded-at) to
            # the SAME form as the lat/lng inputs -- a page can carry several
            # "...assignment id" fields (e.g. a checkpoint form's own
            # "Checkpoint assignment ID"), and an unscoped get_by_label would
            # grab whichever comes first in the DOM, leaving THIS form's own
            # assignment field empty (client-side validation then silently
            # blocks the submit -- no request ever fires).
            container = lat_field.first.locator("xpath=ancestor::form[1]")
            if count(container) == 0:
                container = page

            aid_field = container.get_by_label(_ASSIGNMENT_FIELD)
            if count(aid_field) > 0:
                aid_field.first.fill(assignment_id)
            lat_field.first.fill(_TEST_LAT)
            lng_field.first.fill(_TEST_LNG)
            seq_field = container.get_by_label(_SEQ_FIELD)
            if count(seq_field) > 0:
                seq_field.first.fill("1")
            time_field = container.get_by_label(_TIME_FIELD)
            if count(time_field) > 0:
                _fill_time_field(time_field)
            btn = container.get_by_role("button", name=_LOCATION_ACTION)
            if count(btn) == 0:
                btn = page.get_by_role("button", name=_LOCATION_ACTION)
            if count(btn) > 0:
                btn.first.click()
                return True, "form"
        except Exception:
            pass

    btn = page.get_by_role("button", name=_LOCATION_ACTION)
    if count(btn) > 0:
        try:
            btn.first.click()
            return True, "button"
        except Exception:
            pass
    return False, "none"


def _readback_changed(before: dict, after: dict) -> bool:
    """Whether either AC-113/AC-114-approved read surface changed."""
    pairs = (
        ("latestLocation", "latestLocation"),
        ("locationRecordedAt", "locationRecordedAt"),
        ("workerLatestLocation", "workerLatestLocation"),
        ("workerLocationRecordedAt", "workerLocationRecordedAt"),
    )
    return any(after.get(key) is not None and after.get(key) != before.get(pre_key)
               for key, pre_key in pairs)


def _poll_recheck(seed, before: dict, timeout_ms: int = 5000) -> dict:
    """Bounded poll until either legitimate HTTP readback surface changes."""
    import time

    deadline = time.monotonic() + timeout_ms / 1000.0
    last: dict = {}
    while True:
        last = seed.recheck() if seed.recheck else {}
        if _readback_changed(before, last):
            return last
        if time.monotonic() >= deadline:
            return last
        time.sleep(0.25)


def _run(engine) -> UiReport:
    seed = seed_active_transport_leg(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    assignment_id = seed.evidence.get("transportAssignmentId", "")
    before = {
        "latestLocation": seed.evidence.get("preLatestLocation"),
        "locationRecordedAt": seed.evidence.get("preLocationRecordedAt"),
        "workerLatestLocation": seed.evidence.get("preWorkerLatestLocation"),
        "workerLocationRecordedAt": seed.evidence.get("preWorkerLocationRecordedAt"),
    }

    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, seed.courier_username, seed.courier_password):
            return UiReport(
                positive=UiVerdict(False, "courier could not sign in through the public login form"),
                notes="AC-113/AC-114 location write-through",
            )
        for nav in _WORK_NAV:
            if engine.navigate(page, nav, timeout_ms=2500):
                settle(page)
                break

        clicked, shape = _attempt_record_location(page, assignment_id)
        settle(page)
        after = _poll_recheck(seed, before) if (clicked and seed.recheck) else (seed.recheck() if seed.recheck else {})
        changed = clicked and _readback_changed(before, after)
        positive = UiVerdict(
            ok=changed,
            detail=(
                f"identified a location-recording control (shape={shape}, clicked={clicked}); "
                f"HTTP re-read after action: sampleLocation={after.get('latestLocation')!r}, "
                f"sampleRecordedAt={after.get('locationRecordedAt')!r}, "
                f"workerLocation={after.get('workerLatestLocation')!r}, "
                f"workerRecordedAt={after.get('workerLocationRecordedAt')!r}; "
                f"pre-action={before!r}; write-through-confirmed={changed}"
            ),
            evidence={"seed": seed.evidence, "after": after},
        )
    finally:
        ctx.close()

    return UiReport(positive=positive, notes="AC-113/AC-114 worker location write-through")


CAPABILITY = UiCapability(
    id="worker_records_location_writes_through",
    ac="AC-113/AC-114",
    description="A collector/courier can record their current location through the UI, and it persists server-side.",
    run=_run,
    priority=True,
)
