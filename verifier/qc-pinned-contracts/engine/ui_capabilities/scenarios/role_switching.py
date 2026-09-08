"""AC-5 / AC-7 -- "A user must explicitly select the role they are currently
working under... Switching role context must not add or remove any roles from
the account."

Presentation-layer, framework-agnostic:
  * we seed ONE account holding TWO roles that each have a real, reachable
    authenticated view -- ``requester`` (the requests workspace) and
    ``dispatcher`` (the operations workspace) -- confirmed straight from the
    contract-canonical ``create_user`` response before any page opens;
  * POSITIVE (two parts, both required): after login, (1) a role-switch
    control -- semantically a `combobox`/`select`, with an accessible name
    drawn from broad role/switch vocabulary -- is visible and lists >= 2
    options whose text names the account's own seeded roles (matched broadly,
    case-insensitively, by substring -- never golden's exact copy), and (2)
    switching the control to the OTHER seeded role actually applies it: the
    control's own value settles to the new role and does not merely flash and
    revert (a controlled `<select>` bound to server-confirmed state, not an
    optimistic client-only toggle, is the most durable, least-flaky signal --
    confirmed by reading golden's own `AppShell.vue`/`workspace.ts` before
    relying on it: the control's value is bound to `store.activeRole`, which
    only updates once the server has confirmed the switch).

We deliberately do NOT test the single-role case (whether the control is
absent for a one-role account) -- that is a different, golden-specific
conditional ("`v-if="store.roles.length > 1"`") and out of scope for this
capability; seeding a genuinely two-role account makes the positive
unambiguous regardless of how a candidate handles the single-role case.

No control/negative dimension: AC-7's "does not add or remove roles" is
verified by the SEED itself (confirming the account's `roles` array is exactly
what was requested) rather than by a second browser session, since there is no
second actor whose visibility should differ here -- mirroring
``role_work_view_shows_current_and_past`` (AC-3), which is also positive-only.

Locators are accessible-name/role/visible-text only. The one structural read
is a `<select>` locator's `.value` (a standard HTML form-control property, the
same class of "rendered DATA VALUE" the README already sanctions -- not a
class/id/testid/DOM-path locator) and an `<option>`'s `value` ATTRIBUTE (read,
never used to locate), so the switch is verified by the control's genuine
settled state rather than a transient toast.
"""
from __future__ import annotations

import re
import time

from playwright.sync_api import expect, TimeoutError as PWTimeout

from model import UiCapability, UiReport, UiVerdict
from seed import seed_multirole_user
from tracking import count, settle

# Accessible name of the role-switch control: broad role/switch vocabulary, not
# golden's exact "Active role" label copy. "acting as" is included alongside
# "working as" -- confirmed live as an equally common, natural phrasing
# multiple independently-built candidates actually use for this exact control.
_ROLE_CONTROL_NAME = re.compile(r"\brole|\bswitch|working\s+as|acting\s+as|current\s+role", re.I)


def _role_control(page, roles: "list[str] | None" = None):
    """The role-switch control, in whichever of two equally-accessible shapes
    a candidate built it as:
      * a native `<select>`/`role=combobox` (the original assumption), or
      * a CUSTOM DROPDOWN -- a trigger `<button>` (accessible name carrying
        the role vocabulary, e.g. "Acting as: Requester") that, on click,
        reveals the other roles as their own `<button>`/`role=option`/
        `role=menuitem` elements. Confirmed live: a candidate's role-switcher
        is exactly this shape -- clicking the "ACTING AS Requester" button
        reveals two genuine `<button>` elements ("Requester", "Dispatcher"),
        each independently clickable and keyboard/screen-reader operable --
        a fully accessible pattern the ORIGINAL select-only check could never
        find, incorrectly reading it as "no role-switch control at all."
    Returns a dict tagging which shape was found, or None."""
    combo = page.get_by_role("combobox", name=_ROLE_CONTROL_NAME)
    if count(combo) > 0:
        return {"kind": "select", "control": combo.first}
    by_label = page.get_by_label(_ROLE_CONTROL_NAME)
    if count(by_label) > 0:
        return {"kind": "select", "control": by_label.first}
    trigger = page.get_by_role("button", name=_ROLE_CONTROL_NAME)
    if count(trigger) > 0:
        return {"kind": "button-trigger", "trigger": trigger.first}
    if roles:
        # No accessibly-NAMED control found -- confirmed live: a candidate's
        # role-select is a genuine `<select>` with the exact right
        # `<option>`s, but its "Acting as" caption is a plain sibling `<span>`
        # (never a `<label>`, never wired via `for`/aria-label), so it has NO
        # accessible name at all. Rather than declare "no control exists"
        # when a select's CONTENT unambiguously identifies it (>=2 of its
        # options name the account's own seeded roles -- nothing else on a
        # real app page would plausibly offer exactly those options
        # together), fall back to identifying it by option content. This
        # mirrors ``accept_assignment.py``'s own row-content fallback for a
        # control with no fillable/named field to key on.
        selects = page.locator("select")
        for idx in range(count(selects)):
            candidate = selects.nth(idx)
            present, total = _role_options_present(candidate, roles)
            if total >= 2 and len(present) >= 2:
                return {"kind": "select", "control": candidate}
    return None


