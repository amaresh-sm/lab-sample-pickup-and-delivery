#!/usr/bin/env bash
set -Eeuo pipefail

readonly DATABASE_URL="${DATABASE_URL:-postgresql://arena:arena@127.0.0.1:5432/arena?sslmode=disable}"
readonly MONGODB_URI="${MONGODB_URI:-mongodb://127.0.0.1:27017/arena}"
readonly REDIS_URL="${REDIS_URL:-redis://127.0.0.1:6379/0}"
readonly TIMEOUT_SECONDS="${ARENA_HEALTH_TIMEOUT_SECONDS:-30}"

deadline=$((SECONDS + TIMEOUT_SECONDS))

while true; do
  unhealthy=()

  if ! psql "$DATABASE_URL" --no-psqlrc --tuples-only --command 'SELECT 1' >/dev/null 2>&1; then
    unhealthy+=("postgresql")
  fi

  if ! mongosh "$MONGODB_URI" --quiet \
    --eval 'quit(db.runCommand({ping: 1}).ok === 1 ? 0 : 1)' >/dev/null 2>&1; then
    unhealthy+=("mongodb")
  fi

  if [[ "$(redis-cli -u "$REDIS_URL" --no-auth-warning PING 2>/dev/null || true)" != "PONG" ]]; then
    unhealthy+=("redis")
  fi

  if ((${#unhealthy[@]} == 0)); then
    echo "arena services healthy: postgresql mongodb redis"
    exit 0
  fi

  if ((SECONDS >= deadline)); then
    printf 'arena services unhealthy after %s seconds: %s\n' \
      "$TIMEOUT_SECONDS" "${unhealthy[*]}" >&2
    exit 1
  fi

  sleep 1
done
