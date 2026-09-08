# UI mutant: `ui-audit-hidden`

**Target scenario:** `audit_view_renders_and_searches` (AC-88, spec line 229).

**Mutation:** in a throwaway copy of golden's frontend, removes the rendered
audit ENTRIES list (`<ol class="audit-list">`) from `components/AuditPanel.vue`,
so the audit view no longer renders any human-readable audit records. The
scenario's entry-rendering assertion (>= 1 entry) and search-filtering assertion
therefore fail, while the rest of the app is untouched. The list is EMPTIED
(its opening `<ol v-if="entries.length" ...>` tag's `v-if` is preserved, only
the `<li>` body is dropped and its class renamed) rather than deleted outright,
because a sibling `<EmptyState v-else-if="searched && !action.error.value">`
depends on that `v-if` being present -- deleting the element instead of
emptying it orphans that `v-else-if` and breaks the Vue build (this bit an
earlier version of this exact mutant; the current `mutate.py` avoids it by
re-emitting an empty `<ol v-if="entries.length" class="audit-entries-removed-by-mutant"></ol>`).

`mutate.py <path-to-AuditPanel.vue>` applies it and fails loudly (nonzero exit +
stderr) if the list can't be found, so a silent no-op can never masquerade as a
passing mutant.

## Bonus-fix status (this pass)

This mutant's last known state (from an earlier session, `.mutant-work/ui-audit-hidden/runui.log`)
was a Vue BUILD FAILURE (`v-else/v-else-if has no adjacent v-if or v-else-if`
at the `EmptyState` sibling), left unresolved with this README's own table
showing "pending validation run" placeholders instead of real numbers.
Re-running the CURRENT `mutate.py` from scratch in this pass (fresh `rsync`
copy, `npm ci`, `npm run build`, then `./run-ui`) shows the build now SUCCEEDS
and the mutant is CAUGHT cleanly -- the orphaned-`v-else-if` bug described
above is already fixed in the script on disk; this mutant simply had never
been re-validated after that fix. The table below is the first REAL measured
run.

## How it was applied (real golden untouched)

```bash
rsync -a --exclude node_modules --exclude dist --exclude .git --exclude bin \
  verifier/qc-pinned-contracts/reference-solutions/golden-mongo/ /tmp/mut-ui-audit-hidden/
python3 verifier/qc-pinned-contracts/anchors/mutants/ui-audit-hidden/mutate.py \
  /tmp/mut-ui-audit-hidden/frontend/src/components/AuditPanel.vue
# confirmed the Vue build succeeds on its own before scoring:
( cd /tmp/mut-ui-audit-hidden/frontend && npm ci --no-audit --no-fund && npm run build )
./run-ui --workspace /tmp/mut-ui-audit-hidden --logs-dir /tmp/mut-ui-audit-hidden-logs
```

## Measured result (gate) -- REAL numbers, first successful run of this mutant

| scenario                                          | golden (k=3)      | this mutant |
|----------------------------------------------------|:-----------------:|:-----------:|
| `audit_view_renders_and_searches`                 | 1.0, 1.0, 1.0      | **0.500** (CAUGHT -- positive dimension) |
| `accept_assignment_writes_through`                | 1.0                | 1.000 (isolated) |
| `lab_worker_views_own_capacity`                   | 1.0                | 1.000 (isolated) |
| `login_rejects_invalid_credentials`               | 1.0                | 1.000 (isolated) |
| `map_renders_local_tiles_only`                    | 1.0                | 1.000 (isolated) |
| `monitor_dashboard_renders_categories`            | 1.0                | 1.000 (isolated) |
| `request_form_surfaces_validation_error`          | 1.0                | 1.000 (isolated) |
| `role_switch_control_lists_and_applies_role`      | 1.0                | 1.000 (isolated) |
| `role_work_view_shows_current_and_past`           | 1.0                | 1.000 (isolated) |
| `sample_freshness_visible_to_owner`               | 1.0                | 1.000 (isolated) |
| `sample_view_shows_location_custodian_progress`   | 1.0                | 1.000 (isolated) |
| `sample_view_shows_ordered_status_history`        | 0.5 (pre-existing) | 1.000 (SAME pre-existing flake, ran non-flaky this time -- see suite README) |
| `sample_view_surfaces_exception_flag`             | 1.0                | 1.000 (isolated) |
| `serviceability_dry_run_creates_nothing`          | 1.0                | 1.000 (isolated) |
| `sign_out_ends_authenticated_session`             | 1.0                | 1.000 (isolated) |
| **overall (15 scenarios)**                        | 0.9667             | 0.9667 |

Measured detail on the mutant run:

```
* audit_view_renders_and_searches        AC-88        credit=0.500
  positive: audit entries=0 (>=1:False); field-vocab=['action', 'actor', 'outcome', 'role', 'time'] (>=3:True);
  search term='' filtered 0->0 (ok:False)
  control:  requester is not shown the global audit trail (correctly withheld)
```

The target's POSITIVE dimension flips cleanly (no entries render, so
"search filters the list" cannot even be attempted -- `term=''` because there
was nothing to derive a search term FROM), dropping `audit_view_renders_and_searches`
from 1.0 to 0.5. The CONTROL dimension (a plain requester is correctly denied
the global audit trail) is untouched by this mutation and stays passing, which
is why the overall drop is 0.5 rather than 0.0. Every other scenario stays at
its golden value. `sample_view_shows_ordered_status_history` happened to render
1.000 (non-flaky) on this particular run -- it is the SAME pre-existing,
unrelated timestamp-collision flake documented in the suite README (measured
anywhere from 0.0 to 1.0 across sessions), not something this mutant caused or
fixed.