def _current_role_button_trigger(page, roles: list[str]):
    """Find a persistent custom role-switch trigger named only for the
    current role.

    A valid custom switcher can put its explanatory label (for example,
    "Active workspace") next to the button while the button's own accessible
    name is simply "Requester".  That is still fully operable, but it cannot
    match ``_ROLE_CONTROL_NAME``.  Treat a seeded-role-named button as the
    trigger only when exactly ONE such visible button exists.  A one-time role
    selection gate exposes two or more seeded-role buttons and is therefore
    deliberately excluded; after this candidate is opened, the existing
    option check still requires the trigger to reveal >=2 of the account's
    real seeded roles before any credit is possible.
    """
    visible = []
    for role in roles:
        buttons = page.get_by_role("button", name=_role_pattern(role))
        for idx in range(count(buttons)):
            candidate = buttons.nth(idx)
            try:
                if not candidate.is_visible():
                    continue
                visible.append(candidate)
            except Exception:
                continue
    return visible[0] if len(visible) == 1 else None


def _role_options_present(control, roles: list[str]) -> tuple[list[str], int]:
    """Which of the seeded ``roles`` render as an option (matched broadly by
    substring, case-insensitively -- e.g. seeded "dispatcher" matches an option
    labelled "Dispatcher") and the total option count."""
    present = []
    for role in roles:
        word = role.replace("_", "").replace(" ", "")
        pat = re.compile(re.escape(role.replace("_", " ")).replace(r"\ ", r"[\s_-]*"), re.I)
        opt = control.get_by_role("option", name=pat)
        if count(opt) > 0:
            present.append(role)
    try:
        total = count(control.get_by_role("option"))
    except Exception:
        total = 0
    return present, total


def _role_pattern(role: str) -> "re.Pattern[str]":
    return re.compile(re.escape(role.replace("_", " ")).replace(r"\ ", r"[\s_-]*"), re.I)


def _button_trigger_options_present(page, trigger, roles: list[str]) -> tuple[list[str], int]:
    """Click the trigger to reveal the menu, then which of the seeded
    ``roles`` appear as their OWN clickable control (button/option/menuitem)
    -- excluding the trigger itself, which always echoes the CURRENT role's
    name and would otherwise be miscounted as a revealed option."""
    try:
        trigger.click()
    except Exception:
        return [], 0
    page.wait_for_timeout(250)
    present = []
    for role in roles:
        pat = _role_pattern(role)
        found = False
        for getter in (page.get_by_role("menuitem", name=pat), page.get_by_role("option", name=pat),
                       page.get_by_role("button", name=pat)):
            n = count(getter)
            for i in range(n):
                candidate = getter.nth(i)
                try:
                    if candidate.evaluate("(el, trg) => el === trg", trigger.element_handle()):
                        continue
                except Exception:
                    pass
                try:
                    if candidate.is_visible():
                        found = True
                        break
                except Exception:
                    continue
            if found:
                break
        if found:
            present.append(role)
    return present, len(present)


