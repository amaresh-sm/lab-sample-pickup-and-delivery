"""AC-9 / AC-10 (window and deadline are separate constraints) -- a
form/business-rule violation on the create-request form must be SURFACED to
the user, not silently dropped.

Grounded in golden's own error-announcement pattern, generalized: golden's
``RequestPanel.vue`` renders a caught error via
``<InlineResult kind="error">{{ action.error.value }}</InlineResult>``, and
``InlineResult.vue`` is `<output aria-live="polite">` -- a standard ARIA
live-region, not a framework internal. The GENERIC anchor here is therefore
the `[aria-live]` ATTRIBUTE (any framework's error-announcement idiom likely
uses one) together with `role=alert`/`role=status` as alternates, each
additionally required to carry error VOCABULARY text -- never golden's exact
copy -- so a persistent, unrelated `aria-live` region on the page (e.g. a
connection-status badge that is ALSO `aria-live="polite"` on every page load)
can never be mistaken for the validation error.

Presentation-layer, framework-agnostic:
  * we seed a serviceable network + a requester over HTTP, and confirm --
    against THIS candidate backend, before any page opens -- that an inverted
    collection window (``earliestCollectionAt`` scheduled AFTER
    ``latestCollectionAt``) really is refused with the pinned contract's
    ``invalid_collection_window`` (the exact shape
    ``engine/capabilities/requests.py``'s own negative probe uses);
  * POSITIVE: the requester opens the create-request form, submits that SAME
    inverted-window body (typed into fields located by broad accessible-label
    vocabulary, not golden's exact copy), and a visible error announcement
    carrying error vocabulary (invalid/error/must/cannot/deadline/window)
    appears within a bounded window right after submit. We poll IMMEDIATELY
    after the click (never a fixed sleep) so a candidate that uses a
    transient, auto-dismissing toast instead of a persistent live-region is
    still caught before it disappears; we do not assert on it staying visible
    or disappearing afterward.

No control/negative dimension: "a rejected submission is surfaced" is one
coherent property with no second actor whose visibility should differ
(mirrors ``role_work_view_shows_current_and_past`` (AC-3), also positive-only).
"""
from __future__ import annotations

import re
import time

from helpers import field_by_visible_label
from model import UiCapability, UiReport, UiVerdict
from seed import seed_requester_ready
from tracking import count, settle

_REQUESTS_NAV = (r"\brequests\b", r"\bnew request\b", r"\bcreate request\b")

# The CREATE-request panel's own accessible name -- an action verb ("create"/
# "new"/"submit"/"register") followed within a few words by a request/
# collection/sample noun. This is the SAME "verb near a noun, not exact copy"
# idiom driver.py's own `_ENTER_WORKSPACE` already uses. It exists because a
# requester's "Requests" page ALSO renders a near-identical serviceability
# PRE-CHECK form with the SAME field labels ("Sample type", "Earliest
# collection", ...) -- without scoping to the create-panel's own landmark,
# filling "the field named X" page-wide would silently fill the WRONG form's
# fields (confirmed by reading golden's `RequestsPage.vue` /
# `ServiceabilityCheckPanel.vue`: both panels share field vocabulary, but only
# the create-request panel's submission can ever produce
# `invalid_collection_window`).
_CREATE_PANEL_NAME = re.compile(r"(create|new|submit|register).{0,20}(request|collection|sample)", re.I)

# Broad accessible-label vocabulary for the fields this scenario must fill,
# derived from the contract's own field names, not golden's exact copy.
_SAMPLE_TYPE_FIELD = re.compile(r"sample\s*type", re.I)
_EARLIEST_FIELD = re.compile(r"earliest.*collect|collect.*(earliest|open|start)|earliest", re.I)
_LATEST_FIELD = re.compile(r"latest.*collect|collect.*(latest|close|end)|latest", re.I)
_SUBMIT_NAME = re.compile(r"create.*(collection|request)|submit.*request|create request|submit", re.I)

