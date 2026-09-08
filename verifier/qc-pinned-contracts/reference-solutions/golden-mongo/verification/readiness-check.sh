#!/usr/bin/env bash
# AC-79: /ready must be honest.
#
# A readiness probe that always answers "ready" is useless, so this check proves
# BOTH halves against a real application process:
#
#   1. with the datastore reachable        -> 200 {"status":"ready"}
#   2. with the datastore made unreachable -> 503 {"status":"starting"}
#   3. with the datastore restored         -> 200 again (readiness recovers)
#
# The datastore is taken away without touching MongoDB itself: the probe
# application is started against a local TCP relay that forwards to the real
# MongoDB, and the relay is killed to sever the connection. Nothing in the
# application's own database is read or written -- the probe uses its own
# database name and drops it afterwards.
set -euo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$WORKSPACE_ROOT"
: "${MONGODB_URI:?MONGODB_URI is required}"

WORK_DIR="$(mktemp -d)"
RELAY_PID=""
APP_PID=""
PROBE_URI=""

cleanup() {
  if [[ -n "$APP_PID" ]] && kill -0 "$APP_PID" 2>/dev/null; then
    kill -TERM "$APP_PID" 2>/dev/null || true
    wait "$APP_PID" 2>/dev/null || true
  fi
  if [[ -n "$RELAY_PID" ]] && kill -0 "$RELAY_PID" 2>/dev/null; then
    kill -TERM "$RELAY_PID" 2>/dev/null || true
  fi
  if [[ -n "$PROBE_URI" ]] && command -v mongosh >/dev/null 2>&1; then
    mongosh "$PROBE_URI" --quiet --eval 'db.dropDatabase()' >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

cat >"$WORK_DIR/relay.py" <<'PY'
"""Minimal TCP relay. Killing it makes the datastore unreachable."""
import socket
import sys
import threading

listen_port = int(sys.argv[1])
target_host, target_port = sys.argv[2], int(sys.argv[3])


def pump(source, sink):
    try:
        while True:
            chunk = source.recv(65536)
            if not chunk:
                break
            sink.sendall(chunk)
    except OSError:
        pass
    finally:
        for handle in (source, sink):
            try:
                handle.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            handle.close()


def serve(client):
    try:
        upstream = socket.create_connection((target_host, target_port), timeout=5)
    except OSError:
        client.close()
        return
    threading.Thread(target=pump, args=(client, upstream), daemon=True).start()
    threading.Thread(target=pump, args=(upstream, client), daemon=True).start()


listener = socket.socket()
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(("127.0.0.1", listen_port))
listener.listen(64)
print("relay ready", flush=True)
while True:
    connection, _ = listener.accept()
    serve(connection)
PY

# Two free loopback ports plus the upstream MongoDB endpoint parsed out of
# MONGODB_URI, so the probe never assumes a hard-coded deployment.
read -r RELAY_PORT APP_PORT MONGO_HOST MONGO_PORT PROBE_URI <<<"$(
  MONGODB_URI="$MONGODB_URI" python3 - <<'PY'
import os
import socket
from urllib.parse import urlsplit, urlunsplit

uri = os.environ["MONGODB_URI"]
parts = urlsplit(uri)
if parts.scheme != "mongodb":
    raise SystemExit(f"readiness-check requires a mongodb:// URI, got {parts.scheme!r}")
if "," in parts.netloc:
    raise SystemExit("readiness-check requires a single-host mongodb:// URI")
host = parts.hostname or "127.0.0.1"
port = parts.port or 27017
database = (parts.path or "/").lstrip("/") or "arena"


def free_port():
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


relay_port, app_port = free_port(), free_port()
while app_port == relay_port:
    app_port = free_port()
credentials = ""
if parts.username:
    credentials = parts.username + (f":{parts.password}" if parts.password else "") + "@"
probe_uri = urlunsplit((
    "mongodb",
    f"{credentials}127.0.0.1:{relay_port}",
    f"/{database}__readiness_probe",
    parts.query,
    "",
))
print(relay_port, app_port, host, port, probe_uri)
PY
)"

