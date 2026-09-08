# Mutant: connection-capacity-unreadable

**Real defect (AC-46):** connection capacity is configurable via
`configure_capacity` but never readable back — `read_operations_state`'s
`connections` array (and the per-connection `capacity` object it carries) is
deleted from `internal/service/views.go`'s `readOperations`.

## Apply / revert

```bash
cd verifier/qc-pinned-contracts/reference-solutions/golden-mongo
git apply /path/to/anchors/mutants/connection-capacity-unreadable/mutation.patch
# ... rebuild, test ...
git checkout -- backend/internal/service/views.go   # revert
```

## Rebuild + restart (Docker pattern used this session)

```bash
docker run --rm -v "$PWD:/workspace" -w /workspace/backend \
  lab-sample-pickup-delivery-gym:local bash -lc \
  'go build -trimpath -o /workspace/bin/lab-golden-server ./cmd/server'
docker restart pinned-golden
```

## Reproduce the bug (raw HTTP)

```bash
TOKEN=$(curl -s -X POST http://127.0.0.1:18082/api/authenticate-user \
  -d '{"username":"admin","password":"admin123"}' | python3 -c \
  "import json,sys;print(json.load(sys.stdin)['data']['sessionToken'])")

curl -s -X POST http://127.0.0.1:18082/api/read-operations-state \
  -H "Authorization: Bearer $TOKEN" -d '{"filters":{}}' \
  | python3 -c "import json,sys; d=json.load(sys.stdin)['data']; print('connections' in d)"
# -> False   (should be True; configure_capacity's writes are now write-only)
```

## Engine result (measured this session)

```
overall score: 0.9889   (44.5 / 45)
```

Only capability that dropped credit:

```
read_operations_state   credit=0.500
  positive: status=200 keys_present=['samples','workers','locations','routes','handoffs','delays','exceptions']
            (missing 'connections' -> positive fails; negative RBAC check still passes)
```

**Verdict: caught, and by exactly the right capability.** No cascade — the
other 44 capabilities are unaffected (`tracking_audit.py`'s
`_run_read_operations_state` checks top-level key presence directly; nothing
else reads `connections`).
