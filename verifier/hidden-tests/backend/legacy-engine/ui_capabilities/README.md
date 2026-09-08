# Deterministic UI / behavioural verifier (phase 1)

Grades PRESENTATION-LAYER behaviour of the LabLink app that the HTTP engine
(`engine/capabilities/`, 45 API operations) cannot observe over the wire. It
drives a headless, network-isolated Chromium at `manifest.urls.frontend` with
Python Playwright, seeding known state over HTTP first (reusing the API
engine's `client.World`) exactly like the API engine does.

It is framework-agnostic by construction: golden's frontend is Vue, a
candidate's may be React — the same verifier must fairly grade both.

## Layout

```
engine/ui_capabilities/
  model.py       UiVerdict / UiReport / UiCapability  (mirror of oracle.py's shapes; equal-weight credit)
  driver.py      UiEngine (headless/UTC/en-US/fixed-viewport/reduced-motion context factory,
                 login + nav helpers) and NetworkGate (browser-layer network isolation)
  seed.py        HTTP seeders reusing client.World + oracle.Engine.park_field_workers;
                 returns a clean SeedResult(ok=False, diagnostic) when the BACKEND can't
                 produce the state (mirrors the API engine's planning_failure_report)
  helpers.py     bounded, auto-retrying assertion helpers (never a fixed sleep)
  tracking.py    shared per-sample flow: reach a sample (labelled tracking field OR
                 a "my samples" list opened by the sample id) and scope to the tightest
                 landmark holding the sample id AND a required anchor token
  run_ui.py      CLI scorer: discovers scenarios, writes ui-reward.json (a SEPARATE sub-score)
  scenarios/
    work_lists.py               role_work_view_shows_current_and_past            (AC-3)
    status_timeline.py          sample_view_shows_ordered_status_history         (AC-4)
    freshness.py                sample_freshness_visible_to_owner                (AC-68 / AC-69)
    sample_view.py               sample_view_shows_location_custodian_progress    (AC-65 / AC-66 / AC-67)
    local_map.py                map_renders_local_tiles_only                     (AC-83)
    exception_surfaced.py       sample_view_surfaces_exception_flag              (AC-90)
    monitor_dashboard.py         monitor_dashboard_renders_categories             (AC-76)
    audit_view.py                audit_view_renders_and_searches                  (AC-70/AC-71/AC-73)
    role_switching.py            role_switch_control_lists_and_applies_role       (AC-5 / AC-7)
    sign_out.py                  sign_out_ends_authenticated_session              (session-lifecycle, implicit)
    request_form_validation.py   request_form_surfaces_validation_error           (AC-9 / AC-10)
    login_rejects_invalid_credentials.py  login_rejects_invalid_credentials       (authentication, implicit)
    serviceability_dry_run.py    serviceability_dry_run_creates_nothing           (AC-37 / AC-38)
    capacity_view.py             lab_worker_views_own_capacity                    (AC-91 / instruction.md:68)
    accept_assignment.py         accept_assignment_writes_through                 (AC-31)
```

The canonical ASTRA verifier boots the candidate runtime and invokes this UI
suite inside the private verifier container, writing the UI reward beneath the
ASTRA run's `reports/` directory.

Manual, against an already-running app inside its container:

```bash
PLAYWRIGHT_BROWSERS_PATH=/ms-playwright python3 run_ui.py \
  --frontend-url http://127.0.0.1:18082 --base-url http://127.0.0.1:18082 \
  --contract tasks/public/contracts/openapi.contract.json \
  score --verbose --json-out /logs/ui-reward.json
```

## Locator strategy (anti-golden-bias)

Every locator is built from what a USER perceives, never from markup:

* `get_by_label` / `get_by_role(..., name=...)` — accessible name, matched with
  fuzzy case-insensitive regexes derived from the ACCEPTANCE requirement, not
  from golden's exact copy (e.g. the sample field is a `textbox` named
  `/sample tracking id|sample.*id|tracking/`, the login button a `button` named
  `/sign\s*in|log\s*in/`).
