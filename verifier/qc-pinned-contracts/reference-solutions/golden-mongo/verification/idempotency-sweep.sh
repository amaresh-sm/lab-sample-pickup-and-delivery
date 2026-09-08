#!/usr/bin/env bash
# AC-74: a retried or duplicated request must not take effect twice.
#
# Replays every operation that accepts an idempotencyKey and asserts that the
# replay returns the original response and leaves exactly one effect behind.
# Run it against a started application:
#
#   ADMIN_USERNAME=... ADMIN_PASSWORD=... bash verification/idempotency-sweep.sh
#
# APP_BASE_URL overrides the origin, which otherwise comes from
# verification/manifest.json.
set -euo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$WORKSPACE_ROOT"
: "${ADMIN_USERNAME:?ADMIN_USERNAME is required}"
: "${ADMIN_PASSWORD:?ADMIN_PASSWORD is required}"

exec python3 verification/idempotency_sweep.py "$@"
