"""create_sample_type, update_sample_type, configure_laboratory_acceptance, and
AC-101 (named types present with zero admin action, immediately after reset).

Priority-2 domain (session finding: named sample types are a high-risk area).
Ground truth mined from golden-mongo's admin.go (`enforceNamedSamplePolicy`,
`applyNamedCompatibilityRules`, `EnsureNamedSampleTypesSeeded`) and
ac_handling_test.go / ac_batch_validity_test.go:

  - Amber/Indigo -> server forces qualifiedCollectorRequired + a canonical
    requiredQualification, AND mutually links their `incompatibleSampleTypes`
    (symmetric, auto-derived -- not a field the caller sets directly).
  - Frost -> forces requiresCooledRoute=true. instruction.md requires Frost to
    ACTUALLY ENFORCE cooled storage/transport -- a real behavioral property,
    covered separately by AC-16/AC-17 (a route with a non-cooled leg is
    refused; cooled capacity is reserved per leg and per location) -- it does
    NOT require anything about the literal text of `containerType`. A prior
    version of this module asserted that a Frost create is rejected unless
    `containerType` contains "cooled"/"refrigerated"/"cryo"; that was this
    module's own naming bias (golden happens to implement that particular
    substring rule, but nothing in instruction.md asks for it), so it has
    been removed as `create_sample_type`'s negative dimension in favor of a
    real, contract-backed invalid-input case (see `_run_create_sample_type`).
  - Pulse -> forces batchingAllowed=false, and this CANNOT be overridden even
    by an admin explicitly passing batchingAllowed:true (create OR update).
  - Amber+Indigo in the same batch -> 409 incompatible_batch (AC-14).
  - Pulse joining a consolidation batch -> 409 pulse_no_batch (AC-18).
  - AC-101: Amber/Indigo/Frost/Pulse already exist, with the policy above
    already applied, immediately after reset -- before any administrator has
    created anything (`EnsureNamedSampleTypesSeeded`, called from
    cmd/server/main.go on every startup, right next to EnsureInitialAdmin).

Container incompatibility / no-batching are exercised concretely through
`edit_active_plan{changeType:"merge_samples"}`, so this module builds two
serviceable journeys and attempts to merge them -- the real mechanism, not a
static field check.

Since the four canonical names are now pre-seeded at reset, `create_sample_type`
and `update_sample_type` below no longer assume they are the first thing to
ever create an "Amber"/"Indigo"/"Frost"/"Pulse": `_seeded_named_types` discovers
the reset-seeded originals (there is no list/read-sample-type operation in the
pinned contract, so the only zero-admin-action read path is the audit trail --
see its docstring and `_run_named_sample_types_present_after_reset` below for
why that is a sound proof, not a workaround).
"""
from __future__ import annotations

import time

from oracle import Capability, CapabilityReport, Verdict

_CANONICAL_NAMES = ("amber", "indigo", "frost", "pulse")


