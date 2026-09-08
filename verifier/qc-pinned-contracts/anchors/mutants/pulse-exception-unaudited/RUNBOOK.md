# Mutant: pulse-exception-unaudited

**Real defect:** `expirePulseWithoutWorker` (`internal/service/maintenance.go`,
called when a Pulse assignment's offer expires and no other eligible worker
exists) drops its `state.AddAudit(...)` call. The sample record itself still
flips to `status: "exception"`, and the realtime event still fires, but the
outcome is no longer searchable in the audit trail — no
`sample_exception_recorded` entry is ever written.

## Apply / revert

```bash
cd verifier/qc-pinned-contracts/reference-solutions/golden-mongo
git apply /path/to/anchors/mutants/pulse-exception-unaudited/mutation.patch
git checkout -- backend/internal/service/maintenance.go   # revert
```

## Reproduce the bug (needs real wall-clock time)

Start the container with a short offer TTL (minimum allowed is 15s) and fast
maintenance ticks:

```bash
docker run -d --name pinned-golden ... \
  -e MAINTENANCE_INTERVAL_MILLISECONDS=1000 \
  -e ASSIGNMENT_OFFER_TTL_SECONDS=15 \
  lab-sample-pickup-delivery-gym:local bash -lc './verification/start.sh'
```

Create a **Pulse**-named sample type (forces
`qualifiedCollectorRequired=true`, `requiredQualification="LX-P"|`) with
exactly one qualified collector, one delivery_partner, and one
laboratory_team worker (Pulse's synchronous planning requires an eligible
worker for every leg or it 409s `pulse_no_wait` up front). Plan the request,
then wait past the 15s offer TTL with nobody to accept it:

```python
time.sleep(40)
# sample state -> "exception" (expirePulseWithoutWorker fired)
# but search_audit_trail for that sampleId has NO sample_exception_recorded entry
```

Verified this session: sample transitioned to `status: exception`; the
sample's audit-trail entries stopped at `journey_planned` — no
`sample_exception_recorded` entry exists.

## Engine result (measured this session)

```
overall score: 1.0000   (UNCHANGED from clean golden-mongo)
```

**Verdict: NOT CAUGHT — same coverage-gap category as
`orphan-assignments-on-expiry`.** No capability creates a Pulse request with
a single eligible worker and lets their offer expire; the entire
Pulse-offer-expiry code path (and its audit obligation) is untouched by any
spec in the current 45-op suite.

---

## UPDATE 2026-08-06 -- gap CLOSED, mutant now CAUGHT

The `NOT CAUGHT` verdict above is **stale**. The Pulse-offer-expiry path is now
exercised, and the isolated gate catches this mutant:

```
overall=0.9712
  qualified_collector_exhaustion_exception  credit=0.000
  search_audit_trail                        credit=0.500
```

Same two-capability cluster as `exhaustion-exception-missing`, which is coherent
rather than a cascade: both mutants disable the no-qualified-collector exception
(one removes the exception, one removes its audit record) and both capabilities
assert it must fire AND be searchable in the audit trail.
