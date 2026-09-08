#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
: "${MONGODB_URI:?MONGODB_URI is required}"

SCHEMA_BINARY="$WORKSPACE_ROOT/bin/lab-golden-schema"
if [[ ! -x "$SCHEMA_BINARY" ]]; then
  echo "schema binary is missing; run the manifest build command first" >&2
  exit 1
fi

exec "$SCHEMA_BINARY" --drop