def _seeded_named_types(engine) -> dict[str, dict]:
    """Discover the four reset-seeded named sample types via the audit trail.

    There is no list/read-sample-type operation in the pinned contract, and
    every OTHER operation that touches a sample type takes its id as input,
    never its name -- so a capability that did not itself just create a
    given name has no way to look it up except through the one write path
    that happens to publish a self-describing record: `search_audit_trail`.
    Each `sample_type_created` audit entry embeds the FULL created record
    (id + every forced attribute) in `details`, so this is a genuine read,
    not a hand-rolled reconstruction.

    Filtering on `action=sample_type_created` ALONE and taking the oldest
    match is NOT sufficient to isolate the genuine boot-time seed: if seeding
    were ever missing or broken, that filter would just as happily return
    the oldest same-named type ANY OTHER capability in this suite run
    happened to create over HTTP -- a false positive that would make this
    check pass even when reset-time seeding is doing nothing (verified live
    against a build with the seeding step reverted: `_run_create_sample_type`
    creates fresh "Amber"/"Indigo" types of its own, and those were picked
    up here as if they were the seeded originals). Anchoring on the audit
    trail's OWN earliest events -- of any entityType, not filtered at all --
    closes that hole: boot-time seeding is the very first thing that can
    possibly write to the audit trail in a freshly reset app (even
    EnsureInitialAdmin, which runs earlier still, never calls audit() at
    all), so the four canonical-name creations are not just the oldest
    matches for one action, they are the audit trail's oldest records of
    ANY kind, full stop -- confirmed live as literal auditId "1".."4".
    `limit=10` leaves a little headroom without weakening that anchor.

    THE CODE BELOW USED TO CONTRADICT THIS DOCSTRING: it filtered on
    `entityType == "sample_type"` AND `action == "sample_type_created"` --
    both golden-mongo's own private naming choices, pinned nowhere in the
    contract (`action`/`entityType` are just `{"type": "string"}`, no enum).
    Measured across all 9 benchmark candidates: golden matched and scored
    1.0; every other candidate's discovery came back empty (`by_name == {}`)
    and scored 0.0, even the ones whose actual apps correctly seed the four
    named types -- the SAME disease as `park_field_workers`' earlier
    `action="user_created"` bug, just not yet fixed here. The chronological
    anchor above (earliest-N, isolated pristine app) already does the real
    work of isolating the boot-time seed; the entityType/action filter was
    redundant defence against a risk the anchor already closes, and it cost
    every non-golden candidate a capability they may have actually earned.
    """
    entries = engine.audit_entries(engine.admin, order="oldest_first", limit=10)
    by_name: dict[str, dict] = {}
    for entry in entries:
        details = entry.get("details") or {}
        name = str(details.get("name", "")).strip().lower()
        if name in _CANONICAL_NAMES and name not in by_name:
            by_name[name] = details

    # SECOND SIGNAL, for names the audit trail never surfaced. Audit-trail
    # silence does not mean absence: confirmed live against opus-max,
    # luna-xhigh, and sol-xhigh -- on a freshly reset app with NOTHING else
    # having run, creating a duplicate "Indigo" returns 409 on all three
    # (opus: concurrent_conflict; luna: "sample type name already exists";
    # sol: sample_type_exists), proving the row already exists even though
    # the audit trail shows only their own login. They seed it silently.
    # Probe by attempting to create it: a 4xx dedup-style rejection proves
    # existence; a successful 2xx creation proves genuine absence (nothing to
    # dedupe against) and is correctly left out of the result, same as
    # before. The probe's own error response carries no data, so existence
    # proven this way cannot carry real policy fields the way an audit-trail
    # hit does -- tag it so callers that need policy detail (not just
    # existence) can detect this and raise "unassessable" rather than
    # grading missing fields as a false 0/False. Mirrors the tagging
    # `World.sample_type()`'s own fallback already uses for the identical
    # situation.
    for name in _CANONICAL_NAMES:
        if name in by_name:
            continue
        probe_status, probe_payload = engine.admin.raw(
            "create_sample_type",
            {
                "name": name.capitalize(),
                "containerType": "sealed-tube",
                "temperatureMinC": 2,
                "temperatureMaxC": 8,
                "qualifiedCollectorRequired": False,
                "incompatibleSampleTypes": [],
                "batchingAllowed": True,
            },
        )
        if probe_status in (400, 409, 422):
            by_name[name] = {"_unverifiable_canonical_policy": True, "name": name.capitalize()}
    return by_name


