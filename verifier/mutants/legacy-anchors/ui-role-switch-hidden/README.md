# UI mutant: `ui-role-switch-hidden`

**Target scenario:** `role_switch_control_lists_and_applies_role` (AC-5/AC-7).

**Mutation:** in a throwaway copy of golden's frontend, forces the role-switch
`<select>` in `components/AppShell.vue` to never render, even for a multi-role
account: `<label v-if="store.roles.length > 1" class="role-switcher">` has its
condition forced to a literal `v-if="false"`. The element has no sibling
`v-else`/`v-else-if` (the next sibling is an unconditional `<span>`), so this is
a safe, surgical edit that cannot orphan a conditional chain or break the Vue
build.

`mutate.py <path-to-AppShell.vue>` applies it and fails loudly (nonzero exit +
stderr) if the target's source shape changed, so a silent no-op can never
masquerade as a passing mutant.

## How it was applied (real golden untouched)

```bash
# inside the app container, on a COPY of the frontend at /tmp/mut-rsw
cp -r /workspace/frontend /tmp/mut-rsw
python3 mutate.py /tmp/mut-rsw/src/components/AppShell.vue
( cd /tmp/mut-rsw && npm run build )
APP_ADDR=127.0.0.1:18084 FRONTEND_DIR=/tmp/mut-rsw/dist \
  OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets /workspace/bin/lab-golden-server &
```

In this validation run it was applied and scored via the harness's own
`./run-ui --workspace <throwaway-copy>` (which rebuilds the frontend and boots
the app inside a throwaway container), rather than a manually-started server.

## Measured result (gate)

| scenario                                          | golden (k=3)      | this mutant |
|----------------------------------------------------|:-----------------:|:-----------:|
| `role_switch_control_lists_and_applies_role`      | 1.0, 1.0, 1.0      | **0.000** (CAUGHT) |
| `audit_view_renders_and_searches`                 | 1.0                | 1.000 (isolated) |
| `map_renders_local_tiles_only`                    | 1.0                | 1.000 (isolated) |
| `monitor_dashboard_renders_categories`            | 1.0                | 1.000 (isolated) |
| `request_form_surfaces_validation_error`          | 1.0                | 1.000 (isolated) |
| `role_work_view_shows_current_and_past`           | 1.0                | 1.000 (isolated) |
| `sample_freshness_visible_to_owner`               | 1.0                | 1.000 (isolated) |
| `sample_view_shows_location_custodian_progress`   | 1.0                | 1.000 (isolated) |
| `sample_view_shows_ordered_status_history`        | 0.5 (pre-existing) | 0.500 (unchanged) |
| `sample_view_surfaces_exception_flag`             | 1.0                | 1.000 (isolated) |
| `sign_out_ends_authenticated_session`             | 1.0                | 1.000 (isolated) |
| **overall (11 scenarios)**                        | 0.9545             | 0.8636 |

Measured detail on the mutant run:

```
role_switch_control_lists_and_applies_role AC-5/AC-7    credit=0.000
  positive: no role-switch control found for a 2-role account (roles=['requester', 'dispatcher'])
```

The target scenario's positive flips cleanly 1.0 -> 0.0 (it is positive-only --
no separate control dimension, mirroring `role_work_view_shows_current_and_past`
(AC-3)), while every other scenario stays at its golden value; the one
non-1.0 row (`sample_view_shows_ordered_status_history` at 0.500) is a
PRE-EXISTING flake unrelated to this mutant -- it measures 0.500 on unmodified
golden too (see `../../../../engine/ui_capabilities/README.md`'s own golden
k=3 table and the k=3 runs captured for this validation pass), so it is not
collateral damage.
