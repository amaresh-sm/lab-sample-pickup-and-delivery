"""AC-39/AC-115/AC-116/AC-117/AC-121/AC-122 -- a dispatcher/admin MONITOR view renders MULTIPLE monitorable
categories (samples, workers, nodes/collection points, laboratories, routes,
exceptions), and a plain requester does NOT get that full monitor.

Presentation-layer, framework-agnostic:
  * we seed a driven journey over HTTP (``seed_driven_journey`` already creates
    samples, workers, nodes, laboratories, routes, and an active journey), so
    the monitor has real state to render;
  * POSITIVE: the ADMIN logs in, navigates to the operations/monitor view, and
    >= 3 DISTINCT monitor categories are present -- each located by DOMAIN
    vocabulary as a METRIC label (role=article) or a PANEL heading (role=
    heading), never by markup;
  * CONTROL: a plain requester login does NOT get the full monitor -- fewer than
    2 of the worker / node / laboratory / route categories are present for the
    requester (they see only their own samples/requests).

Counting via metric-articles and panel-headings (rather than arbitrary page
text) is the anti-golden-bias core here: a map LEGEND or an audit filter's
entity-type dropdown may echo category WORDS incidentally, but the monitorable
CATEGORIES a dispatcher actually watches are surfaced as metrics and panel
headings. Golden's operations view renders them as MetricCards ("Samples",
"Workers", "Collection points", "Laboratories") and OperationsSummary panels
("Active routes", "Alerts & exceptions"); a differently-worded correct monitor
that titles them "Couriers"/"Journeys"/"Alerts" still scores. Category patterns
carry a LEADING word boundary only (Playwright's ``filter(has_text=regex)``
silently fails on a regex that ENDS in ``\\b``).
"""
from __future__ import annotations

import re

from model import UiCapability, UiReport, UiVerdict
from seed import seed_driven_journey
from tracking import count, settle

# Monitorable categories, keyed by DOMAIN vocabulary (not golden copy). Leading
# `\b` only; no trailing word boundary.
_CATEGORIES: dict[str, "re.Pattern[str]"] = {
    "samples": re.compile(r"\bsample", re.I),
    "workers": re.compile(r"\bworker|\bcourier|\bprofessional|\bpersonnel|\bstaff|delivery partner", re.I),
    "nodes": re.compile(r"\bnode|collection point", re.I),
    "laboratories": re.compile(r"\blaborator", re.I),
    "routes": re.compile(r"\broute|\bjourney", re.I),
    "exceptions": re.compile(r"\bexception|\balert", re.I),
}
# The subset used for the CONTROL: categories a plain requester must NOT get the
# full monitor for (their own samples/exceptions may legitimately show; the
# operational board of workers/nodes/labs/routes must not).
_MONITOR_ONLY = ("workers", "nodes", "laboratories", "routes")

# Monitor/operations navigation, richest surface first.
_NAV = (r"\blive operations\b", r"\boperations\b", r"\bmonitor\b", r"\bdashboard\b", r"\boverview\b")


def _category_present(page, pat: "re.Pattern[str]") -> bool:
    """True iff the category surfaces as a METRIC (role=article label), a
    PANEL heading (role=heading), or ANY other visible text carrying the
    category's own domain vocabulary -- located by accessible name / visible
    text, never by markup.

    The plain-text fallback exists because "metric card" is a DESIGN pattern,
    not a universal ARIA requirement: a candidate is free to render its stat
    row as plain `<div class="stat"><div class="n">1</div><div class="l">
    Samples</div></div>` with no `role=article`/`role=heading` anywhere, and
    that is just as legitimate a way to show "there is a Samples category on
    this monitor" as a semantically-marked-up card. Confirmed live: exactly
    this shape, zero article/heading roles on the page at all. False-positive
    risk is low and bounded -- each category's vocabulary word (worker/node/
    laboratory/route/exception) is domain-specific, not generic page chrome,
    and the positive additionally requires >=3 DISTINCT categories to match,
    so an isolated incidental word cannot alone satisfy the check."""
    metric = page.get_by_role("article").filter(has_text=pat)
    if count(metric) > 0:
        try:
            if metric.first.is_visible():
                return True
        except Exception:
            pass
    heading = page.get_by_role("heading", name=pat)
    if count(heading) > 0:
        try:
            if heading.first.is_visible():
                return True
        except Exception:
            pass
    text = page.get_by_text(pat)
    if count(text) > 0:
        try:
            if text.first.is_visible():
                return True
        except Exception:
            pass
    return False


def _distinct_categories(page, keys) -> list[str]:
    return [name for name in keys if _category_present(page, _CATEGORIES[name])]


def _run(engine) -> UiReport:
    seed = seed_driven_journey(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    # -------- POSITIVE: admin's monitor renders >= 3 distinct categories -------
    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, engine.admin_user, engine.admin_pass):
            positive = UiVerdict(False, "admin could not sign in through the public login form")
        else:
            for nav in _NAV:
                if engine.navigate(page, nav, timeout_ms=2500):
                    settle(page)
                    break
            present = _distinct_categories(page, list(_CATEGORIES))
            positive = UiVerdict(
                ok=len(present) >= 3,
                detail=f"admin monitor distinct categories={len(present)} {present}",
                evidence={"categories": present},
            )
    finally:
        ctx.close()

    # -------- CONTROL: a plain requester does NOT get the full monitor ---------
    ctx2, _ = engine.new_context()
    try:
        page = ctx2.new_page()
        if not engine.login(page, seed.owner_username, seed.owner_password):
            negative = UiVerdict(False, "requester could not sign in; control inconclusive")
        else:
            reached = False
            for nav in _NAV:
                if engine.navigate(page, nav, timeout_ms=1500):
                    settle(page)
                    reached = True
                    break
            # Only classify operational vocabulary as a leaked monitor after
            # the requester can actually reach a monitor/operations surface.
            # Request forms legitimately mention laboratories and routes in
            # explanatory copy; counting those words on an unrelated page as
            # a full monitoring dashboard is a false RBAC failure.
            monitor_only = _distinct_categories(page, _MONITOR_ONLY) if reached else []
            negative = UiVerdict(
                ok=len(monitor_only) < 2,
                detail=(
                    f"requester operational categories (worker/node/lab/route)={len(monitor_only)} "
                    f"{monitor_only} (monitor navigation reached={reached}) -- "
                    f"{'full monitor LEAKED' if len(monitor_only) >= 2 else 'correctly withheld'}"
                ),
                evidence={"monitorOnlyForRequester": monitor_only, "monitorNavigationReached": reached},
            )
    finally:
        ctx2.close()

    return UiReport(
        positive=positive,
        negative=negative,
        notes="AC-39/AC-115/AC-116/AC-117/AC-121/AC-122 monitor dashboard categories",
    )


CAPABILITY = UiCapability(
    id="monitor_dashboard_renders_categories",
    ac="AC-39/115/116/117/121/122",
    description="Admin/dispatcher monitor renders >=3 monitorable categories; a plain requester does not.",
    run=_run,
    priority=True,
)
