# Golden acceptance checklist

Derived **from `tasks/lab-sample-pickup-and-delivery/instruction.md` only** — deliberately not from any implementation, so a
golden cannot "pass" merely by being self-consistent. Every row is a behaviour observable over HTTP (or a
stated artifact/UI requirement). `AC-n` ids are stable; the validation harness reports pass/fail per id.

Scope note: golden-mongo is the canonical golden. golden-postgres is parked (see task G4).
The React-vs-Vue frontend conflict (instruction says React; golden-mongo is Vue) is knowingly deferred.

## Users, roles, access

| id | Requirement | How to verify over HTTP |
|----|-------------|--------------------------|
| AC-1 | RBAC on actions: a user may only perform actions their role permits | call a dispatcher-only op as a requester → 403 |
| AC-2 | RBAC on data: a requester sees only their own requests | requester A cannot read requester B's sample → 403/404 |
| AC-98 | A relay courier sees only the details needed for their assigned leg, not the full multi-leg journey | courier's assignment/journey view omits other legs' worker identities and stops not on their own leg |
| AC-127 | A dispatcher can view a sample's full journey, including the collection leg and every individual transport leg — not restricted to one worker's own leg, unlike a relay courier | as dispatcher, read a multi-leg journey (collection + several transport legs) → every leg is returned, including the collection leg and all individual transport legs, not just one leg's worth of detail |
| AC-103 | A certified collection professional views only the collection work for which they are qualified — not collection requests requiring a qualification they lack | list collection work as a CCP lacking the qualification a sample type requires → that request is absent from their list; a CCP holding the qualification sees it |
| AC-3 | Per role: current work AND past (completed) work are both visible | work list returns a completed bucket, non-empty after a finished journey |
| AC-4 | Per role: a status timeline (ordered history of status changes) for each item of work | timeline/history array is ordered oldest→newest and covers each transition |
| AC-5 | Multi-role user must explicitly select the role they act under | a role-selection op exists and returns the selected role |
| AC-6 | Permissions AND work views use the **selected** role | holding dispatcher+requester but selecting requester → dispatcher-only op is refused; and that same user's work list under the requester role shows only requester work items, not the dispatcher work items visible under the dispatcher role |
| AC-7 | Switching role context must not add or remove roles on the account | read user before/after switch → `roles` identical |
| AC-92 | An administrator can perform any dispatcher-only operation | admin successfully invokes a dispatcher-only op (e.g. reroute/replan) → accepted |
| AC-93 | Only an administrator can manage which node or laboratory a node handler or laboratory intake operator is assigned to | non-admin attempt → 403; admin attempt → accepted and the assignment is updated |
| AC-112 | Only an administrator can manage users, roles and professional certifications (creating/updating user accounts, assigning roles, and recording professional certifications) | non-admin attempt to create/update a user, assign a role, or record a professional certification → 403; admin attempt → accepted |

## Collection requests

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-8 | A requester can create a collection request with origin home or clinic | requester's create-request action for each of home/clinic origin → both accepted |
| AC-9 | Request carries a collection window (earliest + latest collection time) | create-request accepts window fields and echoes them back |
| AC-10 | Window and deadline are **separate** constraints | collecting after `latestCollectionAt` but before `deliveryDeadline` → rejected |
| AC-11 | Collection inside the window is accepted | collect at window midpoint → accepted |
| AC-12 | A plan is valid only if collection fits the window AND delivery beats the deadline | request whose window makes the deadline unreachable → unserviceable/exception |

## Pre-sealed samples

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-84 | Only a certified collection professional, relay courier, node handler, or laboratory intake operator may register a pre-sealed sample — not a requester, not an administrator | attempt registration as requester/admin → 403; as each of the four roles → accepted |
| AC-85 | The registering caller must be the current holder of the sample themselves; there is no separate field to name a different holder | register a pre-sealed sample as the caller → custody is assigned to the registering caller (no way to set another user as holder) |
| AC-86 | A pre-sealed sample's origin must be a clinic or a network node — never a home | register with origin=home → rejected; origin=clinic or network node → accepted |
| AC-87 | Only the transport legs needed from the point of registration onward are created — no collection-step assignment is generated | inspect the journey after registration → no collection assignment/leg exists, only forward transport legs |