# Error vocabulary -- domain words a validation-rejection message plausibly
# carries, not golden's exact string ("earliestCollectionAt must precede
# latestCollectionAt"). Leading `\b` only (Playwright's filter(has_text=regex)
# silently fails on a trailing `\b`).
_ERROR_VOCAB = re.compile(r"\binvalid|\berror|\bmust\b|\bcannot\b|\bdeadline|\bwindow", re.I)


def _create_panel(page):
    """The create-request panel's own landmark, scoped by its accessible name
    (an ARIA `region` with a heading-derived name, e.g. golden's `<section
    aria-labelledby>` around `<h2>Create request</h2>`) -- so field lookups
    never cross into the page's OTHER form (the serviceability pre-check) that
    happens to share the same field vocabulary. Falls back to the whole page
    (best effort) if no such named region is found."""
    region = page.get_by_role("region", name=_CREATE_PANEL_NAME)
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
    # A <select> rejects .fill() outright -- confirmed live on a real
    # candidate that renders "Sample type" as a dropdown of seeded type
    # names. Fall back to selecting BY VALUE (the id this scenario is
    # actually given), then by visible label text, before giving up.
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


def _is_label_text(loc) -> bool:
    """True iff the matched element IS a `<label>`, sits inside one, OR
    (Playwright's ``get_by_text`` can match a wrapping container whose own
    innerText happens to equal a descendant's) CONTAINS one. A vocabulary word
    (e.g. "deadline", "window") can legitimately be part of an ORDINARY field
    label ("Deadline (hours)") with no error involved at all -- confirmed
    live: a candidate whose deadline/window fields could not even be located
    (a separate, real accessibility gap) still submitted, and the plain-text
    fallback matched that field's own wrapping `<div><label>Deadline
    (hours)</label>...</div>` as if it were the validation error. Checking
    only ANCESTORS (``closest``) missed this -- the match was the wrapping
    div, not the label itself, so ``closest('label')`` from the div correctly
    (but insufficiently) returned null. No genuine error announcement is ever
    rendered AS, or built AROUND, a form control's own label, so excluding
    both directions closes this false-positive without narrowing genuine
    detection."""
    try:
        return bool(
            loc.evaluate("el => el.matches('label') || el.closest('label') !== null || el.querySelector('label') !== null")
        )
    except Exception:
        return False


def _vocab_candidates(page):
    return (
        lambda: page.get_by_role("alert").filter(has_text=_ERROR_VOCAB),
        lambda: page.get_by_role("status").filter(has_text=_ERROR_VOCAB),
        lambda: page.locator("[aria-live]").filter(has_text=_ERROR_VOCAB),
        lambda: page.get_by_text(_ERROR_VOCAB),
    )


def _visible_vocab_texts(page) -> set[str]:
    """Snapshot every CURRENTLY visible vocabulary-matching text, normalized.
    Used to exclude pre-existing page furniture from the post-submit match --
    confirmed live on golden: a static field hint ("Optional. Defaults to the
    delivery deadline.") is present on the page from the moment it loads,
    well before any submit, and satisfies the vocabulary filter purely by
    coincidence (it names the "deadline" field). A vocabulary word that is
    also ordinary domain copy cannot be told apart from a real error by
    content alone -- only by whether the submission is what made it appear."""
    texts: set[str] = set()
    for make in _vocab_candidates(page):
        loc = make()
        for idx in range(count(loc)):
            cand = loc.nth(idx)
            try:
                if cand.is_visible():
                    texts.add(cand.inner_text().strip())
            except Exception:
                continue
    return texts


