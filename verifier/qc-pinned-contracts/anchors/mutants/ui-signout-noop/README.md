# UI mutant: `ui-signout-noop`

**Target scenario:** `sign_out_ends_authenticated_session` (implicit
session-lifecycle requirement).

**Mutation:** in a throwaway copy of golden's frontend, neutralises BOTH
sign-out buttons in `components/AppShell.vue`: the `@click="store.logout"`
handler on the mobile sign-out button and the sidebar "quiet-button" sign-out
button are both replaced with a no-op (`@click="() => {}"`). Both buttons
still RENDER with their original "Sign out" text -- only the click behaviour
is gutted -- so the app stays fully authenticated after the click.

`mutate.py <path-to-AppShell.vue>` applies it and fails loudly (nonzero exit +
stderr) if either target can't be found (it requires exactly 2 occurrences of
`@click="store.logout"`), so a silent no-op can never masquerade as a passing
mutant.

## How it was applied (real golden untouched)

```bash
# inside the app container, on a COPY of the frontend at /tmp/mut-so
cp -r /workspace/frontend /tmp/mut-so
python3 mutate.py /tmp/mut-so/src/components/AppShell.vue
( cd /tmp/mut-so && npm run build )
APP_ADDR=127.0.0.1:18084 FRONTEND_DIR=/tmp/mut-so/dist \
  OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets /workspace/bin/lab-golden-server &
```

In this validation run it was applied and scored via `./run-ui --workspace
<throwaway-copy>` (rebuilds the frontend and boots the app in a throwaway
container).

## A near-miss worth recording

The FIRST version of the `sign_out_ends_authenticated_session` scenario
detected "signed out" by checking (a) the login password field back, OR
falling back to `input[type='password']`, and (b) the login username field
back, falling back to **"the first textbox on the page"** -- mirroring
`UiEngine._locate_username`'s own fallback, which is safe when SIGNING IN
(a bare login page has no other textboxes) but NOT safe reused to detect
being signed OUT: golden's still-authenticated workspace has several
unrelated textboxes (and its own Administration page separately renders an
`input[type="password"]`), so that fallback FALSE-POSITIVED on this very
mutant (measured: password field visible=True, username fallback
visible=True, credit stayed 1.000 even with the no-op handler). The scenario
was corrected to fall back to the sign-in SUBMIT control's accessible name
(login-page-specific, not "any textbox") before this mutant was re-measured
below. This is exactly the class of false-positive the mutant-catch step
exists to surface.

## Measured result (gate)

| scenario                                          | golden (k=3)      | this mutant (after the fallback fix) |
|----------------------------------------------------|:-----------------:|:-----------:|
| `sign_out_ends_authenticated_session`             | 1.0, 1.0, 1.0      | **0.000** (CAUGHT) |
| `audit_view_renders_and_searches`                 | 1.0                | 1.000 (isolated) |
| `map_renders_local_tiles_only`                    | 1.0                | 1.000 (isolated) |
| `monitor_dashboard_renders_categories`            | 1.0                | 1.000 (isolated) |
| `request_form_surfaces_validation_error`          | 1.0                | 1.000 (isolated) |
| `role_switch_control_lists_and_applies_role`      | 1.0                | 1.000 (isolated) |
| `role_work_view_shows_current_and_past`           | 1.0                | 1.000 (isolated) |
| `sample_freshness_visible_to_owner`               | 1.0                | 1.000 (isolated) |
| `sample_view_shows_location_custodian_progress`   | 1.0                | 1.000 (isolated) |
| `sample_view_shows_ordered_status_history`        | 0.5 (pre-existing) | 0.500 (unchanged) |
| `sample_view_surfaces_exception_flag`             | 1.0                | 1.000 (isolated) |
| **overall (11 scenarios)**                        | 0.9545             | 0.8636 |

Measured detail on the mutant run (after the fix):

```
sign_out_ends_authenticated_session    session-lifecycle (implicit) credit=0.000
  positive: after sign-out click: login password field visible=False,
  username field/sign-in control visible=False, sign-out control gone=False (informational)
```

The target scenario's positive flips cleanly 1.0 -> 0.0 (positive-only, no
separate control dimension), while every other scenario stays at its golden
value; `sample_view_shows_ordered_status_history` at 0.500 is the SAME
pre-existing, unrelated flake measured on unmodified golden, not collateral
from this mutant.
