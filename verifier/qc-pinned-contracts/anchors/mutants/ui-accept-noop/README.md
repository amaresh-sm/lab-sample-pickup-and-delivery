# UI mutant: `ui-accept-noop`

**Target scenario:** `accept_assignment_writes_through` (AC-31 / instruction.md
lines 38/48/57/67).

**Mutation:** in a throwaway copy of golden's frontend, neutralises the
accept-assignment form's submit handler in `components/OfferPanel.vue`:
`<form class="stack-form" @submit.prevent="accept">` has its handler replaced
with a no-op (`@submit.prevent="() => {}"`). The "Accept assignment" button
still renders, is still clickable, and the form still submits (prevented from
a real page reload) -- only the ACTUAL `accept_assignment` network call never
fires, so the assignment never moves out of the pending bucket server-side.
The form element has no sibling `v-else`/`v-else-if`, so replacing its
`@submit.prevent` handler is a safe, surgical edit that cannot break the Vue
build.

`mutate.py <path-to-OfferPanel.vue>` applies it and fails loudly (nonzero exit
+ stderr) if the target's source shape changed, so a silent no-op can never
masquerade as a passing mutant.

## How it was applied (real golden untouched)

```bash
rsync -a --exclude node_modules --exclude dist --exclude .git --exclude bin \
  verifier/qc-pinned-contracts/reference-solutions/golden-mongo/ /tmp/mut-ui-accept-noop/
python3 verifier/qc-pinned-contracts/anchors/mutants/ui-accept-noop/mutate.py \
  /tmp/mut-ui-accept-noop/frontend/src/components/OfferPanel.vue
./run-ui --workspace /tmp/mut-ui-accept-noop --logs-dir /tmp/mut-ui-accept-noop-logs
```

## Measured result (gate)

| scenario                                          | golden (k=3)      | this mutant |
|----------------------------------------------------|:-----------------:|:-----------:|
| `accept_assignment_writes_through`                | 1.0, 1.0, 1.0      | **0.500** (CAUGHT -- positive dimension) |
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
| `serviceability_dry_run_creates_nothing`          | 1.0                | 1.000 (isolated) |
| `sign_out_ends_authenticated_session`             | 1.0                | 1.000 (isolated) |
| **overall (15 scenarios)**                        | 0.9667             | 0.9333 |

Measured detail on the mutant run:

```
* accept_assignment_writes_through       AC-31        credit=0.500
  positive: filled assignment id=True, clicked accept=True; HTTP re-read after click:
  bucket='pending' status='pending' (pre-click bucket='pending'); write-through-confirmed=False
  control:  intruder attempted accept (filled=True, clicked=True); intruder's own
  list_role_work now shows this assignment=False (bucket=None); collector's state
  after intruder's attempt: bucket='pending'
```

This is the two-dimension scenario's expected PARTIAL drop (1.0 -> 0.5, not
0.0): the POSITIVE dimension -- the offered worker's own accept click actually
persisting server-side -- flips cleanly (the click succeeds with no JS error,
but `recheck("collector")` proves over HTTP that the assignment never left the
"pending" bucket). The CONTROL dimension (AC-31: an unrelated worker cannot
accept it) stays correctly passing, since it was never exercised by this
mutation -- the intruder's own accept attempt does nothing either way,
which is a NO-OP with or without the mutant, so `stolen=False` holds
regardless. This is exactly why the scenario is modelled as positive+control
rather than a single flag: the two dimensions test genuinely independent
failure modes (a real write vs. an ownership check), and this mutant isolates
the former. Every other scenario stays at its golden value;
`sample_view_shows_ordered_status_history` at 0.500 is the SAME pre-existing,
unrelated flake documented in the suite README, not collateral from this
mutant.
