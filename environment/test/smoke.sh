#!/usr/bin/env bash
set -Eeuo pipefail

readonly DOCKER_CONTEXT="${DOCKER_CONTEXT:-$(docker context show)}"
readonly IMAGE_TAG="${IMAGE_TAG:-lab-sample-pickup-delivery-gym:local}"
readonly CONTAINER_NAME="arena-environment-smoke-$(date +%s)-$$"
readonly CONTAINER_HOSTNAME="arena-smoke"
readonly TEST_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
readonly -a DOCKER=(docker --context "$DOCKER_CONTEXT")

cleanup() {
  "${DOCKER[@]}" rm --force "$CONTAINER_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

container_exec() {
  "${DOCKER[@]}" exec --interactive "$CONTAINER_NAME" "$@"
}

echo "checking image: ${IMAGE_TAG} (${DOCKER_CONTEXT})"
"${DOCKER[@]}" image inspect "$IMAGE_TAG" >/dev/null

cleanup
"${DOCKER[@]}" run --detach \
  --name "$CONTAINER_NAME" \
  --hostname "$CONTAINER_HOSTNAME" \
  --add-host "${CONTAINER_HOSTNAME}:127.0.0.1" \
  --network bridge \
  --cpus 4 \
  --memory 8g \
  --mount "type=bind,source=${TEST_DIR},target=/opt/arena/test,readonly" \
  "$IMAGE_TAG" >/dev/null

for _ in {1..90}; do
  health_status="$("${DOCKER[@]}" inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}' "$CONTAINER_NAME")"
  case "$health_status" in
    healthy)
      echo "container health: PASS"
      break
      ;;
    unhealthy|missing)
      echo "container health failed: ${health_status}" >&2
      "${DOCKER[@]}" logs "$CONTAINER_NAME" >&2
      exit 1
      ;;
  esac
  sleep 2
done

[[ "${health_status:-}" == "healthy" ]]

container_exec bash -s <<'SCRIPT'
set -Eeuo pipefail

[[ "$(id -un)" == "agent" ]]
[[ "$(id -u)" == "1000" ]]
[[ "$(id -g)" == "1000" ]]
[[ "$(id -Gn)" == "agent" ]]
test -w /workspace
test -z "$(find /workspace -mindepth 1 -maxdepth 1 -print -quit)"

[[ "$(go version)" == "go version go1.25.12 linux/"* ]]
[[ "$(node --version)" == "v22.23.1" ]]
[[ "$(npm --version)" == "10.9.8" ]]
[[ "$(python3 --version)" == "Python 3.12."* ]]
[[ "$(pytest --version)" == "pytest 9.1.1" ]]
[[ "$(psql --version)" == "psql (PostgreSQL) 16."* ]]
[[ "$(mongosh --version)" == "2.9.2" ]]
[[ "$(redis-cli --version)" == "redis-cli 7.0.15" ]]
[[ "$(docker --version)" == "Docker version "* ]]
[[ "$(docker compose version)" == "Docker Compose version "* ]]
[[ "$(playwright --version)" == "Version 1.61.1" ]]

test -r /opt/arena/dependencies/backend/go.mod
test -r /opt/arena/dependencies/frontend/package.json
test -r /opt/arena/map-assets/bangalore.pmtiles

for forbidden_path in \
  /app /task /tasks /seed /seeds /verifier /fixtures /expected \
  /grader /golden /solution /solutions /trace /traces /credentials \
  /opt/arena/app \
  /opt/arena/task /opt/arena/tasks /opt/arena/verifier \
  /opt/arena/seed /opt/arena/seeds /opt/arena/fixtures \
  /opt/arena/expected /opt/arena/grader /opt/arena/golden \
  /opt/arena/solution /opt/arena/solutions \
  /opt/arena/trace /opt/arena/traces /opt/arena/credentials; do
  test ! -e "$forbidden_path"
done

if sudo -n id >/dev/null 2>&1; then
  echo "agent unexpectedly has unrestricted passwordless sudo" >&2
  exit 1
fi

python3 /opt/arena/test/source_contract.py
python3 /opt/arena/test/map_contract.py

if docker info >/dev/null 2>&1; then
  echo "Docker daemon is unexpectedly reachable inside the agent container" >&2
  exit 1
fi
docker compose \
  --project-directory /opt/arena/test \
  -f /opt/arena/test/compose-smoke.yml \
  config >/dev/null

