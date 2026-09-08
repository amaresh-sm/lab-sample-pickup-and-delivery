# Mutant: orphan-assignments-on-expiry

**Real defect:** `expireSample` (`internal/service/maintenance.go`, called
from the timer-driven `Maintain()` pass when a sample's delivery deadline
elapses) marks the journey `cancelled` but no longer calls
`cancelUnfinishedAssignments` — the sample's still-pending/in-progress
assignments are orphaned: live, un-cancelled work items pointing at a
journey that is already dead, which jams later plan edits that assume every
assignment on an inactive journey has been resolved.

## Apply / revert

```bash
cd verifier/qc-pinned-contracts/reference-solutions/golden-mongo
git apply /path/to/anchors/mutants/orphan-assignments-on-expiry/mutation.patch
git checkout -- backend/internal/service/maintenance.go   # revert
```

## Reproduce the bug (needs real wall-clock time — no mocked clock in this app)

Start the container with faster timers so the demo doesn't take 10+ minutes:

```bash
docker run -d --name pinned-golden ... \
  -e MAINTENANCE_INTERVAL_MILLISECONDS=1000 \
  lab-sample-pickup-delivery-gym:local bash -lc './verification/start.sh'
```

Then (via the engine's client library): seed a network with 1-minute travel
time, park every other collection_professional so your seeded collector is
guaranteed the offer, plan a request whose `deliveryDeadline` is ~90s out
(long enough to be serviceable, short enough to expire quickly), and sleep
past it:

```python
# ... create request with deliveryDeadline = now + 90s ...
time.sleep(100)   # past the deadline + a maintenance tick
# sample state -> "expired"
# but: search_audit_trail / list_role_work for the collection assignment
# still shows it "pending" -- it was never cancelled.
```

Verified this session: sample transitioned to `status: expired`, and the
collector's `list_role_work` `pending` bucket still contained the collection
assignment (`assignmentId: 9e687cff...`, `status: "pending"`) tied to the
now-cancelled journey — the orphan, reproduced.

## Engine result (measured this session)

```
overall score: 1.0000   (UNCHANGED from clean golden-mongo)
```

**Verdict: NOT CAUGHT — a coverage gap, not a wrong-capability miss.** No
capability in the current 45-op suite creates a request with a near-term
deadline and waits for the timer-driven `Maintain()` pass to expire it; the
entire delivery-deadline-expiry code path is untouched by any spec. This is
the same category of gap as `pulse-exception-unaudited` below: real,
previously-found defects that live behind a background timer, which no
capability currently pays the (real, ~15–100s) wall-clock cost to exercise.

---

## UPDATE 2026-08-06 -- gap CLOSED, mutant now CAUGHT

The `NOT CAUGHT` verdict above is **stale**. A `deadline_expiry_orphan_probe`
has since been added to `list_role_work`, and it does pay the wall-clock cost
the note above says no capability was willing to pay. Isolated gate:

```
overall=0.9904   list_role_work credit=0.500
  deadline_expiry_orphan_probe: waited 96.0s past a 90s deadline;
    sample.state.status=expired journey.status=cancelled
    still_pending=True completed_entry_status=None
```

`still_pending=True` with no completed entry is exactly the consequence of the
patch removing `cancelUnfinishedAssignments(state, journey.ID)`. The negative
dimension (403 on an unheld role) still passes, hence 0.500.