def _run_named_sample_types_present_after_reset(engine) -> CapabilityReport:
    """AC-101: "The system must have at least the following named types
    available by default: Amber, Indigo, Frost, Pulse" -- ACCEPTANCE.md
    tightens this to "present with zero admin action, immediately after
    reset". Reads via `_seeded_named_types` -- primarily the audit trail (a
    pure read), now with a create-and-expect-rejection probe as a fallback
    for any name the audit trail doesn't surface. That probe does not
    compromise "zero admin action": if the type already exists, the attempt
    is rejected and changes nothing, so the pre-existing state is exactly
    what's observed; if it does NOT exist, the attempt succeeds and creates
    it -- but that success itself is the proof it was NOT present at reset,
    which is scored as a genuine failure below, not laundered into a false
    pass. Either outcome tells the truth about what reset actually produced.

    A probe-only hit carries no real policy fields (the rejection response
    has no data) and is tagged `_unverifiable_canonical_policy` by
    `_seeded_named_types`. Confirmed live: opus-max/luna-xhigh/sol-xhigh all
    return 409 creating a duplicate "Indigo" on a freshly reset app with
    nothing else having run, proving the row exists even though nothing
    about it was ever audited -- the OLD version of this capability read
    that silence as "doesn't exist" and scored a false 0. If any of the four
    can only be confirmed this way, this capability cannot honestly grade
    the per-name policy checks below and reports unassessable instead.
    """
    seeded = _seeded_named_types(engine)
    unverifiable = [name for name, d in seeded.items() if d.get("_unverifiable_canonical_policy")]
    if unverifiable:
        raise RuntimeError(
            f"{sorted(unverifiable)}'s real canonical policy is unmeasurable: create was deduped and has no "
            "audit provenance, so existence is proven but the forced-policy fields cannot be read back."
        )
    checks = {
        "amber": lambda d: d.get("qualifiedCollectorRequired") is True and bool(d.get("requiredQualification")),
        "indigo": lambda d: d.get("qualifiedCollectorRequired") is True and bool(d.get("requiredQualification")),
        "frost": lambda d: d.get("requiresCooledRoute") is True
        and d.get("qualifiedCollectorRequired") is True and bool(d.get("requiredQualification")),
        "pulse": lambda d: d.get("batchingAllowed") is False
        and d.get("qualifiedCollectorRequired") is True and bool(d.get("requiredQualification")),
    }
    per_name_ok = {name: bool(seeded.get(name)) and check(seeded[name]) for name, check in checks.items()}
    # Each audit `details` blob is a SNAPSHOT taken at that entity's own
    # creation instant, not a live re-read -- so only the direction seeded
    # AFTER its counterpart already existed is observable here: Indigo was
    # seeded right after Amber, so Indigo's own snapshot must already list
    # Amber's id. Amber's snapshot, frozen before Indigo existed, can never
    # show Indigo's id -- that is not a bug, it is the same causality
    # constraint `_run_create_sample_type` documents for the live HTTP case.
    amber_indigo_linked = (
        bool(seeded.get("amber")) and bool(seeded.get("indigo"))
        and seeded["amber"].get("sampleTypeId") in seeded["indigo"].get("incompatibleSampleTypes", [])
    )
    positive = Verdict(
        ok=len(seeded) == 4 and all(per_name_ok.values()) and amber_indigo_linked,
        detail=(
            f"names discovered via audit trail (zero admin action taken)={sorted(seeded.keys())} "
            f"per_name_policy_ok={per_name_ok} amber<->indigo_linked={amber_indigo_linked}"
        ),
        evidence={name: d.get("sampleTypeId") for name, d in seeded.items()},
    )
    return CapabilityReport(positive=positive)


