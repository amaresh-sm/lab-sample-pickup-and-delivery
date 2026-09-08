# Lab Sample Pickup & Delivery System

Clinical samples may start at a patient’s home, a clinic, or a collection point and must reach a suitable laboratory before their delivery deadline.

Home samples must be collected and sealed by a certified collection professional before they can enter the transport network. Once sealed, a sample may pass through relay couriers, collection centres and hubs before reaching the laboratory.

Dispatchers currently coordinate much of this work through calls, chat groups, and spreadsheets. This makes it difficult to track custody, use available capacity, assign work fairly, combine compatible samples, and recover when part of the network becomes unavailable.

Build a web application that manages this entire process, from the initial serviceability check through laboratory receipt.

## Users and responsibilities

Use role-based access control for both actions and data: each user may perform actions and see data only as permitted by the role or roles they currently hold.

For each of their roles, a user can see their current work and their past (completed) work, and can view a status timeline — the ordered history of status changes — for every item of work they are responsible for.

A user may have more than one assigned role:

- The user must explicitly select the role they are currently working under, and permissions and work views must use that selected role.
- Switching role context must not add or remove any roles from the account.

### Requester

A requester should be able to:

- Check whether a location and sample can be serviced before creating a request.
- Create a home or clinic collection request.
- Follow the sample’s current status, location, route and history.
- See only requests that belong to them.

The serviceability check must be a dry run. It must not create a request, sample, journey, assignment or capacity reservation.

### Certified collection professional

A certified collection professional should be able to:

- View collection work for which they are qualified.
- Accept or decline an offered collection assignment.
- Record that a sample was collected and sealed.
- Record their current location.
- Hand off and confirm the transfer of the sealed sample to a relay courier or facility.

### Relay courier

A relay courier should be able to:

- View delivery work assigned to them.
- Accept or decline an offered transport assignment.
- See only the details needed for their assigned leg.
- Record location and progress updates.
- Confirm pickup and handoff with the other party.

### Node handler

A node handler should be able to:

- Accept or decline an offered node assignment.
- Manage samples arriving at and leaving a collection centre or hub.
- Register a sample that was already collected and sealed before entering the system.
- Confirm incoming and outgoing handoffs.
- View and configure regular and cooled capacity at the node they are assigned to.

### Laboratory intake operator

A laboratory intake operator should be able to:

- Accept or decline an offered laboratory assignment.
- View expected samples and configure regular and cooled capacity at the laboratory they are assigned to.
- Confirm that a sample was received.
- Record an exception when a sample cannot be accepted normally.

### Dispatcher

A dispatcher should be able to:

- Monitor samples, workers, collection nodes, laboratories, routes and exceptions.
- See the current holder of each sample and whether its latest location is stale.
- View the full journey, including collection and individual transport legs.
- Change, split, merge, cancel or reroute unfinished work.
- Replan unfinished work when a worker, node, route, laboratory or capacity pool becomes unavailable.
- Search the complete audit history of a request or sample.

Completed work and confirmed custody must not be rewritten during replanning.

### Administrator

An administrator may also do everything a dispatcher can do.

In addition, only an administrator can:

- Manage users, roles and professional certifications, including which node or laboratory a node handler or laboratory intake operator is assigned to.
- Create and update collection nodes and laboratories.
- Create and update sample types and their handling attributes.
- Activate or deactivate a location, an individual function it provides, or a connection, without restarting the application.
- Configure service coverage, travel times, handling times and laboratory capabilities.
- Configure regular and cooled capacity for any node, laboratory, or transport connection.

## Collection requests

A collection request records where and when a sample can be collected and when it must reach the laboratory:

- **Origin** — a patient’s home, a clinic, or a network node.
- **Sample type** — see Sample types below.
- **Collection window** — the earliest and latest time at which the sample may be collected. Collection must take place inside this window.
- **Delivery deadline** — the time by which the sample must be received at the laboratory.

The collection window and the delivery deadline are separate constraints: the window governs when pickup may happen, the deadline governs when laboratory receipt must happen. A plan is valid only when collection takes place within the collection window and the sample can still reach an accepting laboratory before its delivery deadline.

## Pre-sealed samples

A certified collection professional, relay courier, node handler, or laboratory intake operator may register a sample that was already collected and sealed before entering the system — the person registering it must be the one who currently holds it.

- Its origin must be a clinic or a network node — not a home.
- The system should create only the transport legs needed from the point of registration onward.

## Sample types

