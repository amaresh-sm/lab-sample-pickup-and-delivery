"""AC-3 -- a role's work view renders BOTH current/active work AND past/
completed work.

Presentation-layer, framework-agnostic:
  * we seed ONE collection professional over HTTP with a COMPLETED collection
    leg (a journey driven through accept -> checkpoint -> collect) and an ACTIVE
    one (a second journey's leg only accepted), confirming both buckets via the
    app's own ``list_role_work`` before opening the browser;
  * POSITIVE: that worker logs in, opens their work view, and BOTH pieces of
    work are reachable -- the ACTIVE sample and the COMPLETED sample each
    render, located by their rendered id value (never markup). Frontends split
    current vs past differently (tabs, filters, or one combined list), so if a
    sought item is not already visible we reveal it by clicking a bucket
    affordance named for "current"/"active" or "completed"/"past" (accessible
    name / role only) and re-check.

There is no negative dimension: "shows both buckets" is a single coherent
property, and the ``ui-worklist-no-completed`` mutant -- a golden copy that
renders only the active bucket -- makes the completed sample unreachable, so
the positive flips to fail and the scenario is caught (1.0 -> 0.0) while the
other scenarios stay green.

If the candidate backend cannot bucket a worker into both an active and a
completed assignment, the seed returns ``ok=False`` and the scenario reports
"UI not exercisable" rather than a false UI failure.
"""
from __future__ import annotations

import re
import time

from model import UiCapability, UiReport, UiVerdict
from seed import seed_role_work_buckets
from tracking import id_forms, settle

# Bucket affordances, by accessible name. "active" covers pending/in-progress/
# assigned/current work; "past" covers completed/done/delivered/finished work.
_ACTIVE_BUCKET = re.compile(
    r"in[\s-]?progress|active|current|ongoing|assigned|accepted|pending|open|working|to[\s-]?do",
    re.I,
)
_PAST_BUCKET = re.compile(
    r"completed?|complete|done|delivered|finished|past|closed|history|archive|prior|previous",
    re.I,
)


def _visible(page, id_pat) -> bool:
    try:
        return page.get_by_text(id_pat).first.is_visible()
    except Exception:
        return False


def _reveal_and_find(page, id_pat, bucket_name: re.Pattern, timeout_ms: int = 8000) -> bool:
    """Reach a work item by its rendered id. If it is already visible (a single
    combined list) we are done; otherwise click each bucket affordance whose
    accessible name matches ``bucket_name`` (tab, then button, then link) and
    re-check after each -- so a tabbed/filtered work view is exercised without
    keying on any framework-specific markup."""
    deadline = time.monotonic() + timeout_ms / 1000.0
    # Give an async work list a bounded chance to render before deciding.
    try:
        page.get_by_text(id_pat).first.wait_for(state="visible", timeout=2500)
    except Exception:
        pass
    if _visible(page, id_pat):
        return True
    while time.monotonic() < deadline:
        clicked_any = False
        for role in ("tab", "button", "link"):
            controls = page.get_by_role(role, name=bucket_name)
            try:
                n = controls.count()
            except Exception:
                n = 0
            for i in range(n):
                try:
                    controls.nth(i).click(timeout=1500)
                    clicked_any = True
                    settle(page)
                except Exception:
                    continue
                if _visible(page, id_pat):
                    return True
        if not clicked_any:
            break
        page.wait_for_timeout(200)
    return _visible(page, id_pat)


def _run(engine) -> UiReport:
    seed = seed_role_work_buckets(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, seed.worker_username, seed.worker_password):
            positive = UiVerdict(False, "worker could not sign in through the public login form")
        else:
            # Reach the role's work view via a visible nav affordance.
            for nav in (r"\bmy work\b", r"\bwork\b", r"\bassignments?\b", r"\btasks?\b", r"\bqueue\b"):
                if engine.navigate(page, nav, timeout_ms=2500):
                    settle(page)
                    break
            # Identify each bucket item by EITHER its sample id or its
            # assignment id -- confirmed live: a real candidate renders the
            # active row as "sample X * assignment Y" (both) but its
            # completed row shows ONLY the assignment id, never the sample
            # id. AC-3 requires the past/active work ITEM to be reachable,
            # not that it be identified by any ONE specific field; both ids
            # are equally genuine, HTTP-confirmed identifiers for the SAME
            # work item, so accepting either avoids penalizing a candidate
            # whose completed-work list happens to key on the other one.
            active_aid = seed.evidence.get("activeAssignmentId")
            completed_aid = seed.evidence.get("completedAssignmentId")
            active_pat = id_forms(seed.active_sample_id)
            completed_pat = id_forms(seed.completed_sample_id)
            active_ok = _reveal_and_find(page, active_pat, _ACTIVE_BUCKET)
            if not active_ok and active_aid:
                active_ok = _reveal_and_find(page, id_forms(active_aid), _ACTIVE_BUCKET)
            completed_ok = _reveal_and_find(page, completed_pat, _PAST_BUCKET)
            if not completed_ok and completed_aid:
                completed_ok = _reveal_and_find(page, id_forms(completed_aid), _PAST_BUCKET)
            positive = UiVerdict(
                ok=active_ok and completed_ok,
                detail=(
                    f"worker work view: active/current item rendered(AC-3)={active_ok}, "
                    f"completed/past item rendered(AC-3)={completed_ok} "
                    f"(seeded buckets={seed.evidence.get('buckets')})"
                ),
                evidence={"seed": seed.evidence},
            )
    finally:
        ctx.close()

    return UiReport(positive=positive, notes="AC-3 role work view renders current + past work")


CAPABILITY = UiCapability(
    id="role_work_view_shows_current_and_past",
    ac="AC-3",
    description="A role's work view renders both current/active work and past/completed work.",
    run=_run,
    priority=True,
)
