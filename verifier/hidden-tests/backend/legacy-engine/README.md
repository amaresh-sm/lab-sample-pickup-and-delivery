# Pinned-contract grading engine (phase 1)

Grades a candidate app's implementation of the pinned Lab Sample
Pickup & Delivery contract (`tasks/public/contracts/openapi.contract.json`, 45
operations) purely over HTTP. It never reads a database directly: every
capability writes through the app's API and reads the effect back through
the app's OWN read endpoints (direct read -> audit-trail search ->
operations-state read), which is the same pattern that scored a Postgres
golden and a Mongo golden identically (1.000/23) earlier this session.

## Layout

```
engine/
  client.py            HTTP client, keyed by the pinned contract's operationIds.
                        World: a fixture builder (locations, connections, sample
                        types, users, capacity) used by every capability's setup.
  oracle.py             The grading engine: Capability, CapabilityReport, Verdict,
                        Engine.observe() (the ranked read-back fallback).
  run.py                CLI: `score`, `durability-write`, `durability-verify`.
  capabilities/
    auth_roles.py        authenticate_user, select_active_role, create_user,
                          update_user_access, read_user                        (5)
    locations.py          create_location, update_location, set_location_status,
                          set_location_function_status, create_connection,
                          set_connection_status                                (6)
    sample_types.py       create_sample_type, update_sample_type,
                          configure_laboratory_acceptance                      (3)
    serviceability.py     check_serviceability, read_serviceability_snapshot   (2)
    requests.py           create_collection_request, ingest_presealed_sample   (2)
    capacity.py           configure_capacity, set_worker_availability          (2)
    custody_lifecycle.py  the 11-step custody/plan-edit/disruption chain, plus
                          decline_assignment                                   (12)
    disruption.py         cancel_journey, reroute_journey, split_journey,
                          merge_journeys                                        (4)
    tracking_audit.py     read_sample_state, read_sample_lifecycle,
                          read_journey_plan, read_operations_state,
                          search_audit_trail, list_role_work, read_ui_bootstrap,
                          stream_realtime_events, system_readiness              (9)
                                                                          total = 45
```

One file per capability *domain* (not one file per operation): the 45
operations cluster into ~9 natural domains that share fixtures (a
"basic serviceable network" builder, a "lifecycle world" builder, ...), and
one file per operation would mean copy-pasting that fixture setup 45 times.
Splitting by domain keeps each file's `World`/session reuse visible without
one 3000-line `specs.py`.

Grep this package for `psycopg`, `pymongo`, `sqlite3`, `mongo://`, or any
driver import: there should be zero hits. The only thing `client.py` talks to
is the application's HTTP API.

## Capability spec shape

```python
CAPABILITY = {
    "id": "<operationId, must match the pinned contract exactly>",
    "kind": "write" | "read" | "lifecycle",
    "setup": [...],           # prior calls to reach a valid precondition
    "operation": {...},
    "observe": [...],         # ranked list of HTTP read-backs, never a DB query
    "assert": [...],
    "negative_probes": [...], # invalid variants that must be REJECTED, no state change
    "idempotency_probe": {...},
    "durability_probe": {...},
}
```

is the shape this task specified, and this engine's `oracle.Capability` /
`CapabilityReport` keep exactly that five-phase structure and vocabulary
(`positive` = operation + observe + assert, `negative`, `idempotency`,
`durability`). The one deliberate change: **`setup`/`operation`/`assert` are
one Python function (`Capability.run(engine) -> CapabilityReport`), not a
declarative list of dict steps with `$ref` captures.**

Why: several of this contract's real preconditions are inherently
sequential/conditional, not "call A, capture field X, call B" -- e.g.
"discover which worker an offer actually landed on" (fairness is the app's
choice, not ours), "read the current `planVersion` before editing", "try the
happy path, branch on whether it came back serviceable". A static
declarative list either can't express that, or turns into a tiny bespoke
interpreter anyway -- at which point writing it as a plain function is less
code, easier to read, and (crucially, see below) is what actually fixed the
cascade bug. `run()` still performs setup -> operation -> observe -> assert
-> probes, in that order, every time; it is the same five phases, just
written directly instead of through a second layer of interpretation.

A `Capability` also carries an optional `durability_write` /
`durability_verify` pair (see "Durability probes" below) -- the fifth phase
from the spec sketch, split across a real process restart.

## The cascade problem, and how this fixes it

