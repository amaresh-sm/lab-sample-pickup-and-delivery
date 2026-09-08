#!/usr/bin/env bash
# AC-78: `reset` materializes the full datastore structure, repeatably, and the
# committed artifact matches the LIVE database.
#
# verification/export-mongodb-schema.sh REGENERATES the artifact, so on its own
# it can never reveal drift. This check instead:
#
#   1. runs verification/reset.sh
#   2. introspects the live MongoDB (collections, options, application-defined
#      indexes; MongoDB's automatic `_id_` index excluded)
#   3. DIFFS that against the committed verification/mongodb-schema.json and
#      fails on any difference
#   4. repeats the whole thing so reset is proven repeatable -- run 1 and run 2
#      must produce byte-identical structure
set -euo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$WORKSPACE_ROOT"
: "${MONGODB_URI:?MONGODB_URI is required}"

ARTIFACT="verification/mongodb-schema.json"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

if [[ ! -f "$ARTIFACT" ]]; then
  echo "AC-78 FAILED: $ARTIFACT is missing" >&2
  exit 1
fi

normalize() {
  python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin), indent=2, sort_keys=True))'
}

normalize <"$ARTIFACT" >"$WORK_DIR/artifact.json"

for run in 1 2; do
  echo "run $run: bash verification/reset.sh"
  bash verification/reset.sh >"$WORK_DIR/reset-$run.log" 2>&1 || {
    echo "AC-78 FAILED: reset.sh failed on run $run" >&2
    cat "$WORK_DIR/reset-$run.log" >&2
    exit 1
  }
  bash verification/introspect-mongodb-schema.sh | normalize >"$WORK_DIR/live-$run.json"
  collections="$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))["collections"]))' "$WORK_DIR/live-$run.json")"
  indexes="$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(sum(len(c["indexes"]) for c in d["collections"]))' "$WORK_DIR/live-$run.json")"
  echo "run $run: live structure has $collections collections and $indexes application-defined indexes"
  if ! diff -u "$WORK_DIR/artifact.json" "$WORK_DIR/live-$run.json" >"$WORK_DIR/diff-$run.txt"; then
    echo "AC-78 FAILED: run $run of the live MongoDB structure differs from $ARTIFACT" >&2
    echo "--- $ARTIFACT      +++ live database" >&2
    cat "$WORK_DIR/diff-$run.txt" >&2
    exit 1
  fi
  echo "run $run: live structure == $ARTIFACT"
done

if ! diff -u "$WORK_DIR/live-1.json" "$WORK_DIR/live-2.json" >"$WORK_DIR/diff-runs.txt"; then
  echo "AC-78 FAILED: reset is not repeatable -- run 1 and run 2 differ" >&2
  cat "$WORK_DIR/diff-runs.txt" >&2
  exit 1
fi

echo "AC-78 PASSED: reset materialized the documented structure identically twice"
