# Run log

Tracks time and token cost for (1) candidate-generation runs and (2) verifier build/grading runs, so
future work can be planned/budgeted against real numbers instead of guesses.

- **Generation duration** comes from `runs/<label>-<ts>/solution/run-metadata.json` (`startedAt`/`finishedAt`).
- **Generation tokens** = sum of every `tokens used` checkpoint in that run's `codex.log` (the CLI logs
  one per internal stage — main generation + postprocess/openapi-repair passes — so a single "last value"
  under-counts; sum them all).
- **Verifier timings** are wall-clock durations of each build/fix/validation phase (from task completion
  records), not a token count — these are Claude-agent phases, not model-generation runs.

To log a new candidate-generation run automatically:
```bash
bash /private/tmp/claude-502/-Users-amaresh-Desktop-repositories-amazon-rl-gym-pilot-sample/7ed88696-d344-4a24-b7a8-2b84cded08a8/scratchpad/log_generation_run.sh <label>
```
(Reads `run-metadata.json` + `codex.log` for that label's most recent `runs/<label>-*` dir, appends a row below.)

## Candidate generation runs

| Label | Model | Reasoning | Started | Finished | Duration | Tokens (sum) | Exit | Run dir |
|---|---|---|---|---|---|---|---|---|
| sol-high-pc-v2 | gpt-5.6-sol | high | 2026-07-31T06:25:31Z | 2026-07-31T06:58:45Z | 0:33:14 | 509,003 | 0 | `runs/sol-high-pc-v2-20260731T062529Z` |
| sol-xhigh-pc-v2 | gpt-5.6-sol | xhigh | 2026-07-31T07:04:34Z | 2026-07-31T07:57:15Z | 0:52:41 | 653,436 | 0 | `runs/sol-xhigh-pc-v2-20260731T070432Z` |
| tera-high-pc-v2 | gpt-5.6-terra | high | 2026-07-31T08:06:18Z | 2026-07-31T08:17:57Z | 0:11:39 | 279,511 | 0 | `runs/tera-high-pc-v2-20260731T080617Z` |

## Verifier build / grading runs

Phase durations below are wall-clock time for each background agent phase this session (from task
completion timestamps). These are engine-build/fix/validation phases, not per-candidate grading runs yet —
once `engine/run.py` is used to grade a candidate, log that as its own row (label = candidate name, phase =
"grade").

| Phase | What it did | Duration | Outcome |
|---|---|---|---|
| VB1 | Build the new engine + 45 capability specs | 0:54:23 | golden-mongo scored 1.0000/45 |
| VB2 | Build hollow anchor + 5 mutants, discrimination table | 0:51:34 | hollow 0.2963/45; only 1/5 mutants caught correctly |
| VB2.5 | Fix the 5 critical/high spec-depth gaps VB2 found | 2:01:49 | golden clean 1.0000/45, all 5 mutants + 2 ported bug classes now caught, hollow moved 0.2963->0.2741 |
| VB2.6 | Close hollow's remaining fake credit: harden `oracle.py`'s `observe()` direct-read tier against trivial id-echo, and replace ~19 tautological/echo-only positive assertions (`authenticate_user`, `create_user`, `update_user_access`, `read_user`, `update_location`, `set_location_status`, `set_location_function_status`, `set_connection_status`, `set_worker_availability`, `reroute_journey`, `split_journey`, `read_serviceability_snapshot`, `read_journey_plan`, `read_operations_state`, `read_sample_lifecycle`, `read_sample_state`, `ingest_presealed_sample`, `create_collection_request`) with independent-confirmation checks (cross-endpoint reads, non-echoable fields, before/after deltas) | ~1:30 | golden clean 1.0000/45 (unchanged); hollow 0.2741->0.0778 (12.33/45 -> 3.5/45), clearing the <=0.10 target; `search_audit_trail` intentionally left at 0.500 (its negative dimension passes for a real, non-tautological reason -- hollow happens to correctly implement the `limit<=0` input-validation rule); all 5 patch-based mutants independently re-verified caught post-fix, each with credit loss concentrated on exactly its target capability (no cascades): `connection-capacity-unreadable` 0.9556 (list_role_work, read_operations_state, report_disruption, set_connection_status), `edit-plan-wrong-shape` 0.9889 (edit_active_plan), `orphan-assignments-on-expiry` 0.9889 (list_role_work), `pulse-exception-unaudited` 0.9889 (search_audit_trail), `replan-copies-verbatim` 0.9889 (report_disruption) |

## Candidate scoring runs

Graded against three already-generated pinned-contract candidates (`sol-high-pc-v2`, `sol-xhigh-pc-v2`,
`tera-high-pc-v2`), each run in its own `lab-sample-pickup-delivery-gym:local` container
(`cand-sol-high`:18091, `cand-sol-xhigh`:18092, `cand-tera-high`:18093). sol-xhigh and tera-high binaries
hardcode a `127.0.0.1` bind (ignoring `APP_ADDR`/host), so a small in-container Python TCP proxy
(`0.0.0.0:8080` -> `127.0.0.1:<app-port>`) was used to make them reachable via the mapped host port.

| Candidate | Score | Notable gaps | Score log |
|---|---|---|---|
| sol-high-pc-v2 | 16.83/45 (0.3741) | accept_assignment, check_serviceability, collect_and_seal_sample, configure_laboratory_acceptance, confirm_handoff, confirm_laboratory_receipt, create_location, create_sample_type, decline_assignment, edit_active_plan, ingest_presealed_sample, initiate_handoff, list_role_work, merge_journeys, read_journey_plan, record_handling_checkpoint, record_laboratory_exception, record_worker_location, report_disruption, reroute_journey, search_audit_trail, split_journey, update_assignment_progress, update_sample_type (all 0.0); authenticate_user, cancel_journey, configure_capacity, create_collection_request, read_sample_lifecycle, read_sample_state, select_active_role, set_worker_availability (partial) | `verifier/qc-pinned-contracts/candidate-scores/sol-high-score.log` |
| sol-xhigh-pc-v2 | 20.0/45 (0.4444) | accept_assignment, cancel_journey, collect_and_seal_sample, confirm_handoff, confirm_laboratory_receipt, create_collection_request, create_sample_type, decline_assignment, edit_active_plan, initiate_handoff, merge_journeys, read_journey_plan, record_handling_checkpoint, record_laboratory_exception, record_worker_location, report_disruption, reroute_journey, search_audit_trail, split_journey, update_assignment_progress, update_sample_type (all 0.0); authenticate_user, check_serviceability, create_location, ingest_presealed_sample, list_role_work, select_active_role, set_location_function_status, update_location (partial) | `verifier/qc-pinned-contracts/candidate-scores/sol-xhigh-score.log` |
| tera-high-pc-v2 | 14.5/45 (0.3222) | accept_assignment, cancel_journey, check_serviceability, collect_and_seal_sample, configure_capacity, configure_laboratory_acceptance, confirm_handoff, confirm_laboratory_receipt, create_collection_request, create_location, create_sample_type, decline_assignment, edit_active_plan, ingest_presealed_sample, initiate_handoff, merge_journeys, record_handling_checkpoint, record_laboratory_exception, record_worker_location, report_disruption, reroute_journey, search_audit_trail, set_location_function_status, split_journey, update_assignment_progress, update_sample_type (all 0.0); authenticate_user, create_connection, create_user, list_role_work, read_journey_plan, read_serviceability_snapshot, select_active_role, set_worker_availability, update_location (partial) | `verifier/qc-pinned-contracts/candidate-scores/tera-high-score.log` |

### Earlier golden-mongo extension phases (for reference — not the new engine, but the reference app it grades against)

| Phase | What it did | Duration |
|---|---|---|
| A | Unify Node+Laboratory into one Location entity | 0:36 |
| B | Dry-run serviceability + collection window + versioning | 0:33 |
| C | Server-side role selection + audit overhaul + views fixes | 0:52 |
| D | openapi.json regen + drift gate + Playwright + audit paging | 0:46 |
| G5.0 | Triage 83 acceptance criteria vs existing test evidence | 0:13 |
| G5.1 | Close ~21 cheap acceptance-criteria gaps | ~0:22 |
| G5.2 | Capacity lifecycle + batch validity + 3 defect fixes | ~0:38 |
| G5.3 | Disruption/replanning cluster + 2 expiry defect fixes | ~0:48 |
| G5.4 | Out-of-process (durability/readiness/schema) + UI criteria | ~0:59 |
