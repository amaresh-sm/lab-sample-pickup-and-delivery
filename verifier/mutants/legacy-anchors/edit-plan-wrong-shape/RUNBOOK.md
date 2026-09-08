# Mutant: edit-plan-wrong-shape

**Real defect (AC-77):** `edit_active_plan`'s `reroute`/`replace_route`
changeType branch (`internal/service/plan_changes.go`) reverts to returning
only `{journeyId, status, valid, replacementJourney}` instead of the full
`JourneyPlan` shape every other branch of this same operation returns
(`planVersion`, `legs`, `orderedStops`, etc. all missing from the top level).
A caller can no longer read the plan version it needs for its next
optimistic edit without a separate `read_journey_plan` call.

## Apply / revert

```bash
cd verifier/qc-pinned-contracts/reference-solutions/golden-mongo
git apply /path/to/anchors/mutants/edit-plan-wrong-shape/mutation.patch
# ... rebuild, test ...
git checkout -- backend/internal/service/plan_changes.go   # revert
```

## Reproduce the bug (raw HTTP, via the engine's own client library)

Create a dispatcher + a serviceable journey with an alternate hub route
available, then call `edit_active_plan` with `changeType: "reroute"`:

```python
status, payload = dispatcher.raw("edit_active_plan", {
    "journeyId": journey_id, "expectedPlanVersion": 1,
    "changeType": "reroute", "changes": {"reason": "demo"},
    "idempotencyKey": "...",
})
# payload["data"] == {"journeyId": ..., "status": "replaced", "valid": True,
#                      "replacementJourney": {... full nested plan ...}}
# -- no planVersion, no legs, no orderedStops at the TOP level.
```

Verified this session: status 201, and `data.keys()` was exactly
`{journeyId, replacementJourney, status, valid}` — confirmed missing
`planVersion`/`legs`/`orderedStops` at the top level (they only exist nested
inside `replacementJourney`, which describes the NEW journey, not a
plan-version handle on the edit that was just made).

## Engine result (measured this session)

```
overall score: 1.0000   (UNCHANGED from clean golden-mongo)
```

**Verdict: NOT CAUGHT. This is a critical, prioritized gap, not a soft
finding.** `edit_active_plan`'s only capability
(`capabilities/custody_lifecycle.py`) drives it with
`changeType: "replace_worker"` exclusively (see `_drive_happy_path`'s
`edit_active_plan` step). No capability in the current 45-op suite ever
calls `edit_active_plan` with `changeType: "reroute"` or `"replace_route"`.
`reroute_journey` (a *different* operationId, graded in `disruption.py`) has
its own, correctly-shaped response and is unaffected by this mutation, so it
provides no coverage for this branch either. The bug is real, demonstrated,
and completely invisible to the current suite.

---

## UPDATE 2026-08-06 -- gap CLOSED, mutant now CAUGHT

The `NOT CAUGHT` verdict above is **stale**. It was recorded when
`edit_active_plan` drove only `changeType: "replace_worker"`. A `reroute_shape`
sub-probe has since been added, and the isolated gate now catches this mutant:

```
overall=0.9904   edit_active_plan credit=0.500
  replace_worker: status=201 code=''
  reroute_shape:  status=201 missing_required_top_level=
                  ['planVersion','routeMode','sampleIds','orderedStops','legs']
```

Caught on exactly the fields this patch strips when it replaces
`journeyMap(state, journey)` with a bare four-key map. The negative dimension
(`stale_plan` on replay) still passes, hence 0.500 rather than 0.
