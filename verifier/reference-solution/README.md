# Lab Sample Pickup & Delivery System

Production-style reference application for planning and executing clinical sample
journeys. The backend is written in Go, the browser client uses Vue 3 and
TypeScript, durable state is stored in MongoDB, and live updates are delivered
over WebSockets. MapLibre renders the supplied local PMTiles map assets.

## Runtime contract

Run all commands from the project root (`/workspace` in the task container):

```bash
bash app-setup/build.sh
bash app-setup/reset.sh
ADMIN_USERNAME=admin \
ADMIN_PASSWORD='replace-with-a-strong-password' \
bash app-setup/start.sh
```

The task environment supplies `MONGODB_URI`. Its path selects the application
database, for example `mongodb://127.0.0.1:27017/arena`. `reset.sh` drops only
that database, explicitly recreates every application collection and index, and
leaves the application stopped.

The server listens on `127.0.0.1:18082`:

- Frontend: `http://127.0.0.1:18082`
- API base: `http://127.0.0.1:18082/api`
- Readiness: `http://127.0.0.1:18082/ready`

Readiness returns `{"status":"ready"}` only after MongoDB can be reached. On an
empty database, the first start creates exactly one administrator from
`ADMIN_USERNAME` and `ADMIN_PASSWORD`; later starts preserve existing users.

`start.sh` stays in the foreground and forwards termination to the Go process.

## Implemented workflows

- Role-specific requester, collection, delivery, node, laboratory, dispatcher,
  and administrator workspaces, including multi-role switching.
- Direct, multi-stop, batched, split, merged, cancelled, and rerouted journeys.
- Fair single-worker offers, decline/re-offer and expiry, qualified collection,
  pre-sealed clinic/node intake, two-sided custody handoffs, laboratory receipt,
  handling exceptions, and deadline enforcement.
- Amber (`LX-A`), Indigo (`LX-I`, separated from Amber), Frost (`LX-F`,
  continuously cooled), and Pulse (`LX-P`, never waiting or batching) policies.
- Versioned plan edits, atomic capacity reservations, strictly ordered location
  updates, idempotent critical commands, disruption replanning, audit history,
  and durable WebSocket replay after reconnect.
- Configurable node coverage (never beyond the inclusive 5 km boundary),
  handling dwell time, regular/cooled capacity, laboratory acceptance,
  connection cost/travel/cooling, and worker availability.

The MongoDB deployment supplied by the task is standalone rather than a replica
set. To retain transaction-like domain invariants without unsupported
multi-document transactions, each mutation first writes an immutable snapshot
in bounded 8 MiB chunks and then atomically swaps one small versioned head with
compare-and-swap. Readers follow only a fully committed head, so audit, realtime
outbox, custody, capacity, and idempotency changes remain one logical commit
without risking MongoDB's 16 MiB per-document limit. Existing single-document
installations are migrated on startup.

## Runtime configuration

In addition to `MONGODB_URI`, `ADMIN_USERNAME`, and `ADMIN_PASSWORD`, these
optional variables tune operational timers:

- `ASSIGNMENT_OFFER_TTL_SECONDS` — offer expiry, default `300`, range
  `15..86400`.
- `STALE_LOCATION_THRESHOLD_SECONDS` — map staleness threshold, default `300`,
  range `5..86400`.
- `MAINTENANCE_INTERVAL_MILLISECONDS` — background deadline and offer sweep,
  default `2000`, range `100..60000`.
- `DECLINE_PENALTY_WINDOW_SECONDS` — how long a decline lowers a worker's offer
  priority, default `3600`, range `60..604800`.

`APPLICATION_FIXED_CLOCK` is reserved for deterministic verification. Do not set
it in a normal deployment.

## Verification artifacts

`app-setup/openapi.json` is the complete API contract and
`app-setup/mongodb-schema.json` is a data-free introspection of the live
MongoDB structure. After changing database migrations, regenerate the latter
from a stopped, freshly reset application:

```bash
bash app-setup/reset.sh
bash app-setup/export-mongodb-schema.sh
```

