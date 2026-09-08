"""AC-37 ("A requester can run the check independently of creating a
request") / AC-38 ("The dry run creates NO request, sample, journey,
assignment or capacity reservation -- snapshot counts before/after are
identical").

Presentation-layer, framework-agnostic:
  * ``seed.seed_serviceability_dry_run`` seeds a serviceable network + a
    requester, confirms over HTTP that ``check_serviceability`` reports this
    network serviceable for THIS candidate before any page opens, and hands
    back a ``request_count`` callable that counts the requester's OWN total
    work items via ``list_role_work`` -- the SAME operation every other
    seeder here already uses, called before AND after the UI action;
  * POSITIVE (two parts, both required): the requester opens the
    serviceability/coverage CHECK panel (located by a verb-near-noun accessible
    name -- "check"/"verify"/"preview"/"dry run" near "service"/"coverage" --
    the same idiom ``request_form_validation`` already uses to avoid the
    look-alike-forms trap: this app's create-request panel and its
    serviceability pre-check panel share field vocabulary like "Sample type",
    so scoping to the CHECK panel's own landmark is what keeps this scenario
    from accidentally filling, and submitting, the WRONG form), fills the
    contract-REQUIRED fields (origin lat/lng, sample type -- leaving
    requestedAt/deliveryDeadline at the panel's own sensible defaults, exactly
    what a real user would do), submits, and (1) a serviceable verdict is
    rendered (domain vocabulary drawn from the pinned contract's OWN
    ``check_serviceability`` response enum, not golden's copy: "serviceable",
    explicitly NOT preceded by "not"/"un"), while (2) the requester's own
    HTTP-visible work-item count is UNCHANGED after the click -- confirmed by
    calling ``request_count()`` again, never by trusting the UI's own claim
    that nothing was created (the SIDE-EFFECT-AWARENESS rule: a hollow or
    cheating candidate could render "Serviceable" while secretly having
    created a request anyway).

No control/negative dimension: "the dry run is safe and reports a verdict" is
one coherent property with no second actor whose visibility should differ
(mirrors ``role_work_view_shows_current_and_past`` (AC-3), also positive-only).
"""
from __future__ import annotations

import re

from helpers import field_by_visible_label
from model import UiCapability, UiReport, UiVerdict
from seed import seed_serviceability_dry_run
from tracking import count, settle

_REQUESTS_NAV = (r"\brequests\b", r"\bnew request\b", r"\bcreate request\b")

# Verb-near-noun accessible name for the DRY-RUN check panel -- NOT the
# create-request panel, which shares field vocabulary (both render "Sample
# type", origin fields, etc.) but a different verb/noun pairing
# ("create"/"submit request" vs "check"/"verify" a "service"/"coverage").
_CHECK_PANEL_NAME = re.compile(
    r"(check|verify|preview|dry.?run).{0,25}(servic|coverage)"
    r"|(servic|coverage).{0,25}(check|verify|preview|dry.?run)",
    re.I,
)
_CHECK_SUBMIT_NAME = re.compile(r"check.{0,15}servic|verify.{0,15}(servic|coverage)|check.{0,15}coverage|dry.?run", re.I)

_SAMPLE_TYPE_FIELD = re.compile(r"sample\s*type", re.I)
_LATITUDE_FIELD = re.compile(r"lat(itude)?", re.I)
_LONGITUDE_FIELD = re.compile(r"lon(g|gitude)?", re.I)

# The pinned contract's OWN check_serviceability response enum (`reason`):
# "serviceable" is a literal contract vocabulary token, not golden's copy.
# Negative lookbehind excludes "not serviceable" / "un-serviceable".
_SERVICEABLE_YES = re.compile(r"(?<!not\s)(?<!un)\bservice-?able\b", re.I)


def _check_panel(page):
    region = page.get_by_role("region", name=_CHECK_PANEL_NAME)
    if count(region) > 0:
        return region.first
    return page


