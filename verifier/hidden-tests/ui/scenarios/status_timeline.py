"""AC-4 -- a work item shows an ORDERED HISTORY of status changes
(oldest -> newest), not merely its current status.

Presentation-layer, framework-agnostic, built on the same ``tracking``-module
flow the freshness / sample-view scenarios use:
  * we seed a DRIVEN journey over HTTP (accept -> checkpoint -> collect ->
    record location), so the sample genuinely passed through several distinct
    lifecycle transitions and the backend holds an ordered event history for it;
  * POSITIVE: the OWNER opens the sample and, WITHIN the sample's own detail
    region, a HISTORY of >= 2 distinct status-change entries is rendered, in
    chronological (non-decreasing) order -- located by visible text, never markup;
  * CONTROL: a non-owner is not shown that sample's history.

Isolating the STATUS HISTORY from the other ordered lists on the same view
(the current-status summary, the "my requests" list, and the ROUTE-PROGRESS
"ordered journey" stop list graded by AC-67) without golden-specific markup:
we count list ENTRIES (role=listitem) whose visible text names a lifecycle
TRANSITION EVENT -- a domain vocabulary (accepted / checkpoint / collected /
handoff / received / ... ) derived from the sample lifecycle, NOT from golden's
exact copy -- and require >= 2 such entries carrying >= 2 DISTINCT transition
tokens. A single current-status chip (one status word) and the route stops
(place + planned/completed badge, no transition verb) can never reach two
distinct transition entries, so only a real multi-event history satisfies it;
the ``ui-timeline-hidden`` mutant, which renders only the current status,
drops to zero matches and is caught.

Ordering note: golden stamps every transition with the same wall-minute when a
journey is driven this fast, and ``formatTime`` renders to minute precision, so
the rendered timestamps are typically equal. The ordering check is therefore a
MONOTONICITY guard (it catches a history rendered newest-first) rather than a
fine-grained sort test; the substantive assertion is the presence of the
ordered multi-event history itself. Patterns carry a LEADING word boundary only
(Playwright's ``filter(has_text=regex)`` silently fails on a trailing ``\\b``).
"""
from __future__ import annotations

import re

from model import UiCapability, UiReport, UiVerdict
from seed import seed_driven_journey
from tracking import open_tracked_sample, sample_region

# The pinned contract's OWN lifecycle-transition operationIds -- a candidate is
# free to log its history using these VERBATIM (or truncated, dropping a
# trailing noun the candidate considered redundant) instead of a human-worded
# label; anchoring on the contract's own operation vocabulary is the same
# anti-golden-bias principle serviceability_dry_run.py already uses for its
# response enum. Confirmed live: a candidate's timeline renders literally
# "create_collection_request" and "collect_and_seal" (a truncated
# `collect_and_seal_sample`) as its two entries -- genuinely two DIFFERENT
# lifecycle events, both of which are real, informative history entries; AC-4
# does not require human-friendly wording. These are listed BEFORE the
# shorter human-word alternatives below (and before each other, longest
# overlapping prefix first) so ``re.finditer`` extracts the full distinctive
# phrase as the match -- e.g. "create_collection_request" -- rather than
# collapsing to a short substring like "collect" that a SHORTER alternative
# would otherwise grab from within "collection" or "collect_and_seal" (which
# would make two genuinely different events indistinguishable as the SAME
# detected token, defeating the >=2-DISTINCT-transitions check for exactly the
# reason it exists: to require a REAL multi-event history, not one repeated
# word).
_OPERATION_ID = (
    r"create_collection_request|ingest_presealed_sample|"
    r"collect_and_seal(?:_sample)?|confirm_laboratory_receipt|"
    r"record_handling_checkpoint|record_laboratory_exception|"
    r"update_assignment_progress|confirm_handoff|initiate_handoff|"
    r"decline_assignment|accept_assignment|report_disruption|"
    r"reroute_journey|cancel_journey|split_journey|merge_journeys|"
    r"edit_active_plan"
)
# Lifecycle TRANSITION events (domain vocabulary, not golden copy). Deliberately
# EXCLUDES bare "planned"/"completed" (the route-stop badges) and place words
# (pickup/transfer/delivery) so the route-progress list is never mistaken for
# the status history.
_EVENT = re.compile(
    _OPERATION_ID + r"|"
    r"accepted|declined|checkpoint|collect(?:ed)?|sealed|hand(?:ed|[\s-]?off)|"
    r"received|receipt|reroute|disrupt|offered|assigned|recorded|delivered|"
    r"in[\s-]?transit|picked[\s-]?up|exception|custody",
    re.I,
)
# The subset used to SCOPE the sample's detail region: transition ACTIONS that
# a lifecycle history renders but that are NEVER a sample's current-status chip
# (accepted / checkpoint / collected / recorded / handoff / received / ...).
# Excluding current-status words (sealed / delivered / in-transit) here stops
# the region from resolving to a one-line "my requests" row that merely shows
# the sample id next to its current status -- it forces resolution up to the
# detail panel that actually holds the multi-event history.
_HISTORY_ANCHOR = re.compile(
    _OPERATION_ID + r"|"
    r"accepted|declined|checkpoint|collected|recorded|hand(?:ed|[\s-]?off)|"
    r"received|receipt|reroute|disrupt|offered|assigned",
    re.I,
)
_TIME = re.compile(r"\b(\d{1,2}):(\d{2})\s*([ap]\.?m\.?)?", re.I)


