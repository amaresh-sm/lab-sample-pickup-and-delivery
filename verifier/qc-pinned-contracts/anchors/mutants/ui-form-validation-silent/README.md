# UI mutant: `ui-form-validation-silent`

**Target scenario:** `request_form_surfaces_validation_error` (AC-9/AC-10).

**Mutation:** in a throwaway copy of golden's frontend, swallows the
create-request form's validation-error announcement in
`components/RequestPanel.vue`: `<InlineResult v-if="action.error.value"
kind="error">{{ action.error.value }}</InlineResult>` has its `v-if` forced to
a literal `false`. A rejected submission (e.g. the pinned contract's
`invalid_collection_window`) is still caught by `useAction()` -- the error
text is still computed and stored in `action.error.value` -- but it is never
RENDERED anywhere on the page. The element has no sibling
`v-else`/`v-else-if`, so forcing it off is a safe, surgical edit that cannot
break the Vue build.

`mutate.py <path-to-RequestPanel.vue>` applies it and fails loudly (nonzero
exit + stderr) if the target's source shape changed, so a silent no-op can
never masquerade as a passing mutant.

## How it was applied (real golden untouched)

```bash
# inside the app container, on a COPY of the frontend at /tmp/mut-fv
cp -r /workspace/frontend /tmp/mut-fv
python3 mutate.py /tmp/mut-fv/src/components/RequestPanel.vue
( cd /tmp/mut-fv && npm run build )
APP_ADDR=127.0.0.1:18084 FRONTEND_DIR=/tmp/mut-fv/dist \
  OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets /workspace/bin/lab-golden-server &
```

In this validation run it was applied and scored via `./run-ui --workspace
<throwaway-copy>` (rebuilds the frontend and boots the app in a throwaway
container).

## Measured result (gate)

| scenario                                          | golden (k=3)      | this mutant |
|----------------------------------------------------|:-----------------:|:-----------:|
| `request_form_surfaces_validation_error`          | 1.0, 1.0, 1.0      | **0.000** (CAUGHT) |
| `audit_view_renders_and_searches`                 | 1.0                | 1.000 (isolated) |
| `map_renders_local_tiles_only`                    | 1.0                | 1.000 (isolated) |
| `monitor_dashboard_renders_categories`            | 1.0                | 1.000 (isolated) |
| `role_switch_control_lists_and_applies_role`      | 1.0                | 1.000 (isolated) |
| `role_work_view_shows_current_and_past`           | 1.0                | 1.000 (isolated) |
| `sample_freshness_visible_to_owner`               | 1.0                | 1.000 (isolated) |
| `sample_view_shows_location_custodian_progress`   | 1.0                | 1.000 (isolated) |
| `sample_view_shows_ordered_status_history`        | 0.5 (pre-existing) | 0.500 (unchanged) |
| `sample_view_surfaces_exception_flag`             | 1.0                | 1.000 (isolated) |
| `sign_out_ends_authenticated_session`             | 1.0                | 1.000 (isolated) |
| **overall (11 scenarios)**                        | 0.9545             | 0.8636 |

The target scenario's positive flips cleanly 1.0 -> 0.0 (positive-only, no
separate control dimension), while every other scenario stays at its golden
value; `sample_view_shows_ordered_status_history` at 0.500 is the SAME
pre-existing, unrelated flake measured on unmodified golden, not collateral
from this mutant.
