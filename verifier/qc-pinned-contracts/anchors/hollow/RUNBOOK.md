# Hollow anchor — runbook

`server.py` is a ~450-line, dependency-free Python `http.server` app. It
serves all 45 pinned operations at their contract-exact paths/methods, always
returns the contract's declared 2xx status with a plausible-looking,
fabricated body (echoing request fields + random ids), and **persists
nothing** — no dict, no file, no per-request memory. Every handler is a pure
function of the one incoming request.

It deliberately does NOT enforce business rules (named-sample-type policy,
RBAC-by-role, optimistic concurrency, capacity limits, serviceability
geometry, idempotency replay). The only gate it does apply: every op except
`authenticate_user`/`system_readiness` requires a non-empty `Authorization`
header (401 if missing) — a technical baseline, not a business rule, since
without it nothing in this app could ever be exercised at all.

## Run it

```bash
python3 verifier/qc-pinned-contracts/anchors/hollow/server.py 8099
```

## Score it

```bash
cd verifier/qc-pinned-contracts/engine
python3 run.py --base-url http://127.0.0.1:8099 \
  --admin-user admin --admin-pass admin123 \
  score --verbose --json-out /tmp/hollow_score.json
```

## Result (superseded — see VB2.6 update below)

**Overall: 0.2963 / 1.0000** (13.33 / 45 capability-credits), pre-VB2.5.

Distribution:

| Credit | Count | Capabilities |
|---|---|---|
| 1.000 | 3 | `read_ui_bootstrap`, `stream_realtime_events`, `system_readiness` |
| 0.500 | 21 | `authenticate_user`, `create_user`, `ingest_presealed_sample`, `list_role_work`, `read_journey_plan`, `read_operations_state`, `read_sample_lifecycle`, `read_sample_state`, `read_serviceability_snapshot`, `read_user`, `report_disruption`, `reroute_journey`, `search_audit_trail`, `set_connection_status`, `set_location_function_status`, `set_location_status`, `set_worker_availability`, `split_journey`, `update_location`, `update_user_access` |
| 0.333 | 1 | `create_collection_request` |
| 0.000 | 20 | everything else (`accept_assignment`, `cancel_journey`, `check_serviceability`, `collect_and_seal_sample`, `configure_capacity`, `configure_laboratory_acceptance`, `confirm_handoff`, `confirm_laboratory_receipt`, `create_connection`, `create_location`, `create_sample_type`, `decline_assignment`, `edit_active_plan`, `initiate_handoff`, `merge_journeys`, `record_handling_checkpoint`, `record_laboratory_exception`, `record_worker_location`, `select_active_role`, `update_assignment_progress`, `update_sample_type`) |

## Result: VB2.6 (current)

**Overall: 0.0778 / 1.0000** (3.5 / 45 capability-credits) — down from VB2.5's
0.2741 (12.33/45). Target was <= 0.10; cleared with margin.

VB2.5 had already fixed `list_role_work` and `report_disruption` (0.5 -> 0.0)
by requiring their discovery step (`list_role_work`) to actually find
something, which hollow's empty buckets can never satisfy. VB2.6 closed the
remaining 18 capabilities stuck at 0.500 plus `create_collection_request`
(0.333), by hardening two systemic defect patterns rather than special-casing
hollow:

