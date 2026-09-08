#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export APP_ADDR="${APP_ADDR:-127.0.0.1:18082}"
export FRONTEND_DIR="${FRONTEND_DIR:-$WORKSPACE_ROOT/frontend/dist}"
export OFFLINE_MAP_ASSET_DIR="${OFFLINE_MAP_ASSET_DIR:-/opt/arena/map-assets}"
: "${MONGODB_URI:?MONGODB_URI is required}"

SERVER_BINARY="$WORKSPACE_ROOT/bin/lab-golden-server"
if [[ ! -x "$SERVER_BINARY" ]]; then
  echo "server binary is missing; run the manifest build command first" >&2
  exit 1
fi
if [[ ! -f "$FRONTEND_DIR/index.html" ]]; then
  echo "frontend build is missing; run the manifest build command first" >&2
  exit 1
fi

exec "$SERVER_BINARY"