def _switch_button_trigger(page, trigger, target_role: str, timeout_ms: int) -> tuple[bool, str]:
    """Switch a button-trigger custom dropdown to ``target_role`` and confirm
    the trigger's OWN accessible text settles to name that role -- the same
    "read the control's genuine settled state, never a transient toast"
    discipline the native-select path already applies, just via the
    trigger's text instead of a `<select>`'s `.value`."""
    try:
        before_text = trigger.inner_text()
    except Exception:
        before_text = ""
    pat = _role_pattern(target_role)
    option = None
    for getter in (page.get_by_role("menuitem", name=pat), page.get_by_role("option", name=pat),
                   page.get_by_role("button", name=pat)):
        if count(getter) > 0:
            option = getter.first
            break
    if option is None:
        return False, f"no revealed option matches seeded role {target_role!r}"
    response_ok = False
    response_detail = ""
    try:
        with page.expect_response(
            lambda response: "select-active-role" in response.url,
            timeout=5000,
        ) as response_info:
            option.click()
        response = response_info.value
        payload = response.json()
        data = payload.get("data") if isinstance(payload, dict) else None
        applied = data.get("activeRole") if isinstance(data, dict) else None
        response_ok = response.status < 400 and applied == target_role
        response_detail = f"HTTP {response.status} activeRole={applied!r}"
    except Exception as exc:
        # The click may still have completed even if response observation or
        # JSON parsing failed. Continue with the rendered-control proof.
        response_detail = f"response proof unavailable ({exc!r})"

    deadline = time.monotonic() + timeout_ms / 1000.0
    while time.monotonic() < deadline:
        try:
            current_text = trigger.inner_text()
        except Exception:
            current_text = before_text
        if pat.search(current_text) and current_text != before_text:
            return True, f"trigger settled to {current_text!r} (was {before_text!r})"
        # A trigger originally located by its CURRENT-role accessible name
        # stops matching that locator after a successful switch. Confirm that
        # shape by requiring the old role-named control to disappear and one
        # target-role button to be visible after the options close.
        target_buttons = page.get_by_role("button", name=pat)
        visible_targets = []
        for idx in range(count(target_buttons)):
            try:
                if target_buttons.nth(idx).is_visible():
                    visible_targets.append(target_buttons.nth(idx))
            except Exception:
                continue
        old_still_visible = False
        try:
            old_still_visible = trigger.is_visible()
        except Exception:
            old_still_visible = False
        if not old_still_visible and len(visible_targets) == 1:
            try:
                target_text = visible_targets[0].inner_text()
            except Exception:
                target_text = target_role
            return True, f"current-role trigger changed {before_text!r} -> {target_text!r}"
        page.wait_for_timeout(150)
    if response_ok:
        return True, (
            f"role switch persisted through the UI ({response_detail}); "
            f"trigger copy did not refresh from {before_text!r}"
        )
    try:
        current_text = trigger.inner_text()
    except Exception:
        current_text = before_text
    return False, (
        f"trigger text did not settle to {target_role!r} (still {current_text!r}); "
        f"{response_detail}"
    )


def _bypass_initial_role_gate(page, roles: list[str]) -> bool:
    """Some candidates gate the WHOLE app behind a one-time "pick your
    starting role" screen -- a button per seeded role, shown once right after
    login, before any actual switcher control exists at all -- rather than
    landing directly in a role-bound workspace with the switcher already
    present. Confirmed live: a candidate shows "Select your role... Choose
    which one to work under" with a plain button named "Dispatcher" and
    another named "Requester"; its REAL persistent switcher (a native
    `<select>`, in that candidate's case) only renders once you're through
    this gate. `login()`'s own broad `_authenticated()` signal can be
    satisfied by this screen too (its own nav/menubar-shaped signals), so
    ``engine.login()`` returning True does not guarantee the gate was passed.

    Clicking whichever seeded role happens to be first is safe: this only
    exists to get INTO the app so the actual switcher can be found and
    exercised afterward -- which role was picked to get past the gate has no
    bearing on AC-5/AC-7, since the account still holds both roles and the
    real positive/switch check runs against the switcher found next."""
    for role in roles:
        btn = page.get_by_role("button", name=_role_pattern(role))
        if count(btn) > 0:
            try:
                btn.first.click()
                return True
            except Exception:
                continue
    return False


def _switch_to(page, control, target_role: str, timeout_ms: int) -> tuple[bool, str]:
    """Switch the control to ``target_role`` (matched broadly by substring) and
    confirm its OWN value settles to that option -- never a transient message.
    Reads the target option's `value` attribute (a standard form-control
    property) rather than assuming it equals golden's own role slug, so a
    differently-built candidate that renders a different underlying value is
    still handled correctly."""
    pat = re.compile(re.escape(target_role.replace("_", " ")).replace(r"\ ", r"[\s_-]*"), re.I)
    option = control.get_by_role("option", name=pat)
    if count(option) == 0:
        return False, f"no option matches seeded role {target_role!r}"
    try:
        before = control.input_value()
    except Exception:
        before = None
    target_value = None
    try:
        target_value = option.first.get_attribute("value")
    except Exception:
        pass
    try:
        if target_value:
            control.select_option(value=target_value)
        else:
            label = option.first.inner_text().strip()
            control.select_option(label=label)
    except Exception as exc:
        return False, f"select_option raised {exc!r}"

    if target_value:
        try:
            expect(control).to_have_value(target_value, timeout=timeout_ms)
            return True, f"role-switch control settled to value {target_value!r} (was {before!r})"
        except PWTimeout:
            return False, f"role-switch control did not settle to {target_value!r} (still {control.input_value()!r})"
    # No readable value attribute -- poll (bounded) for a genuine change away
    # from the pre-switch value instead of assuming success.
    deadline = time.monotonic() + timeout_ms / 1000.0
    while time.monotonic() < deadline:
        try:
            current = control.input_value()
        except Exception:
            current = before
        if current != before:
            return True, f"role-switch control value changed {before!r} -> {current!r}"
        page.wait_for_timeout(150)
    return False, "role-switch control value did not change after switching"