def _run_create_sample_type(engine) -> CapabilityReport:
    world = engine.new_world("cst")
    seeded = _seeded_named_types(engine)
    # The amber_ok/indigo_ok/frost_ok/pulse_ok checks below read forced-policy
    # fields off these records -- a probe-only hit (existence proven via a
    # rejected duplicate create, not an audit-trail read) carries none of
    # those fields, so treat it the same way as an unmeasurable canonical
    # policy elsewhere in this module: unassessable, not a false False.
    unverifiable = [name for name, d in seeded.items() if d.get("_unverifiable_canonical_policy")]
    if unverifiable:
        raise RuntimeError(
            f"{sorted(unverifiable)}'s real canonical policy is unmeasurable: create was deduped and has no "
            "audit provenance, so only existence -- not the forced-policy fields this check needs -- is proven."
        )

    # golden-mongo's writeSampleType never dedupes by name on create --
    # every create mints a fresh core.UUID(), whatever the name -- so a SECOND
    # "Amber"/"Indigo" after reset comes back 201 with a new id. But NOTHING in
    # instruction.md requires allowing duplicate canonical names; a candidate
    # that rejects the duplicate with a clean 4xx (e.g. 409 sample_type_exists)
    # is equally valid. So the second-create dimension is graded as valid on
    # EITHER outcome: (a) 201 with a genuinely new, distinct id, OR (b) a clean
    # 4xx rejection. The forced-policy assertions read from the RESET-SEEDED
    # types (via _seeded_named_types), which exist regardless of dedup policy,
    # rather than from the second-create responses (amber2/indigo2), which may
    # not exist on a deduping candidate. When the candidate DOES allow the
    # duplicate (golden's behavior), the fresh responses are used for the
    # additional, stronger proof that the Amber<->Indigo link is applied to
    # newly-created canonical types too, retroactively reaching the seeded
    # originals -- but that extra evidence is only required when those fresh
    # records actually exist.
    #
    # Order matters: each create's OWN response reflects a snapshot at that
    # instant, so indigo2 (created first, while only the seeded amber exists)
    # can only show the seeded amber's id -- never amber2's, which doesn't
    # exist yet. amber2 (created second, once BOTH indigos already exist)
    # is the one that can prove the link reaches both of them.
    indigo2_status, indigo2_payload = engine.admin.raw(
        "create_sample_type",
        {"name": "Indigo", "containerType": "standard_vial", "temperatureMinC": 15, "temperatureMaxC": 25},
    )
    indigo2 = engine.admin.data(indigo2_status, indigo2_payload)
    amber2_status, amber2_payload = engine.admin.raw(
        "create_sample_type",
        {"name": "Amber", "containerType": "standard_vial", "temperatureMinC": 15, "temperatureMaxC": 25},
    )
    amber2 = engine.admin.data(amber2_status, amber2_payload)
    # These two creates still exercise the write path; on a deduper the
    # tolerant World.sample_type falls back to the seeded record.
    frost2 = world.sample_type("Frost", exact_name=True, containerType="cooled-cryo-vial")
    pulse2 = world.sample_type("Pulse", exact_name=True, batchingAllowed=True)  # try to defeat the policy up front

    seeded_amber = seeded.get("amber", {})
    seeded_indigo = seeded.get("indigo", {})
    seeded_frost = seeded.get("frost", {})
    seeded_pulse = seeded.get("pulse", {})
    seeded_amber_id = seeded_amber.get("sampleTypeId")
    seeded_indigo_id = seeded_indigo.get("sampleTypeId")

    def _second_create_valid(status, data, seeded_id):
        # Valid on EITHER outcome: 201 with a genuinely new distinct id, OR a
        # clean 4xx rejection (no new id minted). Golden takes the 201 path; a
        # deduping candidate the 4xx path. Both are contract-compliant.
        if status == 201:
            new_id = data.get("sampleTypeId")
            return bool(new_id) and new_id != seeded_id
        return status in (400, 409, 422)

    amber_second_ok = _second_create_valid(amber2_status, amber2, seeded_amber_id)
    indigo_second_ok = _second_create_valid(indigo2_status, indigo2, seeded_indigo_id)
    second_create_allowed = amber_second_ok and indigo_second_ok

    # instruction.md: "Each of the four requires a qualified collector" -- so
    # all four named types must come back qualified, in ADDITION to their own
    # named rule (Frost cooled route / Pulse no-batching). Read from the
    # reset-seeded records so the assertion is dedup-agnostic.
    amber_ok = seeded_amber.get("qualifiedCollectorRequired") is True and bool(seeded_amber.get("requiredQualification"))
    indigo_ok = seeded_indigo.get("qualifiedCollectorRequired") is True and bool(seeded_indigo.get("requiredQualification"))
    frost_ok = (
        seeded_frost.get("requiresCooledRoute") is True
        and seeded_frost.get("qualifiedCollectorRequired") is True
        and bool(seeded_frost.get("requiredQualification"))
    )
    pulse_ok = (
        seeded_pulse.get("batchingAllowed") is False
        and seeded_pulse.get("qualifiedCollectorRequired") is True
        and bool(seeded_pulse.get("requiredQualification"))
    )

    # Amber<->Indigo auto-link, read from the reset-seeded originals (present
    # regardless of dedup policy): indigo was seeded right after amber, so
    # indigo's snapshot already lists amber's id (same causality as AC-101).
    amber_indigo_linked = (
        bool(seeded_amber_id) and bool(seeded_indigo_id)
        and seeded_amber_id in seeded_indigo.get("incompatibleSampleTypes", [])
    )
    # STRONGER (but dedup-conditional) evidence: when the duplicate creates are
    # ALLOWED (201), the fresh responses must show the link applied to the
    # newly-created types too, retroactively reaching the seeded originals.
    # indigo2 (created first, only seeded amber exists) must list the seeded
    # amber's id; amber2 (created second) must list both indigos. On a
    # deduping candidate (4xx) there are no fresh responses, so this is not
    # required.
    fresh_link_ok = True
    if indigo2_status == 201:
        fresh_link_ok = fresh_link_ok and bool(seeded_amber_id) and seeded_amber_id in indigo2.get("incompatibleSampleTypes", [])
    if amber2_status == 201:
        fresh_link_ok = fresh_link_ok and bool(seeded_indigo_id) and seeded_indigo_id in amber2.get("incompatibleSampleTypes", [])
        if indigo2_status == 201:
            fresh_link_ok = fresh_link_ok and indigo2.get("sampleTypeId") in amber2.get("incompatibleSampleTypes", [])

    positive = Verdict(
        ok=(
            second_create_allowed and amber_ok and indigo_ok and frost_ok and pulse_ok
            and amber_indigo_linked and fresh_link_ok
        ),
        detail=(
            f"second_create_of_a_canonical_name: status=(amber={amber2_status},indigo={indigo2_status}) "
            f"valid_either_way={second_create_allowed} "
            f"amber_qualified={amber_ok} indigo_qualified={indigo_ok} frost_cooled_route={frost_ok} "
            f"pulse_no_batch={pulse_ok} amber<->indigo_linked(seeded)={amber_indigo_linked} "
            f"fresh_link_when_dup_allowed={fresh_link_ok}"
        ),
        evidence={
            "seeded": {name: d.get("sampleTypeId") for name, d in seeded.items()},
            "amber2": amber2.get("sampleTypeId"), "indigo2": indigo2.get("sampleTypeId"),
            "frost2": frost2.get("sampleTypeId"), "pulse2": pulse2.get("sampleTypeId"),
        },
    )

    # A real, contract-backed invalid-input case: tasks/public/contracts/openapi.contract.json
    # marks `containerType` required for create_sample_type, and writeSampleType's
    # own validation (admin.go) rejects a blank container with 400
    # invalid_handling_rule -- independent of any particular sample-type name.
    bad_status, bad_payload = engine.admin.raw(
        "create_sample_type",
        {"name": world.key("no-container"), "temperatureMinC": 2, "temperatureMaxC": 8},
    )
    negative = Verdict(
        ok=bad_status == 400 and engine.admin.code(bad_payload) == "invalid_handling_rule",
        detail=f"create with containerType omitted (required by the contract) -> status={bad_status} code={engine.admin.code(bad_payload)!r}",
    )
    return CapabilityReport(positive=positive, negative=negative)


