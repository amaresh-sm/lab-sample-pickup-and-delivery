"""Implicit session-lifecycle requirement (no single instruction.md line -- a
UI that authenticates a user must also let them END that session) -- a user
can sign out, and doing so genuinely returns the app to an unauthenticated
state, not merely a decorative click.

Presentation-layer, framework-agnostic:
  * no HTTP seed is needed -- ANY authenticated user exercises this, so we sign
    in as the admin account the harness already carries (the same
    no-seed-needed pattern ``map_renders_local_tiles_only`` (AC-83) uses);
  * POSITIVE: after login, click the sign-out control -- located by the SAME
    broad accessible-name vocabulary ``driver.py``'s own login helper already
    uses to detect an authenticated state (``sign out`` / ``log out`` /
    ``log off``) -- and confirm the app genuinely left the authenticated state:
    the PUBLIC LOGIN FORM's PASSWORD field (or a fallback `input[type=
    'password']`, the same universal HTML-spec-level signal ``UiEngine`` itself
    falls back to) is visible again, together with EITHER a labelled username
    field or the sign-in SUBMIT control. This is the symmetric inverse of how
    ``UiEngine.login`` finds/fills those fields to sign IN, so it is
    unambiguous and durable (a static form re-render, not a toast that can
    auto-dismiss before the assertion runs) and does not depend on any
    URL/route convention, which varies across SPA routers.

    Deliberately NOT used: falling back to "the first textbox on the page" for
    the username field (the way ``UiEngine._locate_username`` does when
    SIGNING IN, where the only textboxes present on a bare login page are its
    own). Reused here to detect being SIGNED OUT, that fallback would false-
    positive on a still-authenticated workspace that simply has SOME OTHER
    textbox visible (confirmed against golden itself: its Administration page
    renders its own `input[type="password"]`, and its workspace has several
    unrelated textboxes) -- exactly the kind of non-owner/no-op mutant this
    scenario exists to catch. The username fallback here is instead the
    sign-in SUBMIT control's accessible name, which is login-page-specific.

No control/negative dimension: "sign-out actually signs out" is one coherent
property with no second actor whose visibility should differ (mirrors
``role_work_view_shows_current_and_past`` (AC-3), also positive-only).
"""
from __future__ import annotations

import re

from model import UiCapability, UiReport, UiVerdict
from tracking import count
from helpers import appears_within

# Mirrors driver.py's UiEngine._SIGNOUT_NAME -- kept local so this scenario does
# not depend on a driver internal, per the module's own "mirror the pattern"
# convention.
_SIGNOUT_NAME = re.compile(r"sign\s*out|log\s*out|log\s*off", re.I)
# Mirrors driver.py's UiEngine._USERNAME_LABEL / _PASSWORD_LABEL / _SUBMIT_NAME.
_USERNAME_LABEL = re.compile(r"user\s*name|email|e-mail|account|login|user id", re.I)
_PASSWORD_LABEL = re.compile(r"pass\s*word|passcode|pin", re.I)
_SUBMIT_NAME = re.compile(r"sign\s*in|log\s*in|log\s*on|continue|submit|enter", re.I)


def _signout_control(page):
    for role in ("button", "link"):
        loc = page.get_by_role(role, name=_SIGNOUT_NAME)
        if count(loc) > 0:
            return loc.first
    return None


def _password_field(page):
    """The login form's password field. Prefers its accessible label; falls
    back to the password-typed input -- a standard, user-perceivable HTML
    semantic (a masked field), mirroring UiEngine._locate_password. A raw
    `input[type=password]` is a strong, login-page-specific signal: it is not
    something an authenticated workspace renders incidentally."""
    by_label = page.get_by_label(_PASSWORD_LABEL)
    if count(by_label) > 0:
        return by_label.first
    return page.locator("input[type='password']").first


def _username_signal(page):
    """The login form's username field OR its sign-in submit control.
    Deliberately does NOT fall back to "the first textbox on the page" (the
    way UiEngine._locate_username does to SIGN IN, safe there because a bare
    login page has no other textboxes) -- reused to detect being signed OUT,
    that fallback would false-positive on a still-authenticated workspace that
    has some unrelated textbox visible. The sign-in submit control's
    accessible name is login-page-specific and carries no such risk."""
    by_label = page.get_by_label(_USERNAME_LABEL)
    if count(by_label) > 0:
        return by_label.first
    for role in ("button", "link"):
        submit = page.get_by_role(role, name=_SUBMIT_NAME)
        if count(submit) > 0:
            return submit.first
    return page.get_by_label(_USERNAME_LABEL).first  # resolves to an empty/absent locator


def _run(engine) -> UiReport:
    tmo = engine.timeout_ms
    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, engine.admin_user, engine.admin_pass):
            return UiReport(
                positive=UiVerdict(False, "admin could not sign in through the public login form"),
                notes="login failure",
            )

        control = _signout_control(page)
        if control is None:
            return UiReport(
                positive=UiVerdict(False, "no sign-out control (button/link) found after login"),
                notes="sign-out control absent",
            )
        try:
            control.click()
        except Exception as exc:
            return UiReport(
                positive=UiVerdict(False, f"sign-out control click raised {exc!r}"),
                notes="sign-out click failed",
            )

        # Symmetric to UiEngine.login(): the public login form's own fields
        # reappearing is the clearest, durable "no longer authenticated" signal
        # -- a static form re-render, never a transient message.
        password_back = appears_within(_password_field(page), tmo)
        username_back = appears_within(_username_signal(page), tmo)
        signout_control_gone = not appears_within(
            page.get_by_role("button", name=_SIGNOUT_NAME).or_(page.get_by_role("link", name=_SIGNOUT_NAME)),
            1000,
        )

        positive = UiVerdict(
            ok=password_back and username_back,
            detail=(
                f"after sign-out click: login password field visible={password_back}, "
                f"username field/sign-in control visible={username_back}, "
                f"sign-out control gone={signout_control_gone} (informational)"
            ),
        )
    finally:
        ctx.close()

    return UiReport(positive=positive, notes="sign-out ends the authenticated session")


CAPABILITY = UiCapability(
    id="sign_out_ends_authenticated_session",
    ac="session-lifecycle (implicit)",
    description="Clicking sign-out genuinely returns the app to the public, unauthenticated login form.",
    run=_run,
    priority=False,
)
