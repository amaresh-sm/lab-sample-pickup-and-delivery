# UI mutant: `ui-login-silent`

**Target scenario:** `login_rejects_invalid_credentials` (authentication,
implicit -- no dedicated ACCEPTANCE.md line, mirrors
`sign_out_ends_authenticated_session`'s "session-lifecycle (implicit)").

**Mutation:** in a throwaway copy of golden's frontend, silences the login
form's error announcement in `components/LoginPage.vue`: the error paragraph
`<p ref="errorElement" class="login-error" role="alert" tabindex="-1">{{ error
}}</p>` has its interpolation forced to `{{ "" }}`. The backend still
genuinely refuses the fabricated credentials (the app never authenticates) --
only the VISIBLE feedback is gutted. The element has no sibling
`v-else`/`v-else-if`, so forcing its text empty is a safe, surgical edit that
cannot break the Vue build.

`mutate.py <path-to-LoginPage.vue>` applies it and fails loudly (nonzero exit
+ stderr) if the target's source shape changed, so a silent no-op can never
masquerade as a passing mutant.

## How it was applied (real golden untouched)

```bash
# on a COPY of the whole golden-mongo workspace at /tmp/mut-ui-login-silent
# (node_modules/dist excluded from the copy -- `npm ci` regenerates them)
rsync -a --exclude node_modules --exclude dist --exclude .git --exclude bin \
  verifier/qc-pinned-contracts/reference-solutions/golden-mongo/ /tmp/mut-ui-login-silent/
python3 verifier/qc-pinned-contracts/anchors/mutants/ui-login-silent/mutate.py \
  /tmp/mut-ui-login-silent/frontend/src/components/LoginPage.vue
./run-ui --workspace /tmp/mut-ui-login-silent --logs-dir /tmp/mut-ui-login-silent-logs
```

`./run-ui` rebuilds the frontend (`npm ci && npm run build`) and boots the
whole app fresh inside a throwaway container, then scores it exactly like any
other workspace.

## Measured result (gate)

| scenario                                          | golden (k=3)      | this mutant |
|----------------------------------------------------|:-----------------:|:-----------:|
| `login_rejects_invalid_credentials`               | 1.0, 1.0, 1.0      | **0.000** (CAUGHT) |
| `accept_assignment_writes_through`                | 1.0                | 1.000 (isolated) |
| `audit_view_renders_and_searches`                 | 1.0                | 1.000 (isolated) |
| `lab_worker_views_own_capacity`                   | 1.0                | 1.000 (isolated) |
| `map_renders_local_tiles_only`                    | 1.0                | 1.000 (isolated) |
| `monitor_dashboard_renders_categories`            | 1.0                | 1.000 (isolated) |
| `request_form_surfaces_validation_error`          | 1.0                | 1.000 (isolated) |
| `role_switch_control_lists_and_applies_role`      | 1.0                | 1.000 (isolated) |
| `role_work_view_shows_current_and_past`           | 1.0                | 1.000 (isolated) |
| `sample_freshness_visible_to_owner`               | 1.0                | 1.000 (isolated) |
| `sample_view_shows_location_custodian_progress`   | 1.0                | 1.000 (isolated) |
| `sample_view_shows_ordered_status_history`        | 0.5 (pre-existing) | 0.500 (unchanged) |
| `sample_view_surfaces_exception_flag`             | 1.0                | 1.000 (isolated) |
| `serviceability_dry_run_creates_nothing`          | 1.0                | 1.000 (isolated) |
| `sign_out_ends_authenticated_session`             | 1.0                | 1.000 (isolated) |
| **overall (15 scenarios)**                        | 0.9667             | 0.9000 |

Measured detail on the mutant run:

```
* login_rejects_invalid_credentials      authentication (implicit) credit=0.000
  positive: invalid credentials submitted: authenticated-signal-absent=True,
  visible-error-feedback=False text='' (seeded HTTP status=401, code='invalid_credentials')
```

The target scenario's positive flips cleanly 1.0 -> 0.0 (positive-only, no
separate control dimension: "authenticated-signal-absent" stays `True` since
the backend genuinely never authenticates the fabricated credentials, but
"visible-error-feedback" flips to `False`, which alone fails the AND), while
every other scenario stays at its golden value; `sample_view_shows_ordered_status_history`
at 0.500 is the SAME pre-existing, unrelated flake documented in the suite
README, not collateral from this mutant.
