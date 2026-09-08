"""AC-65 / AC-66 / AC-67 -- the owner's sample view renders, for the loaded
sample, its current-or-last-known LOCATION (AC-65), its current CUSTODIAN
(AC-66), and its ROUTE PROGRESS (AC-67); a non-owner does not get that sample's
detail.

Presentation-layer, framework-agnostic, and built on the same
``tracking``-module flow the freshness scenario uses:
  * we seed a DRIVEN journey over HTTP (accept -> checkpoint -> collect ->
    record location), so the sample genuinely has a custodian, a recorded
    location, and partial route progress;
  * POSITIVE: the OWNER opens the sample and, WITHIN the sample's own detail
    region (the tightest landmark holding the sample id AND a location token),
    all three fields are present -- located by visible text/labels, never by
    markup;
  * CONTROL: a different requester does NOT get that sample's custodian-bearing
    detail.

Region scoping is anchored on a LOCATION token (rather than custodian/progress)
on purpose: it is the field the AC-66/67 mutant leaves in place, so the
sample's detail region is still found in the mutant and the positive fails
specifically because CUSTODIAN and PROGRESS are gone -- a precise catch rather
than a "region vanished" catch.

The three field patterns are matched with a LEADING word boundary only (no
trailing ``\\b``: Playwright's ``filter(has_text=regex)`` silently fails on a
regex ending in ``\\b``).
"""
from __future__ import annotations

import re

from model import UiCapability, UiReport, UiVerdict
from seed import seed_driven_journey
from helpers import appears_within
from tracking import open_tracked_sample, sample_region

# AC-65 -- current / last-known location.
_LOCATION = re.compile(r"\blocation|\bposition|last known|coordinate|latitude|\blat\b", re.I)
# AC-66 -- current custodian / holder.
_CUSTODIAN = re.compile(r"\bcustod|\bholder|\bholding|carried by|\bhandler|in hands of|held by", re.I)
# AC-67 -- route progress: an ACTUAL progress indicator. Deliberately excludes
# tokens that appear regardless of whether route progress is rendered: a bare
# "progress" (the "View progress" SUBMIT button), a bare "N / M" ratio (a
# rendered date like "8/4/2026"), and a bare "in transit"/"en route" (the sample
# STATUS, graded elsewhere). Matches a "route/journey/delivery progress" label,
# an "ordered journey", a legs/stops count, a "step N", a "N / M legs" ratio, or
# a percentage -- an indicator specifically of position along the route.
_PROGRESS = re.compile(
    r"(?:route|journey|delivery)\s+progress|ordered journey|\blegs?\b|\bstops?\b|"
    r"\bstep \d|\d+\s*/\s*\d+\s*(?:legs?|stops?|steps?)|\d+\s*%",
    re.I,
)


def _fields_present(region) -> tuple[bool, bool, bool]:
    loc_ok = appears_within(region.get_by_text(_LOCATION), 3000)
    cust_ok = appears_within(region.get_by_text(_CUSTODIAN), 2000)
    prog_ok = appears_within(region.get_by_text(_PROGRESS), 2000)
    return loc_ok, cust_ok, prog_ok


def _run(engine) -> UiReport:
    seed = seed_driven_journey(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    # -------- POSITIVE: owner sees location + custodian + route progress -------
    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, seed.owner_username, seed.owner_password):
            positive = UiVerdict(False, "owner could not sign in through the public login form")
        else:
            open_tracked_sample(engine, page, seed.sample_id)
            region = sample_region(page, seed.sample_id, _LOCATION)
            if region is None:
                positive = UiVerdict(
                    False,
                    "no sample-scoped region carrying a location was found for the owner "
                    f"(orderedLegs={seed.evidence.get('orderedLegs')})",
                    evidence={"seed": seed.evidence},
                )
            else:
                loc_ok, cust_ok, prog_ok = _fields_present(region)
                positive = UiVerdict(
                    ok=loc_ok and cust_ok and prog_ok,
                    detail=(
                        f"owner sample region: location(AC-65)={loc_ok}, custodian(AC-66)={cust_ok}, "
                        f"route-progress(AC-67)={prog_ok} (orderedLegs={seed.evidence.get('orderedLegs')})"
                    ),
                    evidence={"seed": seed.evidence},
                )
    finally:
        ctx.close()

    # -------- CONTROL: a non-owner is NOT shown that sample's detail -----------
    ctx2, _ = engine.new_context()
    try:
        page = ctx2.new_page()
        if not engine.login(page, seed.intruder_username, seed.intruder_password):
            negative = UiVerdict(False, "non-owner could not sign in; control inconclusive")
        else:
            open_tracked_sample(engine, page, seed.sample_id)
            region = sample_region(page, seed.sample_id, _CUSTODIAN, timeout_ms=3000)
            leaked = region is not None
            negative = UiVerdict(
                ok=not leaked,
                detail=(
                    "non-owner sees this sample's custodian detail (LEAK)"
                    if leaked
                    else "non-owner is not shown this sample's detail (correctly scoped)"
                ),
            )
    finally:
        ctx2.close()

    return UiReport(positive=positive, negative=negative, notes="AC-65/66/67 owner sample view")


CAPABILITY = UiCapability(
    id="sample_view_shows_location_custodian_progress",
    ac="AC-65/AC-66/AC-67",
    description="Owner's sample view shows current location, custodian, and route progress; non-owner does not.",
    run=_run,
    priority=True,
)