BASE="http://127.0.0.1:$APP_PORT"
echo "AC-79 readiness probe: app=$BASE relay=127.0.0.1:$RELAY_PORT -> $MONGO_HOST:$MONGO_PORT"
echo "AC-79 probe database: $PROBE_URI"

start_relay() {
  python3 "$WORK_DIR/relay.py" "$RELAY_PORT" "$MONGO_HOST" "$MONGO_PORT" \
    >"$WORK_DIR/relay.log" 2>&1 &
  RELAY_PID=$!
  for _ in $(seq 1 100); do
    if grep -q "relay ready" "$WORK_DIR/relay.log" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
  done
  echo "relay did not start" >&2
  return 1
}

# Prints "<status> <body>" for one /ready probe.
probe_ready() {
  curl -sS -m 5 -o "$WORK_DIR/body" -w '%{http_code}' "$BASE/ready" 2>/dev/null \
    || echo "000"
}

fail() {
  echo "AC-79 FAILED: $*" >&2
  echo "--- application log (tail) ---" >&2
  tail -20 "$WORK_DIR/app.log" >&2 || true
  exit 1
}

start_relay

MONGODB_URI="$PROBE_URI" \
  APP_ADDR="127.0.0.1:$APP_PORT" \
  ADMIN_USERNAME="${ADMIN_USERNAME:-readiness-probe-admin}" \
  ADMIN_PASSWORD="${ADMIN_PASSWORD:-readiness-probe-password}" \
  bash verification/start.sh >"$WORK_DIR/app.log" 2>&1 &
APP_PID=$!

for _ in $(seq 1 150); do
  status="$(probe_ready)"
  [[ "$status" == "200" ]] && break
  kill -0 "$APP_PID" 2>/dev/null || fail "the probe application exited before becoming ready"
  sleep 0.2
done

# ---------------------------------------------------------------- half 1: ready
status="$(probe_ready)"
body="$(cat "$WORK_DIR/body")"
echo "  datastore reachable    -> HTTP $status $body"
[[ "$status" == "200" ]] || fail "expected HTTP 200 with the datastore reachable, got $status $body"
[[ "$body" == '{"status":"ready"}' ]] || fail "expected {\"status\":\"ready\"}, got $body"

# ------------------------------------------------------------ half 2: not ready
kill -TERM "$RELAY_PID" 2>/dev/null || true
wait "$RELAY_PID" 2>/dev/null || true
RELAY_PID=""

negative_status=""
negative_body=""
for _ in $(seq 1 100); do
  negative_status="$(probe_ready)"
  negative_body="$(cat "$WORK_DIR/body")"
  [[ "$negative_status" != "200" ]] && break
  sleep 0.2
done
echo "  datastore unreachable  -> HTTP $negative_status $negative_body"
[[ "$negative_status" != "200" ]] || fail "/ready still answered 200 with the datastore unreachable"
[[ "$negative_status" == "503" ]] || fail "expected HTTP 503 with the datastore unreachable, got $negative_status"
[[ "$negative_body" == '{"status":"starting"}' ]] || fail "expected {\"status\":\"starting\"}, got $negative_body"
kill -0 "$APP_PID" 2>/dev/null || fail "the application died instead of reporting not-ready"

# --------------------------------------------------------------- half 3: recovery
start_relay
recovered=""
for _ in $(seq 1 150); do
  recovered="$(probe_ready)"
  [[ "$recovered" == "200" ]] && break
  sleep 0.2
done
echo "  datastore restored     -> HTTP $recovered $(cat "$WORK_DIR/body")"
[[ "$recovered" == "200" ]] || fail "/ready did not recover to 200 after the datastore came back"

echo "AC-79 PASSED: /ready reports 200 ready, 503 starting, and 200 again"
