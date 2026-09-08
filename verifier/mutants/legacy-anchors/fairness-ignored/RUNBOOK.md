# Mutant: fairness-ignored

**Defect (AC-128/129/130):** the worker-offer algorithm ignores fairness.
`fairWorker` (`backend/internal/service/planning.go`, ~line 780) still
filters candidates for eligibility exactly as before (role, qualification,
location scope, availability, capacity), but its `sort.Slice` comparator no
longer ranks the eligible candidates by current active workload
(`activeWork`), same-role recent-offer count (`roleOffers`) or how recently
each was last offered work (`LastOfferedAt`). It now sorts ONLY by a
deterministic, stable key — `creationOrder`, then `Username`, then `ID` — so
the algorithm just grabs whichever eligible worker sorts first, regardless of
load or recency. This is a plausible candidate mistake (keep the eligibility
gate, drop the fairness weighting). It compiles cleanly (behavioural bug).

## Apply / revert

The patch path is full-repo-relative, so apply it with `-p5` from the golden
root (the convention the rebased `pulse-exception-unaudited` patch uses):

```bash
cd verifier/qc-pinned-contracts/reference-solutions/golden-mongo
git apply -p5 /path/to/anchors/mutants/fairness-ignored/mutation.patch
# ... rebuild, test ...
git checkout -- backend/internal/service/planning.go   # revert
```

NEVER apply to the real golden. Copy golden to a scratch dir first and apply
there; keep `reference-solutions/golden-mongo/` pristine at 1.0.

## Build + boot (isolated Docker, fast timers)

```bash
# 1. copy golden to scratch, apply the patch there, then:
docker run --rm -v "$SCRATCH:/workspace" -w /workspace/backend \
  lab-sample-pickup-delivery-gym:local bash -lc \
  'go build ./... && \
   go build -trimpath -o /workspace/bin/lab-golden-server ./cmd/server && \
   go build -trimpath -o /workspace/bin/lab-golden-schema ./cmd/schema'

# 2. boot an OWN-named container on an OWN port (never pinned-golden/pinned-mongo)
docker run -d --name mut1-fairness -p 19101:18082 \
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

## Run the engine (52 caps, --base-url = origin, no /api)

The engine's own timer-driven probes read matching env vars, so mirror the
container's fast timers in the engine's environment:

```bash
UNASSIGNED_SEARCH_TIMEOUT_SECONDS=60 ENGINE_MAINTENANCE_INTERVAL_SECONDS=1 \
  python3 run.py --base-url http://127.0.0.1:19101 \
  --admin-user admin --admin-pass admin123 score --verbose
```

## Engine result (measured this session)

```
capabilities graded: 52
overall score:       0.9231
```

Capabilities that dropped credit (mutation-attributable):

```
fairness_workload_disparity   credit=0.000   offer went to handler_loaded (18 active, uncompleted) instead of handler_fresh (0)
fairness_recency_disparity    credit=0.000   worker_a won both drives (stable creationOrder) -> cannot isolate recency; same worker always chosen
fairness_offer_distribution   credit=0.000   distribution={worker1:10, worker2:0, worker3:0, worker4:0}; worst=10 > loose bound 5
```

`openapi_json_well_formed` also read 0.000, but this is ENVIRONMENTAL, NOT a
catch: that capability is a pure local-filesystem check of
`$WORKSPACE/verification/openapi.json` (default `/workspace`), which is
absent on the host where the engine was run (no `WORKSPACE` set). It fails
identically for the other mutant and for clean golden run the same way; the
`openapi.json` in the scratch copy is byte-identical and untouched by this
backend-only mutation. Set `WORKSPACE=$SCRATCH` when running the engine to
clear it.

**Verdict: CAUGHT, by exactly the three intended fairness capabilities.**
The drop is a coherent cluster on `fairness_*` only — the eligibility gate is
untouched, so every non-fairness capability (which seeds a single eligible
worker per role, or discovers the owner from the app rather than assuming a
fair one) still passes. In particular `qualified_collector_exhaustion_exception`
PASSES here (sample.status='exception', reason 'no_qualified_collector_available'),
proving this mutant does not touch the exception path — the clean separation
from `exhaustion-exception-missing`.