## Sample types (named profiles)

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-13 | Amber — standard handling, using the type as it already exists by default (no manual creation required) | request an Amber-typed sample without creating the type first → accepted and the journey plans normally under standard handling rules |
| AC-101 | Amber, Indigo, Frost and Pulse are available by default — present with zero admin action, immediately after reset | reset the app, then list/query sample types with no admin setup performed → all four named types (Amber, Indigo, Frost, Pulse) are already present |
| AC-14 | Indigo must not share a container with Amber | attempt to batch Amber+Indigo → rejected |
| AC-16 | Frost requires cooled storage **and** transport throughout the journey | route with a non-cooled leg → refused; all-cooled route → planned |
| AC-17 | Frost reserves cooled capacity at every location and leg | cooled reservations exist per leg and per location |
| AC-18 | Pulse must not wait for / join a consolidation batch | attempt to merge Pulse into a batch → rejected |
| AC-109 | Pulse must be routed without batching delay — dispatched promptly rather than held waiting for a consolidation batch to fill | create a Pulse-typed request alongside batchable others → it is planned/assigned immediately, not left pending while awaiting batch partners |
| AC-19 | Named rules are in addition to generic attributes (container/temp/qualification/incompatibility) | generic rules still enforced independently |
| AC-131 | Each of the four named types (Amber, Indigo, Frost, Pulse) requires a qualified collector — only a certified collection professional holding that type's own matching qualification may be offered, or may complete, that sample's collection | attempt to offer/complete the collection assignment for each named type to/by a collector who lacks that type's qualification → refused; a collector holding the matching qualification → accepted, for all four types |
| AC-96 | Only an administrator can create or update sample types and their handling attributes | non-admin attempt → 403; admin attempt → accepted |

## Network locations (unified model)

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-20 | ONE location may serve collection point, transfer point, hub, laboratory, or any combination | create a location serving hub+laboratory → accepted |
| AC-21 | A combined hub+laboratory works as BOTH an intermediate stop and a final destination | plan a journey using it in both positions → valid |
| AC-22 | Serviceability is measured from active **collection point** locations | a location without collection_point does not provide coverage |
| AC-23 | A laboratory location accepts specific sample types | acceptance configured against a laboratory-function location |
| AC-24 | Only an administrator can deactivate the WHOLE location, and doing so requires no restart | non-admin attempt to deactivate a location → 403; admin: set-location-status false → accepted, excluded from planning, process not restarted |
| AC-25 | Only an administrator can deactivate an INDIVIDUAL function of a location, and doing so requires no restart | non-admin attempt to disable a function (e.g. collection_point) → 403; admin: disable collection_point only → accepted, location still works as laboratory, process not restarted |
| AC-26 | A function a location does not currently provide cannot be used for that location's operations (the instruction states admins can activate/deactivate an individual function without restart, but does not define an error-code taxonomy distinguishing "unknown function name" from "function the location lacks") | attempt an operation that depends on a function the location does not provide (e.g. it lacks that function, or it has been deactivated) → rejected/fails; no specific status code is asserted since instruction.md does not state one |
| AC-95 | Only an administrator can activate or deactivate a transport connection, without restarting the application | non-admin attempt → 403; admin toggles connection status → accepted, no restart, connection excluded/included from routing accordingly |
| AC-102 | Only an administrator can create or update collection nodes and laboratories | non-admin attempt to create/update a location → 403; admin attempt → accepted |

