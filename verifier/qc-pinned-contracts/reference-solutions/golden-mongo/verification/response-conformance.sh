#!/usr/bin/env bash
# AC-77: the responses the running application actually sends must match the
# response schemas verification/openapi.json documents -- the one clause the
# static drift gate in verification/smoke-contract.sh cannot check.
#
# Run it alongside the smoke check against a started application:
#
#   bash verification/smoke-contract.sh        # spec <-> route registry
#   ADMIN_USERNAME=... ADMIN_PASSWORD=... \
#     bash verification/response-conformance.sh   # spec <-> live payloads
#
# APP_BASE_URL overrides the origin, which otherwise comes from
# verification/manifest.json.
set -euo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$WORKSPACE_ROOT"
: "${ADMIN_USERNAME:?ADMIN_USERNAME is required}"
: "${ADMIN_PASSWORD:?ADMIN_PASSWORD is required}"

exec python3 verification/response_conformance.py "$@"
