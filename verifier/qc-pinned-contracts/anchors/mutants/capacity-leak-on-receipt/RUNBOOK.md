# Described scenario: capacity-leak-on-receipt (golden-postgres bug — not built here)

Same treatment as `assignment-stealing`: golden-postgres is a different
codebase and is NOT built/booted here. This is an assessment of whether the
current capability suite would catch this class of bug, done by reading the
specs (`engine/capabilities/capacity.py` and
`engine/capabilities/custody_lifecycle.py`).

## The bug

Reservations (`state.Reservations`, the units held against a location's or
connection's `capacityUnits`/`cooledCapacityUnits`) are never released when a
journey reaches its terminal success path via `confirm_laboratory_receipt`.
Every OTHER terminal path releases them (`cancelJourneyState`,
`expireSample`, `expirePulseWithoutWorker`, `replaceJourneyRoute` all call
`releaseReservations`) — receipt confirmation is the one path that leaks:
capacity silently drains on every successfully-completed journey and is
never returned to the pool.

## Would the current spec catch it? — No.

`capacity.py`'s own module docstring makes an explicit claim:

> "The capacity RESERVE/RELEASE lifecycle itself (AC-47/48/49 — reserved on
> plan, released on every terminal path, never overbooked) is graded in
> `custody_lifecycle.py` and `disruption.py`, where journeys are actually
> planned and driven to a terminal state."

Grepping both files for any capacity/reservation check:

```
$ grep -n "reservedUnits\|ReservedUnits\|reserved\|capacityUnits" custody_lifecycle.py disruption.py
(no matches)
```

**The claim in the docstring does not match the code.** Neither
`custody_lifecycle.py` (which drives `confirm_laboratory_receipt` to
completion in `_drive_happy_path`) nor `disruption.py` ever reads
`read_operations_state`'s `locations[].capacity.reservedUnits` /
`connections[].capacity.reservedUnits` before and after a journey completes,
so nothing in the current 45-capability suite would notice reserved units
staying pinned forever after a successful delivery. `capacity.py`'s own two
capabilities (`configure_capacity`, `set_worker_availability`) only check the
configuration primitives in isolation, immediately after writing them — not
across a completed journey's lifecycle.

## Verdict

**Gap confirmed, not caught — and it's a real doc/reality mismatch worth
fixing regardless of the golden-postgres angle.** Fix for the next phase:
add an assertion (most naturally inside `custody_lifecycle.py`'s shared
happy-path drive, right after `confirm_laboratory_receipt` succeeds, or as a
dedicated capacity capability) that reads each resource's
`capacity.reservedUnits` before planning and again after
`confirm_laboratory_receipt` completes the journey, and requires it returns
to (or below) its pre-journey baseline.

---

## UPDATE 2026-08-06 -- prescribed fix IMPLEMENTED

The `Gap confirmed, not caught` verdict above is **stale**, as is the
doc/reality mismatch it reports. The fix this RUNBOOK prescribes has been
implemented in `custody_lifecycle.py`'s `_confirm_laboratory_receipt_extra`,
which snapshots the journey's pickup / laboratory / connection capacity before
planning and again immediately after the SAME journey's terminal
`confirm_laboratory_receipt`, and requires `reservedUnits` /
`cooledReservedUnits` to return to (or below) their pre-plan baseline.

`capacity.py`'s docstring has been corrected to point at that capability instead
of making the claim this RUNBOOK caught it making, and it now also records what
remains genuinely open: `disruption.py` still does NOT check reservations, since
`cancel_journey` / `reroute_journey` / `split_journey` / `merge_journeys` each
build their own short-lived journeys and never drive them to a terminal state.
So the release-on-EVERY-terminal-path half of AC-47/48/49 is verified only on
the receipt path.

Same evidence caveat as `assignment-stealing`: golden passes the probe, so it
does not fire falsely, but with no live mutant there is no run showing it fails
on the defect.
