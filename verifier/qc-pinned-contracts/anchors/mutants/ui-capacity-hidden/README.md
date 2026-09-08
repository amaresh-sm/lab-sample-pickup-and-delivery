# UI mutant: `ui-capacity-hidden`

**Target scenario:** `lab_worker_views_own_capacity` (AC-91 / instruction.md
line 68).

**Mutation:** in a throwaway copy of golden's frontend, suppresses the
destination-capacity reading in `components/WorkQueue.vue`: `capacityText()`
is forced to always `return ""`, regardless of what `item.destinationCapacity`
actually holds. The backend still genuinely reserves and reports capacity (the
pinned contract's `AssignmentWorkItem.destinationCapacity` is completely
unaffected -- this is a presentation-only cut), but a laboratory/node worker's
own work view no longer shows the regular/cooled capacity at their assigned
facility at all. The template's `<small v-if="capacityText(item)">` has no
sibling `v-else`/`v-else-if`, so forcing the function to return `""` simply
makes that `v-if` always false -- a safe, surgical edit that cannot break the
Vue build.

`mutate.py <path-to-WorkQueue.vue>` applies it and fails loudly (nonzero exit +
stderr) if the target's source shape changed, so a silent no-op can never
masquerade as a passing mutant.

## How it was applied (real golden untouched)

```bash
rsync -a --exclude node_modules --exclude dist --exclude .git --exclude bin \
  verifier/qc-pinned-contracts/reference-solutions/golden-mongo/ /tmp/mut-ui-capacity-hidden/
python3 verifier/qc-pinned-contracts/anchors/mutants/ui-capacity-hidden/mutate.py \
  /tmp/mut-ui-capacity-hidden/frontend/src/components/WorkQueue.vue
./run-ui --workspace /tmp/mut-ui-capacity-hidden --logs-dir /tmp/mut-ui-capacity-hidden-logs
```

## Measured result (gate)

| scenario                                          | golden (k=3)      | this mutant |
|----------------------------------------------------|:-----------------:|:-----------:|
| `lab_worker_views_own_capacity`                   | 1.0, 1.0, 1.0      | **0.000** (CAUGHT) |
| `accept_assignment_writes_through`                | 1.0                | 1.000 (isolated) |
| `audit_view_renders_and_searches`                 | 1.0                | 1.000 (isolated) |
| `login_rejects_invalid_credentials`               | 1.0                | 1.000 (isolated) |
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
* lab_worker_views_own_capacity          AC-91        credit=0.000
  positive: laboratory worker's own work view shows HTTP-confirmed capacity
  (available=57, cooledAvailable=34)=False (configured regular=58, cooled=34) text=''
```

Note the seed's OWN HTTP confirmation (`available=57, cooledAvailable=34`) is
unaffected by the mutation -- the backend still genuinely reserves and reports
the capacity; only the front-end's OWN rendering of it is gutted, which is
exactly the presentation-layer gap this scenario exists to catch (the API
engine's own capability probes would not catch this at all, since they never
render anything). The target scenario's positive flips cleanly 1.0 -> 0.0
(positive-only, no separate control dimension), while every other scenario
stays at its golden value; `sample_view_shows_ordered_status_history` at 0.500
is the SAME pre-existing, unrelated flake documented in the suite README, not
collateral from this mutant.
