"""Shared sample-view navigation + region scoping for the per-sample scenarios
(freshness AC-68/69, sample view AC-65/66/67).

The presentation of a single sample varies across candidate frontends: golden
reaches it by typing a tracking id into a labelled field and submitting, while
others show a "my samples" LIST whose card/row/link is opened by the sample id
to reveal a detail dialog/region. These helpers reach the loaded sample either
way, then scope an assertion to the SMALLEST landmark that a user reads as that
sample's detail -- the tightest landmark that contains BOTH the sample id AND a
required "anchor" token. Scoping by the id keeps the signal tied to the loaded
sample and away from unrelated copy elsewhere on the page; requiring the anchor
token means the resolver returns None (not a false region) when the field a
scenario grades is simply absent -- which is what makes both the surgical
mutants and the non-owner controls score correctly.

Everything here is locator-only via accessible name / role / visible text and
uses bounded, auto-retrying waits (never a fixed-length sleep). One Playwright
quirk is worked around by callers: ``filter(has_text=regex)`` silently fails to
match a regex that ENDS in ``\\b`` -- anchor patterns must avoid a trailing
word boundary (a leading ``\\b`` is fine).
"""
from __future__ import annotations

import re
import time

from playwright.sync_api import Locator, Page

# Owner-samples-list navigation, most-specific first. Plurals ("samples",
# "requests") deliberately avoid clicking a CREATE form ("New request") or the
# role switcher ("Requester") -- only a list affordance ("My samples", golden's
# "Requests", an admin "Samples") is matched.
NAV = (
    r"\bmy samples\b",
    r"\bmy requests\b",
    r"\bmy work\b",
    r"\btrack(?:ing)?\b",
    r"\bsamples\b",
    r"\brequests\b",
)
_OPEN_DETAIL = re.compile(r"details|view|track|open|progress|inspect", re.I)
_TRACKING_FIELD = re.compile(r"sample tracking id|sample.*id|tracking", re.I)
_TRACKING_SUBMIT = re.compile(r"view|track|progress|search|look", re.I)

# Roles tried when scoping to a sample's detail, detail-panel-first then
# row-granularity, so a candidate that shows a sample inline in a row (rather
# than a dedicated panel) is still scoped to THAT row.
_REGION_ROLES = ("dialog", "region", "article", "tabpanel", "listitem", "row", "group")

# Fallback container tags when NONE of the above ARIA roles match -- confirmed
# live on a real candidate whose entire sample-detail panel is a bare
# `<div class="panel"><h3>Tracking ...</h3>...<ul class="timeline">...`, with
# role=null all the way up to the document root (no dialog/region/article/...
# anywhere in its ancestor chain). Requiring an explicit ARIA role would
# silently score that candidate as "no detail rendered" even though the
# content is genuinely there -- exactly the golden-bias (rewarding only
# golden's own markup convention) this project's rules forbid. A bare `<div>`
# used as a detail panel is an extremely common, valid pattern with no ARIA
# violation of any kind (a landmark role is a nice-to-have, not a requirement
# for a sample detail view to legitimately exist).
_GENERIC_LANDMARKS = "div, section, li, td, article, main, aside"


def _smallest(loc: Locator, cap: int = 30) -> "Locator | None":
    """Among ``loc``'s matches, the one with the LEAST rendered text -- a
    proxy for "tightest" when candidates aren't scoped by ARIA role (ancestor
    containers of the true detail panel also match the same id+anchor filter,
    but carry MORE text, e.g. surrounding page furniture)."""
    best, best_len = None, None
    for idx in range(min(count(loc), cap)):
        cand = loc.nth(idx)
        try:
            text = cand.inner_text()
        except Exception:
            continue
        if best_len is None or len(text) < best_len:
            best, best_len = cand, len(text)
    return best


def count(locator: Locator) -> int:
    try:
        return locator.count()
    except Exception:
        return 0


def settle(page: Page) -> None:
    try:
        page.wait_for_load_state("networkidle", timeout=3500)
    except Exception:
        pass