## Journeys, assignments, fairness

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-27 | Direct, multi-stop and batched journeys are supported | one of each plans successfully |
| AC-28 | Each movement between two locations is a separate assignment | multi-leg journey yields one assignment per leg |
| AC-29 | Collection before transport; pickup before delivery; confirmed handoff before the next leg | out-of-order attempts rejected |
| AC-30 | Work offered to one eligible worker at a time | a pending offer names exactly one worker |
| AC-31 | An assignment cannot be accepted by anyone other than the offered worker | a different eligible worker attempting accept → refused (**incl. when no worker was pre-assigned**) |
| AC-32 | Declining does not make the worker unavailable; decline recorded; re-offered to another eligible worker | decline → worker still gets later work; assignment moves on |
| AC-33 | Repeated declines may lower priority **for a limited time** but never permanently exclude | after repeated declines, the worker is still offered suitable work again later (not permanently excluded) |
| AC-97 | An offer that receives no response within a reasonable time is treated as declined and re-offered to the next eligible worker | let an offer go unanswered past the reasonable-time window → it is withdrawn from that worker and re-offered to another eligible worker |
| AC-104 | A dispatcher can change, split, merge, cancel or reroute unfinished work on demand, not only when a disruption triggers a replan | as dispatcher, invoke change/split/merge/cancel/reroute on an unfinished assignment with no disruption in progress → accepted and the plan is updated |
| AC-126 | A node or laboratory assignment may only be offered to a worker assigned to that specific node or laboratory — never to a worker assigned elsewhere | create two node handlers assigned to two different nodes; trigger a node assignment at one of those nodes → only the handler assigned to that specific node is ever offered it, and the handler assigned to the other node is never offered it — true of the initial offer and of any subsequent re-offer (after a decline/timeout or a disruption-triggered replan) |
| AC-128 | The worker-offer algorithm must not ignore a worker's current active workload — this tests the OUTCOME of "consider each worker's current workload" (instruction.md), not any specific weighting formula, so any implementation that genuinely accounts for workload passes | seed two node handlers assigned to the SAME node (location-matched so workload is the only differing variable); give one of them ~15–20 accepted, uncompleted ("active") node assignments at that node while the other has zero and has never been offered anything; trigger a new node assignment at that same node → it is offered to the zero-workload worker, not the heavily-loaded one |
| AC-129 | The worker-offer algorithm must not ignore a worker's recent-assignment history — again an OUTCOME check, not a specific formula: among workers whose current active workload is equal, one given something more recently must be passed over in favor of one who has been idle longer | seed two otherwise-identical eligible workers with zero workload and no history; have one accept-and-complete an assignment, then, after a real time gap, have the other accept-and-complete one too, so both return to zero active workload but were last given something at very different times; trigger a further eligible assignment → it goes to the longer-idle worker, not the more-recently-served one |
| AC-130 | Repeated offers among several equally-eligible, always-available workers with no distinguishing signal must be spread reasonably evenly over time, not concentrated on one worker — a deliberately loose bound that only catches fairness being ignored entirely, not any specific expected split | seed 3–4 identical eligible workers, all zero workload and no history; trigger 8–12 sequential eligible assignments, each accepted-and-completed immediately by whichever worker receives it before the next is created; tally offers per worker → no worker's share exceeds roughly twice the average |
| AC-132 | If no qualified worker is ever available for an assignment that requires one, and none becomes available within a reasonable time, the sample must be flagged as an exception for that specific reason — distinguishable from a missed delivery deadline | create a serviceable collection request of a type requiring a qualified collector, where zero eligible collectors exist anywhere and none ever becomes available; after a reasonable time the sample's status becomes an exception (never a missed-deadline expiry — its own delivery deadline has demonstrably not yet elapsed) carrying a specific, discoverable reason, distinct from the deadline-expiry case and searchable in the audit trail |

## Serviceability

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-34 | Serviceable requires: ≤5 km of an active collection point, an accepting active laboratory, a valid route, regular/cooled capacity, handling+container rules satisfiable, arrival before deadline | positive case and one negative per clause |
| AC-35 | Requests beyond 5 km of every active collection point are unserviceable on that clause alone (the instruction says "within 5 km" but does not specify whether the boundary itself is inclusive or exclusive) | a request just outside the app's own 5 km cutoff is refused on the distance clause; the exact-5.0km case is not asserted either way |
| AC-36 | Serviceability depends only on network structure, not transient staff/vehicle availability | mark all workers unavailable → the serviceable verdict is unchanged |
| AC-37 | A requester can run the check **independently of creating a request** | a dry-run op exists |
| AC-38 | The dry run creates NO request, sample, journey, assignment or capacity reservation | snapshot counts before/after are identical |
| AC-39 | A dispatcher can monitor collection nodes, as part of the dispatcher's monitoring capability (the instruction does not describe a separate "coverage regions" artifact; "active" is not part of this bullet — that qualifier belongs to the separate Serviceability requirement, already covered by AC-22) | as dispatcher, list/view nodes via the monitoring/nodes view → nodes are returned |
| AC-115 | A dispatcher can monitor workers, as part of the dispatcher's monitoring capability | as dispatcher, list/view workers → worker identities and status are returned |
| AC-116 | A dispatcher can monitor routes, as part of the dispatcher's monitoring capability | as dispatcher, list/view routes → routes are returned |
| AC-117 | A dispatcher can monitor exceptions, as part of the dispatcher's monitoring capability — distinct from creating/recording an exception | as dispatcher, list/view exceptions → previously recorded exceptions across samples are returned |
| AC-121 | A dispatcher can monitor samples, as part of the dispatcher's monitoring capability | as dispatcher, list/view samples → sample identities and status are returned |
| AC-122 | A dispatcher can monitor laboratories, as part of the dispatcher's monitoring capability | as dispatcher, list/view laboratories → laboratory locations/status are returned |
| AC-94 | Only an administrator can configure service coverage, travel times, handling times and laboratory capabilities | non-admin attempt → 403; admin attempt → accepted |
| AC-40 | The serviceability calculation is published as ONE complete version at a time; while a new version is being computed, the system keeps serving the previous complete version, never a partial one (instruction.md does not require an API-visible version identifier at all — only atomic, always-complete serving) | trigger a recompute (e.g. by changing network configuration) and query serviceability concurrently → every response reflects either the fully-old or the fully-new calculation, never a partial or inconsistent mix; if the app happens to expose a version identifier it may also be checked for atomic replacement, but its presence is not required |

