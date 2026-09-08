# Durability wiring plan (DUR2 policy pass)

instruction.md:239 — *"Application state that is expected to be durable must
survive an ordinary application restart."* — is a real, in-scope requirement.
Today only `create_collection_request` is wired to check it (see
`engine/README.md`). This document classifies all 52 capabilities and states
the calling convention to use when wiring the rest (DUR3/DUR4), so that work
doesn't repeat the golden-bias mistakes found and fixed earlier this session
(the `report_disruption` `assignedWorkerIds` check, and the admin-vs-requester
scoping bug on `create_collection_request`).

## The one policy rule that matters most

**`durability_write` must call the mutating operation through the EXACT SAME
session its capability's own `_run_xxx` positive probe already uses — never
default to `engine.admin`.**

Each capability's positive probe has already been through the same scrutiny
`create_collection_request` got today (or, where it already used `admin`
correctly — e.g. `configure_capacity`, which instruction.md explicitly grants
to an administrator for any resource — that choice was already right). Reusing
that same session for the durability write reuses that same correctness for
free, and never reintroduces the "admin can do anything" assumption that
caused today's cascading false-failure. There is no case-by-case role table to
maintain — the rule is structural: whatever session earns a 2xx from the
capability's own positive probe is definitionally the right session to use
here too.

For `durability_verify` (the READ-BACK after restart), use `engine.admin` —
reading is broadly granted to admin/dispatcher for monitoring
(instruction.md's Dispatcher/Administrator sections), unlike writing on
someone's behalf, which is where today's bug actually lived. This mirrors
`_durability_verify_ccr`'s existing pattern.

## Classification

**N/A — no durable state of its own (13, kind=read/artifact):** `list_role_work`,
`named_sample_types_present_after_reset`, `read_journey_plan`,
`read_operations_state`, `read_sample_lifecycle`, `read_sample_state`,
`read_serviceability_snapshot`, `read_ui_bootstrap`, `read_user`,
`search_audit_trail`, `stream_realtime_events`, `system_readiness`,
`openapi_json_well_formed`. These only read; whatever they read is
durability-tested via the capability that WROTE it.

**N/A — ephemeral by nature (3, kind=write):** `authenticate_user` (a session
token, not state "expected to be durable"), `select_active_role` (which role
is currently active — a fresh login re-selects it), `check_serviceability`
(instruction.md is explicit: "must NOT create a request, sample, journey,
assignment or capacity reservation" — a dry run has nothing to check).

**N/A — outcome probes, not a resource (3, kind=fairness):**
`fairness_offer_distribution`, `fairness_recency_disparity`,
`fairness_workload_disparity`. These test which worker an algorithm picked at
a point in time, not a piece of state whose SURVIVAL is meaningful — the
underlying assignment they produce is already durability-tested via
`accept_assignment`/`create_collection_request`.

**Durability-applicable (33):**

*DUR3 — simple, standalone group, write the sentinel + restart + read back
(mechanical, same shape as `create_collection_request`):*
`cancel_journey`, `configure_laboratory_acceptance`, `configure_capacity`,
`create_connection`, `create_location`, `create_sample_type`, `create_user`,
`decline_assignment`, `merge_journeys`, `record_laboratory_exception`,
`reroute_journey`, `set_connection_status`, `set_location_function_status`,
`set_location_status`, `set_worker_availability`, `split_journey`,
`update_location`, `update_sample_type`, `update_user_access`,
`ingest_presealed_sample`, `report_disruption` (21; `create_collection_request`
already done separately, bringing this bucket to 22 write-kind capabilities
total).

*DUR3, lower priority (redundant with an already-wired check):*
`frost_cooled_route_enforcement` — its own positive probe already calls
`create_collection_request` internally; its durability story is the same
sentinel `create_collection_request` already durability-tests. Wire it only
if time allows; skipping it costs nothing not already covered.

*DUR4 — harder, needs individual judgment (12):* the 8 capabilities sharing
`group=lifecycle_drive` (`accept_assignment` is its own group but belongs in
this same batch of reasoning) — `accept_assignment`,
`collect_and_seal_sample`, `confirm_handoff`, `confirm_laboratory_receipt`,
`edit_active_plan`, `initiate_handoff`, `record_handling_checkpoint`,
`record_worker_location`, `update_assignment_progress` (9) — plus
`qualified_collector_exhaustion_exception` (durable exception-marking on a
sample, standalone group). These need thought about WHAT state a mid-drive
restart should be checked against (the shared journey, not a fresh one) and
whether the shared-group's single-instance-per-group isolation model
tolerates a restart at all mid-drive — that's the actual novel design
question DUR4 has to answer, not present in DUR3's mechanical cases.

## Totals

13 (read/artifact) + 3 (ephemeral) + 3 (fairness) + 33 (applicable, incl.
`create_collection_request` already done) = 52. ✓