def _error_region(page, timeout_ms: int, exclude_texts: set[str] = frozenset()):
    """The smallest live/alert region that ALSO carries error vocabulary --
    requiring BOTH rules out a persistent, unrelated `aria-live` region (e.g. a
    connection-status badge) ever being mistaken for the validation error.

    Falls back to ANY visible vocabulary-matching text (excluding a form
    field's own `<label>`, see ``_is_label_text``, AND excluding anything
    already visible before submission, see ``_visible_vocab_texts``) when no
    ARIA-marked region exists: AC-9/AC-10 require the rejection to be
    SURFACED, not specifically ARIA-announced, so a candidate rendering a
    plain, non-ARIA error element still passes. The vocabulary requirement
    alone is not enough to rule out a PRE-EXISTING vocabulary-matching element
    (an ordinary field hint that happens to name "deadline"/"window"); only a
    NEWLY-appeared match can honestly be attributed to the submission."""
    deadline = time.monotonic() + timeout_ms / 1000.0
    while True:
        for make in _vocab_candidates(page):
            loc = make()
            n = count(loc)
            for idx in range(n):
                cand = loc.nth(idx)
                try:
                    if not cand.is_visible():
                        continue
                    text = cand.inner_text().strip()
                except Exception:
                    continue
                if text in exclude_texts:
                    continue
                if _is_label_text(cand):
                    continue
                return cand
        if time.monotonic() >= deadline:
            return None
        page.wait_for_timeout(150)


def _run(engine) -> UiReport:
    seed = seed_requester_ready(
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

        # Some apps land a requester directly on the create form. In that
        # shape, "Create request" is the destructive SUBMIT action, not a nav
        # affordance; clicking it here would create a valid default request
        # before this scenario installs its inverted window. Navigate only
        # when no sample-type form field is already visible.
        if field_by_visible_label(page, _SAMPLE_TYPE_FIELD) is None:
            for nav in _REQUESTS_NAV:
                if engine.navigate(page, nav, timeout_ms=2500):
                    settle(page)
                    break

        panel = _create_panel(page)

        filled_sample_type = _fill_if_present(
            panel, _SAMPLE_TYPE_FIELD, seed.evidence.get("sampleTypeId", "")
        )
        filled_earliest = _fill_if_present(
            panel, _EARLIEST_FIELD, seed.evidence.get("earliestCollectionAt", "")
        )
        filled_latest = _fill_if_present(
            panel, _LATEST_FIELD, seed.evidence.get("latestCollectionAt", "")
        )

        before_texts = _visible_vocab_texts(page)

        submit = panel.get_by_role("button", name=_SUBMIT_NAME)
        submitted = False
        if count(submit) > 0:
            try:
                submit.first.click()
                submitted = True
            except Exception:
                submitted = False

        region = _error_region(page, 4000, exclude_texts=before_texts) if submitted else None
        error_text = ""
        if region is not None:
            try:
                error_text = region.inner_text()
            except Exception:
                error_text = ""

        positive = UiVerdict(
            # Credit only when the browser actually constructed the same
            # inverted-window body the HTTP seed proved invalid. A generic
            # error after submitting a partially-filled/default form does not
            # verify AC-9/AC-10 validation surfacing.
            ok=(
                filled_sample_type
                and filled_earliest
                and filled_latest
                and submitted
                and region is not None
            ),
            detail=(
                f"filled sampleType={filled_sample_type}, earliestCollection={filled_earliest}, "
                f"latestCollection={filled_latest}; submitted={submitted}; "
                f"validation error surfaced={region is not None} text={error_text!r} "
                f"(seeded invalidCode={seed.evidence.get('invalidCode')})"
            ),
            evidence={"seed": seed.evidence, "errorText": error_text},
        )
    finally:
        ctx.close()

    return UiReport(positive=positive, notes="rejected create-request submission is surfaced to the user")


CAPABILITY = UiCapability(
    id="request_form_surfaces_validation_error",
    ac="AC-9/AC-10",
    description="Submitting a contract-invalid collection window on the create-request form surfaces a visible error.",
    run=_run,
    priority=True,
)
