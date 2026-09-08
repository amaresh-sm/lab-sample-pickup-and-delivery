# UI mutant: `ui-dryrun-writes`

**Target scenario:** `serviceability_dry_run_creates_nothing` (AC-37/AC-38).

**Mutation:** in a throwaway copy of golden's frontend, makes the "dry run"
LIE in `components/ServiceabilityCheckPanel.vue`: `check()` still calls
`check_serviceability` and still renders a verdict (AC-37's half stays
intact), but a SECOND, fire-and-forget `POST` to `create_collection_request`
is added right after it -- using the current session and the panel's own
sample type, with hardcoded `containerType`/temperature fields -- so every
"dry run" click silently creates a real request/sample behind the scenes,
violating AC-38 ("The dry run creates NO request, sample, journey, assignment
or capacity reservation").

This is exactly the SIDE-EFFECT-AWARENESS risk the scenario's own seeder
(`seed_serviceability_dry_run`) exists to catch: a candidate could render a
correct-looking "Serviceable" verdict while secretly creating something
anyway. `mutate.py <path-to-ServiceabilityCheckPanel.vue>` applies it and
fails loudly (nonzero exit + stderr) if the target's source shape changed, so
a silent no-op can never masquerade as a passing mutant.

## How it was applied (real golden untouched)

```bash
rsync -a --exclude node_modules --exclude dist --exclude .git --exclude bin \
  verifier/qc-pinned-contracts/reference-solutions/golden-mongo/ /tmp/mut-ui-dryrun-writes/
python3 verifier/qc-pinned-contracts/anchors/mutants/ui-dryrun-writes/mutate.py \
  /tmp/mut-ui-dryrun-writes/frontend/src/components/ServiceabilityCheckPanel.vue
./run-ui --workspace /tmp/mut-ui-dryrun-writes --logs-dir /tmp/mut-ui-dryrun-writes-logs
```

## Measured result (gate)

| scenario                                          | golden (k=3)      | this mutant |
|----------------------------------------------------|:-----------------:|:-----------:|
| `serviceability_dry_run_creates_nothing`          | 1.0, 1.0, 1.0      | **0.000** (CAUGHT) |
| `accept_assignment_writes_through`                | 1.0                | 1.000 (isolated) |
| `audit_view_renders_and_searches`                 | 1.0                | 1.000 (isolated) |
| `lab_worker_views_own_capacity`                   | 1.0                | 1.000 (isolated) |
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
| `sign_out_ends_authenticated_session`             | 1.0                | 1.000 (isolated) |
| **overall (15 scenarios)**                        | 0.9667             | 0.9000 |

Measured detail on the mutant run:

```
* serviceability_dry_run_creates_nothing AC-37/AC-38  credit=0.000
  positive: filled sampleType=True, lat=True, lng=True; submit_count=1 submitted=True click_error='';
  serviceable-verdict-rendered(AC-37)=True; requester work-item count before=0 after=1 unchanged(AC-38)=False
```

The verdict still renders correctly (AC-37's half stays `True` -- the mutant
deliberately leaves the real `check_serviceability` call and its rendering
untouched, so this mutant is a genuine test of the AC-38 HTTP re-read, not a
confound with a broken verdict), while the requester's own HTTP-visible
work-item count moves 0 -> 1 after the click, which alone fails the AND and
flips the target scenario's positive cleanly 1.0 -> 0.0. Every other scenario
stays at its golden value; `sample_view_shows_ordered_status_history` at 0.500
is the SAME pre-existing, unrelated flake documented in the suite README, not
collateral from this mutant.
