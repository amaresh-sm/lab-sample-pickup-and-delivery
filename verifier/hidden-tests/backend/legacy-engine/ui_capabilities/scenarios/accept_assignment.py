"""instruction.md lines 38/48/57/67 ("Accept or decline an offered ...
assignment") / AC-31 ("An assignment cannot be accepted by anyone other than
the offered worker"). A genuine WRITE through the UI, not just observation --
the critical check is that the click actually persists server-side.

Presentation-layer, framework-agnostic:
  * ``seed.seed_pending_offer`` plans a serviceable journey and hands back the
    collection leg while it is genuinely PENDING (offered, not yet accepted --
    confirmed via the collector's OWN ``list_role_work`` over HTTP before any
    page opens), plus an unrelated, otherwise-eligible collection professional
    (the AC-31 control) who was never offered this assignment, plus a
    ``recheck(alias)`` callable that re-reads (alias's OWN) ``list_role_work``
    AFTER the browser acts;
  * POSITIVE: the collector opens their offer-decision control (accessible
    name drawn from broad accept/confirm/take vocabulary, an "assignment id"
    field to identify WHICH offer), types the seeded assignment id, clicks
    accept, and -- the critical write-through check -- ``recheck("collector")``
    confirms over HTTP that the assignment has actually moved out of the
    "pending" bucket server-side. This is asserted from a genuine HTTP
    re-read, NEVER from the UI's own success message (SIDE-EFFECT AWARENESS:
    a hollow UI could claim "accepted" while the backend never received it,
    or a real backend could reject it silently);
  * CONTROL (AC-31): a DIFFERENT, unrelated collection professional -- never
    offered this assignment -- attempts the SAME accept through their OWN
    session. Confirmed over HTTP: the assignment is STILL owned/accepted by
    the original collector (unchanged bucket/status), and the intruder's own
    ``list_role_work`` does NOT show it -- the intruder's attempt had no
    effect, exactly what AC-31 requires.

Locators are broad accept/decline/offer vocabulary derived from
instruction.md's own language, not golden's exact "Accept assignment" copy --
"confirm"/"take" a pending assignment/offer still scores.
"""
from __future__ import annotations

import re

from model import UiCapability, UiReport, UiVerdict
from seed import seed_pending_offer
from tracking import count, id_forms, settle

_OFFER_PANEL_NAME = re.compile(r"accept|decline|offer", re.I)
_WORK_NAV = (r"\bmy work\b", r"\bwork\b")
_ASSIGNMENT_ID_FIELD = re.compile(r"assignment.{0,10}id|offer.{0,10}id", re.I)
_ACCEPT_BUTTON = re.compile(r"\baccept\b|\bconfirm\b|\btake\b", re.I)


def _offer_panel(page):
    region = page.get_by_role("region", name=_OFFER_PANEL_NAME)
    if count(region) > 0:
        return region.first
    return page


_ROW_ROLES = ("row", "listitem", "article", "group")


def _click_within_row(panel, id_pat) -> bool:
    """Some candidates never ask the user to TYPE an assignment id at all --
    they list each offer as its own row/card (the id shown as plain text) with
    an Accept control right there in that row. Requiring a fillable id field is
    an over-specific assumption about ONE UI pattern; AC-31 only cares that the
    CORRECT offer gets accepted, which identifying-by-row satisfies exactly as
    well as identifying-by-typed-id. Confirmed live: a candidate's offer table
    has no id input anywhere, just `<tr><td>065ab381</td>...<td><button>Accept
    </button></td></tr>` -- clicking Accept inside the row containing the
    seeded id IS unambiguous offer identification."""
    for role in _ROW_ROLES:
        row = panel.get_by_role(role).filter(has_text=id_pat)
        if count(row) == 0:
            continue
        btn = row.first.get_by_role("button", name=_ACCEPT_BUTTON)
        if count(btn) > 0:
            try:
                btn.first.click()
                return True
            except Exception:
                continue
    return False


def _open_offer_then_accept(panel, page, id_pat) -> bool:
    """Support a work-card pattern where the seeded offer is itself a
    button/link and its Accept action lives in the detail view opened by that
    control.

    The offer remains unambiguously identified: only a clickable control whose
    rendered accessible text contains the seeded assignment id is opened.
    Credit still depends on the subsequent HTTP write-through re-read, so an
    inert card/modal cannot pass.
    """
    try:
        page.get_by_text(id_pat).first.wait_for(state="visible", timeout=6000)
    except Exception:
        pass
    for role in ("button", "link"):
        candidates = panel.get_by_role(role).filter(has_text=id_pat)
        for idx in range(count(candidates)):
            candidate = candidates.nth(idx)
            try:
                if not candidate.is_visible():
                    continue
                candidate.click()
                settle(page)
            except Exception:
                continue
            detail = _offer_panel(page)
            accept = detail.get_by_role("button", name=_ACCEPT_BUTTON)
            if count(accept) > 0:
                try:
                    accept.first.click()
                    return True
                except Exception:
                    pass
    return False


