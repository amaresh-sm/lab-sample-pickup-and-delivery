#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$WORKSPACE_ROOT"

bash -n \
  verification/build.sh \
  verification/reset.sh \
  verification/start.sh \
  verification/export-mongodb-schema.sh

python3 - <<'PY'
import json
import re
import sys
from pathlib import Path
from urllib.parse import urlparse

root = Path.cwd()
verification = root / "verification"
manifest = json.loads((verification / "manifest.json").read_text())
openapi = json.loads((verification / "openapi.json").read_text())
mongodb = json.loads((verification / "mongodb-schema.json").read_text())

failures = []


def check(condition, message):
    if not condition:
        failures.append(message)
    return condition


assert manifest["contractVersion"] == "1.0"
assert manifest["datastoresUsed"] == ["mongodb"]
assert manifest["artifacts"]["mongodbSchema"] == "verification/mongodb-schema.json"
assert "postgresSchema" not in manifest["artifacts"]
assert not (verification / "postgres-schema.sql").exists()
for command in ("build", "reset", "start"):
    value = manifest["commands"][command]
    assert isinstance(value, list) and value and all(isinstance(item, str) for item in value)
for name in ("frontend", "api", "ready"):
    parsed = urlparse(manifest["urls"][name])
    assert parsed.scheme == "http" and parsed.hostname in {"127.0.0.1", "localhost", "::1"}
for artifact in manifest["artifacts"].values():
    assert (root / artifact).is_file()

assert str(openapi["openapi"]).startswith(("3.0.", "3.1."))
assert openapi.get("components", {}).get("securitySchemes")

HTTP_METHODS = {"get", "put", "post", "delete", "patch", "head", "options", "trace"}

# ---------------------------------------------------------------------------
# Drift gate: the route registry in backend/internal/httpapi/server.go is the
# single source of truth for which operations exist, at which path and method,
# under which operationId, and with which success status. It is parsed here
# rather than duplicated, so openapi.json cannot silently drift away from the
# application it is supposed to describe. The pinned task contract is derived
# from openapi.json, so that drift would propagate straight into the contract.
# ---------------------------------------------------------------------------
server_source = (root / "backend" / "internal" / "httpapi" / "server.go").read_text()

GO_METHODS = {
    "http.MethodGet": "get",
    "http.MethodPost": "post",
    "http.MethodPut": "put",
    "http.MethodPatch": "patch",
    "http.MethodDelete": "delete",
}

registered = {}


def register(method, path, operation, status, source):
    key = (method, path)
    check(key not in registered, f"{method.upper()} {path} is registered twice in {source}")
    registered[key] = (operation, status)


# 1. The s.add(...) command registry.
add_re = re.compile(
    r"s\.add\(\s*(http\.Method\w+)\s*,\s*\"([^\"]+)\"\s*,\s*\"([^\"]+)\"\s*,\s*(\d+)\s*\)"
)
add_matches = add_re.findall(server_source)
check(add_matches, "no s.add(...) route registrations were found in server.go")
for go_method, path, operation, status in add_matches:
    check(go_method in GO_METHODS, f"unsupported HTTP method constant {go_method}")
    register(GO_METHODS[go_method], path, operation, int(status), "the s.add registry")

# 2. Path-addressed reads, whose operationId, path template and 200 success
#    status all come from the s.pathRead(...) call that serves them.
path_read_re = re.compile(
    r"s\.pathRead\(\s*w\s*,\s*r\s*,\s*\"([^\"]+)\"\s*,\s*\"([^\"]+)\"\s*,"
    r"\s*strings\.TrimPrefix\(r\.URL\.Path\s*,\s*\"([^\"]+)\"\)\s*\)"
)
path_read_prefixes = set()
for operation, field, prefix in path_read_re.findall(server_source):
    path_read_prefixes.add(prefix)
    register("get", f"{prefix}{{{field}}}", operation, 200, "the s.pathRead readers")

