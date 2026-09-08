"""instruction.md line 68 ("View ... regular and cooled capacity at the
laboratory they are assigned to") / AC-91 (a node handler or laboratory
intake operator can view/configure capacity only at the node or laboratory
they are ASSIGNED to).

Presentation-layer, framework-agnostic:
  * ``seed.seed_lab_capacity_view`` configures the LAB's regular and cooled
    capacity to distinctive, ASYMMETRIC values (never golden's generic 20/20
    fixture default used elsewhere on the same page, and never 0, so a hollow
    app rendering a fixed placeholder can never accidentally match), assigns a
    laboratory_team worker to that lab, plans a serviceable journey so the
    worker genuinely holds a laboratory-kind work item whose destination is
    their OWN lab, and reads back the EXACT ``availableUnits`` /
    ``cooledAvailableUnits`` the pinned contract's own ``list_role_work``
    (``AssignmentWorkItem.destinationCapacity`` -- a formally named schema
    field in the PINNED contract, not golden's own choice) reports for that
    worker over HTTP, before any page opens;
  * POSITIVE: the lab worker logs in, opens their own work view, and the two
    HTTP-CONFIRMED numbers appear TOGETHER, in the same listitem/row/
    region/article, alongside broad capacity vocabulary (capacity/regular/
    cooled/available/units) -- never golden's exact phrasing ("Capacity 58
    regular · 34 cooled"): a differently-worded correct app ("Available:
    58 (regular) / 34 (cooled)") still scores, because the anchor is the
    ACTUAL NUMBERS this candidate's own backend reserved, not a copy string.

Why the numbers are read back from the app's own response rather than
asserted as the raw configured values: capacity is RESERVED as soon as
planning completes (instruction.md line 192: "tracked as configured, reserved,
and available units"), so ``availableUnits`` is ``configured - reserved``, not
the raw number this seeder configured. Asserting the exact configured number
would either be wrong (if reservation legitimately consumed some units) or
would force a fragile "no reservation happened yet" race; reading the ACTUAL
number back over HTTP and asserting the UI shows THAT is correct for any
backend regardless of how much it reserves.

No control/negative dimension: "the worker's own assigned lab shows its own
capacity" is one coherent property with no second actor whose visibility
should differ here (mirrors ``role_work_view_shows_current_and_past`` (AC-3),
also positive-only) -- AC-91's "NOT at a facility they are not assigned to"
half is a backend authorization check on the CONFIGURE write path, already
exercised by the API engine, not a presentation-layer slice.
"""
from __future__ import annotations

import re

from model import UiCapability, UiReport, UiVerdict
from seed import seed_lab_capacity_view
from tracking import count, settle

_CAPACITY_NAV = (r"\bcapacity\b", r"\bmy work\b", r"\bwork\b")
_CAPACITY_VOCAB = re.compile(r"\bcapacity|\bregular|\bcooled|\bavailable|\bunits", re.I)
_REGION_ROLES = ("listitem", "row", "article", "region", "group")


def _numbers_together(page, regular: int, cooled: int, timeout_ms: int = 6000):
    """The tightest landmark holding BOTH distinctive numbers AND capacity
    vocabulary -- so a coincidental lone digit elsewhere on the page (a date,
    a count badge) can never be mistaken for the capacity reading."""
    regular_pat = re.compile(re.escape(str(regular)))
    cooled_pat = re.compile(re.escape(str(cooled)))
    import time

    deadline = time.monotonic() + timeout_ms / 1000.0
    while True:
        for role in _REGION_ROLES:
            loc = (
                page.get_by_role(role)
                .filter(has_text=regular_pat)
                .filter(has_text=cooled_pat)
                .filter(has_text=_CAPACITY_VOCAB)
            )
            if count(loc) > 0:
                return loc.first
        if time.monotonic() >= deadline:
            return None
        page.wait_for_timeout(200)


def _run(engine) -> UiReport:
    seed = seed_lab_capacity_view(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    avail = seed.evidence.get("availableUnits")
    cooled_avail = seed.evidence.get("cooledAvailableUnits")

    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, seed.worker_username, seed.worker_password):
            return UiReport(
                positive=UiVerdict(False, "laboratory worker could not sign in through the public login form"),
                notes="login failure",
            )

        for nav in _CAPACITY_NAV:
            if engine.navigate(page, nav, timeout_ms=2500):
                settle(page)
                break

        region = _numbers_together(page, int(avail), int(cooled_avail))
        shown = region is not None
        shown_text = ""
        if shown:
            try:
                shown_text = region.inner_text()
            except Exception:
                shown_text = ""

        positive = UiVerdict(
            ok=shown,
            detail=(
                f"laboratory worker's own work view shows HTTP-confirmed capacity "
                f"(available={avail}, cooledAvailable={cooled_avail})={shown} "
                f"(configured regular={seed.evidence.get('configuredRegularUnits')}, "
                f"cooled={seed.evidence.get('configuredCooledUnits')}) text={shown_text!r}"
            ),
            evidence={"seed": seed.evidence},
        )
    finally:
        ctx.close()

    return UiReport(positive=positive, notes="AC-91 / instruction.md:68 lab worker's own capacity view")


CAPABILITY = UiCapability(
    id="lab_worker_views_own_capacity",
    ac="AC-91",
    description="A laboratory worker's own work view shows the HTTP-confirmed regular/cooled capacity at their assigned lab.",
    run=_run,
    priority=True,
)