def id_forms(sample_id: str) -> "re.Pattern[str]":
    """Case-insensitive alternation over the id forms a UI might render: full
    uuid, first-8 (lower/upper), last-6 (lower/upper)."""
    forms = [sample_id, sample_id[:8], sample_id[:8].upper(), sample_id[-6:], sample_id[-6:].upper()]
    return re.compile("|".join(re.escape(f) for f in forms), re.I)


def open_by_id(page: Page, id_pat: "re.Pattern[str]") -> bool:
    """Open a sample's detail from a list by its id, framework-agnostically:
    click an id-named control, else a Details/View/Track control inside the id's
    row, else the id text itself. Best-effort; returns whether something opened.

    First waits (bounded) for the sample id to render at all, so an async-loaded
    list ("Loading work...") is not mistaken for an absent sample."""
    try:
        page.get_by_text(id_pat).first.wait_for(state="visible", timeout=6000)
    except Exception:
        pass
    for role in ("button", "link", "tab", "menuitem"):
        named = page.get_by_role(role, name=id_pat)
        if count(named) == 0:
            continue
        try:
            loc = named.first
            loc.wait_for(state="visible", timeout=4000)
            loc.click()
            settle(page)
            return True
        except Exception:
            continue
    for role in ("row", "listitem", "article", "group"):
        cont = page.get_by_role(role).filter(has_text=id_pat)
        if count(cont) == 0:
            continue
        try:
            btn = cont.first.get_by_role("button", name=_OPEN_DETAIL).first
            btn.wait_for(state="visible", timeout=2500)
            btn.click()
            settle(page)
            return True
        except Exception:
            continue
    el = page.get_by_text(id_pat)
    if count(el) > 0:
        try:
            first = el.first
            first.wait_for(state="visible", timeout=3000)
            first.click()
            settle(page)
            return True
        except Exception:
            return False
    return False


def open_tracked_sample(engine, page: Page, sample_id: str) -> None:
    """Reach the loaded sample's detail. Golden path: a labelled tracking-id
    field -> fill + submit. List path: navigate to the samples list and open the
    sample by its id. Navigation is retried (bounded) while the post-login
    workspace renders its nav bar."""
    id_pat = id_forms(sample_id)
    settle(page)
    deadline = time.monotonic() + 6.0
    while True:
        clicked = False
        for nav in NAV:
            if engine.navigate(page, nav, timeout_ms=2000):
                settle(page)
                clicked = True
                break
        if clicked or time.monotonic() >= deadline:
            break
        page.wait_for_timeout(250)
    field = page.get_by_role("textbox", name=_TRACKING_FIELD).first
    used_field = False
    if count(field) > 0:
        try:
            field.wait_for(state="visible", timeout=2500)
            field.fill(sample_id)
            page.get_by_role("button", name=_TRACKING_SUBMIT).first.click()
            settle(page)
            used_field = True
        except Exception:
            used_field = False
    if not used_field:
        open_by_id(page, id_pat)


def sample_region(page: Page, sample_id: str, anchor: "re.Pattern[str]", timeout_ms: int = 6000):
    """The tightest landmark that contains BOTH the sample id AND the ``anchor``
    token -- the region a user reads as this sample's detail. Returns None when
    no such landmark exists. Chained ``has_text`` filters (id AND anchor) keep it
    fast (no per-element text extraction), re-checked on a bounded poll so an
    async-loaded detail panel is awaited -- it returns as soon as it resolves.

    ``anchor`` MUST NOT end in ``\\b`` (Playwright ``filter(has_text=regex)``
    silently fails on a trailing word boundary)."""
    id_pat = id_forms(sample_id)
    deadline = time.monotonic() + timeout_ms / 1000.0
    while True:
        for role in _REGION_ROLES:
            loc = page.get_by_role(role).filter(has_text=id_pat).filter(has_text=anchor)
            if count(loc) > 0:
                return loc.first
        generic = page.locator(_GENERIC_LANDMARKS).filter(has_text=id_pat).filter(has_text=anchor)
        if count(generic) > 0:
            best = _smallest(generic)
            if best is not None:
                return best
        if time.monotonic() >= deadline:
            return None
        page.wait_for_timeout(200)