# 3. Endpoints mounted directly on the mux. Their paths come from Handler(); the
#    operationId is read from the spec because the source does not name one.
#    Prefix handlers that are not API operations are named explicitly, so adding
#    any new mux registration fails this gate until it is accounted for.
mux_paths = re.findall(r"mux\.HandleFunc\(\"([^\"]+)\"", server_source)
check(mux_paths, "no mux.HandleFunc registrations were found in server.go")
NON_OPERATION_PREFIXES = {
    "/api/",  # the s.add command dispatcher, already covered above
    "/map-assets/",  # local-only static map assets, not a JSON API operation
    "/",  # single-page-application fallback
}
mux_prefixes = {path for path in mux_paths if path.endswith("/")}
unexpected_prefixes = mux_prefixes - path_read_prefixes - NON_OPERATION_PREFIXES
check(
    not unexpected_prefixes,
    f"server.go mounts unaccounted prefix handlers: {sorted(unexpected_prefixes)}",
)
mux_exact = [path for path in mux_paths if not path.endswith("/")]
spec_get_paths = {
    path for path, item in openapi["paths"].items() if "get" in item
}
for path in mux_exact:
    check(
        path in spec_get_paths,
        f"server.go serves GET {path} but openapi.json does not document it",
    )
    item = openapi["paths"].get(path, {}).get("get", {})
    register("get", path, item.get("operationId"), 200, "the mux endpoints")

# The spec's view of the same four facts.
documented = {}
operation_ids = []
for path, path_item in openapi["paths"].items():
    for method, operation in path_item.items():
        if method.lower() not in HTTP_METHODS:
            continue
        operation_id = operation.get("operationId")
        check(
            isinstance(operation_id, str) and operation_id,
            f"{method.upper()} {path} has no operationId",
        )
        operation_ids.append(operation_id)
        success = sorted(
            int(code)
            for code in operation["responses"]
            if code.isdigit() and code.startswith("2")
        )
        check(
            len(success) == 1,
            f"{operation_id} declares {len(success)} success statuses, want exactly one",
        )
        documented[(method.lower(), path)] = (
            operation_id,
            success[0] if success else None,
        )

only_registered = sorted(set(registered) - set(documented))
only_documented = sorted(set(documented) - set(registered))
check(
    not only_registered,
    "routes served by the application but missing from openapi.json: "
    + ", ".join(f"{method.upper()} {path}" for method, path in only_registered),
)
check(
    not only_documented,
    "operations documented in openapi.json but not served by the application: "
    + ", ".join(f"{method.upper()} {path}" for method, path in only_documented),
)
for key in sorted(set(registered) & set(documented)):
    method, path = key
    want_operation, want_status = registered[key]
    got_operation, got_status = documented[key]
    check(
        want_operation == got_operation,
        f"{method.upper()} {path}: openapi.json says operationId {got_operation!r}, "
        f"the application registers {want_operation!r}",
    )
    check(
        want_status == got_status,
        f"{method.upper()} {path}: openapi.json documents success {got_status}, "
        f"the application returns {want_status}",
    )

check(
    len(operation_ids) == len(set(operation_ids)),
    "operationIds are not unique: "
    + ", ".join(sorted({v for v in operation_ids if operation_ids.count(v) > 1})),
)

# ---------------------------------------------------------------------------
# Every operation must be usable from the document alone: auth, a request-body
# schema wherever the application reads a body, a success schema, and error
# schemas that name the codes the operation can return.
# ---------------------------------------------------------------------------
login_id = manifest["initialAdmin"]["loginOperationId"]
check(login_id in operation_ids, f"{login_id} is not an operation in openapi.json")
readiness_path = urlparse(manifest["urls"]["ready"]).path
unauthenticated = {login_id}

