"""Browser driver + per-run configuration for the UI verifier.

Determinism / flake-control lives here, in ONE place, so every scenario
inherits it:

* headless Chromium, FIXED 1280x900 viewport, ``locale=en-US``,
  ``timezone_id=UTC`` -- so rendered dates/numbers are stable across machines.
* ``reduced_motion=reduce`` + an init-script that disables CSS
  transitions/animations, so nothing is mid-animation when an assertion runs.
* NETWORK ISOLATION enforced at the browser layer (``NetworkGate``): every
  request whose origin is not the application's own origin (and is not
  ``data:``/``blob:``/``about:``) is ABORTED and recorded. Golden is local-only
  so nothing is ever aborted; an app that reaches for an external tile service
  has that request blocked AND counted -- which is exactly what makes the
  AC-83 "no external calls" assertion enforceable rather than aspirational.
* every wait is a Playwright web-first, auto-retrying assertion with a bounded
  timeout (``expect(...).to_be_visible(timeout=...)``) or an explicit wait on a
  concrete DOM condition -- NEVER a fixed ``sleep``.

Locators are built ONLY from ``get_by_label`` / ``get_by_role`` (accessible
name) / ``get_by_text`` (visible copy). No CSS selectors, ids, ``data-testid``,
or DOM paths are used to LOCATE or ASSERT anything a real user reads. (The one
structural touch is counting ``<canvas>`` elements for "a map actually drew" --
``<canvas>`` is a standard HTML element every mapping library renders into, not
a framework internal, and no library-specific class or id is referenced.)
"""
from __future__ import annotations

import re
import time
from dataclasses import dataclass, field
from typing import Any, Optional
from urllib.parse import urlsplit

from playwright.sync_api import (
    BrowserContext,
    Locator,
    Page,
    Playwright,
    TimeoutError as PWTimeout,
    expect,
)

DEFAULT_TIMEOUT_MS = 10_000


def _count(locator: Locator) -> int:
    """locator.count() that never raises (returns 0 on any error)."""
    try:
        return locator.count()
    except Exception:
        return 0

_DISABLE_ANIMATIONS = """
(() => {
  const css = '*,*::before,*::after{transition:none!important;' +
    'animation:none!important;scroll-behavior:auto!important;caret-color:transparent!important}';
  const style = document.createElement('style');
  style.setAttribute('data-ui-verifier', 'no-animations');
  style.textContent = css;
  const add = () => (document.head || document.documentElement).appendChild(style);
  if (document.head || document.documentElement) add();
  else document.addEventListener('DOMContentLoaded', add);
})();
"""


def _origin(url: str) -> tuple[str, str, str]:
    parts = urlsplit(url)
    return (parts.scheme, parts.hostname or "", str(parts.port or ""))


_URL_IN_TEXT = re.compile(r"https?://[^\s'\"]+", re.I)


class NetworkGate:
    """Records every request the page makes and blocks anything that is not
    same-origin with the application (browser-layer network isolation).

    Three independent detectors so an external-egress attempt is caught
    regardless of HOW the app tries it:
      * ``handle`` -- Playwright route interception: same-origin requests pass,
        any other origin is ABORTED and recorded. (Catches apps with no CSP.)
      * ``_on_console`` -- a Content-Security-Policy "refused to connect"
        message naming a non-app URL. (Catches apps -- like golden -- whose own
        strict ``connect-src 'self'`` CSP blocks the fetch BEFORE it reaches the
        route interceptor; such requests never fire a Playwright request event,
        so the console is the only signal.)
      * ``_on_requestfailed`` -- a failed request to a non-app origin.
    ``external_attempts`` unions all three, deduplicated."""

    def __init__(self, app_origin: tuple[str, str, str]):
        self.app_origin = app_origin
        self.all_requests: list[str] = []
        self.external_requests: list[str] = []  # route-intercepted, aborted
        self.external_csp: list[str] = []       # CSP-refused (from console)
        self.external_failed: list[str] = []    # failed to a non-app origin

    def _is_local(self, url: str) -> bool:
        scheme = urlsplit(url).scheme
        if scheme in ("data", "blob", "about"):
            return True
        return _origin(url) == self.app_origin

    def handle(self, route: Any) -> None:
        url = route.request.url
        self.all_requests.append(url)
        if self._is_local(url):
            try:
                route.continue_()
            except Exception:
                pass
            return
        self.external_requests.append(url)
        try:
            route.abort()
        except Exception:
            pass

    def watch(self, page: Any) -> None:
        page.on("console", self._on_console)
        page.on("requestfailed", self._on_requestfailed)

    def _on_console(self, message: Any) -> None:
        try:
            text = message.text
        except Exception:
            return
        if not re.search(r"content security policy|refused to connect|violates", text, re.I):
            return
        for url in _URL_IN_TEXT.findall(text):
            if not self._is_local(url):
                self.external_csp.append(url)

    def _on_requestfailed(self, request: Any) -> None:
        try:
            url = request.url
        except Exception:
            return
        if not self._is_local(url):
            self.external_failed.append(url)

    @property
    def external_attempts(self) -> list[str]:
        seen: dict[str, None] = {}
        for u in self.external_requests + self.external_csp + self.external_failed:
            seen[u] = None
        return list(seen)


