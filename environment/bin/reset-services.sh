#!/usr/bin/env bash
set -Eeuo pipefail

if ((EUID != 0)); then
  exec sudo -- /opt/arena/bin/reset-services.sh "$@"
fi

export DATABASE_URL="${DATABASE_URL:-postgresql://arena:arena@127.0.0.1:5432/arena?sslmode=disable}"
export MONGODB_URI="${MONGODB_URI:-mongodb://127.0.0.1:27017/arena}"
export REDIS_URL="${REDIS_URL:-redis://127.0.0.1:6379/0}"

/opt/arena/bin/start-services.sh >/dev/null

runuser -u postgres -- dropdb --if-exists --force arena
runuser -u postgres -- createdb --owner arena arena

mongosh "$MONGODB_URI" --quiet --eval 'db.dropDatabase()' >/dev/null

redis_response="$(redis-cli -u "$REDIS_URL" --no-auth-warning FLUSHDB)"
if [[ "$redis_response" != "OK" ]]; then
  echo "Redis reset failed: ${redis_response}" >&2
  exit 1
fi

/opt/arena/bin/healthcheck.sh
echo "arena service data reset complete"
