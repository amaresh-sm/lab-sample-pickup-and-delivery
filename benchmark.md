# Candidate benchmark

_Scored on the pinned-contract verifier, **`isolated` mode**, `Final Score = 10% x Contract + 70% x Blended Functional + 20% x NFR`, where `Blended Functional = 90% x Functional + 10% x UI` (Functional domain-weighted 80% Core / 20% Supporting; NFR equal-weighted across Durability / Idempotency / Concurrency-safety; UI equal-weighted across the 16 presentation-layer capabilities in `engine/ui_capabilities/`). Generated 2026-08-13 UTC._

> **`isolated` mode** gives every capability group a PRISTINE app (stop → reset → start →
> score that group only), instead of running all 52 against one accumulating instance.
> Under the old `shared` mode, field workers left behind by earlier capabilities won the
> offers a later capability's own worker needed, so it scored 0 on a correct app — and which
> leftovers were waiting depended on timing, which is what made scores irreproducible.
> Those runs also predate the `park_field_workers` fix, which selected workers by
> `action="user_created"` — golden's audit-naming convention, pinned nowhere in the
> contract — and therefore parked ZERO workers on 8 of 8 candidates while parking golden
> correctly. **Shared-mode scores are NOT comparable to these** and are excluded; set
> `BENCH_MODE=shared` (or `all`) to see them.
>
> **This pass** re-ran both the full backend verifier (`verify-isolated`) and all 16 UI
> capabilities (`run-ui`) fresh for every candidate. UI verifier includes four fixes applied
> this session: ARIA-role fallback in `sample_region()`, assignment-id fallback in work-list
> matching, content-based fallback for role-switch control detection, and `select_option`
> fallback in form-fill — all three verified against golden (stays 1.0000) and adversarial
> mutants (still caught). A new 16th UI capability (AC-113/AC-114 worker location write-through)
> was also added this session.

| Model | Effort | Gen (s) | Tokens | P/Part/F (core) | Functional | NFR | UI | Contract | Final Score |
|---|---|--:|--:|:--:|:--:|:--:|:--:|:--:|--:|
| claude-opus-4-8 | max | 5340 | 1051980 | 15/4/5 | 0.7587 | 0.9783 | 0.8125 | 0.9886 | **0.8294** |
| gpt-5.6-luna | xhigh | 2488 | 498256 | 10/6/8 | 0.6177 | 0.8889 | 0.4375 | 0.9942 | **0.6969** |
| gpt-5.6-sol | xhigh | 3613 | 512491 | 8/8/8 | 0.5720 | 0.7317 | 0.3438 | 0.9477 | **0.6256** |
| gpt-5.6-terra | high | 929 | 185248 | 5/13/6 | 0.5436 | 0.6875 | 0.4688 | 0.9667 | **0.6095** |
| claude-sonnet-5 | medium | 2404 | 473588 | 8/4/12 | 0.4789 | 0.7105 | 0.6250 | 0.9583 | **0.5834** |
| glm-5.2 | — | 2434 | 479498 | 4/7/13 | 0.3864 | 0.6667 | 0.1875 | 0.9848 | **0.4884** |
| gpt-5.6-luna | high | 1717 | 338735 | 1/7/16 | 0.2374 | 0.3871 | 0.2812 | 0.8793 | **0.3346** |
| gpt-5.6-sol | low | 1047 | 179547 | 13/18/8 | 0.5897 | 0.6728 | 0.1250 | 0.9375 | **0.6086** |
| claude-haiku-4-5-20251001 | low | 934 | 183998 | 0/0/24 | 0.0129 | 0.0000 | 0.1562 | 0.9062 | **0.1097** |

- **Final Score** = `10% x Contract + 70% x Blended Functional + 20% x NFR`, where
  `Blended Functional = 90% x Functional + 10% x UI` — UI carries a 10% share of the
  functional blend (7% of the overall score) because it is a presentation-layer check on top
  of the same backend the Functional column already scores; a candidate cannot buy a good UI
  score by itself, since a broken backend leaves most UI capabilities "not exercisable" (credit
  0) regardless of how the frontend renders. The BEST (max) run per candidate is shown as the
  headline number, not diluted by an unlucky run.
- **Functional** = `80% x Core_mean + 20% x Supporting_mean`, computed from
  ONLY positive/negative probes (idempotency and durability are excluded here — they now have their
  own share in NFR below, so folding them in here too would double-count them). **Core** = the core
  transactional flow (lifecycle & admin, routing/serviceability, handoff & fairness, disruption &
  replanning). **Supporting** = observability reads/exception-visibility, realtime/audit/UI-support.
- **P/Part/F** = fully passed / partial / assessed-zero, counted over the CORE capabilities of the run
  that produced the max score, using the SAME functional-only credit Functional is built from (Core
  carries 80% of Functional's weight, so it's the more informative split). Capabilities whose SETUP
  could not be exercised are NOT counted as fails.
- **NFR** = equal-weighted mean of Durability / Idempotency / Concurrency-safety (each 1/3, same
  equal-share convention Contract's own 4 sub-scores already use). Idempotency is wired for
  17 capabilities (every write op in the pinned contract with an idempotencyKey field, bar
  record_worker_location); Durability is wired for 32 capabilities (see `engine/DURABILITY.md`),
  denominator ALWAYS 32 regardless of how much a candidate leaves assessable; Concurrency is the
  mean credit of the 3 standalone concurrency-safety capabilities. A capability that could not be
  checked counts like a confirmed failure, not an exclusion, for all three.
- **UI** = equal-weighted mean credit across the 16 capabilities in `engine/ui_capabilities/scenarios/`
  (each a genuine browser-driven check: locate by accessible role/label/visible-text only, seed state
  over HTTP first, and for every WRITE action confirm the effect via a fresh HTTP re-read — never the
  UI's own success message). A capability whose seed could not produce the state it needs (e.g. no
  serviceable journey) scores 0 as "UI not exercisable," same convention as Functional/NFR.
- **Contract** = the run's raw `contract` score (build ok + reset/start health over the 45 isolation
  groups + response schema-conformance over the pinned operations exercised) — its 10%
  share of the blend above, shown separately so a low Contract score is visible even though it's a
  small enough share of Final Score to otherwise hide there.
- **Tokens** are provider-measured where available; claude/OpenCode runs report no usage totals, so
  those are estimated from generation time (~197 tok/s, the median of the measured runs).

**Caveats** (kept out of the table to match the requested column set, but load-bearing):
- † **gpt-5.6-sol (low)**: the model was throttled (HTTP 429) mid-generation, producing only a
  partial app — all scores above are equally affected.
- ‡ **claude-haiku-4-5 (low)**: `SETUP-FAIL` — 39/39 core capabilities unassessable on the
  backend (its own admin API returns 403 on basic seeding calls like `create_sample_type`), so
  Functional/NFR/UI are ALL floor-scored by the same root cause, not independent signals.