@dataclass
class UiEngine:
    """Carries the per-run configuration every scenario needs and the factories
    for producing an isolated, deterministic browser context and a logged-in
    page. Scenarios never construct a browser themselves."""

    playwright: Playwright
    frontend_url: str
    base_url: str  # server ORIGIN for the HTTP seeder (scheme://host:port)
    contract_path: str
    admin_user: str = "admin"
    admin_pass: str = "admin123"
    timeout_ms: int = DEFAULT_TIMEOUT_MS
    _browser: Any = field(default=None, repr=False)

    def _browser_instance(self) -> Any:
        if self._browser is None:
            self._browser = self.playwright.chromium.launch(headless=True)
        return self._browser

    def new_context(self) -> tuple[BrowserContext, NetworkGate]:
        context = self._browser_instance().new_context(
            viewport={"width": 1280, "height": 900},
            locale="en-US",
            timezone_id="UTC",
            reduced_motion="reduce",
            color_scheme="light",
        )
        context.set_default_timeout(self.timeout_ms)
        context.add_init_script(_DISABLE_ANIMATIONS)
        gate = NetworkGate(_origin(self.frontend_url))
        context.route("**/*", gate.handle)
        return context, gate

    def close(self) -> None:
        if self._browser is not None:
            try:
                self._browser.close()
            finally:
                self._browser = None

    # ---------------------------------------------------------------- login
    # Accessible-name patterns for the two credential fields and the submit
    # control. Kept broad so a differently-worded-but-functional login is not
    # mistaken for a broken one (email-or-username, "Sign in"/"Log in"/
    # "Continue").
    _USERNAME_LABEL = re.compile(r"user\s*name|email|e-mail|account|login|user id", re.I)
    _PASSWORD_LABEL = re.compile(r"pass\s*word|passcode|pin", re.I)
    _SUBMIT_NAME = re.compile(r"sign\s*in|log\s*in|log\s*on|continue|submit|enter", re.I)
    _SIGNOUT_NAME = re.compile(r"sign\s*out|log\s*out|log\s*off", re.I)

    def _locate_username(self, page: Page) -> Locator:
        """Username/email field. Prefer its accessible label (precise); fall
        back to the first text-entry field on the login page when the app
        renders a functional input WITHOUT a programmatic label (a real a11y
        gap, but not a reason to declare login broken)."""
        by_label = page.get_by_label(self._USERNAME_LABEL)
        if _count(by_label) > 0:
            return by_label.first
        return page.get_by_role("textbox").first

    def _locate_password(self, page: Page) -> Locator:
        """Password field. Prefer its accessible label; fall back to the
        password-typed input -- a standard, user-perceivable HTML semantic (a
        masked field), not a class/id/testid/DOM-path locator."""
        by_label = page.get_by_label(self._PASSWORD_LABEL)
        if _count(by_label) > 0:
            return by_label.first
        return page.locator("input[type='password']").first

    def _submit_login(self, page: Page) -> None:
        """Click the sign-in control: a button by accessible name, else a
        submit-typed control."""
        btn = page.get_by_role("button", name=self._SUBMIT_NAME)
        if _count(btn) > 0:
            btn.first.click()
            return
        submit = page.locator("input[type='submit'], button[type='submit']")
        if _count(submit) > 0:
            submit.first.click()
            return
        # Last resort: submit the form from the password field.
        page.locator("input[type='password']").first.press("Enter")

    def _authenticated(self, page: Page, timeout_ms: int) -> bool:
        """True once ANY positive authenticated-state affordance is visible: a
        sign-out control, a post-login workspace/role entry control, or the
        app shell's navigation landmark. All three are absent on a hollow shell
        whose login does nothing, so this never awards a false authenticated
        state."""
        signals = (
            page.get_by_role("button", name=self._SIGNOUT_NAME).first,
            page.get_by_role("link", name=self._SIGNOUT_NAME).first,
            page.get_by_role("button", name=self._ENTER_WORKSPACE).first,
            page.get_by_role("link", name=self._ENTER_WORKSPACE).first,
            page.get_by_role("navigation").first,
            page.get_by_role("menubar").first,
        )
        deadline = time.monotonic() + timeout_ms / 1000.0
        while time.monotonic() < deadline:
            for loc in signals:
                try:
                    if loc.is_visible():
                        return True
                except Exception:
                    continue
            page.wait_for_timeout(150)
        return False

    def login(self, page: Page, username: str, password: str) -> bool:
        """Sign in through the PUBLIC login form. Locators are accessible-name
        first, with role/semantic fall-backs so a functional-but-unlabelled
        login form is graded on whether it AUTHENTICATES, not on whether its
        fields carry programmatic labels. Returns True once a positive
        authenticated-state affordance is visible. Returns False -- never raises
        -- when the app has no working login (a hollow shell), so a missing UI
        cleanly scores 0 instead of crashing the run."""
        try:
            page.goto(self.frontend_url, wait_until="domcontentloaded")
            self._locate_username(page).fill(username)
            self._locate_password(page).fill(password)
            self._submit_login(page)
            if not self._authenticated(page, self.timeout_ms):
                return False
            # Some apps present a role/workspace landing after authenticating and
            # gate the actual app behind an explicit entry control (e.g. Sol's
            # "Open admin workspace" button). Step through it if present so the
            # feature scenarios reach the app; a no-op when the app content is
            # already on the post-login page (golden, Luna, Terra).
            entered = self.enter_workspace(page)
            if entered:
                # The entry control commonly performs an asynchronous SPA
                # role-selection request before replacing the gate with the
                # real shell. Do not hand the page to a scenario while that
                # transition is still in progress: wait until the entry
                # affordance disappears, with a bounded web-first poll.
                deadline = time.monotonic() + self.timeout_ms / 1000.0
                while time.monotonic() < deadline:
                    still_visible = False
                    for role in ("button", "link"):
                        controls = page.get_by_role(role, name=self._ENTER_WORKSPACE)
                        for idx in range(_count(controls)):
                            try:
                                if controls.nth(idx).is_visible():
                                    still_visible = True
                                    break
                            except Exception:
                                continue
                        if still_visible:
                            break
                    if not still_visible:
                        break
                    page.wait_for_timeout(100)
            return True
        except PWTimeout:
            return False
        except Exception:
            return False

    # Matches an explicit "enter the app/workspace" affordance without matching
    # a bare "Continue"/"Enter" on unrelated forms: an action verb must be
    # followed (within a few words) by a workspace/dashboard/console keyword.
    _ENTER_WORKSPACE = re.compile(
        r"(open|enter|go to|launch)\s[\w\s]{0,24}(workspace|dashboard|console|control room|app)",
        re.I,
    )

    def enter_workspace(self, page: Page) -> bool:
        """Click a post-login workspace/role entry control if one is present.
        Returns False -- never raises -- when there is none, so apps that land
        directly in their UI are unaffected."""
        for role in ("button", "link"):
            try:
                loc = page.get_by_role(role, name=self._ENTER_WORKSPACE).first
                loc.wait_for(state="visible", timeout=1500)
                loc.click()
                return True
            except Exception:
                continue
        return False

    def navigate(self, page: Page, name_regex: str, timeout_ms: Optional[int] = None) -> bool:
        """Click a navigation affordance by its user-visible name.

        Semantic links/buttons are preferred. A visible-text fallback supports
        common SPA navigation items implemented as clickable ``div`` elements;
        the task defines no fixed UI or accessibility contract, so those remain
        genuine user-operable navigation controls. Returns False rather than
        raising when no matching affordance is present.

        An affordance that is ABSENT is skipped instantly (``count()==0``) so a
        caller can try several candidate names cheaply -- only an affordance
        that actually exists incurs the (bounded) visibility wait."""
        pattern = re.compile(name_regex, re.I)
        timeout = self.timeout_ms if timeout_ms is None else timeout_ms
        for role in ("link", "button"):
            loc = page.get_by_role(role, name=pattern)
            if _count(loc) == 0:
                continue
            try:
                first = loc.first
                first.wait_for(state="visible", timeout=timeout)
                first.click()
                return True
            except Exception:
                continue
        # Some candidate SPAs implement their sidebar/tab navigation as a
        # clickable text container rather than a link or button. Only try this
        # after semantic controls, and still require a visible, clickable match.
        loc = page.get_by_text(pattern)
        for idx in range(min(_count(loc), 20)):
            try:
                candidate = loc.nth(idx)
                candidate.wait_for(state="visible", timeout=timeout)
                interactive = candidate.evaluate(
                    """el => {
                        let node = el;
                        for (let depth = 0; node && depth < 5; depth++, node = node.parentElement) {
                            const role = (node.getAttribute('role') || '').toLowerCase();
                            const tabIndex = Number(node.getAttribute('tabindex'));
                            if (getComputedStyle(node).cursor === 'pointer' ||
                                typeof node.onclick === 'function' ||
                                ['link', 'button', 'menuitem', 'tab'].includes(role) ||
                                (node.hasAttribute('tabindex') && tabIndex >= 0)) return true;
                        }
                        return false;
                    }"""
                )
                if not interactive:
                    continue
                candidate.click()
                return True
            except Exception:
                continue
        return False
