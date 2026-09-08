#!/usr/bin/env python3
"""UI MUTANT: dryrun-writes.

Makes the "dry run" LIE in a COPY of golden's `ServiceabilityCheckPanel.vue`:
the `check()` function still calls `check_serviceability` and still renders a
verdict (AC-37 stays intact), but a SECOND, fire-and-forget POST to
`create_collection_request` is added right after it, using the CURRENT
session's own identity and the panel's own sample type -- so every "dry run"
click silently creates a real request/sample behind the scenes, violating
AC-38 ("The dry run creates NO request, sample, journey, assignment or
capacity reservation").

This is exactly the SIDE-EFFECT-AWARENESS risk the scenario's own seeder
guards against: a candidate (or a hollow/cheating one) could render a
correct-looking "Serviceable" verdict while secretly creating something
anyway. The scenario must catch this via an HTTP re-read (the requester's own
`list_role_work` count before/after), never by trusting the rendered verdict.

Usage: python3 mutate.py <path-to-ServiceabilityCheckPanel.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly (nonzero exit + stderr) if the target can't be found, so a silent no-op
can never masquerade as a passing mutant.
"""
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

target = (
    '  const next = await action.run(() =>\n'
    '    request<ServiceabilityVerdict>(\n'
    '      routes.checkServiceability,\n'
    '      { method: "POST", body: JSON.stringify(payload) },\n'
    '      store.session,\n'
    '    ),\n'
    '  );\n'
    '  if (!next) return;\n'
    '  verdict.value = next;\n'
    '}'
)
replacement = (
    '  const next = await action.run(() =>\n'
    '    request<ServiceabilityVerdict>(\n'
    '      routes.checkServiceability,\n'
    '      { method: "POST", body: JSON.stringify(payload) },\n'
    '      store.session,\n'
    '    ),\n'
    '  );\n'
    '  if (!next) return;\n'
    '  verdict.value = next;\n'
    '  // MUTATION: a "dry run" that is not actually dry -- fire-and-forget a\n'
    '  // real create alongside the check, attributed to the CURRENT session.\n'
    '  void request(routes.createCollectionRequest, {\n'
    '    method: "POST",\n'
    '    body: JSON.stringify({\n'
    '      origin: payload.origin,\n'
    '      originType: payload.originType,\n'
    '      requestedAt: payload.requestedAt,\n'
    '      deliveryDeadline: payload.deliveryDeadline,\n'
    '      sampleTypeId: payload.sampleTypeId,\n'
    '      containerType: "sealed-tube",\n'
    '      temperatureMinC: 2,\n'
    '      temperatureMaxC: 8,\n'
    '    }),\n'
    '  }, store.session).catch(() => undefined);\n'
    '}'
)
if target not in src:
    print("MUTATION DID NOT APPLY -- check() function shape changed", file=sys.stderr)
    sys.exit(2)
new_src = src.replace(target, replacement, 1)

if "createCollectionRequest" not in new_src:
    print("MUTATION INCOMPLETE -- side-effect create call was not inserted", file=sys.stderr)
    sys.exit(3)

path.write_text(new_src)
print("ui-dryrun-writes applied to", path)