* `get_by_text` scoped to the region a user reads (e.g. the sample-tracking
  region, found by a fuzzy accessible name) — so a freshness word is tied to
  the loaded sample and not to unrelated static copy elsewhere on the page.
* Assertions are on visible text and rendered DATA VALUES (the staleness
  indicator, a last-update time) and on the NETWORK the browser makes.

No CSS class, element id, `data-testid`, DOM path, or framework internal is
ever used to locate or assert. The single structural touch is counting
`<canvas>` elements to confirm a map actually drew — `<canvas>` is a standard
HTML element every mapping library renders into, referenced with no
library-specific class or id.

## Determinism / flake-control

* headless Chromium, fixed 1280×900 viewport, `locale=en-US`, `timezone_id=UTC`,
  `reduced_motion=reduce` + an init-script that disables CSS
  transitions/animations.
* state is seeded over HTTP BEFORE any page opens; each scenario seeds its own
  isolated `World` (unique prefix) and parks other field workers so fairness
  offers land on the seeded workers on a re-run.
* every wait is a Playwright web-first auto-retrying assertion with a bounded
  timeout, or a bounded wait on a concrete condition (canvas visible, network
  idle) — never a fixed sleep.
* the browser is network-isolated: `NetworkGate` blocks (and records) every
  request to a non-app origin, and additionally captures CSP-refused and failed
  external attempts — so AC-83 "no external calls" is enforced, not assumed.

## Clean-fail on a broken backend

If a scenario's HTTP seed can't reach the needed state (e.g. the candidate's
planner returns unserviceable for a network golden plans), the scenario returns
a zero-credit verdict whose detail says the BACKEND could not produce the state
and the UI is "not exercisable" — a broken backend never masquerades as a UI
failure.

## Shipped scenarios and the discrimination gate

Fifteen scenarios are admitted (5 original + `audit_view`/`monitor_dashboard`/
`exception_surfaced` from a later pass + `role_switching`/`sign_out`/
`request_form_validation` from a subsequent pass + 4 more from this pass:
`login_rejects_invalid_credentials`/`serviceability_dry_run_creates_nothing`/
`lab_worker_views_own_capacity`/`accept_assignment_writes_through`). Each
satisfies {golden green k=3/3 (or the same pre-existing value if a scenario is
already flaky on golden), ≥1 mutant caught and isolated}:

| capability                                        | AC                | golden k=3  | hollow-ui | mutant (scenario → credit) |
|---------------------------------------------------|-------------------|:-----------:|:---------:|----------------------------|
| `role_work_view_shows_current_and_past`           | AC-3              | 1.0,1.0,1.0 | 0.000 | `ui-worklist-no-completed` → 0.000 (caught; other 4 stay 1.0) |
| `sample_view_shows_ordered_status_history`        | AC-4              | 1.0,1.0,0.5*| 0.000 | `ui-timeline-hidden` → 0.500 (caught; other 4 stay 1.0) |
| `sample_freshness_visible_to_owner`               | AC-68/AC-69       | 1.0,1.0,1.0 | 0.000 | `ui-freshness-hidden` → 0.500 (caught; others stay 1.0) |
| `sample_view_shows_location_custodian_progress`   | AC-65/AC-66/AC-67 | 1.0,1.0,1.0 | 0.000 | `ui-sample-view-sparse` → 0.500 (caught; others stay 1.0) |
| `map_renders_local_tiles_only`                    | AC-83             | 1.0,1.0,1.0 | 0.000 | `ui-map-external-tiles` → 0.500 (caught; others stay 1.0) |
| `sample_view_surfaces_exception_flag`             | AC-90             | 1.0,1.0,1.0 | —     | `ui-exception-hidden` → 0.500 (caught; others stay 1.0) |
| `monitor_dashboard_renders_categories`            | AC-39/115/116/117/121/122 | 1.0,1.0,1.0 | — | `ui-monitor-sparse` → 0.500 (caught; others stay 1.0) |
| `audit_view_renders_and_searches`                 | AC-70/AC-71/AC-73 | 1.0,1.0,1.0 | —     | `ui-audit-hidden` → drafted; run not yet re-validated (see its own README) |
| `role_switch_control_lists_and_applies_role`      | AC-5/AC-7         | 1.0,1.0,1.0 | —     | `ui-role-switch-hidden` → 0.000 (caught; other 10 stay unaffected) |
| `sign_out_ends_authenticated_session`             | session-lifecycle | 1.0,1.0,1.0 | —     | `ui-signout-noop` → 0.000 (caught; other 10 stay unaffected) |
| `request_form_surfaces_validation_error`          | AC-9/AC-10        | 1.0,1.0,1.0 | —     | `ui-form-validation-silent` → 0.000 (caught; other 10 stay unaffected) |
| `login_rejects_invalid_credentials`               | authentication (implicit) | 1.0,1.0,1.0 | —     | `ui-login-silent` → 0.000 (caught; other 14 stay unaffected) |
| `serviceability_dry_run_creates_nothing`          | AC-37/AC-38       | 1.0,1.0,1.0 | —     | `ui-dryrun-writes` → 0.000 (caught; other 14 stay unaffected) |
| `lab_worker_views_own_capacity`                   | AC-91             | 1.0,1.0,1.0 | —     | `ui-capacity-hidden` → 0.000 (caught; other 14 stay unaffected) |
| `accept_assignment_writes_through`                | AC-31             | 1.0,1.0,1.0 | —     | `ui-accept-noop` → 0.500 (caught; other 14 stay unaffected) |

