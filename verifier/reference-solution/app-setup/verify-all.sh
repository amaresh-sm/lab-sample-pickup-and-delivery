#!/usr/bin/env bash
# Every app-setup check, in an order where each one's preconditions hold.
#
#   MONGODB_URI=... ADMIN_USERNAME=... ADMIN_PASSWORD=... bash app-setup/verify-all.sh
#
# The static checks run first, then the checks that need a datastore, then the
# two that need a started application -- which this script starts on a free
# loopback port and stops again. It resets the database, so do not point it at an
# instance whose data matters.
set -uo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$WORKSPACE_ROOT"
: "${MONGODB_URI:?MONGODB_URI is required}"
: "${ADMIN_USERNAME:?ADMIN_USERNAME is required}"
: "${ADMIN_PASSWORD:?ADMIN_PASSWORD is required}"

WORK_DIR="$(mktemp -d)"
APP_PID=""
declare -a NAMES=()
declare -a OUTCOMES=()

cleanup() {
  if [[ -n "$APP_PID" ]] && kill -0 "$APP_PID" 2>/dev/null; then
    kill -TERM "$APP_PID" 2>/dev/null || true
    wait "$APP_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

run() {
  local name="$1"
  shift
  echo
  echo "=============================================================="
  echo "== $name"
  echo "=============================================================="
  if "$@"; then
    NAMES+=("$name")
    OUTCOMES+=("PASSED")
  else
    NAMES+=("$name")
    OUTCOMES+=("FAILED")
  fi
}

run "AC-76/77/81 contract smoke (static)" bash app-setup/smoke-contract.sh
run "AC-82 documentation accuracy" bash app-setup/docs-check.sh
run "AC-78 datastore structure and reset repeatability" bash app-setup/mongodb-schema-check.sh
run "AC-79 readiness honesty" bash app-setup/readiness-check.sh
run "AC-75 durability across a restart" bash app-setup/durability-check.sh

# ------------------------------------------------- one application for the rest
APP_PORT="$(python3 -c 'import socket
with socket.socket() as probe:
    probe.bind(("127.0.0.1", 0))
    print(probe.getsockname()[1])')"
export APP_BASE_URL="http://127.0.0.1:$APP_PORT"
APP_ADDR="127.0.0.1:$APP_PORT" bash app-setup/start.sh >"$WORK_DIR/app.log" 2>&1 &
APP_PID=$!
ready=""
for _ in $(seq 1 150); do
  if [[ "$(curl -sS -m 5 -o /dev/null -w '%{http_code}' "$APP_BASE_URL/ready" 2>/dev/null)" == "200" ]]; then
    ready=yes
    break
  fi
  kill -0 "$APP_PID" 2>/dev/null || break
  sleep 0.2
done
if [[ -z "$ready" ]]; then
  echo "the application did not become ready on $APP_BASE_URL" >&2
  tail -20 "$WORK_DIR/app.log" >&2
  exit 1
fi
echo
echo "application started for the live checks on $APP_BASE_URL"

run "AC-77 live response conformance" bash app-setup/response-conformance.sh
run "AC-74 idempotency replay sweep" bash app-setup/idempotency-sweep.sh

echo
echo "=============================================================="
echo "== summary"
echo "=============================================================="
status=0
for index in "${!NAMES[@]}"; do
  printf '  %-6s %s\n' "${OUTCOMES[$index]}" "${NAMES[$index]}"
  [[ "${OUTCOMES[$index]}" == "PASSED" ]] || status=1
done
exit "$status"
