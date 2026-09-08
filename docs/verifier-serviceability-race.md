# Verifier flakiness: the serviceability-recompute race (and its cascade)

_Status: root-caused + fix implemented 2026-08-03; validation (golden + Opus×3) in progress at time of writing._
_This file lives under `/docs`, which is gitignored — it's a local engineering note, not shipped._

## TL;DR

The backend verifier is **non-deterministic** in the journey/lifecycle/serviceability/
fairness cluster. The same candidate scores differently run-to-run because of a
**race condition in the verifier** (not in the candidate apps): capability seeds add a
collection node and then plan a request against it *immediately*, before the app has
published the serviceability calculation that includes that new node. On apps that
publish serviceability **asynchronously** (which the spec explicitly permits, AC-40),
the request intermittently reads `unserviceable`, no journey forms, and **~14 downstream
capabilities cascade to 0**.

Fix: before planning a happy-path request, poll `check_serviceability` until the app
confirms the new node is serviceable (`World.await_serviceable`). This adds the missing
synchronization so the recompute can never be raced.

## Symptom

- Opus-4.8-max backend: **0.5192 on run 1, 0.6314 on run 2 — same app, no changes.**
- **14 capabilities flipped between the two runs, in both directions** (e.g.
  `collect_and_seal 0→1.0`, `confirm_handoff 0→1.0`, `read_journey_plan 0→1.0`,
  `merge_journeys 0→1.0`; and `reroute_journey 1.0→0`, `create_collection_request
  1.0→0.667`).
- ~14 of the 19 zero-credit caps on run 1 carried the identical detail:
  `"candidate produced no serviceable journey/assignments for a network golden plans
  (serviceability=unserviceable, journeyId=False, assignments=0)"`.

## Why it is the VERIFIER, not the app (three independent proofs)

1. **It hits nearly every candidate, not just Opus.** The same cascade caps zero out
   across apps on the same verifier:
   | Candidate | overall | cascade caps (of 12) |
   |---|---|---|
   | Sol | 0.609 | 5 zero |
   | Terra | 0.381 | 11 zero |
   | Luna | 0.308 | 12 zero |
   | Opus run 1 | 0.519 | 12 zero |
   | Opus run 2 | 0.631 | 3 zero |
   An app bug would not hit almost every app identically.

2. **The app plans it perfectly in isolation.** Driving Opus directly with the exact
   lifecycle seed (`engine/_svc_probe.py`) returns `serviceable=True`, a `journeyId`, and
   3 assignments — for the same scenario the full run recorded as `unserviceable`.

3. **It's flaky on re-run with no app change** (see the run1→run2 flips above). A
   deterministic app defect would reproduce; this doesn't.

## Root cause: two unsynchronized timelines

Each capability builds its own world through the app's admin API, then plans:

```
verifier: create_location(pickup, [collection_point])   # adds a new collection node
   app  : ...begins (re)computing the serviceability calculation, async...
verifier: create_collection_request(origin = that pickup)   # plans IMMEDIATELY
   app  : the *published* serviceability version is still the previous one,
          which does not yet contain `pickup` → responds "unserviceable"
```

The app's background recompute and the verifier's next API call are **independent
timelines with no synchronization** — a classic race. Whoever wins ("did the recompute
publish before the verifier planned?") decides the verdict, and it varies run to run.

### Why it's spec-compliant app behavior (so the verifier is wrong to race it)

instruction.md / AC-40:

> The network's serviceability calculation must be published as one complete
> serviceability-calculation version at a time. While a new version is being computed,
> the system must keep serving the previous complete version, so that no request is
> checked against a partial or in-progress calculation.

So a just-added node being briefly not-yet-serviceable is **correct**. A verifier that
plans immediately is *assuming synchronous recompute* — that assumption is the bug.

### Why the zero-count varies by app (Sol 5, Terra 11, Luna 12, Opus 12/3)

It's the app's recompute latency vs the verifier's next call:
- Synchronous/inline recompute (node added → instantly in the published version) → wins
  the race almost always → few zeros (Sol).
- Async/versioned recompute (a publish lag window) → loses more often → more zeros
  (Terra/Luna/Opus).

Perverse consequence: **the verifier penalized the apps that implemented AC-40's
async/versioned serviceability most faithfully.**

## The cascade mechanism (1 failure → ~14 zeros)

The 9 custody/lifecycle capabilities grade a single **memoised** driven journey
(`custody_lifecycle._drive_happy_path`): plan → accept → collect+seal → checkpoint →
worker-location → handoff (both sides) → progress → lab receipt. If that one plan fails,
the verifier (by design) returns a clean "candidate-side planning failure" verdict = 0
for every capability that needs the journey, rather than crashing. Disruption caps
(cancel/reroute/split/merge/report_disruption) build on a planned journey too, and
fairness caps need assignments to form — so all of them cascade off the same upstream
plan.

## The fix

`verifier/qc-pinned-contracts/engine/client.py` — new shared primitive:

```python
World.await_serviceable(body, *, tries=30, delay=0.2) -> bool
# Poll check_serviceability with the SAME request shape (idempotencyKey stripped —
# a dry run creates nothing) until serviceable, or ~6s timeout. Proceeds on timeout so
# genuinely-unserviceable cases are still exercised.
```

Applied at the **happy-path planning sites only**:
- `capabilities/custody_lifecycle.py` — new `_plan_serviceable(...)` wraps the 5 lifecycle
  plan points (the memoised 9-cap journey).
- `capabilities/disruption.py` — wait added inside the shared `_plan(...)`
  (cancel/reroute/split/merge/report_disruption).
- `capabilities/fairness.py` — new `_fair_plan(...)` wraps the workload/recency/
  distribution setup creates.

**Deliberately NOT applied** to the negatives — inverted-window (expects 400),
out-of-coverage (expects unserviceable), idempotency replay — those must plan
immediately and still fail as intended.

## Residual risks / follow-ups

- **Disruption body lacks window fields.** `disruption._plan` omits
  earliest/latestCollectionAt; if `check_serviceability` requires them, the poll can't
  confirm serviceable and will time out (~6s) then proceed — ineffective for those caps.
  Watch the disruption caps in the Opus×3 validation; refine the probe body if they
  still flake.
- **Backstop: pass^k on the backend.** Even with the race removed, add k=3 aggregation
  (median/all-pass), mirroring the UI k=3 gate, so no single flaky roll sets a score.
- **`summarize-run` "100% deterministic" label is misleading** — the *engine* has no LLM,
  but *scenario execution against a live app* was demonstrably not deterministic. Reword
  once the fix + pass^k are in.
- **Re-score the whole leaderboard on the fixed engine.** Sol/Terra/Luna single-run
  backend scores (0.609 / 0.381 / 0.308) were all depressed by this cascade; Terra/Luna
  should rise materially. The current leaderboard is not trustworthy until re-scored.

## Artifacts

- Root-cause probe: `verifier/qc-pinned-contracts/engine/_svc_probe.py` (+ `verifier-logs/_svc_probe_run.sh`) — throwaway, drives an app and prints `serviceabilityReason`.
- Re-run evidence: `runs/v3-opus-4.8-max-*/verifier-logs/` (run 1) vs `.../verifier-logs-rerun/` (run 2).
- Fix validation: `verifier-logs/revalidate/` (golden + Opus×3).