def _sort_key(text: str):
    """A coarse comparable key from the first clock time in an entry, so a
    history rendered oldest->newest is non-decreasing and one rendered
    newest-first is decreasing. Returns None when no time is present."""
    m = _TIME.search(text)
    if not m:
        return None
    hour = int(m.group(1)) % 12
    if m.group(3) and m.group(3).lower().startswith("p"):
        hour += 12
    return hour * 60 + int(m.group(2))


def _history_entries(region) -> list[str]:
    items = region.get_by_role("listitem").filter(has_text=_EVENT)
    try:
        items.first.wait_for(state="visible", timeout=4000)
    except Exception:
        return []
    out: list[str] = []
    for i in range(min(items.count(), 40)):
        try:
            out.append(items.nth(i).inner_text())
        except Exception:
            continue
    return out


def _distinct_tokens(entries: list[str]) -> set[str]:
    tokens: set[str] = set()
    for text in entries:
        for m in _EVENT.finditer(text):
            tokens.add(m.group(0).lower().replace(" ", "").replace("-", ""))
    return tokens


def _ordered(entries: list[str]) -> tuple[bool, str]:
    keys = [k for k in (_sort_key(t) for t in entries) if k is not None]
    if len(keys) < 2:
        return True, "ordering not contradicted (timestamps not comparable / collapsed to the minute)"
    ok = all(a <= b for a, b in zip(keys, keys[1:]))
    return ok, f"rendered times {'non-decreasing' if ok else 'OUT OF ORDER'} {keys}"


def _run(engine) -> UiReport:
    seed = seed_driven_journey(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    # -------- POSITIVE: owner sees an ordered >=2-entry status history --------
    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, seed.owner_username, seed.owner_password):
            positive = UiVerdict(False, "owner could not sign in through the public login form")
        else:
            open_tracked_sample(engine, page, seed.sample_id)
            region = sample_region(page, seed.sample_id, _HISTORY_ANCHOR)
            if region is None:
                positive = UiVerdict(
                    False,
                    "no sample-scoped region carrying a status-history event was found for the owner "
                    f"(orderedLegs={seed.evidence.get('orderedLegs')})",
                    evidence={"seed": seed.evidence},
                )
            else:
                entries = _history_entries(region)
                tokens = _distinct_tokens(entries)
                ordered_ok, ordered_detail = _ordered(entries)
                enough = len(entries) >= 2 and len(tokens) >= 2
                positive = UiVerdict(
                    ok=enough and ordered_ok,
                    detail=(
                        f"owner status history: entries={len(entries)}, distinct-transitions={len(tokens)} "
                        f"{sorted(tokens)}; {ordered_detail}"
                    ),
                    evidence={"entries": entries[:10], "tokens": sorted(tokens)},
                )
    finally:
        ctx.close()

    # -------- CONTROL: a non-owner is NOT shown that sample's history ---------
    ctx2, _ = engine.new_context()
    try:
        page = ctx2.new_page()
        if not engine.login(page, seed.intruder_username, seed.intruder_password):
            negative = UiVerdict(False, "non-owner could not sign in; control inconclusive")
        else:
            open_tracked_sample(engine, page, seed.sample_id)
            region = sample_region(page, seed.sample_id, _HISTORY_ANCHOR, timeout_ms=3000)
            leaked = region is not None and len(_history_entries(region)) >= 2
            negative = UiVerdict(
                ok=not leaked,
                detail=(
                    "non-owner sees this sample's status history (LEAK)"
                    if leaked
                    else "non-owner is not shown this sample's status history (correctly scoped)"
                ),
            )
    finally:
        ctx2.close()

    return UiReport(positive=positive, negative=negative, notes="AC-4 ordered status timeline")


CAPABILITY = UiCapability(
    id="sample_view_shows_ordered_status_history",
    ac="AC-4",
    description="Owner's sample view renders an ordered (oldest->newest) history of >=2 status changes; non-owner does not.",
    run=_run,
    priority=True,
)