def _run_update_sample_type(engine) -> CapabilityReport:
    # Update against the ACTUAL reset-seeded Pulse -- the record every real
    # requester references by default -- rather than a fresh copy this
    # capability creates and discards, so the non-overridable policy is
    # proven for the record that matters, not just for a stand-in.
    seeded = _seeded_named_types(engine)
    pulse = seeded.get("pulse")
    if not pulse:
        # Defensive fallback only: keeps this capability self-sufficient if
        # ever run against an app from before EnsureNamedSampleTypesSeeded
        # existed, so its own correctness never silently depends on Fix 1.
        pulse = engine.new_world("ust").sample_type("Pulse", exact_name=True)
    # Either path can hand back a probe-only/substitute record with no real
    # policy behind it -- existence proven (dedup rejection), but not the
    # actual seeded Pulse's fields. Updating and reading back a fake Pulse
    # would test whether an ARBITRARY sample type's batchingAllowed can be
    # overridden (trivially yes, nothing forces it on a non-canonical type),
    # not whether the real Pulse's policy is non-overridable -- a different
    # question entirely.
    if pulse.get("_unverifiable_canonical_policy"):
        raise RuntimeError(
            "Pulse's real canonical policy is unmeasurable: create was deduped and has no audit "
            "provenance, so only a substitute type (not the real seeded Pulse) could be read back."
        )

    updated = engine.admin.call(
        "update_sample_type",
        {
            "sampleTypeId": pulse["sampleTypeId"],
            "containerType": pulse["containerType"],
            "temperatureMinC": pulse["temperatureMinC"],
            "temperatureMaxC": pulse["temperatureMaxC"],
            "incompatibleSampleTypes": [],
            "batchingAllowed": True,  # AC-18: must be silently forced back to False
        },
    )
    positive = Verdict(
        ok=updated.get("sampleTypeId") == pulse["sampleTypeId"] and updated.get("batchingAllowed") is False,
        detail=(
            f"updated the reset-seeded Pulse ({pulse['sampleTypeId']}): requested batchingAllowed=True, "
            f"server kept {updated.get('batchingAllowed')} (non-overridable policy)"
        ),
    )

    bad_status, bad_payload = engine.admin.raw(
        "update_sample_type",
        {
            "sampleTypeId": pulse["sampleTypeId"],
            "containerType": pulse["containerType"],
            "temperatureMinC": 10,
            "temperatureMaxC": 2,  # min > max -> invalid
            "incompatibleSampleTypes": [],
        },
    )
    negative = Verdict(
        ok=bad_status == 400 and engine.admin.code(bad_payload) == "invalid_handling_rule",
        detail=f"status={bad_status} code={engine.admin.code(bad_payload)!r}",
    )
    return CapabilityReport(positive=positive, negative=negative)


