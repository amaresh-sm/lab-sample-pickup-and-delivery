#!/bin/sh
set -eu

# Candidate lifecycle commands run only in the separate candidate-runtime container.

if [ -z "${VERIFIER_COMMAND:-}" ]; then
  echo "VERIFIER_COMMAND is required" >&2
  exit 2
fi

cd /input/verifier
exec sh -c "$VERIFIER_COMMAND"
