"""AC-68 / AC-69 -- sample freshness is visible to the role that OWNS the
sample view (time of last update + whether the location is stale), and is NOT
surfaced to a non-owning role.

Presentation-layer, framework-agnostic:
  * We seed a driven journey over HTTP and record a worker location, so the
    backend genuinely holds a location reading with a freshness state.
  * POSITIVE: the OWNER, viewing the sample, sees -- WITHIN the sample's own
    detail region -- a freshness/staleness indicator (AC-69) together with a
    last-update time (AC-68). Both are located by visible text scoped to that
    region, never by markup.
  * CONTROL: a different requester (who does not own the sample) does NOT get
    that sample's freshness surfaced.

Two rendering shapes are supported without any golden-specific markup:
  * a dedicated tracking view reached by typing a tracking id into a labelled
    field and submitting (golden's "Track a sample" panel), and
  * a "my samples" LIST where each sample is a card/row/link opened by its id
    to reveal a detail dialog/region (Sol, and candidates with no id field).

Region scoping (the anti-golden-bias core): the freshness signal is tied to the
LOADED sample by choosing the SMALLEST landmark (dialog/region/article/row/...)
that contains BOTH the sample id AND a freshness token. This keeps "stale"/
"fresh"/"up to date" bound to this sample and away from unrelated copy -- e.g.
Sol's network-overview "1 stale location updates" metric (a different landmark,
no sample id) and a list header like "Current and completed work".

The freshness vocabulary deliberately EXCLUDES a bare "current": the
`ui-freshness-hidden` mutant removes golden's freshness rows but leaves the
"Current holder"/"Current custodian" field intact, so matching bare "current"
would silently defeat that mutant. Unambiguous fresh-state words (fresh, stale,
up to date, outdated, recent) are matched instead; a differently-worded UI that
uses one of those still scores, while "Current holder" never counts as
freshness.
"""
from __future__ import annotations

import re

from model import UiCapability, UiReport, UiVerdict
from seed import seed_driven_journey
from helpers import appears_within
from tracking import open_tracked_sample, sample_region

# AC-69 -- the freshness/staleness indicator. A LEADING `\b` keeps "fresh" from
# matching inside "Refresh"; trailing word-boundaries are deliberately omitted
# because Playwright's `filter(has_text=regex)` silently fails to match a regex
# that ends in `\b`. NOTE: a bare "current" is intentionally NOT matched (see
# module docstring -- it collides with "Current holder" and would defeat the
# freshness-removed mutant).
_FRESHNESS = re.compile(
    r"\bfresh|\bstale|\bup[\s-]?to[\s-]?date|\bout[\s-]?dated|\bout of date|\brecent",
    re.I,
)
# AC-68 -- a context-bearing last-update indicator. A bare clock time is not
# sufficient: sample detail panels commonly render a delivery deadline, and
# treating that unrelated HH:MM as the location's last-update time awards a
# false positive.
_LAST_UPDATE = re.compile(r"last[\s-]?updat|updated|as of|\bago\b", re.I)


def _run(engine) -> UiReport:
    seed = seed_driven_journey(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    tmo = engine.timeout_ms

    # -------- POSITIVE: owner sees a freshness indicator + a last-update time --
    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, seed.owner_username, seed.owner_password):
            positive = UiVerdict(False, "owner could not sign in through the public login form")
        else:
            open_tracked_sample(engine, page, seed.sample_id)
            region = sample_region(page, seed.sample_id, _FRESHNESS)
            if region is None:
                positive = UiVerdict(
                    False,
                    "no sample-scoped region carrying a freshness indicator was found for the owner "
                    f"(seeded locationStale={seed.evidence.get('locationStale')}, "
                    f"locationRecordedAt={seed.evidence.get('locationRecordedAt')})",
                    evidence={"seed": seed.evidence},
                )
            else:
                fresh_ok = appears_within(region.get_by_text(_FRESHNESS), 3000)
                update_ok = appears_within(region.get_by_text(_LAST_UPDATE), 3000)
                positive = UiVerdict(
                    ok=fresh_ok and update_ok,
                    detail=(
                        f"owner sample region: staleness-indicator(AC-69)={fresh_ok}, "
                        f"last-update(AC-68)={update_ok} "
                        f"(seeded locationStale={seed.evidence.get('locationStale')}, "
                        f"locationRecordedAt={seed.evidence.get('locationRecordedAt')})"
                    ),
                    evidence={"seed": seed.evidence},
                )
    finally:
        ctx.close()

    # -------- CONTROL: a non-owning requester is NOT shown that sample's freshness
    ctx2, _ = engine.new_context()
    try:
        page = ctx2.new_page()
        if not engine.login(page, seed.intruder_username, seed.intruder_password):
            negative = UiVerdict(False, "non-owner could not sign in; control inconclusive")
        else:
            open_tracked_sample(engine, page, seed.sample_id)
            region = sample_region(page, seed.sample_id, _FRESHNESS, timeout_ms=3000)
            leaked = region is not None and appears_within(region.get_by_text(_FRESHNESS), 3000)
            negative = UiVerdict(
                ok=not leaked,
                detail=(
                    "non-owner sees this sample's freshness (LEAK)"
                    if leaked
                    else "non-owner is not shown this sample's freshness (correctly scoped)"
                ),
            )
    finally:
        ctx2.close()

    return UiReport(positive=positive, negative=negative, notes="AC-68/69 freshness visibility")


CAPABILITY = UiCapability(
    id="sample_freshness_visible_to_owner",
    ac="AC-68/AC-69",
    description="Owner's sample view shows last-update time + staleness; non-owner does not.",
    run=_run,
    priority=True,
)
