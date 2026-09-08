#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

cd "$WORKSPACE_ROOT/backend"
# SKIP_GO_TESTS: for mutant-gate builds only (gate-isolated.sh), never for
# real candidate scoring. A behavioural mutant can correctly break golden's
# OWN pre-existing Go unit tests (that's evidence the mutation is real, not
# a build defect) -- what a mutant gate needs to know is whether the
# HTTP-observable capability checks ALSO catch it, a different question
# from "does golden's internal test suite pass". Defaults to running tests
# (unchanged for every other caller of this script).
if [[ "${SKIP_GO_TESTS:-0}" != "1" ]]; then
  go test ./...
fi
mkdir -p "$WORKSPACE_ROOT/bin"
go build -trimpath -o "$WORKSPACE_ROOT/bin/lab-golden-server" ./cmd/server
go build -trimpath -o "$WORKSPACE_ROOT/bin/lab-golden-schema" ./cmd/schema

cd "$WORKSPACE_ROOT/frontend"
if [[ -f package-lock.json ]]; then
  npm ci --no-audit --no-fund
elif [[ ! -d node_modules ]]; then
  npm install --no-audit --no-fund
fi
npm run build
