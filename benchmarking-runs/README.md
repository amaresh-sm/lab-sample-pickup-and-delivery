# Benchmarking Runs

This directory archives metadata-only benchmark evidence for every candidate listed in `scores.json`. Candidate workspaces are intentionally excluded.

Each run is stored under `runs/<model>/<reasoning>/<run-id>/` with standardized metadata, telemetry, trajectory, sanitized generation logs, verifier summary, and the exact score report.

Scores are copied only after matching the source candidate's `reports/score.json` against `scores.json`. A missing source telemetry or trajectory artifact is represented explicitly with `available: false`; no metrics are fabricated.

Sensitive credential-like fields and host-specific paths are redacted in archived event and gateway logs.