\* `sample_view_shows_ordered_status_history` is a PRE-EXISTING flake,
independent of this pass: golden's driven journey occasionally stamps two
transitions to the same wall-minute (see the scenario's own "ordering note"),
which the monotonicity guard reads as out-of-order on some runs (measured
0.500 in 1 of 4 fresh runs during this validation pass, 1.0 in the other 3;
historically measured anywhere from 0.0 to 1.0 across different sessions --
see `verifier-logs/golden-newscen-{1..5}` and `verifier-logs/ui-all-scores.txt`
in this worktree). It is unrelated to, and unaffected by, every mutant in this
table.

`role_work_view_shows_current_and_past` (AC-3),
`role_switch_control_lists_and_applies_role` (AC-5/AC-7),
`sign_out_ends_authenticated_session`, and
`request_form_surfaces_validation_error` (AC-9/AC-10) are positive-only (one
coherent property, no second actor whose visibility should differ) — their
mutant catch is therefore a clean 1.0 → 0.0 rather than → 0.5.

Anchors live under `../../anchors/hollow-ui/` and `../../anchors/mutants/ui-*/`.
`ui-audit-hidden`'s mutant was re-run and fixed in THIS pass (bonus item, see
its own README): the orphaned-`v-else-if` build failure recorded in
`.mutant-work/ui-audit-hidden/runui.log` from an earlier session turned out to
already be fixed in the `mutate.py` on disk -- it had simply never been
re-validated since. A fresh run now builds cleanly and catches the target
scenario (1.0 → 0.5) with zero collateral on the other 14.
The `map_renders_local_tiles_only` scenario has demonstrated real discrimination
on a candidate: Luna's frontend fetches external `tile.openstreetmap.org` tiles
(18 blocked egress attempts) and scores 0.500, while golden/Sol/Opus/Terra draw
from local assets and score 1.000.

### Near-misses caught during THIS pass's own validation

**`serviceability_dry_run_creates_nothing`'s look-alike-form trap.** The first
draft never navigated to the "Requests" page before locating the check panel
-- it logged in and immediately searched for the panel from whatever the
requester's DEFAULT landing page (`/dashboard`) happened to be. `DashboardPage.vue`
renders `RequestPanel` (the CREATE-request form) for a requester but NOT
`ServiceabilityCheckPanel` (that only lives on `/requests`, alongside
`RequestPanel` again). Because both panels share field vocabulary ("Sample
type", origin lat/lng — this is the exact "look-alike form" trap the brief
warned about), the panel-name regex fell back to page scope, silently filled
the CREATE panel's fields instead of the check panel's, and then found no
matching submit button in that scope — a clean 0.000 on golden, caught
immediately in the first validation run (never a false PASS, but still wrong
for the wrong reason). Fixed by explicitly navigating to "Requests" first, the
same way `request_form_surfaces_validation_error` already does.