Every sample has a type that determines its handling rules, and those rules apply for the whole journey, from collection through laboratory receipt. The system must have at least the following named types available by default: Amber, Indigo, Frost, Pulse. Each of the four requires a qualified collector — only a certified collection professional holding the matching qualification may collect it.

- **Amber** — standard handling.
- **Indigo** — must not share a container with an Amber sample.
- **Frost** — requires cooled storage and transport throughout its entire journey.
- **Pulse** — time-critical; it must not wait to be combined into a consolidation batch and must be routed without batching delay.

These named rules are in addition to the generic handling attributes (container, temperature range, qualified-collector requirement, and incompatibilities).

## Network locations

The transport network is made up of locations. A single location — a node — may act as a collection point, a transfer point, a hub, a laboratory, or any combination of these (for example, a hub that also acts as a laboratory).

- **Collection point** — where samples are collected from or enter the network. Serviceability is measured from active collection points.
- **Transfer point / hub** — where a sample changes custody or moves between transport legs.
- **Laboratory** — where a sample is received and its journey completes; a laboratory accepts specific sample types.

A location may perform several of these functions at the same time.

## Journey and assignment rules

The system must support direct, multi-stop and batched journeys.

A journey may contain several separately assigned transport legs. For example:

1. Patient’s home to a local collection centre
2. Local collection centre to a hub
3. Hub to the laboratory

Each movement between two locations is a separate assignment and may be handled by a different worker.

Collection must happen before transport. Pickup must happen before delivery, and responsibility for the sample must be transferred through a confirmed handoff before the next person continues the journey.

Work should be assigned fairly among eligible and available workers:

- Consider each worker’s current workload and recent assignments.
- Offer an assignment to one worker at a time.
- A node or laboratory assignment may only be offered to a worker assigned to that specific node or laboratory
- A worker who declines an assignment may still receive suitable work later.
- An offer that receives no response within a reasonable time must be treated as declined and re-offered to the next eligible worker.
- If no qualified worker is ever available for an assignment that requires one, and none becomes available within a reasonable time, the sample must be flagged as an exception for that specific reason — distinguishable from a missed delivery deadline.
- Repeated declines may lower a worker’s priority for future offers for a limited time, but must never permanently exclude them from future work.
- An assignment cannot be accepted by someone other than the worker to whom it was offered.

## Serviceability

A request is serviceable only when:

- Its origin is within 5 km of an active collection node.
- The sample can reach an active laboratory that accepts its sample type.
- A valid route is available.
- Required regular or cooled capacity is available.
- All handling and container rules can be satisfied.
- The sample can reach the laboratory before its deadline.

Serviceability depends only on the structure of the network — active nodes, routes, laboratory capabilities, and capacity. The temporary availability of a particular staff member or vehicle at a given moment must not change the serviceable region.

A requester should be able to perform this check independently of creating a request.

The network's serviceability calculation must be published as one complete serviceability-calculation version at a time. While a new serviceability-calculation version is being computed, the system must keep serving the previous complete serviceability-calculation version, so that no request is checked against a partial or in-progress calculation.

A batch is valid only when:

- The assigned worker is eligible.
- The samples are allowed to travel together.
- Their container and temperature requirements are compatible.
- Enough capacity is available.
- Stops are in the correct order.
- Every sample can still meet its deadline.

Whenever a route or batch changes, validate every affected leg and capacity reservation against these same criteria. If that validation fails, reject the change and keep the current plan in place.

## Capacity

Collection nodes, laboratories and transport connections may have separate regular and cooled capacity, tracked as configured, reserved, and available units for both regular and cooled storage or transport.

Capacity must be reserved when a journey is created and released when it is no longer required. The system must never overbook a capacity pool.

## Chain of custody

Every sealed sample must have exactly one current holder at all times.

A handoff requires confirmation from both the sender and receiver. Until both sides confirm, the sender remains responsible for the sample. The receiver becomes the current holder only after the handoff is complete. Repeated or delayed handoff confirmations must not create duplicate transfers of custody. All custody changes must appear in the sample’s lifecycle and audit history. 

At any point while holding custody of a sample, a certified collection professional, relay courier, node handler, or laboratory intake operator may record a handling checkpoint - the container type and temperature they currently observe. If the observed container or temperature falls outside the sample's required range, the sample must be marked as an exception.

## Disruptions and replanning

When part of a journey becomes unavailable:

- Keep the current sample holder unchanged.
- Preserve completed assignments, stops and handoffs.
- Replan only unfinished work.
- Allow only one active replacement plan.
- Recheck worker eligibility, compatibility, capacity and deadlines.
- Mark the sample as expired if no valid route can meet its deadline.

Plan changes must use journey plan-version checks so that outdated updates cannot overwrite newer ones for that journey.

## Tracking and audit

Show routes, nodes, laboratories, and the current locations of certified collection professionals and relay couriers on a live map.

For each sample, show:

- Its current or last known location
- Current custodian
- Route progress
- The time of its last update
- Whether that location is stale

Keep searchable audit logs covering sign-ins, permission checks, offers, collection, sealing, custody, routes, nodes, capacity, overrides, exceptions, and completion. Each audit record must capture the user, their role, the action taken, the affected item, the time, the outcome, and the relevant changes.

## Technical requirements

- Backend: Go
- Frontend: React with TypeScript
- Maps: MapLibre GL JS with the supplied local PMTiles map assets

Document how to build, run, reset and test the complete application.

The environment provides PostgreSQL, MongoDB, and Redis. Use the services that are appropriate for your design. You are not required to use every available datastore only because it is present. Application state that is expected to be durable must survive an ordinary application restart.

## Application Setup and Handoff

Create a `/workspace/verification` directory containing:

- `manifest.json`
- `openapi.json`

These artifacts must describe how to build, start, reset, and interact with the completed application.

### Manifest

`manifest.json` must follow this structure.

```json
{
  "contractVersion": "1.0",
  "commands": {
    "build": ["bash", "verification/build.sh"],
    "start": ["bash", "verification/start.sh"],
    "reset": ["bash", "verification/reset.sh"]
  },
  "urls": {
    "frontend": "http://127.0.0.1:8080",
    "api": "http://127.0.0.1:8080/api",
    "ready": "http://127.0.0.1:8080/ready"
  },
  "artifacts": {
    "openapi": "verification/openapi.json"
  },
  "initialAdmin": {
    "loginOperationId": "auth.login"
  },
  "datastoresUsed": ["postgresql", "mongodb"]
}
```

List only the datastores the application actually uses.

Command values are argument arrays and are executed from `/workspace`.

- `build` must install or compile what the application needs and then exit.
- `start` must run the application in the foreground and remain active until terminated. It must not leave detached application processes behind.
- `reset` is called while the application is stopped. It must clear application-owned state, apply migrations or other required initialization, and leave the application stopped. Reset must **materialize your full datastore structure** immediately — create every table/collection and index your application needs — rather than relying on lazy or on-first-write creation (MongoDB in particular does not create a collection or index until it is first used).
- Running `reset` more than once must produce the same clean state.
- `datastoresUsed` may contain any subset of `postgresql`, `mongodb`, and `redis`. No particular datastore or combination is preferred.

The frontend, API, and readiness URLs must use a loopback address. The readiness URL must return HTTP 200 with `{"status":"ready"}` only when the application can serve requests and reach every datastore it depends on.

### First Administrator

`ADMIN_USERNAME` and `ADMIN_PASSWORD` will be set as environment variables when the application is started.

When the application starts with no existing users, use these values to create the first administrator account. This is the initial user who can sign in, set up the application, and onboard other users.

Starting the application again must not create a duplicate administrator or remove existing users.

`initialAdmin.loginOperationId` must identify the administrator login operation in `openapi.json`.

### OpenAPI

`openapi.json` must contain a valid JSON object describing the application API using OpenAPI 3.0 or 3.1. Do not use YAML syntax in this file.

It must include:

- Every API operation needed to use the application.
- A unique `operationId` for every operation.
- Authentication and authorization schemes.
- Request parameters and request-body schemas.
- Successful response schemas.
- Expected error response schemas.
- Enough descriptions and field information to understand each operation's purpose.

The document must match the submitted application, which itself must conform exactly to the pinned HTTP API contract described below.

The code repository is at `/workspace`. You are inside a Docker container and may not be able to perform all the operations you normally would on a local machine. The dependencies listed under Technology are already available in the container. You may need to install any additional dependencies yourself. You are expected to work autonomously to complete the task.

## HTTP API Contract — IMPLEMENT EXACTLY (pinned)

Your backend MUST implement the HTTP API described in `openapi.contract.json` (provided in your workspace) **exactly as specified** — the same paths, methods, request field names, request/response body shapes, and status codes. Do **not** rename fields, change nesting, add or remove fields, or redesign the routes. Your generated `verification/openapi.json` must match this contract.