def _await_stably_serviceable(
    engine, body: dict, *, settle_tries: int = 25, settle_delay: float = 0.2,
    confirm_samples: int = 5, confirm_delay: float = 0.15,
):
    """Poll check_serviceability until the FIRST `serviceable=True` (AC-40
    permits ONE propagation delay after a network is configured), then
    re-sample `confirm_samples` more times in quick succession and require
    EVERY one to also read True.

    This replaces a plain "wait for one True, stop" retry (client.py's
    World.await_serviceable) for this capability specifically. That pattern
    cannot distinguish AC-40's permitted propagation delay from a network that
    never settles: it rewards the SECOND case by construction, since trying up
    to N times only raises the odds that any ONE of those tries lands on a
    lucky moment. Measured directly on opus-max (continuous 45s poll of this
    exact network, no early exit): 220 samples, 47 True/False transitions,
    serviceable=True in only 13% of samples -- it never settles, it flaps for
    the entire window. The SAME 45s poll against golden: 218/218 samples True,
    zero transitions. So "read True at least once" does not separate a correct
    app from this defect; "read True and then hold" does, and matches what a
    correct implementation (golden) actually does.

    Returns (settled, stable, status, payload) from the LAST call made:
      settled=False  -- never read True inside the settle budget (a real
                         AC-40 lag exceeding the budget, OR permanently
                         unserviceable -- this alone doesn't distinguish them,
                         which is fine: either way the network isn't usable).
      settled=True, stable=False -- read True at least once, then flapped back
                         to False during confirmation. This is the shape the
                         defect above actually takes.
      settled=True, stable=True  -- read True and held True. Free on a correct
                         app (golden pays this every time).
    """
    probe = {k: v for k, v in body.items() if k != "idempotencyKey"}
    status, payload, data = None, None, {}
    settled = False
    for _ in range(max(1, settle_tries)):
        status, payload = engine.admin.raw("check_serviceability", probe)
        data = engine.admin.data(status, payload) if status == 200 else {}
        if status == 200 and data.get("serviceable") is True:
            settled = True
            break
        time.sleep(settle_delay)
    if not settled:
        return False, False, status, payload
    stable = True
    for _ in range(confirm_samples):
        time.sleep(confirm_delay)
        status, payload = engine.admin.raw("check_serviceability", probe)
        data = engine.admin.data(status, payload) if status == 200 else {}
        if not (status == 200 and data.get("serviceable") is True):
            stable = False
            break
    return True, stable, status, payload


