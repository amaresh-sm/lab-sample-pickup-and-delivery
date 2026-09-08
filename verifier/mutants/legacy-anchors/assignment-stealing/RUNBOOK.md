# Described scenario: assignment-stealing (golden-postgres bug — not built here)

golden-postgres is a different codebase from golden-mongo; per the task
constraints, this is NOT built/booted as a live mutant. This is an
assessment of whether the CURRENT capability suite (as written today, ground
truth: `engine/capabilities/custody_lifecycle.py`) would catch this class of
bug if it existed in golden-mongo, done by reading the specs, not by
literally reintroducing the bug.

## The bug

`accept_assignment` accepts any assignment id from any user who currently
holds the *role* the assignment's `Kind` maps to
(`collection_professional`/`delivery_partner`/`node_handler`/
`laboratory_team`) — with no check that THIS specific user is the one the
assignment was actually offered to (`assignment.WorkerID == p.ID`). Any
role-matching user can "steal" an assignment that was never offered to them,
as long as no worker was pre-offered (or even if one was, if the check is
missing entirely).

## Would the current spec catch it? — No.

Read `engine/capabilities/custody_lifecycle.py`:

```python
def _make_step_capability(cap_id: str, description: str, extra_check=None) -> Capability:
    def run(engine) -> CapabilityReport:
        drive = _drive_happy_path(engine)
        positive = _accepted(drive["steps"], cap_id)
        if positive.ok and extra_check:
            positive = extra_check(drive, positive)
        return CapabilityReport(positive=positive, notes="...")
    return Capability(cap_id, "lifecycle", description, run, priority=...)

CAPABILITIES = [
    _make_step_capability("accept_assignment", "Discovered collection-leg worker accepts their offered assignment."),
    ...
```

`accept_assignment`'s `CapabilityReport` carries **only a `positive`
dimension** — no `negative` field is ever populated. The capability proves
the CORRECT worker (discovered via `list_role_work`, per the module's own
cascade-fix) can accept their own offer. It never attempts an accept from an
uninvolved, same-role worker who was never offered anything, and there is no
assertion anywhere that such an attempt would be rejected.

Contrast with the sibling `decline_assignment` capability
(`_run_decline_assignment`), which DOES have exactly this negative probe:

```python
world.user("bystander_decline", ["collection_professional"], worker=True)
bystander = world.session("bystander_decline")
bad_status, bad_payload = bystander.raw("decline_assignment", {"assignmentId": collection_aid, ...})
negative = Verdict(ok=bad_status == 409 and bystander.code(bad_payload) == "offer_not_available", ...)
```

`accept_assignment` has no equivalent. If golden-mongo (or any candidate app)
let an uninvolved same-role worker successfully `accept_assignment` an offer
that was never theirs, the current suite would score it exactly the same as
a correct app — **1.0000, unchanged.**

## Verdict

**Gap confirmed, not caught.** Fix for the next phase: add a negative probe
to `accept_assignment`'s capability mirroring `decline_assignment`'s —
create a same-role "bystander" worker who was never offered the collection
(or transport) leg, attempt `accept_assignment` on it, and assert rejection
(409, most likely `offer_not_available` or an equivalent contract-exact
code).

---

## UPDATE 2026-08-06 -- prescribed fix IMPLEMENTED

The `Gap confirmed, not caught` verdict above is **stale**. The fix this RUNBOOK
prescribes has been implemented: `custody_lifecycle.py`'s
`_run_accept_assignment` now runs its OWN dedicated scenario rather than reusing
`_drive_happy_path` (that drive is monolithic -- by the time it returns the real
collector has already accepted, so no "still pending, not yet accepted" moment
remains to probe a bystander against), and carries a real `negative` dimension:

  - a same-role `bystander_accept` worker who was never offered the collection
    leg attempts `accept_assignment` WHILE it is still genuinely pending, and
    must be rejected with 409 -- accepting BOTH `offer_not_available` and
    `offer_not_offered`, since `accept_assignment`'s 409 enum pins both (golden
    picks the former, but hardcoding that choice would be the same golden-bias
    bug class fixed elsewhere in this suite);
  - the rejected attempt must not appear in the bystander's own `list_role_work`;
  - the ACTUAL offeree must still be able to accept afterwards, untouched.

CAVEAT on the evidence: golden scores 1.0000 on `accept_assignment`, so the probe
is confirmed not to fire falsely on correct behaviour. But because this defect
lives in golden-POSTGRES and was never built as a live mutant here, there is no
run demonstrating the probe FAILS on the defect. To claim that, the mutation
would have to be written against golden-mongo (drop the
`assignment.WorkerID == p.ID` check in `accept_assignment`) and graded like the
seven live mutants.