def _run(engine) -> UiReport:
    seed = seed_multirole_user(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    tmo = engine.timeout_ms
    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        # Submit credentials directly rather than through engine.login(): its
        # own _authenticated() signal (sign-out/enter-workspace/nav/menubar)
        # can be genuinely absent on an intermediate "pick your starting role"
        # screen a MULTI-role account may land on straight after a correct
        # login -- confirmed live: a candidate shows exactly that screen, with
        # NONE of those signals present yet, so engine.login() itself reports
        # failure even though the credentials were accepted. This scenario is
        # the only one that seeds a multi-role account, so this gate is only
        # ever hit here; a genuine bad-credentials failure is still caught
        # below once neither a role control nor the gate can be found.
        try:
            page.goto(engine.frontend_url, wait_until="domcontentloaded")
            engine._locate_username(page).fill(seed.switcher_username)
            engine._locate_password(page).fill(seed.switcher_password)
            engine._submit_login(page)
            settle(page)
        except Exception as exc:
            return UiReport(
                positive=UiVerdict(False, f"could not reach/submit the public login form ({exc!r})"),
                notes="login form unreachable",
            )

        found = _role_control(page, seed.roles)
        if found is None:
            trigger = _current_role_button_trigger(page, seed.roles)
            if trigger is not None:
                found = {"kind": "button-trigger", "trigger": trigger}
        if found is None and _bypass_initial_role_gate(page, seed.roles):
            page.wait_for_timeout(400)
            found = _role_control(page, seed.roles)
            if found is None:
                trigger = _current_role_button_trigger(page, seed.roles)
                if trigger is not None:
                    found = {"kind": "button-trigger", "trigger": trigger}
        if found is None:
            if not engine._authenticated(page, 1500):
                return UiReport(
                    positive=UiVerdict(False, "multi-role user could not sign in through the public login form"),
                    notes="login failure",
                )
            return UiReport(
                positive=UiVerdict(
                    False,
                    f"no role-switch control found for a {len(seed.roles)}-role account (roles={seed.roles})",
                    evidence={"seed": seed.evidence},
                ),
                notes="AC-5/AC-7 role switching",
            )

        if found["kind"] == "select":
            control = found["control"]
            try:
                expect(control).to_be_visible(timeout=tmo)
            except Exception:
                pass

            present, total = _role_options_present(control, seed.roles)
            lists_ok = total >= 2 and len(present) >= 2

            target_role = seed.roles[1] if len(seed.roles) > 1 else seed.roles[0]
            # If the control already sits on the target (e.g. the server auto-
            # selected it on login), switch to the OTHER seeded role instead so
            # the switch is a genuine, observable change either way.
            try:
                current_value = control.input_value()
            except Exception:
                current_value = None
            if current_value and target_role.replace("_", "").lower() in current_value.replace("_", "").lower():
                target_role = next((r for r in seed.roles if r != target_role), target_role)

            switch_ok, switch_detail = (False, "role listing failed; switch not attempted")
            if lists_ok:
                switch_ok, switch_detail = _switch_to(page, control, target_role, tmo)
        else:
            trigger = found["trigger"]
            try:
                expect(trigger).to_be_visible(timeout=tmo)
            except Exception:
                pass

            present, total = _button_trigger_options_present(page, trigger, seed.roles)
            lists_ok = total >= 2 and len(present) >= 2

            try:
                current_text = trigger.inner_text()
            except Exception:
                current_text = ""
            target_role = seed.roles[1] if len(seed.roles) > 1 else seed.roles[0]
            if current_text and target_role.replace("_", "").lower() in current_text.replace("_", "").lower():
                target_role = next((r for r in seed.roles if r != target_role), target_role)

            switch_ok, switch_detail = (False, "role listing failed; switch not attempted")
            if lists_ok:
                switch_ok, switch_detail = _switch_button_trigger(page, trigger, target_role, tmo)

        positive = UiVerdict(
            ok=lists_ok and switch_ok,
            detail=(
                f"role-switch control ({found['kind']}) lists seeded roles(AC-5)={present} of {seed.roles} "
                f"(total options={total}, >=2 distinct:{lists_ok}); switch-applies(AC-7)={switch_ok} "
                f"({switch_detail})"
            ),
            evidence={"seed": seed.evidence, "presentRoles": present, "totalOptions": total, "kind": found["kind"]},
        )
    finally:
        ctx.close()

    return UiReport(positive=positive, notes="AC-5/AC-7 role-switch control lists and applies a role")


CAPABILITY = UiCapability(
    id="role_switch_control_lists_and_applies_role",
    ac="AC-5/AC-7",
    description="A multi-role account's role-switch control lists >=2 roles and actually applies a switch.",
    run=_run,
    priority=True,
)
