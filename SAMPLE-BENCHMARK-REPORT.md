# Sample benchmark report

A SAMPLE, illustrative only -- not an authoritative or exhaustive scoreboard.
Scores produced by the pinned-contract verifier in this package (`./verify`),
aggregated by `./benchmark`. These are the maintainer's own reference runs
across a range of models and reasoning efforts, kept as a static snapshot so
the numbers persist independently of any local `./benchmark` run (which
regenerates `benchmark.md` from whatever is in `runs/`).

| Model | Effort | Gen (s) | Tokens | Score (max) | P/Part/F (core) | Runs | Per-run scores | Notes |
|---|---|--:|--:|--:|:--:|:--:|---|---|
| claude-opus-4-8 | max | 5340 | 1051980 | 0.7256 | 24/10/5 | 3 | 0.726 / 0.686 / 0.708 |  |
| gpt-5.6-sol | xhigh | 3613 | 512491 | 0.6034 | 16/13/10 | 3 | 0.603 / 0.603 / 0.603 |  |
| gpt-5.6-luna | xhigh | 2488 | 498256 | 0.5513 | 15/11/13 | 3 | 0.551 / 0.504 / 0.550 |  |
| gpt-5.6-terra | high | 929 | 185248 | 0.5120 | 13/13/13 | 3 | 0.512 / 0.407 / 0.496 | flaky -- 0.41-0.51 across 3 runs (candidate's own routing nondeterminism) |
| claude-sonnet-5 | medium | 2404 | 473588 | 0.5060 | 14/12/13 | 3 | 0.504 / 0.501 / 0.506 |  |
| glm-5.2 | - | 2434 | 479498 | 0.4615 | 14/5/7 | 3 | 0.462 / 0.462 / 0.462 |  |
| gpt-5.6-sol | low | 1047 | 179547 | 0.3667 | 6/11/9 | 3 | 0.367 / 0.367 / 0.367 | throttled -- HTTP 429 mid-generation -> partial app |
| gpt-5.6-luna | high | 1717 | 338735 | 0.2949 | 4/11/24 | 2 | 0.295 / 0.295 |  |
| claude-haiku-4-5 | low | 934 | 183998 | 0.0308 | 0/0/0 | 3 | 0.031 / 0.031 / 0.031 | SETUP-FAIL: 39/39 core caps unassessable (score NOT comparable) |

- **Score** = `80% x Core_mean + 20% x Supporting_mean` over that run's capability credits, then the
  BEST (max) run is shown as the headline number -- a candidate is judged on its ceiling, not diluted
  by an unlucky run. **Core** = the core transactional flow (lifecycle & admin, routing/serviceability,
  handoff & fairness, disruption & replanning). **Supporting** = observability reads/exception-
  visibility, realtime/audit/UI-support, and contract/operability.
- **P/Part/F** = fully passed / partial / assessed-zero, counted over the CORE capabilities of the run
  that produced the max score (Core carries 80% of the weight, so it's the more informative split).
  Capabilities whose setup could not be exercised are NOT counted as fails -- flagged in **Notes**
  (`SETUP-FAIL`) instead.
- **Runs** = how many times this candidate was scored; **Per-run scores** shows every run's weighted
  score, oldest→newest -- read a flaky candidate as that whole spread, not just the max headline.
- **Tokens** are provider-measured where available; runs whose CLI reports no usage totals are
  estimated from generation time (~197 tok/s, the median of the measured runs).
- To reproduce on your own runs: `./generate …` → `./verify …` → `./benchmark` (writes `benchmark.md`).
