#!/usr/bin/env python3
"""UI MUTANT: capacity-hidden.

Suppresses the destination-capacity reading in a COPY of golden's
`WorkQueue.vue`: `capacityText()` is forced to always return an empty string,
regardless of what `item.destinationCapacity` actually holds. The backend
still genuinely reserves and reports capacity (the pinned contract's
`AssignmentWorkItem.destinationCapacity` is unaffected -- this is a
presentation-only cut), but a laboratory/node worker's own work view no
longer shows the regular/cooled capacity at their assigned facility at all
(instruction.md line 68 / AC-91's "View" half).

The template's `<small v-if="capacityText(item)">{{ capacityText(item) }}</small>`
has no sibling `v-else`/`v-else-if`; forcing the function to return "" simply
makes that `v-if` always false, which is a safe, surgical edit that cannot
break the Vue build.

Usage: python3 mutate.py <path-to-WorkQueue.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly (nonzero exit + stderr) if the target can't be found, so a silent no-op
can never masquerade as a passing mutant.
"""
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

target = (
    'function capacityText(item: EntityRecord): string {\n'
    '  const value = item.destinationCapacity;\n'
    '  if (!value || typeof value !== "object") return "";\n'
    '  const capacity = value as EntityRecord;\n'
    '  return `Capacity ${String(capacity.availableUnits ?? 0)} regular · ${String(\n'
    '    capacity.cooledAvailableUnits ?? 0,\n'
    '  )} cooled`;\n'
    '}'
)
replacement = (
    'function capacityText(item: EntityRecord): string {\n'
    '  void item;\n'
    '  return "";\n'
    '}'
)
if target not in src:
    print("MUTATION DID NOT APPLY -- capacityText() function shape changed", file=sys.stderr)
    sys.exit(2)
new_src = src.replace(target, replacement, 1)

if replacement not in new_src or "capacity.availableUnits" in new_src:
    print("MUTATION INCOMPLETE -- capacityText() was not forced empty", file=sys.stderr)
    sys.exit(3)

path.write_text(new_src)
print("ui-capacity-hidden applied to", path)