## Batching and plan changes

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-42 | A batch is valid only when: worker eligible, samples may travel together, container+temperature compatible, capacity available, stops correctly ordered, all deadlines still met | one negative test per clause |
| AC-43 | Any route/batch change revalidates every affected leg and capacity reservation | change touching a leg triggers full revalidation |
| AC-44 | If validation fails, the change is rejected and the current plan is kept | after a failed change, plan version and stops are unchanged |

## Capacity

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-45 | Locations and connections may have separate regular and cooled capacity | both dimensions configurable |
| AC-46 | Authorized users can see configured, reserved, and available capacity, separately for regular and cooled | all six numbers readable |
| AC-47 | Capacity is reserved when a journey is created | reservations appear on planning |
| AC-48 | Capacity is released when no longer required | after receipt/cancel/exception, reserved returns to prior level (**no leak**) |
| AC-49 | A capacity pool is never overbooked | concurrent reservations beyond capacity → refused |
| AC-91 | A node handler or laboratory intake operator can view/configure capacity only at the node or laboratory they are assigned to; an administrator can do so for any node, laboratory, or connection | node handler/lab operator configures capacity at a facility they are NOT assigned to → 403; at their assigned facility → accepted; admin succeeds on any facility |
| AC-118 | A laboratory intake operator can view expected samples inbound to the laboratory they are assigned to | as laboratory intake operator, list/view expected samples for their assigned laboratory → samples with a journey leg destined for that laboratory are returned; a lab operator assigned to a different laboratory does not see them |

## Chain of custody

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-50 | Exactly one current holder at all times — no exception, even during a pending handoff | holder is single-valued at all times, including while a handoff awaits confirmation |
| AC-51 | A handoff needs both sender and receiver confirmation | one-sided confirm does not transfer |
| AC-52 | Sender remains responsible until both confirm | holder stays sender after one-sided confirm |
| AC-53 | Receiver becomes holder only once the handoff completes | holder flips exactly at completion |
| AC-54 | Repeated or delayed confirmations must not create duplicate transfers | replay/concurrent confirms → exactly one transfer |
| AC-55 | All custody changes appear in the sample lifecycle and audit history | both surfaces show each change |
| AC-88 | A handling checkpoint (container type + temperature observed) may be recorded only by a certified collection professional, relay courier, node handler, or laboratory intake operator, and only while they currently hold custody | attempt to record a checkpoint while not holding custody, or as a non-handling role (e.g. requester/dispatcher) → refused |
| AC-89 | A handling checkpoint records the container type and the temperature currently observed | recorded checkpoint round-trips container type + temperature |
| AC-90 | If the observed container or temperature falls outside the sample's required range, the sample is marked as an exception | record an out-of-range checkpoint → sample status becomes exception |
| AC-105 | A laboratory intake operator can record an exception when a sample cannot be accepted normally at receipt — independent of, and distinct from, a handling-checkpoint-triggered exception | as a laboratory intake operator, reject an arriving sample at receipt (not via a checkpoint reading) → sample status becomes exception |
| AC-110 | A certified collection professional can record that a sample was collected and sealed | as CCP, record collection+sealing for an assigned home/clinic collection → sample transitions to a sealed/collected state and becomes eligible to enter the transport network |
| AC-111 | A laboratory intake operator can confirm that a sample was received | as laboratory intake operator, confirm receipt of an arriving sample → sample/journey is marked received/complete |

## Disruptions and replanning

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-56 | Replan when a worker, node, route, laboratory or capacity pool becomes unavailable | disruption triggers replan of unfinished work |
| AC-57 | The current holder is unchanged by replanning | holder identical before/after |
| AC-58 | Completed assignments, stops and handoffs are preserved | completed records untouched |
| AC-59 | Only unfinished work is replanned | finished legs not rewritten |
| AC-60 | Only one active replacement plan | a second replan does not create a second active plan |
| AC-61 | Replanning rechecks worker eligibility, compatibility, capacity and deadlines | replacement plan is validated, not copied verbatim |
| AC-62 | Sample marked expired when no valid plan can meet the deadline | infeasible replan → expired |
| AC-63 | Journey plan-version checks stop outdated updates overwriting newer ones | stale `expectedPlanVersion` → conflict |