for path, path_item in openapi["paths"].items():
    for method, operation in path_item.items():
        if method.lower() not in HTTP_METHODS:
            continue
        operation_id = operation["operationId"]
        check(
            bool(operation.get("summary")) and bool(operation.get("description")),
            f"{operation_id} has no summary or description",
        )

        security = operation.get("security")
        check(
            isinstance(security, list),
            f"{operation_id} does not declare security",
        )
        if path != readiness_path and operation_id not in unauthenticated:
            check(
                bool(security),
                f"{operation_id} declares no authentication requirement",
            )
            check(
                all(scheme in openapi["components"]["securitySchemes"] for requirement in security for scheme in requirement),
                f"{operation_id} references an undefined security scheme",
            )

        body = operation.get("requestBody")
        if method.lower() == "post":
            check(
                isinstance(body, dict)
                and isinstance(
                    body.get("content", {}).get("application/json", {}).get("schema"),
                    dict,
                ),
                f"{operation_id} reads a JSON request body but documents no request schema",
            )
        else:
            check(body is None, f"{operation_id} is a {method.upper()} yet documents a request body")
            for parameter in operation.get("parameters", []):
                check(
                    isinstance(parameter.get("schema"), dict),
                    f"{operation_id} parameter {parameter.get('name')} has no schema",
                )
            for template in re.findall(r"\{([^}]+)\}", path):
                check(
                    any(
                        parameter.get("in") == "path" and parameter.get("name") == template
                        for parameter in operation.get("parameters", [])
                    ),
                    f"{operation_id} does not document its path parameter {template}",
                )

        responses = operation["responses"]
        success_codes = [code for code in responses if code.isdigit() and code.startswith("2")]
        for code in success_codes:
            check(
                isinstance(
                    responses[code].get("content", {}).get("application/json", {}).get("schema"),
                    dict,
                ),
                f"{operation_id} documents no success response schema for {code}",
            )

        is_readiness = path == readiness_path
        error_codes = [
            code
            for code in responses
            if code.isdigit() and code[0] in ({"5"} if is_readiness else {"4", "5"})
        ]
        check(
            bool(error_codes),
            f"{operation_id} documents no error responses",
        )
        for code in error_codes:
            schema = (
                responses[code].get("content", {}).get("application/json", {}).get("schema")
            )
            check(
                isinstance(schema, dict),
                f"{operation_id} documents no schema for its {code} response",
            )
            if not isinstance(schema, dict) or is_readiness:
                continue
            properties = schema.get("properties", {})
            check(
                set(schema.get("required", [])) >= {"error", "code"},
                f"{operation_id} {code} error schema does not require error and code",
            )
            enum = properties.get("code", {}).get("enum")
            check(
                isinstance(enum, list) and enum and all(isinstance(item, str) and item for item in enum),
                f"{operation_id} {code} error schema does not enumerate its failure codes",
            )

# Every $ref must resolve, so no operation is documented by a dangling pointer.
def walk(node, trail):
    if isinstance(node, dict):
        target = node.get("$ref")
        if isinstance(target, str):
            check(
                target.startswith("#/components/schemas/")
                and target.split("/")[-1] in openapi["components"]["schemas"],
                f"unresolved $ref {target} at {trail}",
            )
        for key, value in node.items():
            walk(value, f"{trail}.{key}")
    elif isinstance(node, list):
        for index, value in enumerate(node):
            walk(value, f"{trail}[{index}]")


walk(openapi["paths"], "paths")
walk(openapi["components"]["schemas"], "components.schemas")

check(
    openapi["servers"][0]["url"].rstrip("/") == manifest["urls"]["frontend"].rstrip("/"),
    "openapi.json server URL does not match manifest.urls.frontend",
)

# ---------------------------------------------------------------------------
# MongoDB structure artifact.
# ---------------------------------------------------------------------------
assert mongodb["schemaVersion"] == "1.0"
collections = mongodb["collections"]
names = [collection["name"] for collection in collections]
assert len(names) == len(set(names))
for collection in collections:
    assert collection["type"] in {"collection", "view", "timeseries"}
    assert isinstance(collection["options"], dict)
    indexes = collection["indexes"]
    index_names = [index["name"] for index in indexes]
    assert "_id_" not in index_names
    assert len(index_names) == len(set(index_names))
    for index in indexes:
        assert isinstance(index["key"], dict) and index["key"]

if failures:
    print("contract smoke FAILED:", file=sys.stderr)
    for failure in failures:
        print(f"  - {failure}", file=sys.stderr)
    raise SystemExit(1)

print(
    f"contract smoke passed: {len(operation_ids)} operations "
    f"({len(add_matches)} command routes + {len(path_read_prefixes)} path reads "
    f"+ {len(mux_exact)} mux endpoints) match the route registry, "
    f"{len(collections)} collections"
)
PY
