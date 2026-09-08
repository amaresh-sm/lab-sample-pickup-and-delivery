#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
: "${MONGODB_URI:?MONGODB_URI is required}"

OUTPUT_PATH="$WORKSPACE_ROOT/verification/mongodb-schema.json"
TEMP_PATH="$(mktemp "$WORKSPACE_ROOT/verification/.mongodb-schema.XXXXXX")"
trap 'rm -f "$TEMP_PATH"' EXIT

# The projection lives in introspect-mongodb-schema.sh so that
# mongodb-schema-check.sh can diff the live database against this artifact using
# exactly the same definition of "structure".
bash "$WORKSPACE_ROOT/verification/introspect-mongodb-schema.sh" > "$TEMP_PATH"

node -e '
const fs = require("fs");
const value = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
if (value.schemaVersion !== "1.0" || !Array.isArray(value.collections)) {
  throw new Error("MongoDB schema export has an invalid shape");
}
' "$TEMP_PATH"

mv "$TEMP_PATH" "$OUTPUT_PATH"
trap - EXIT
echo "wrote $OUTPUT_PATH"
