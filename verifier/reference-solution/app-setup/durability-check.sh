#!/usr/bin/env bash
# AC-75: durable state survives an ordinary application restart.
#
#   write over HTTP -> stop the application process -> run app-setup/start.sh
#   again (MongoDB untouched, reset.sh NOT run) -> read the same data back.
#
# Set DURABILITY_SKIP_RESET=1 to keep whatever is already in the database; by
# default the script resets ONCE before writing anything so worker-fairness is
# deterministic. Nothing is reset after the write.
set -euo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$WORKSPACE_ROOT"
: "${MONGODB_URI:?MONGODB_URI is required}"
: "${ADMIN_USERNAME:?ADMIN_USERNAME is required}"
: "${ADMIN_PASSWORD:?ADMIN_PASSWORD is required}"

exec python3 app-setup/durability_check.py "$@"
