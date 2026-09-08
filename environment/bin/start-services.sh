#!/usr/bin/env bash
set -Eeuo pipefail

if ((EUID != 0)); then
  exec sudo -- /opt/arena/bin/start-services.sh "$@"
fi

export DATABASE_URL="${DATABASE_URL:-postgresql://arena:arena@127.0.0.1:5432/arena?sslmode=disable}"
export MONGODB_URI="${MONGODB_URI:-mongodb://127.0.0.1:27017/arena}"
export REDIS_URL="${REDIS_URL:-redis://127.0.0.1:6379/0}"

read -r postgres_version postgres_cluster < <(
  pg_lsclusters --no-header | awk 'NR == 1 {print $1, $2}'
)

if [[ -z "${postgres_version:-}" || -z "${postgres_cluster:-}" ]]; then
  echo "no PostgreSQL cluster is installed" >&2
  exit 1
fi

postgres_config="/etc/postgresql/${postgres_version}/${postgres_cluster}/postgresql.conf"
sed -ri \
  "s/^[#[:space:]]*listen_addresses[[:space:]]*=.*/listen_addresses = '127.0.0.1'/" \
  "$postgres_config"

if ! pg_isready --host 127.0.0.1 --port 5432 --quiet; then
  pg_ctlcluster --skip-systemctl-redirect "$postgres_version" "$postgres_cluster" start
fi

if ! runuser -u postgres -- psql --dbname postgres --tuples-only --no-align \
  --command "SELECT 1 FROM pg_roles WHERE rolname = 'arena'" | grep -qx '1'; then
  runuser -u postgres -- psql --dbname postgres --set ON_ERROR_STOP=1 \
    --command "CREATE ROLE arena LOGIN PASSWORD 'arena'"
else
  runuser -u postgres -- psql --dbname postgres --set ON_ERROR_STOP=1 \
    --command "ALTER ROLE arena WITH LOGIN PASSWORD 'arena'" >/dev/null
fi

if ! runuser -u postgres -- psql --dbname postgres --tuples-only --no-align \
  --command "SELECT 1 FROM pg_database WHERE datname = 'arena'" | grep -qx '1'; then
  runuser -u postgres -- createdb --owner arena arena
fi

install -d -m 0750 -o mongodb -g mongodb /var/lib/arena/mongodb
install -d -m 0755 -o mongodb -g mongodb /var/log/arena/mongodb /run/arena/mongodb

if ! mongosh "$MONGODB_URI" --quiet \
  --eval 'quit(db.runCommand({ping: 1}).ok === 1 ? 0 : 1)' >/dev/null 2>&1; then
  rm -f /run/arena/mongodb/mongod.pid
  runuser -u mongodb -- mongod \
    --fork \
    --dbpath /var/lib/arena/mongodb \
    --logpath /var/log/arena/mongodb/mongod.log \
    --pidfilepath /run/arena/mongodb/mongod.pid \
    --bind_ip 127.0.0.1 \
    --port 27017
fi

install -d -m 0750 -o redis -g redis /var/lib/arena/redis
install -d -m 0755 -o redis -g redis /var/log/arena/redis /run/arena/redis

if [[ "$(redis-cli -u "$REDIS_URL" --no-auth-warning PING 2>/dev/null || true)" != "PONG" ]]; then
  rm -f /run/arena/redis/redis.pid
  runuser -u redis -- redis-server \
    --daemonize yes \
    --bind 127.0.0.1 \
    --protected-mode yes \
    --port 6379 \
    --dir /var/lib/arena/redis \
    --dbfilename dump.rdb \
    --pidfile /run/arena/redis/redis.pid \
    --logfile /var/log/arena/redis/redis.log
fi

/opt/arena/bin/healthcheck.sh