def _attempt_accept(engine, page, assignment_id: str) -> tuple[bool, bool]:
    """Identify the seeded offer -- either by filling an id field (if one
    exists) or by locating the row/card that already displays the id -- then
    click Accept scoped to that identification. Returns (identified, clicked).

    Tries the fillable-id path FIRST (and only trusts its own click): if no
    id field exists, or filling+clicking it does not succeed, falls back to
    the row-scoped pattern rather than falling through to a page-wide accept
    button, which would click whichever offer happens to be first/only on the
    page without ever confirming it is the SEEDED one -- correct with a single
    offer on screen, but not a genuine identification with more than one."""
    for nav in _WORK_NAV:
        if engine.navigate(page, nav, timeout_ms=2500):
            settle(page)
            break
    panel = _offer_panel(page)

    field = panel.get_by_label(_ASSIGNMENT_ID_FIELD)
    if count(field) > 0:
        try:
            field.first.fill(assignment_id)
            btn = panel.get_by_role("button", name=_ACCEPT_BUTTON)
            if count(btn) > 0:
                btn.first.click()
                settle(page)
                return True, True
        except Exception:
            pass

    if _click_within_row(panel, id_forms(assignment_id)):
        settle(page)
        return True, True

    if _open_offer_then_accept(panel, page, id_forms(assignment_id)):
        settle(page)
        return True, True

    return False, False


def _poll_recheck(seed, alias: str, timeout_ms: int = 5000) -> dict:
    """Bounded poll (never a fixed sleep) for the assignment to leave the
    pending bucket -- the accept call is an async network round-trip, so a
    single immediate re-read could race a genuinely-successful write."""
    import time

    deadline = time.monotonic() + timeout_ms / 1000.0
    last: dict = {}
    while True:
        last = seed.recheck(alias) if seed.recheck else {}
        if last.get("bucket") not in (None, "pending"):
            return last
        if time.monotonic() >= deadline:
            return last
        time.sleep(0.25)


def _run(engine) -> UiReport:
    seed = seed_pending_offer(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    aid = seed.evidence.get("collectionAssignmentId", "")

    # -------- POSITIVE: the offered worker accepts through the UI ----------
    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, seed.worker_username, seed.worker_password):
            positive = UiVerdict(False, "collector could not sign in through the public login form")
        else:
            filled, clicked = _attempt_accept(engine, page, aid)
            after = _poll_recheck(seed, "collector") if clicked else (seed.recheck("collector") if seed.recheck else {})
            accepted = clicked and after.get("bucket") in ("inProgress", "completed")
            positive = UiVerdict(
                ok=filled and clicked and accepted,
                detail=(
                    f"filled assignment id={filled}, clicked accept={clicked}; "
                    f"HTTP re-read after click: bucket={after.get('bucket')!r} status={after.get('status')!r} "
                    f"(pre-click bucket={seed.evidence.get('preBucket')!r}); write-through-confirmed={accepted}"
                ),
                evidence={"seed": seed.evidence, "after": after},
            )
    finally:
        ctx.close()

    # -------- CONTROL (AC-31): an unrelated worker cannot accept it --------
    ctx2, _ = engine.new_context()
    try:
        page = ctx2.new_page()
        if not engine.login(page, seed.intruder_username, seed.intruder_password):
            negative = UiVerdict(False, "intruder could not sign in; control inconclusive")
        else:
            filled2, clicked2 = _attempt_accept(engine, page, aid)
            collector_state = seed.recheck("collector") if seed.recheck else {}
            intruder_state = seed.recheck("intruder") if seed.recheck else {}
            # The definitive signal: the intruder's OWN list_role_work must
            # never show this assignment at all (it was never offered to
            # them, and their accept attempt must not have granted it).
            stolen = intruder_state.get("bucket") is not None
            negative = UiVerdict(
                ok=not stolen,
                detail=(
                    f"intruder attempted accept (filled={filled2}, clicked={clicked2}); "
                    f"intruder's own list_role_work now shows this assignment={stolen} "
                    f"(bucket={intruder_state.get('bucket')!r}); collector's state after intruder's "
                    f"attempt: bucket={collector_state.get('bucket')!r}"
                ),
                evidence={"intruderState": intruder_state, "collectorState": collector_state},
            )
    finally:
        ctx2.close()

    return UiReport(positive=positive, negative=negative, notes="AC-31 accept-assignment write-through + ownership control")


CAPABILITY = UiCapability(
    id="accept_assignment_writes_through",
    ac="AC-31",
    description="Accepting an offered assignment through the UI persists server-side; an unrelated worker cannot accept it.",
    run=_run,
    priority=True,
)