def _fill_if_present(scope, label_pattern, value: str) -> bool:
    target = field_by_visible_label(scope, label_pattern)
    if target is None:
        return False
    try:
        target.fill(value)
        return True
    except Exception:
        pass
    # A <select> rejects .fill() outright (see request_form_validation.py's
    # own fix for the same shape) -- fall back to select_option.
    try:
        if target.evaluate("el => el.tagName") == "SELECT":
            target.select_option(value=value)
            return True
    except Exception:
        pass
    try:
        target.select_option(label=value)
        return True
    except Exception:
        return False


def _verdict_region(scope, timeout_ms: int = 5000):
    try:
        loc = scope.locator("[aria-live]").filter(has_text=_SERVICEABLE_YES)
        loc.first.wait_for(state="visible", timeout=timeout_ms)
        if count(loc) > 0:
            return loc.first
    except Exception:
        pass
    # Fall back to any visible text within the panel/page carrying the token.
    loc = scope.get_by_text(_SERVICEABLE_YES)
    if count(loc) > 0:
        try:
            if loc.first.is_visible():
                return loc.first
        except Exception:
            pass
    return None


def _run(engine) -> UiReport:
    seed = seed_serviceability_dry_run(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, seed.owner_username, seed.owner_password):
            return UiReport(
                positive=UiVerdict(False, "requester could not sign in through the public login form"),
                notes="login failure",
            )

        before = seed.request_count() if seed.request_count else None

        # A requester may land directly on a combined create/check panel. If
        # the check action already exists, "Create request" on the same panel
        # is a WRITE action, not navigation; clicking it would make the
        # verifier itself violate AC-38 before exercising the dry run.
        if count(page.get_by_role("button", name=_CHECK_SUBMIT_NAME)) == 0:
            for nav in _REQUESTS_NAV:
                if engine.navigate(page, nav, timeout_ms=2500):
                    settle(page)
                    break

        panel = _check_panel(page)
        filled_sample_type = _fill_if_present(panel, _SAMPLE_TYPE_FIELD, seed.evidence.get("sampleTypeId", ""))
        filled_lat = _fill_if_present(panel, _LATITUDE_FIELD, str(seed.evidence.get("latitude", "")))
        filled_lng = _fill_if_present(panel, _LONGITUDE_FIELD, str(seed.evidence.get("longitude", "")))

        submit = panel.get_by_role("button", name=_CHECK_SUBMIT_NAME)
        submit_count = count(submit)
        submitted = False
        click_error = ""
        if submit_count > 0:
            try:
                submit.first.click()
                submitted = True
                settle(page)
            except Exception as exc:
                submitted = False
                click_error = repr(exc)

        verdict_region = _verdict_region(panel) if submitted else None
        if verdict_region is None and submitted:
            # Some candidates render the verdict OUTSIDE the check panel's own
            # landmark (e.g. a page-level result area) -- fall back to the
            # whole page before concluding no verdict rendered.
            verdict_region = _verdict_region(page)
        served = verdict_region is not None

        after = seed.request_count() if seed.request_count else None
        nothing_created = (before is not None and after is not None and after == before)

        positive = UiVerdict(
            ok=filled_sample_type and filled_lat and filled_lng and submitted and served and nothing_created,
            detail=(
                f"filled sampleType={filled_sample_type}, lat={filled_lat}, lng={filled_lng}; "
                f"submit_count={submit_count} submitted={submitted} click_error={click_error!r}; "
                f"serviceable-verdict-rendered(AC-37)={served}; "
                f"requester work-item count before={before} after={after} "
                f"unchanged(AC-38)={nothing_created}"
            ),
            evidence={"seed": seed.evidence, "before": before, "after": after},
        )
    finally:
        ctx.close()

    return UiReport(positive=positive, notes="AC-37/AC-38 serviceability dry run renders a verdict and creates nothing")


CAPABILITY = UiCapability(
    id="serviceability_dry_run_creates_nothing",
    ac="AC-37/AC-38",
    description="The serviceability dry-run check renders a verdict and creates no request/sample (HTTP-confirmed).",
    run=_run,
    priority=True,
)
