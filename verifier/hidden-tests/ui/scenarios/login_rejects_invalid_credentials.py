"""Implicit authentication requirement (no dedicated ACCEPTANCE.md line --
mirrors ``sign_out_ends_authenticated_session``'s "session-lifecycle
(implicit)"): a login form must REJECT invalid credentials, both (a) not
authenticating the browser, and (b) telling the user something went wrong --
a UI that silently does nothing on a bad password is just as broken as one
that logs the user in anyway.

Presentation-layer, framework-agnostic, built directly on ``driver.py``'s own
login machinery rather than duplicating it:
  * no HTTP state is seeded; instead ``seed.confirm_bad_login_refused``
    confirms, over HTTP via a fresh anonymous session and the pinned
    contract's own ``authenticate_user`` operation, that a FABRICATED
    username/password pair (never created by anything) really is refused by
    THIS candidate's backend before the browser ever opens -- so the UI
    submission is guaranteed to hit a genuine rejection, never a hypothetical
    one, and no real account is ever touched;
  * POSITIVE (two parts, both required): after submitting those exact bogus
    credentials through the PUBLIC login form (the same
    ``_locate_username``/``_locate_password``/``_submit_login`` pieces
    ``UiEngine.login()`` itself uses), (1) ``UiEngine._authenticated()``
    -- the SAME broad sign-out-control/workspace/nav-landmark signal the
    driver already trusts for a successful login -- must NOT become true
    (bounded, short window: a failed login must not eventually "catch up" and
    authenticate), and (2) a visible feedback element carrying error
    vocabulary (invalid/incorrect/failed/wrong/error/denied/credential)
    appears within a bounded window. Preferred as an alert/status/
    `[aria-live]` region when present (broad enough to catch a differently-
    worded correct app), but falls back to ANY visible vocabulary-matching
    text when no such region exists -- requirement (b) is "the user is told
    something went wrong," not "the message carries a specific ARIA role,"
    so a candidate that renders a plain, non-ARIA-marked error element still
    passes. Requiring the vocabulary (in both the ARIA-preferred path and the
    plain-text fallback) is what rules out golden's own login-error paragraph
    (``role="alert"``, but ALWAYS present in the DOM, empty or not -- see
    below) or an unrelated persistent live region (e.g. a connection-status
    badge) ever being mistaken for genuine feedback: an empty or off-topic
    element never carries the vocabulary, so it never matches either path.

Read from golden's own ``LoginPage.vue`` before relying on this: its error
paragraph (`<p role="alert">{{ error }}</p>`) has NO ``v-if`` -- it is always
in the DOM, with `min-height: 21px` in `styles.css` so it never collapses to
zero size even when empty. That means "is the alert region visible" is
ALWAYS true on golden, whether or not a login failed -- exactly the kind of
false-positive this scenario's own author must NOT walk into: the assertion
here is on the region containing ERROR TEXT after submission, never merely on
the region's bare existence/visibility.
"""
from __future__ import annotations

import re
import time

from model import UiCapability, UiReport, UiVerdict
from seed import confirm_bad_login_refused
from tracking import count

_ERROR_VOCAB = re.compile(
    r"\binvalid|\bincorrect|\bfailed|\bwrong|\berror|\bdenied|\bcredential|\bunauthoriz|\bunable",
    re.I,
)


def _is_label_text(loc) -> bool:
    """True iff the matched element IS a `<label>`, sits inside one, or
    CONTAINS one -- mirrors ``request_form_validation``'s own guard (checking
    only ancestors misses a matched WRAPPING element whose innerText just
    happens to equal a `<label>` descendant's, e.g. `<div><label>Username</
    label><input></div>` matched as a whole by `get_by_text`). A genuine error
    announcement is never rendered AS, or built AROUND, a form control's
    label, so a stray vocabulary-word collision with an ordinary field label
    (e.g. a field literally named "Failed attempts") must not be mistaken for
    feedback."""
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
    """Snapshot every CURRENTLY visible vocabulary-matching text, normalized
    -- mirrors ``request_form_validation``'s own guard. Used to exclude
    pre-existing page furniture (e.g. a field literally named "Failed
    attempts", or unrelated copy that happens to contain "error") from the
    post-submit match: a vocabulary word that was ALREADY visible before
    submission cannot honestly be attributed to the submission."""
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


def _feedback_region(page, timeout_ms: int, exclude_texts: set[str] = frozenset()):
    """The smallest alert/status/live region that ALSO carries error
    vocabulary -- requiring BOTH (mirrors ``request_form_validation``'s own
    ``_error_region``) rules out a persistent, unrelated `aria-live` badge, and
    rules out golden's own always-present-but-possibly-empty login-error
    paragraph being matched before it actually carries text.

    Falls back to ANY visible text carrying error vocabulary (excluding a form
    field's own `<label>`, see ``_is_label_text``, AND excluding anything
    already visible before submission, see ``_visible_vocab_texts``) when no
    ARIA-marked region matches: a candidate is free to render its error in a
    plain element with no `role`/`aria-live` and still satisfy (b) ("telling
    the user something went wrong") -- that requirement is about visible
    feedback existing, not about a specific accessibility API being wired up.
    The vocabulary filter alone already rules out golden's always-present
    empty paragraph (it has no text to match while empty); the before/after
    exclusion additionally rules out a pre-existing, vocabulary-coincidental
    element (this scenario has no create-request form's static hints to worry
    about, but the guard is kept for the same reason and cheap to apply)."""
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
    seed = confirm_bad_login_refused(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        try:
            page.goto(engine.frontend_url, wait_until="domcontentloaded")
            before_texts = _visible_vocab_texts(page)
            engine._locate_username(page).fill(seed.owner_username)
            engine._locate_password(page).fill(seed.owner_password)
            engine._submit_login(page)
        except Exception as exc:
            return UiReport(
                positive=UiVerdict(False, f"could not reach/submit the public login form ({exc!r})"),
                notes="login form unreachable",
            )

        # (1) must NOT become authenticated -- bounded, short window; a login
        # that eventually "succeeds" after a delay is just as wrong as one
        # that succeeds immediately.
        not_authenticated = not engine._authenticated(page, 2500)

        # (2) visible error feedback carrying error vocabulary, polled
        # immediately (never a fixed sleep) so a transient toast is still
        # caught before it disappears.
        region = _feedback_region(page, 4000, exclude_texts=before_texts)
        feedback_text = ""
        if region is not None:
            try:
                feedback_text = region.inner_text()
            except Exception:
                feedback_text = ""
        feedback_shown = region is not None

        positive = UiVerdict(
            ok=not_authenticated and feedback_shown,
            detail=(
                f"invalid credentials submitted: authenticated-signal-absent={not_authenticated}, "
                f"visible-error-feedback={feedback_shown} text={feedback_text!r} "
                f"(seeded HTTP status={seed.evidence.get('status')}, code={seed.evidence.get('code')!r})"
            ),
            evidence={"seed": seed.evidence, "feedbackText": feedback_text},
        )
    finally:
        ctx.close()

    return UiReport(positive=positive, notes="login rejects invalid credentials (not authenticated + visible feedback)")


CAPABILITY = UiCapability(
    id="login_rejects_invalid_credentials",
    ac="authentication (implicit)",
    description="Submitting fabricated credentials on the login form neither authenticates nor stays silent.",
    run=_run,
    priority=True,
)
