# Mutant: exhaustion-exception-missing

**Defect (AC-132):** when no qualified collector is ever available for a
collection that requires one, the system no longer fires the distinguishable
`no_qualified_collector_available` exception within the reasonable-time
window. The sample just sits pending until its ordinary delivery deadline, so
the outcome is NOT distinguishable from a missed deadline.

The distinct exception is produced by `recordNoQualifiedCollectorException`
(`backend/internal/service/maintenance.go`), called from two places. This
mutant neuters BOTH:

- **Call site A** — the `next == "" && required` branch in `Maintain`'s
  offer-expiry loop (fires when a re-offer search comes up empty for a
  qualified-collector-required assignment that WAS offered once): removed.
- **Call site B** — `expireUnassignedQualifiedCollectionSearches` (fires when
  an assignment has required a qualified collector since creation and has
  NEVER once had an eligible worker, after the search timeout): made a no-op.

A no-collector sample is now left pending and only ever resolves later via the
generic `expireSample(..., "delivery_deadline_elapsed")` path.
`recordNoQualifiedCollectorException` remains defined but unreferenced (dead
code) — Go tolerates unused package functions, so it compiles cleanly.

IMPORTANT: this is NOT a reason-string change. The capability deliberately
does not assert a specific reason value (to avoid golden-bias); it detects
that the DISTINCT EXCEPTION NEVER FIRES within the reasonable-time window
(status never becomes `exception` well before the 12h delivery deadline).

## Apply / revert

Full-repo-relative path → apply with `-p5` from the golden root:

```bash
cd verifier/qc-pinned-contracts/reference-solutions/golden-mongo
git apply -p5 /path/to/anchors/mutants/exhaustion-exception-missing/mutation.patch
# ... rebuild, test ...
git checkout -- backend/internal/service/maintenance.go   # revert
```

NEVER apply to the real golden. Copy to a scratch dir and apply there.

## Build + boot (isolated Docker, fast timers)

```bash
docker run --rm -v "$SCRATCH:/workspace" -w /workspace/backend \
  lab-sample-pickup-delivery-gym:local bash -lc \
  'go build ./... && \
   go build -trimpath -o /workspace/bin/lab-golden-server ./cmd/server && \
   go build -trimpath -o /workspace/bin/lab-golden-schema ./cmd/schema'

docker run -d --name mut2-exhaustion -p 19102:18082 \
  -v "$SCRATCH:/workspace" \
  -e ADMIN_USERNAME=admin -e ADMIN_PASSWORD=admin123 \
  -e APP_ADDR=0.0.0.0:18082 \
  -e MONGODB_URI=mongodb://127.0.0.1:27017/arena \
  -e MAINTENANCE_INTERVAL_MILLISECONDS=1000 \
  -e ASSIGNMENT_OFFER_TTL_SECONDS=120 \
  -e UNASSIGNED_SEARCH_TIMEOUT_SECONDS=60 \
  lab-sample-pickup-delivery-gym:local \
  bash -lc 'cd /workspace && bash verification/reset.sh && bash verification/start.sh'
```

`go build ./...` compiles: **confirmed**.

## Run the engine

The engine waits `UNASSIGNED_SEARCH_TIMEOUT_SECONDS` (+ buffer) before reading
the sample back, so it MUST be set to match the container (else it defaults to
3600s and hangs):

```bash
UNASSIGNED_SEARCH_TIMEOUT_SECONDS=60 ENGINE_MAINTENANCE_INTERVAL_SECONDS=1 \
  python3 run.py --base-url http://127.0.0.1:19102 \
  --admin-user admin --admin-pass admin123 score --verbose
```

## Engine result (measured this session)

```
capabilities graded: 52
overall score:       0.9519
```

Capabilities that dropped credit (mutation-attributable):

```
qualified_collector_exhaustion_exception   credit=0.000   (PRIMARY TARGET)
    waited 65s; sample.status='planned' (never 'exception'); discovered_reason=None;
    reason_searchable_via_query=False -- the Indigo sample sat pending via call site B's
    neutered search-timeout, so the distinguishable exception never fired.
search_audit_trail                          credit=0.500
    its pulse-exception sub-probe traverses call site A (a Pulse offer expiring with no other
    eligible worker); with A removed the sample never becomes 'exception', so became_exception
    fails. RBAC/search dimensions still pass -> 0.500.
```

`openapi_json_well_formed` also read 0.000 — same ENVIRONMENTAL local-filesystem
artifact miss as the other mutant (engine run from the host with no `WORKSPACE`
set), NOT a catch. See `fairness-ignored/RUNBOOK.md`.

**Verdict: CAUGHT, primary target `qualified_collector_exhaustion_exception`
zeroed.** `search_audit_trail`'s partial drop is a COHERENT cluster, not a wild
cascade: both capabilities assert that the no-qualified-collector exception
must FIRE, and the mutation removes the exception's occurrence from BOTH shared
call sites, so both the Indigo (call site B) and Pulse (call site A) paths stop
producing it. This is the intended consequence of "the distinct exception never
fires anywhere". All fairness and lifecycle capabilities pass.

## Distinct from `pulse-exception-unaudited`

Genuinely different defects:

- **This mutant** removes the exception's OCCURRENCE — the sample never becomes
  `exception` at all (measured `sample.status='planned'`); it only resolves
  later as a generic delivery-deadline expiry. Code change: delete the two
  CALL SITES in `maintenance.go`.
- **`pulse-exception-unaudited`** keeps the occurrence — the sample record DOES
  flip to `exception` and the realtime event still fires — and removes only the
  durable AUDIT record (`state.AddAudit`) INSIDE
  `recordNoQualifiedCollectorException`.

Different code, different observable: occurrence-gone vs audit-gone.