The exporter excludes MongoDB's automatic `_id_` indexes and contains no
application documents or credentials. Its projection lives in
`app-setup/introspect-mongodb-schema.sh`, which prints the same structure to
standard output and is shared with the drift check below.

Run the dependency-free structural smoke check at any time:

```bash
bash app-setup/smoke-contract.sh
```

The smoke check is also the **drift gate** for the API contract. It parses the
route registry in `backend/internal/httpapi/server.go` — the `s.add(...)`
commands, the `s.pathRead(...)` readers and the endpoints mounted directly on the
mux — and fails unless the set of (method, path, `operationId`, success status)
in `openapi.json` is exactly the set the application serves. It additionally
requires every operation to have a unique `operationId`, a declared security
requirement, a request-body schema wherever the application reads a body, a
success-response schema, and error responses whose `code` enumerates the failure
codes that operation can return. Adding or removing a route without updating
`openapi.json` therefore fails the check rather than drifting silently.

## Verification checks

The smoke check is static. These checks exercise a real application process and
a real MongoDB, and each one prints its own PASS or FAIL line. Run them all in
order with:

```bash
MONGODB_URI="$MONGODB_URI" ADMIN_USERNAME="$ADMIN_USERNAME" ADMIN_PASSWORD="$ADMIN_PASSWORD" bash app-setup/verify-all.sh
```

or individually:

- `app-setup/response-conformance.sh` drives every documented operation
  against a started application and validates each response body against the
  response schema `openapi.json` declares for that status. Every response object
  in the spec is closed (`additionalProperties: false`), so a field the
  application returns but the document does not describe fails the check. Needs
  `ADMIN_USERNAME`, `ADMIN_PASSWORD`, and `APP_BASE_URL` if the application is
  not on the manifest origin.
- `app-setup/idempotency-sweep.sh` replays every operation that accepts an
  `idempotencyKey` with a byte-identical body and asserts the replay returns the
  original response, adds no audit record, and leaves exactly one effect. Where
  the operation carries a `reason`, it also asserts that reusing the key with a
  different body is refused with `409 idempotency_conflict`.
- `app-setup/durability-check.sh` writes over HTTP, stops the application,
  runs `app-setup/start.sh` again against the same MongoDB without any reset,
  and reads the data back. Set `DURABILITY_SKIP_RESET=1` to keep existing data;
  by default it resets once *before* writing so worker fairness is deterministic.
- `app-setup/readiness-check.sh` proves `/ready` is honest: it starts an
  application against a local relay to MongoDB, checks `200 {"status":"ready"}`,
  kills the relay and checks `503 {"status":"starting"}`, then restores it.
- `app-setup/mongodb-schema-check.sh` runs `app-setup/reset.sh`,
  introspects the live database and diffs it against
  `app-setup/mongodb-schema.json`, twice, so drift and non-repeatable resets
  both fail.
- `app-setup/docs-check.sh` compares this README against the repository: the
  manifest commands and URLs, the documented test commands, every
  `app-setup/*.sh` entry point, and every operator-tunable environment
  variable with its default and range.

The browser suite covers the live map end to end in
`frontend/tests/live-map.spec.ts`: it seeds a network over the API until
`read_operations_state` returns a real route and a courier with a recorded
location, then proves in Chromium that the laboratory and the courier are
hit-testable map features and that the route line is painted between them,
using only local map assets.

## Tests

The manifest build runs all Go tests and compiles both runtime binaries before
building the Vue client.

The Playwright suite runs against a started application. Point it at the running
application with `GOLDEN_BASE_URL` — which serves both the built client and the
API — and supply the bootstrap administrator credentials, otherwise the
credential-gated specs skip themselves:

```bash
npx playwright install chromium   # once, if no browsers are present
cd frontend
GOLDEN_BASE_URL="http://127.0.0.1:18082" \
  ADMIN_USERNAME="$ADMIN_USERNAME" ADMIN_PASSWORD="$ADMIN_PASSWORD" \
  npm test
```

The Go integration suite uses an isolated database when
`TEST_MONGODB_URI` is supplied:

```bash
cd backend
TEST_MONGODB_URI="$MONGODB_URI" go test -count=1 ./...
```