- **Pattern A (tautological/echo-only positive checks):** a capability's
  positive assertion only checked the WRITE's own echoed response (or had a
  vacuous `x or x` fallback), so no real persistence was ever required.
  Fixed in `capabilities/*.py` by requiring an INDEPENDENT confirmation --
  cross-endpoint reads (`read_operations_state`'s own `locations`/
  `connections`/`workers` arrays, not the write's own response), fields that
  are not derivable from the request (`requesterId`, `activeJourneyId`,
  `username` on an independent `read_user`, exact role-set equality instead
  of substring containment), or a before/after delta that a fixed
  fabrication cannot move (`activeCollectionPointCount`).
- **Pattern B (direct-read defeated by id-echo):** `engine.observe()`'s
  direct-read tier trusted ANY response containing the sentinel, including
  when the sentinel was just the literal path parameter the call itself
  supplied -- something any GET-by-id handler echoes back regardless of
  persistence. Fixed at the engine level in `oracle.py`'s `observe()`: the
  direct-read tier is now disqualified when `sentinel == path_param`,
  falling through to the audit-trail/ops-state tiers instead.

New distribution:

| Credit | Count | Capabilities |
|---|---|---|
| 1.000 | 3 | `read_ui_bootstrap`, `stream_realtime_events`, `system_readiness` |
| 0.500 | 1 | `search_audit_trail` (left as-is: its NEGATIVE dimension passes for a real reason -- hollow correctly implements the `limit<=0 -> 400 invalid_limit` input-validation rule, which needs no persistence to get right; its POSITIVE dimension still correctly fails) |
| 0.000 | 41 | everything else |

golden-mongo re-scored 1.0000/45 (unchanged, zero capabilities below 1.0)
after every VB2.6 change. All 5 patch-based mutants
(`connection-capacity-unreadable`, `edit-plan-wrong-shape`,
`orphan-assignments-on-expiry`, `pulse-exception-unaudited`,
`replan-copies-verbatim`) were re-applied against the patched
`reference-solutions/golden-mongo`, rebuilt, and re-scored to confirm none
were accidentally un-caught by the VB2.6 changes -- see RUN_LOG.md's VB2.6
row for the per-mutant result.

### Why the floor isn't 0.0 — explained per bucket

**The 3 full (1.0) passes** are legitimate, not bugs:
- `system_readiness` has only a positive dimension and no state dependency by
  design (`/ready` is a bare liveness probe).
- `read_ui_bootstrap` / `stream_realtime_events` need only structural keys
  (`user`/`serverTime`, `events`/`cursor`) plus an anonymous-401 check the
  hollow's blanket auth gate happens to satisfy for the right reason (no
  token → no access), even though the hollow never checks WHO the token
  belongs to.

**The 21 half (0.5) passes** all follow one pattern: the capability's
*positive* dimension checks only response-shape or is independently
satisfiable, so the hollow's echoed/fabricated fields pass it; the
*negative* dimension requires an actual business rule (RBAC 403, stale-plan
409, unknown-id 404, invalid-value 400) that the hollow deliberately never
enforces, so it fails. E.g. `update_location`'s positive check is `updated
== new_name and (via is not None or updated == new_name)` — a tautology once
the first clause holds, so no real persistence is required to pass it.

**`create_collection_request` (0.333)** passes only its *positive* dimension:
`read_sample_state`'s direct-read echoes the requested path-parameter id back
verbatim, which alone satisfies `engine.observe()`'s direct-read tier for a
sentinel that IS that same id — no real persistence needed. Its *negative*
(reject an inverted collection window) and *idempotency* (replay same id) both
correctly fail, since the hollow fabricates a fresh random id every call.

**The 20 zero (0.0) capabilities** are where the engine's ranked
`observe()` fallback (direct-read → audit-trail search → ops-state search)
actually earns its keep: `search_audit_trail` and `list_role_work` are
always empty (no persistence), so nothing written can ever be found again,
and the ENTIRE discovery-driven custody-lifecycle chain
(`accept_assignment` → … → `confirm_laboratory_receipt`, plus
`decline_assignment`, `record_laboratory_exception`) never even starts,
because the chain's own discovery step (find which worker an assignment was
offered to, via `list_role_work`) always comes back empty. Business-rule-
heavy domains (`create_sample_type`, `update_sample_type`,
`configure_laboratory_acceptance`, `check_serviceability`) score a clean 0.0
because their defaults are the WRONG values unless the server actively
overrides them (Amber/Indigo/Frost/Pulse policy, serviceability geometry) —
echoing the request body verbatim is exactly the wrong answer here.

**Nothing scored suspiciously high given what it actually checks** — but see
the top-level report's gap list for capabilities whose *positive* dimension
is weaker than its description implies (vacuous ORs, unused `via`, ID-echo
defeating `observe()`'s direct-read tier).