echo "identity, tooling, isolation, contracts, and Compose config: PASS"
SCRIPT

container_exec bash -s <<'SCRIPT'
set -Eeuo pipefail

go_smoke_dir="$(mktemp -d /tmp/arena-go-smoke.XXXXXX)"
trap 'rm -rf "$go_smoke_dir"' EXIT

cat >"$go_smoke_dir/go.mod" <<'GO_MOD'
module arena.local/registry-smoke

go 1.25.0

require (
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/gorilla/websocket v1.5.3
	github.com/jackc/pgx/v5 v5.10.0
	github.com/redis/go-redis/v9 v9.21.0
	github.com/stretchr/testify v1.11.1
	go.mongodb.org/mongo-driver/v2 v2.8.0
)
GO_MOD

cat >"$go_smoke_dir/modules_test.go" <<'GO_TEST'
package registry_smoke

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func TestCachedModulesCompile(t *testing.T) {
	assert.NotNil(t, jwt.New(jwt.SigningMethodHS256))
	assert.NotNil(t, websocket.DefaultDialer)
	assert.NotEmpty(t, pgx.Identifier{"registry"}.Sanitize())
	assert.NotNil(t, redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"}))
	assert.NotNil(t, mongo.Connect)
}
GO_TEST

cd "$go_smoke_dir"
go mod tidy
go mod download all
go test ./...
echo "Go modules: PASS"
SCRIPT

container_exec bash -s <<'SCRIPT'
set -Eeuo pipefail

browser_smoke_dir="$(mktemp -d /tmp/arena-browser-smoke.XXXXXX)"
trap 'rm -rf "$browser_smoke_dir"' EXIT

cp -R /opt/arena/test/browser-fixture/. "$browser_smoke_dir/"
mkdir -p "$browser_smoke_dir/public"
ln -s /opt/arena/map-assets "$browser_smoke_dir/public/map-assets"

cd "$browser_smoke_dir"
npm install --ignore-scripts --no-audit --no-fund
npm run build
echo "frontend build: PASS"
npm test
echo "local browser map: PASS"
SCRIPT

container_exec /opt/arena/bin/start-services.sh

container_exec bash -s <<'SCRIPT'
set -Eeuo pipefail

psql "$DATABASE_URL" --no-psqlrc --set ON_ERROR_STOP=1 \
  --command "CREATE TABLE smoke_marker (value text PRIMARY KEY); INSERT INTO smoke_marker VALUES ('preserve');" \
  >/dev/null
mongosh "$MONGODB_URI" --quiet \
  --eval "db.smoke_marker.insertOne({_id: 'preserve'})" >/dev/null
[[ "$(redis-cli -u "$REDIS_URL" --no-auth-warning SET arena:smoke-marker preserve)" == "OK" ]]
touch /workspace/reset-preserved
echo "datastore markers: PASS"
SCRIPT

container_exec /opt/arena/bin/start-services.sh

container_exec bash -s <<'SCRIPT'
set -Eeuo pipefail

[[ "$(psql "$DATABASE_URL" --no-psqlrc --tuples-only --no-align \
  --command "SELECT count(*) FROM smoke_marker WHERE value = 'preserve'")" == "1" ]]
[[ "$(mongosh "$MONGODB_URI" --quiet \
  --eval "db.smoke_marker.countDocuments({_id: 'preserve'})")" == "1" ]]
[[ "$(redis-cli -u "$REDIS_URL" --no-auth-warning GET arena:smoke-marker)" == "preserve" ]]
/opt/arena/bin/healthcheck.sh
echo "idempotent startup preserves datastore markers: PASS"
SCRIPT

container_exec /opt/arena/bin/reset-services.sh

container_exec bash -s <<'SCRIPT'
set -Eeuo pipefail

[[ "$(psql "$DATABASE_URL" --no-psqlrc --tuples-only --no-align \
  --command "SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename = 'smoke_marker'")" == "0" ]]
[[ "$(mongosh "$MONGODB_URI" --quiet \
  --eval "db.smoke_marker.countDocuments({})")" == "0" ]]
[[ "$(redis-cli -u "$REDIS_URL" --no-auth-warning EXISTS arena:smoke-marker)" == "0" ]]
test -f /workspace/reset-preserved
/opt/arena/bin/healthcheck.sh
echo "reset clears datastores and preserves workspace: PASS"
SCRIPT

echo "core container smoke: PASS"
