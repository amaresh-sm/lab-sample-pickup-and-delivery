# Benchmarking Runs

This directory archives metadata-only benchmark evidence for every candidate listed in `scores.json`. Candidate workspaces are intentionally excluded.

Each run is stored under `runs/<model>/<reasoning>/<run-id>/` with standardized metadata, telemetry, trajectory, sanitized generation logs, verifier summary, and the exact score report.

Scores are copied only after matching the source candidate's `reports/score.json` against `scores.json`. A missing source telemetry or trajectory artifact is represented explicitly with `available: false`; no metrics are fabricated.

Sensitive credential-like fields and host-specific paths are redacted in archived event and gateway logs.

The cache backfill is recorded in `cache-backfill-dry-run.json`. Sixteen eligible runs now have
model-ratio-derived `cached_input_tokens` and `cache_read_tokens`; their independent estimated
costs were recalculated from the updated usage. Gateway-reported costs and scores were not changed.

`benchmarking-data.md` is generated from this archive. Its core source metrics exclude dependency
and build output, lockfiles, logs, database dumps/migrations, contracts, caches, and other generated
artifacts when calculating LOC and the largest file.
