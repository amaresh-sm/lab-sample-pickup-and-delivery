# Mutant: replan-copies-verbatim — the highest-value mutant

**Real defect (reproduced from the sibling golden-postgres app):**
`reportDisruption`'s replan path (`internal/service/plan_changes.go`) no
longer calls the shared `replaceJourneyRoute` (which genuinely recomputes the
route via `findRoute`, re-derives worker offers via `fairWorker`, and
re-validates the whole candidate plan via `validateJourneyPlan`). Instead it
calls a new `copyJourneyRouteVerbatim`, which clones the disrupted journey's
stops and assignments onto a new journey id **with the same workers, same
route, same ETA** — no eligibility recheck, no capacity recheck, no deadline
recheck, ever. `edit_active_plan`'s `reroute` branch and the standalone
`reroute_journey` operation are untouched (they still call the real
`replaceJourneyRoute`) — this mutation is scoped exactly to
`report_disruption`, mirroring the golden-postgres defect precisely.

## Apply / revert

```bash
cd verifier/qc-pinned-contracts/reference-solutions/golden-mongo
git apply /path/to/anchors/mutants/replan-copies-verbatim/mutation.patch
git checkout -- backend/internal/service/plan_changes.go   # revert
```

## Reproduce the bug (raw HTTP, via the engine's client library)

```python
world.user("solo_courier", ["delivery_partner"], worker=True)   # the ONLY eligible delivery_partner
created = admin.call("create_collection_request", body)
journey_id = created["journeyId"]

before = admin.data(*admin.raw("read_journey_plan", {"journeyId": journey_id}))
# before["assignedWorkerIds"] == [solo_courier_id]

admin.call("update_user_access", {"userId": solo_courier_id, "roles": ["requester"], "active": True})  # revoke role

data = admin.data(*admin.raw("report_disruption", {
    "resourceType": "connection", "resourceId": net.connection["connectionId"],
    "reason": "demo", "occurredAt": ..., "idempotencyKey": "...",
}))
replacement_id = data["replacementPlanIds"][0]

after = admin.data(*admin.raw("read_journey_plan", {"journeyId": replacement_id}))
# after["assignedWorkerIds"] == [solo_courier_id]   <- STILL there, role already revoked
# after["estimatedDeliveryAt"] == before["estimatedDeliveryAt"]  <- identical, never recomputed
```

Verified this session exactly as above: `solo_courier` (role revoked to
`["requester"]` before the disruption) is **still** the assigned worker on
the replacement plan, and the replacement's `estimatedDeliveryAt` is
byte-identical to the pre-disruption plan's — direct proof the replan copied
rather than recomputed.

## Engine result (measured this session)

```
overall score: 1.0000   (UNCHANGED from clean golden-mongo)
```

### Verdict: NOT CAUGHT — and this is the single most important finding in
### this whole exercise.

`report_disruption`'s own capability (`custody_lifecycle.py`,
`_run_report_disruption`) is *specifically designed* to catch exactly this
class of bug (its docstring cites "AC-61 ... proving the replan re-validated
eligibility rather than copying the old plan verbatim"). It:

1. Seeds a `standby` delivery_partner worker.
2. Plans a request.
3. Revokes `standby`'s role.
4. Disrupts the route and reads the replacement plan.
5. Asserts `standby`'s user id is **not** in the replacement's
   `assignedWorkerIds`.

**This check is vacuous as written.** Instrumented this session
(`_seed_lifecycle_world` + the exact `_run_report_disruption` setup,
querying `read_journey_plan` immediately after planning and *before* any
role revocation):

```
assignedWorkerIds BEFORE role revocation: ['252828c6...', '414de71b...', 'e828b01b...']
standby is on the plan BEFORE: False
```

`standby` was **never** the worker the app's own fairness algorithm actually
picked for the transport leg — `courier`/`courier2` (seeded earlier in the
same `_seed_lifecycle_world` call, and NOT parked before `standby` is
created) win the offer instead. So `standby not in assignedWorkerIds` was
already true before the disruption, before the revocation, before ANY
replan logic ran at all. The assertion is checking a precondition that was
never true, and it will read as "passing" whether the replan is genuine
(golden, clean) or a wholesale verbatim copy (this mutant) — which is exactly
what was measured: identical 1.0000 score on both.

**Fix for the next phase:** force the precondition instead of hoping for it
— e.g. park every OTHER delivery_partner (mirroring
`engine.park_field_workers`) before creating the plan so `standby` is
*guaranteed* to be the worker actually offered the transport leg, then
assert that precondition explicitly (fail loudly, don't silently pass, if
`standby` isn't on the plan before revocation). Only then does "not on the
plan after" mean anything.