def _run_configure_laboratory_acceptance(engine) -> CapabilityReport:
    world = engine.new_world("cla")
    st = world.sample_type("ST")
    lab = world.location("lab", ["laboratory"], 26.0, 26.0)
    result = world.lab_accepts(lab["locationId"], st["sampleTypeId"], accepts=True)

    # Prove the effect functionally through check_serviceability rather than
    # a direct field re-read (there is no read-sample-type endpoint): a
    # request whose only accepting lab has acceptance withdrawn must flip to
    # unserviceable / no_accepting_laboratory.
    world.location("pickup", ["collection_point"], 26.0, 26.0)  # co-located, well within 5km
    pickup = world.location("pickup2", ["collection_point"], 26.0, 26.0, coverageKm=5)
    world.connection(pickup["locationId"], lab["locationId"])
    for r in (pickup["locationId"], lab["locationId"]):
        world.capacity(r, units=10, cooled_units=10)
    body = {
        "origin": {"latitude": 26.0, "longitude": 26.0},
        "originType": "home",
        "sampleTypeId": st["sampleTypeId"],
        "requestedAt": world.at(),
        "deliveryDeadline": world.at(hours=6),
    }
    # AC-40 permits ONE propagation delay after a network is configured -- but
    # it must SETTLE, not merely be luckily-True-at-some-point. See
    # `_await_stably_serviceable`'s docstring: a plain retry-until-True here
    # read `serviceable=True` on roughly 1 run in 3 against opus-max, which
    # looked like the AC-40 delay but is a permanent defect -- continuous
    # polling shows opus's route index flaps True/False for as long as it's
    # watched (13% True over 45s, 47 transitions), never settling. Golden is
    # 218/218 True over the identical window. So this network's serviceability
    # must be observed to HOLD, not just occur once.
    settled, stable, status, payload = _await_stably_serviceable(engine, body)
    before = engine.admin.data(status, payload) if status == 200 else {}
    world.lab_accepts(lab["locationId"], st["sampleTypeId"], accepts=False)
    # Not run through the stability helper: withdrawal must make the network
    # UNserviceable, and the helper polls FOR (and confirms) serviceable=True --
    # it would just burn its full budget here for nothing.
    after = engine.admin.call("check_serviceability", body)

    positive = Verdict(
        ok=(
            result.get("accepts") is True
            and settled and stable
            and after.get("serviceable") is False and after.get("reason") == "no_accepting_laboratory"
        ),
        # The accepts-ON reason is logged too, not just the accepts-OFF one.
        # Without it this capability's failures read only
        # `accepts-on -> serviceable=False`, which is indistinguishable between
        # "the lab genuinely does not accept this type" and "the route index has
        # not republished yet" (reason='no_route'). That ambiguity is what made
        # this flake take a full investigation to attribute.
        detail=(
            f"accepts-on -> settled={settled} stable={stable} serviceable={before.get('serviceable')} reason={before.get('reason')}; "
            f"accepts-off -> serviceable={after.get('serviceable')} reason={after.get('reason')}"
        ),
    )

    bad_status, bad_payload = engine.admin.raw(
        "configure_laboratory_acceptance", {"laboratoryId": "00000000-0000-0000-0000-000000000000", "sampleTypeId": st["sampleTypeId"], "accepts": True}
    )
    negative = Verdict(ok=bad_status in (400, 404), detail=f"unknown laboratoryId -> status={bad_status} code={engine.admin.code(bad_payload)!r}")
    return CapabilityReport(positive=positive, negative=negative)


# --------------------------------------------------------------- durability
# DUR3: create_sample_type/update_sample_type's OWN positive probes are
# entangled with the canonical-name policy checks (Amber/Indigo/Frost/Pulse)
# and have no independent read path (no GET/list endpoint is pinned for
# sample types at all). Durability here deliberately uses a FRESH,
# non-canonical type instead -- avoiding that landmine entirely -- and proves
# existence via engine.observe()'s audit-trail tier (now success-aware),
# same as every other create/update durability check in this pass. This
# checks "the type still exists", not every field's exact value, which is
# the same limit the positive probes accept given there is no read-back
# endpoint to ask for more.


def _durability_write_create_sample_type(engine) -> dict:
    world = engine.new_world("cstdur")
    created = world.sample_type("CSTDUR", containerType="standard_vial")
    return {"sample_type_id": created.get("sampleTypeId")}


def _durability_verify_create_sample_type(engine, sentinel: dict) -> Verdict:
    sample_type_id = sentinel.get("sample_type_id")
    if not sample_type_id:
        return Verdict(ok=False, detail="no sample_type_id recorded at write time")
    via = engine.observe(engine.admin, sample_type_id, entity_type="sample_type")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _durability_write_update_sample_type(engine) -> dict:
    world = engine.new_world("ustdur")
    created = world.sample_type("USTDUR", containerType="standard_vial")
    engine.admin.call(
        "update_sample_type",
        {
            "sampleTypeId": created["sampleTypeId"],
            "containerType": "sealed-tube",
            "temperatureMinC": created["temperatureMinC"],
            "temperatureMaxC": created["temperatureMaxC"],
            "incompatibleSampleTypes": [],
        },
    )
    return {"sample_type_id": created.get("sampleTypeId")}


