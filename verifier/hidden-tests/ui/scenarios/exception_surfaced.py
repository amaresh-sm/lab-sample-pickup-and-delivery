"""AC-90 (spec lines 158, 202) -- the owner's sample view SURFACES that the
sample has been FLAGGED as an exception (an out-of-range temperature / container
handling breach), and that flag is NOT surfaced to a non-owning role.

Presentation-layer, framework-agnostic, built on the same ``tracking``-module
flow the freshness / sample-view scenarios use:
  * we seed a driven journey over HTTP and record a handling checkpoint whose
    observed temperature is OUTSIDE the sample's required range, so the backend
    genuinely flags the sample's status as ``exception`` (confirmed over HTTP by
    the seeder before any page opens);
  * POSITIVE: the OWNER opens the sample and, WITHIN the sample's own detail
    region, an exception/out-of-range/flagged indicator is visible -- located by
    visible text scoped to that region, never by markup;
  * CONTROL: a different requester does NOT get that sample's exception detail.

The region is scoped on the EXCEPTION token itself (the smallest landmark
holding BOTH the sample id AND an exception word). The exception vocabulary is a
DOMAIN vocabulary derived from the spec ("flagged as an exception",
"out-of-range temperature/container") rather than golden's exact copy: golden
renders the status as an "Exception" badge, but a differently-worded correct UI
that says "out of range", "flagged", "breach", "anomaly", or "alert" still
scores. Patterns carry a LEADING word boundary only -- Playwright's
``filter(has_text=regex)`` silently fails on a regex that ENDS in ``\\b``.
"""
from __future__ import annotations

import re

from model import UiCapability, UiReport, UiVerdict
from seed import seed_exception_sample
from helpers import appears_within
from tracking import open_tracked_sample, sample_region

# AC-90 -- the exception / out-of-range / flagged indicator. Domain vocabulary,
# not golden copy; LEADING `\b` only (no trailing word boundary).
_EXCEPTION = re.compile(
    r"\bexception|out[\s-]?of[\s-]?range|\bflagged|\bbreach|\banomal|\balert",
    re.I,
)
# A STABLE anchor for the sample's own detail panel -- generic sample-detail
# vocabulary that any conformant tracking/detail view renders alongside the
# sample id (status, holder/custodian, deadline, request/tracking). Used to
# resolve the detail region WITHOUT anchoring on the exception token itself, so
# the region is still found when the status badge and the id sit in sibling
# elements rather than one landmark; the exception indicator is then asserted
# WITHIN that region.
_DETAIL_ANCHOR = re.compile(
    r"\bstatus|\bholder|\bcustod|\bdeadline|\brequest|\btracking|\bsample|\bjourney|\bstage",
    re.I,
)


def _run(engine) -> UiReport:
    seed = seed_exception_sample(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    # -------- POSITIVE: owner sees an exception indicator for the sample -------
    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, seed.owner_username, seed.owner_password):
            positive = UiVerdict(False, "owner could not sign in through the public login form")
        else:
            open_tracked_sample(engine, page, seed.sample_id)
            # Primary: the tightest landmark holding BOTH the sample id AND an
            # exception word. Fallback: resolve the sample's detail panel by a
            # STABLE detail anchor (status/holder/deadline/...) -- so the region
            # is found even when the exception badge and the id sit in sibling
            # elements -- then assert the exception indicator WITHIN it.
            region = sample_region(page, seed.sample_id, _EXCEPTION)
            if region is None:
                region = sample_region(page, seed.sample_id, _DETAIL_ANCHOR)
            if region is None:
                positive = UiVerdict(
                    False,
                    "no sample-scoped detail region was found for the owner "
                    f"(seeded sampleStatus={seed.evidence.get('sampleStatus')})",
                    evidence={"seed": seed.evidence},
                )
            else:
                flagged = appears_within(region.get_by_text(_EXCEPTION), 3000)
                positive = UiVerdict(
                    ok=flagged,
                    detail=(
                        f"owner sample region: exception-flag(AC-90)={flagged} "
                        f"(seeded sampleStatus={seed.evidence.get('sampleStatus')})"
                    ),
                    evidence={"seed": seed.evidence},
                )
    finally:
        ctx.close()

    # -------- CONTROL: a non-owner is NOT shown that sample's exception detail --
    ctx2, _ = engine.new_context()
    try:
        page = ctx2.new_page()
        if not engine.login(page, seed.intruder_username, seed.intruder_password):
            negative = UiVerdict(False, "non-owner could not sign in; control inconclusive")
        else:
            open_tracked_sample(engine, page, seed.sample_id)
            region = sample_region(page, seed.sample_id, _EXCEPTION, timeout_ms=3000)
            leaked = region is not None and appears_within(region.get_by_text(_EXCEPTION), 3000)
            negative = UiVerdict(
                ok=not leaked,
                detail=(
                    "non-owner sees this sample's exception detail (LEAK)"
                    if leaked
                    else "non-owner is not shown this sample's exception detail (correctly scoped)"
                ),
            )
    finally:
        ctx2.close()

    return UiReport(positive=positive, negative=negative, notes="AC-90 sample exception surfaced to owner")


CAPABILITY = UiCapability(
    id="sample_view_surfaces_exception_flag",
    ac="AC-90",
    description="Owner's sample view surfaces that the sample is flagged as an exception; non-owner does not.",
    run=_run,
    priority=True,
)