## Tracking and audit

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-64 | Live map shows routes, locations (incl. laboratories), and the current locations of certified collection professionals and relay couriers | map data available; UI renders it |
| AC-65 | Per sample: current or last known location | present on the sample view |
| AC-66 | Per sample: current custodian | present |
| AC-67 | Per sample: route progress | present |
| AC-68 | Per sample: time of last update | present **to the roles that own the sample view**, not only dispatchers |
| AC-69 | Per sample: whether the location is stale | present, same audience |
| AC-113 | A certified collection professional can record their current location, and the update is reflected in what gets read back | as CCP, submit a location update → accepted; a subsequent read (live map data and/or the location field of a sample they are handling) reflects the new location |
| AC-114 | A relay courier can record location and progress updates for their assigned leg, and the updates are reflected in what gets read back | as relay courier, submit a location/progress update → accepted; a subsequent read (live map data and/or the affected sample's location/last-update fields) reflects the new value |
| AC-70 | Audit covers: sign-ins, permission checks, offers, collection, sealing, custody, routes, nodes, capacity, overrides, exceptions, completion | one probe per category |
| AC-71 | Every audit record carries user, **role**, action, affected item, time, outcome, relevant changes | all seven fields populated |
| AC-72 | Outcome is meaningful — failed actions are recorded too | a rejected action appears with a failure outcome |
| AC-73 | Audit is searchable and a request/sample can be traced across its full history | search returns the full chain |

## Artifacts, durability, operability

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-75 | Durable state survives an ordinary application restart | write → restart (start only, no reset) → read back |
| AC-76 | `verification/` contains manifest.json and openapi.json | files present |
| AC-124 | `openapi.json` is itself a syntactically valid JSON document (not malformed, not YAML) that structurally conforms to the OpenAPI 3.0 or 3.1 specification — checked before and independently of matching its content against the running app or the pinned contract | parse `openapi.json` as JSON → parsing succeeds; validate the parsed document as an OpenAPI 3.0/3.1 document → required top-level structure (`openapi` version field, `info`, `paths`) is present and correctly typed, independent of and prior to the AC-77/AC-99/AC-100 content-matching checks |
| AC-77 | `openapi.json` matches the running application — every operation, unique operationIds, auth, request+response schemas, errors | spec ↔ route-registry cross-check, and no undocumented field the app returns |
| AC-78 | `reset` materializes the full datastore structure (all collections + indexes) immediately — not via lazy or on-first-write creation — and repeatably | run reset twice → identical live structure both times, present even before any data is written |
| AC-79 | `/ready` returns 200 `{"status":"ready"}` only when the app can serve and reach its datastore | probe before/after datastore availability |
| AC-80 | First administrator created from env vars; restart does not duplicate or remove users | boot twice → one admin, users intact |
| AC-81 | Frontend, API and readiness URLs are loopback | manifest inspection |
| AC-82 | Build/run/reset/test are documented | README present and accurate |
| AC-83 | Map assets are local only — no external tile service | no external network requests from the map |
| AC-106 | `manifest.json`'s `initialAdmin.loginOperationId` must identify the actual admin-login operation present in the generated `openapi.json` | look up that operationId in `openapi.json` → it exists and its path/method correspond to the admin login/authentication endpoint |
| AC-119 | `build` installs/compiles what the application needs and then exits — it must not remain running | run `verification/build.sh` → the process completes and exits with a normal exit code after setup; no build-spawned process remains running afterward |
| AC-120 | `start` runs the application in the foreground and remains active until terminated, leaving no detached application processes behind | run `verification/start.sh`, then send it a termination signal → the process itself blocks in the foreground until terminated, and no application child process remains running after termination |
| AC-123 | `reset` is run only while the application is stopped, and it leaves the application stopped afterward — it must not itself start or leave running any application process | run `verification/reset.sh` (with the application not running) → it completes and exits normally, and no frontend/API process is left running afterward |

## Pinned HTTP API contract

| id | Requirement | How to verify |
|----|-------------|----------------|
| AC-99 | The backend implements the pinned `openapi.contract.json` exactly — same paths, methods, request field names, request/response body shapes and status codes; no renamed/added/removed fields, no redesigned routes | call each pinned operation → path, method, request/response shape and status code match the contract exactly |
| AC-100 | The generated `verification/openapi.json` matches the pinned `openapi.contract.json`, not just the live app's actual behaviour | diff generated openapi.json against openapi.contract.json → same paths/operations/schemas |