The old verifier graded the custody chain as one unbroken sequence, indexing
`activeAssignmentIds[0]` = "the collection leg" and `[1]` = "the transport
leg". That is a **harness assumption about plan shape**, not a fact the
contract guarantees -- a correct app is free to plan a different number of
assignments, or to offer the collection leg to whichever worker its own
fairness algorithm currently favours. When that assumption was wrong (one
golden plans 3 assignments, not 2), the harness's own indexing broke, and
because everything downstream was chained off `assigns[1]`, roughly 15
capabilities were zeroed for a reason that had nothing to do with the app
under test.

`capabilities/custody_lifecycle.py`'s `_classify_assignments()` never indexes
into `activeAssignmentIds`. It asks the app: for each assignment id, scan
`list_role_work` across every worker we seeded for the relevant field roles,
and use whichever alias's `pending`/`inProgress`/`completed` bucket actually
contains that assignment id (this mirrors golden-mongo's own verification
   helper, `World.find_offered` in `app-setup/api_client.py` -- the same
discovery idea, generalised to grading rather than to golden's own
self-checks). The chain is then driven using the ids and aliases the app
itself produced. A step still earns 0 credit if it genuinely fails; the fix
only removes the FALSE zeros that came from the harness's own shape
assumptions, which is exactly what was asked for.

`record_laboratory_exception`, `report_disruption`, `decline_assignment`, and
the four operations in `disruption.py` each get their **own** fresh,
self-contained journey rather than sharing the happy-path drive, because they
are terminal/alternate branches that would otherwise corrupt it (you cannot
both receive AND except the same sample; disrupting the happy path's only
connection would reroute -- or expire -- the very sample the other 8 steps
depend on). `report_disruption`'s scenario goes further than a bare 2xx
check: it revokes a standby worker's role before disrupting the primary
route, then reads the replacement plan back and asserts the now-ineligible
worker is NOT on it -- proving the replan re-validated eligibility rather
than copying the old plan verbatim (AC-61), which is the exact bug class this
session found in a sibling app.

## Durability probes

A true durability check spans an application **restart**, i.e. two separate
process lifetimes, which a single `Capability.run()` call cannot express. A
capability that wants one declares:

```python
Capability(
    "create_collection_request", ...,
    durability_write=lambda engine: {...sentinel...},   # called BEFORE restart
    durability_verify=lambda engine, sentinel: Verdict(...),  # called AFTER restart, HTTP-only
)
```

`run.py durability-write --statefile f.json` calls every declared
`durability_write`, persisting the returned sentinels; you then restart the
app under test (start-only -- **never** `reset`); `run.py durability-verify
--statefile f.json` re-authenticates and calls each `durability_verify`,
which must re-observe the sentinel through `engine.observe()` (direct read ->
audit-trail -> operations-state) exactly like the proven
`durability_grade.py` / `durability_probe.py` prototypes this engine
generalises. Only `create_collection_request` has this wired in phase 1
(`requests.py`) as the worked example; wiring the rest is mechanical and left
to the anchor/mutant phase, since durability-across-restart was already
validated as a general mechanism earlier this session (see
`db-state-oracle-phase1` in project memory) and is not this phase's focus.

## Adding a capability

1. Pick (or create) the domain file it belongs with.
2. Write `_run_<operation_id>(engine) -> CapabilityReport`: build a `World`
   (`engine.new_world("tag")`), do the setup calls, call the operation,
   observe with `engine.observe(session, sentinel, direct=(op_id, path_param),
   entity_type=...)`, and return a `CapabilityReport(positive=..., negative=...,
   idempotency=...)`.
3. Append a `Capability(...)` to the module's `CAPABILITIES` list, `id` equal
   to the exact pinned `operationId`. `run.py` discovers it automatically and
   fails loudly (`duplicate capability id`) if two modules claim the same id.
4. Ground every assertion in the pinned contract's exact field/error-code
   names (`tasks/public/contracts/openapi.contract.json`) and, where one exists, the
   matching row in `verifier/qc-pinned-contracts/ACCEPTANCE.md` -- never
   invent a field name; if in doubt, mine golden-mongo's
   `backend/internal/service/*_test.go`.

## How anchors/mutants will plug in (next phase, not built here)

This phase only builds the oracle (spec -> credit against a live app). The
next phase's hollow/mutant anchors are expected to be additional
**applications** run through the *same* `run.py score --base-url ...`
unchanged -- a hollow app should score near 0 because its writes don't
survive `engine.observe()`'s ranked fallback, and a mutant (a known bug
planted in golden) should selectively fail exactly the capability whose
`negative_probes`/`assert` encode the behaviour that bug breaks. No mutant
harness is implemented in this phase.

## Running it

```bash
python3 run.py score \
  --base-url http://127.0.0.1:8080 \
  --admin-user admin --admin-pass admin123 \
  --verbose --json-out /tmp/score.json
```