**`accept_assignment_writes_through`'s immediate-recheck race.** The first
draft called `seed.recheck("collector")` a single time, immediately after the
click. On the first validation run this measured `bucket='pending'` even
though the click had genuinely succeeded — the accept call is an async
network round-trip, and a single immediate HTTP re-read can race a real write.
Fixed with a short bounded poll (`_poll_recheck`, up to 5s, never a fixed
sleep) that keeps re-reading until the bucket leaves `pending` or the timeout
elapses, mirroring the "every wait bounded + event-driven" rule.

### Scenarios that were DESIGNED but DROPPED

**Live-map CONTENT — routes / lab & node markers / worker positions (AC-64).**
Distinct from the AC-83 locality scenario (which already asserts the map draws a
non-blank canvas from local assets), AC-64 asks that the map RENDERS the network
CONTENT. It is dropped because that content cannot be asserted framework-
agnostically without golden-specific markup or a flaky signal:

1. golden (and any MapLibre/Mapbox candidate) draws routes/markers/workers into a
   WebGL `<canvas>`, NOT into DOM nodes — so there is no role/accessible-name/
   visible-text locator for a route or a marker (a Leaflet candidate WOULD use
   DOM markers, but keying on that is framework-specific, not agnostic).
2. the WebGL drawing buffer is not readable (`toDataURL` returns blank without
   `preserveDrawingBuffer`); a Playwright screenshot captures pixels but
   distinguishing "basemap only" from "basemap + overlay" needs golden-specific
   marker/route COLORS — exactly the golden bias the rules forbid.
3. the overlay data (routes/locations/workers) is loaded into the shared app
   store that ALSO feeds the dashboard, so there is no map-dedicated network
   request to key on, and golden's legend is static (not data-driven).

The intended mutant ("empty basemap, no overlaid features") would therefore only
be catchable by a golden-color pixel diff. Per the brief ("DROP any scenario
that can't pass golden k=3 without golden-specific copy/markup"), AC-64 map
content is deferred rather than shipped golden-biased/flaky.

**Role-scoped journey view (AC-98 / AC-127)** — "a dispatcher sees a sample's
full multi-leg journey; a relay courier sees only their own leg." Re-attempted
in this phase and again DROPPED, for honest, now evidence-backed reasons:

1. Seeding a genuine RELAY (collection leg + ≥2 transport legs to two distinct
   couriers via a `pickup → hub → lab` network with no direct connection) IS
   reproducible over HTTP — a probe reliably produced a 2-transport-leg journey
   whose legs were offered to two different couriers plus a hub node handler.
   So seeding is *not* the blocker it was feared to be.
2. The UI slice itself is what cannot be asserted fairly. AC-98's meaningful
   form — "stops NOT on the courier's own leg are hidden" — has no
   accessible-name / role / visible-text handle: distinguishing "my leg" from
   "the other leg" in a courier's view requires keying on the rendered
   **location ids** (golden's truncated `displayId`) of the far stop, exactly
   the golden-specific data-layout reliance the anti-bias rule forbids, and a
   candidate may render location NAMES or omit far stops structurally. AC-127's
   UI slice reduces to counting rendered stops/legs by location id, which
   re-tests `read_journey_plan` and overlaps the AC-67 route-progress scenario.
3. Golden already enforces this scoping at the BACKEND (`read_sample_state`
   403s non-owners; `list_role_work` / `read_journey_plan` are server-scoped),
   all HTTP-observable and already graded by the API engine — so the residual
   presentation-only slice adds little beyond golden-biased re-testing.

Per the brief ("a smaller pilot that golden genuinely passes beats a larger
flaky/biased one"), it is deferred rather than shipped golden-biased.
