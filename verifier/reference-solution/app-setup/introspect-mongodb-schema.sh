#!/usr/bin/env bash
# Data-free introspection of the LIVE MongoDB structure, printed to stdout.
#
# This is the single source of the projection used both to regenerate
# app-setup/mongodb-schema.json (export-mongodb-schema.sh) and to diff the
# live database against it (mongodb-schema-check.sh), so the artifact and the
# check can never disagree about what "the structure" means.
#
# MongoDB's automatic `_id_` index is excluded, as are the per-server `v` and
# `ns` index fields, and no application document or credential is read.
set -euo pipefail

: "${MONGODB_URI:?MONGODB_URI is required}"

mongosh "$MONGODB_URI" --quiet --eval '
const collections = db.getCollectionInfos({})
  .filter(info => !info.name.startsWith("system."))
  .sort((left, right) => left.name.localeCompare(right.name))
  .map(info => {
    const indexes = info.type === "view"
      ? []
      : db.getCollection(info.name).getIndexes()
          .filter(index => index.name !== "_id_")
          .map(index => {
            const normalized = {...index};
            delete normalized.v;
            delete normalized.ns;
            return normalized;
          })
          .sort((left, right) => left.name.localeCompare(right.name));
    return {
      name: info.name,
      type: info.type,
      options: info.options || {},
      indexes
    };
  });
print(JSON.stringify({schemaVersion: "1.0", collections}, null, 2));
'