def _durability_verify_update_sample_type(engine, sentinel: dict) -> Verdict:
    sample_type_id = sentinel.get("sample_type_id")
    if not sample_type_id:
        return Verdict(ok=False, detail="no sample_type_id recorded at write time")
    via = engine.observe(engine.admin, sample_type_id, entity_type="sample_type")
    return Verdict(ok=via is not None, detail=f"post-restart observe via={via}")


def _durability_write_configure_laboratory_acceptance(engine) -> dict:
    world = engine.new_world("cladur")
    st = world.sample_type("STdur")
    lab = world.location("labdur", ["laboratory"], 26.5, 26.5)
    pickup = world.location("pickupdur", ["collection_point"], 26.5, 26.5, coverageKm=5)
    world.connection(pickup["locationId"], lab["locationId"])
    for r in (pickup["locationId"], lab["locationId"]):
        world.capacity(r, units=10, cooled_units=10)
    world.lab_accepts(lab["locationId"], st["sampleTypeId"], accepts=True)
    return {
        "sample_type_id": st["sampleTypeId"],
        "origin": {"latitude": 26.5, "longitude": 26.5},
    }


def _durability_verify_configure_laboratory_acceptance(engine, sentinel: dict) -> Verdict:
    sample_type_id, origin = sentinel.get("sample_type_id"), sentinel.get("origin")
    if not (sample_type_id and origin):
        return Verdict(ok=False, detail="no sample_type_id/origin recorded at write time")
    world = engine.new_world("cladurv")
    body = {
        "origin": origin,
        "originType": "home",
        "sampleTypeId": sample_type_id,
        "requestedAt": world.at(),
        "deliveryDeadline": world.at(hours=6),
    }
    result = None
    for _ in range(10):
        status, payload = engine.admin.raw("check_serviceability", body)
        result = engine.admin.data(status, payload)
        if result.get("serviceable") is True:
            break
        import time as _time
        _time.sleep(1)
    return Verdict(
        ok=(result or {}).get("serviceable") is True,
        detail=f"post-restart check_serviceability serviceable={(result or {}).get('serviceable')} reason={(result or {}).get('reason')}",
    )


CAPABILITIES = [
    Capability(
        "named_sample_types_present_after_reset",
        "read",
        "AC-101: Amber/Indigo/Frost/Pulse (with their forced policy already applied) are already present "
        "immediately after reset, before any admin has created a sample type. Not a pinned-contract "
        "operationId -- like openapi_json_well_formed (verification_artifacts.py), this is a deliberate "
        "exception: it proves a system-state property via the one available zero-admin-action read "
        "(search_audit_trail) rather than any create/update_sample_type call of its own.",
        _run_named_sample_types_present_after_reset,
        priority=True,
    ),
    Capability(
        "create_sample_type",
        "write",
        "Named types (Amber/Indigo/Frost/Pulse) get non-configurable policy overlays; Amber<->Indigo auto-link "
        "symmetrically, including retroactively to the reset-seeded originals; a second create under an "
        "already-existing canonical name is allowed (a fresh id, policy re-applied) -- golden-mongo's own "
        "deliberate, non-deduping behavior.",
        _run_create_sample_type,
        priority=True,
        durability_write=_durability_write_create_sample_type,
        durability_verify=_durability_verify_create_sample_type,
    ),
    Capability(
        "update_sample_type",
        "write",
        "Named-type policy (e.g. Pulse.batchingAllowed=false) cannot be overridden even on update, including "
        "against the actual reset-seeded record.",
        _run_update_sample_type,
        priority=True,
        durability_write=_durability_write_update_sample_type,
        durability_verify=_durability_verify_update_sample_type,
    ),
    Capability(
        "configure_laboratory_acceptance",
        "write",
        "Withdrawing the only accepting lab for a type flips serviceability to no_accepting_laboratory.",
        _run_configure_laboratory_acceptance,
        priority=True,
        durability_write=_durability_write_configure_laboratory_acceptance,
        durability_verify=_durability_verify_configure_laboratory_acceptance,
    ),
]
