#!/usr/bin/env bash
# Portkey readiness probe — run the moment you get a Portkey Config id (or AI-Provider slug), BEFORE a full run.
# Confirms your token + routing (config/provider) + model actually return a completion through the gateway.
#
# Reads everything from verifier.env (OPENAI_API_KEY = token, OPENAI_BASE_URL = endpoint), plus routing:
#   PORTKEY_CONFIG=<config-id>     -> sends  x-portkey-config     (the admin's "config id" path)
#   PORTKEY_PROVIDER=<slug>        -> sends  x-portkey-provider   (AI-Provider slug path)
# Put PORTKEY_CONFIG in verifier.env, or pass it inline. Shell env overrides the file.
#
# Usage:
#   ./portkey-probe.sh <model>                              # all from verifier.env
#   PORTKEY_CONFIG=pc-xxxx ./portkey-probe.sh <model>       # inline routing override
set -Eeuo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
model="${1:?usage: ./portkey-probe.sh <model>   (reads verifier.env: OPENAI_API_KEY, OPENAI_BASE_URL, PORTKEY_CONFIG)}"

# Single source of truth: verifier.env (shell env still overrides).
if [[ -f "$here/verifier.env" ]]; then set -a; source "$here/verifier.env"; set +a; fi
base="${OPENAI_BASE_URL:-https://api.portkey.ai/v1}"
key="${OPENAI_API_KEY:-${PORTKEY_API_KEY:-}}"
[[ -n "$key" ]] || { echo "no OPENAI_API_KEY in verifier.env (nor PORTKEY_API_KEY in env)" >&2; exit 2; }

route_headers=(-H "x-portkey-api-key: $key")
if [[ -n "${PORTKEY_CONFIG:-}" ]]; then
  route_headers+=(-H "x-portkey-config: $PORTKEY_CONFIG"); routing="config=$PORTKEY_CONFIG"
elif [[ -n "${PORTKEY_PROVIDER:-}" ]]; then
  route_headers+=(-H "x-portkey-provider: $PORTKEY_PROVIDER"); routing="provider=$PORTKEY_PROVIDER"
else
  echo "set PORTKEY_CONFIG=<config-id> or PORTKEY_PROVIDER=<slug>" >&2; exit 2
fi

echo "== base=$base  $routing  model=$model =="

echo "--- 1) chat/completions (the generation wire_api) ---"
code=$(curl -sS -o /tmp/pk_probe_chat.json -w '%{http_code}' --max-time 60 \
  -X POST "$base/chat/completions" "${route_headers[@]}" -H "Content-Type: application/json" \
  -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: PONG\"}]}")
echo "HTTP $code"; head -c 500 /tmp/pk_probe_chat.json; echo

echo "--- 2) responses (the mapper wire_api) ---"
code=$(curl -sS -o /tmp/pk_probe_resp.json -w '%{http_code}' --max-time 60 \
  -X POST "$base/responses" "${route_headers[@]}" -H "Content-Type: application/json" \
  -d "{\"model\":\"$model\",\"input\":\"Reply with exactly: PONG\"}")
echo "HTTP $code"; head -c 500 /tmp/pk_probe_resp.json; echo

echo ""
echo "PASS: (1) must be HTTP 200 + PONG for generation. If (2) is 200 the mapper's Responses fallback works too."
echo "400 'config/provider required' -> routing header missing/empty. 401 -> config/provider has no downstream key."
