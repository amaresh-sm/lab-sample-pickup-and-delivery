#!/usr/bin/env python3
"""UI MUTANT: role-switch-hidden.

Forces the role-switch `<select>` in a COPY of golden's `AppShell.vue` to never
render, even for a multi-role account: the wrapping `<label v-if="store.roles
.length > 1" class="role-switcher">` condition is forced to a literal `false`.
The element has NO sibling `v-else`/`v-else-if` (the next sibling is an
unconditional `<span>`), so forcing its `v-if` false is a safe, surgical edit
that cannot orphan a conditional chain or break the Vue build.

Usage: python3 mutate.py <path-to-AppShell.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly (nonzero exit + stderr) if the target can't be found, so a silent no-op
can never masquerade as a passing mutant.
"""
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

target = 'v-if="store.roles.length > 1" class="role-switcher"'
replacement = 'v-if="false" class="role-switcher"'
if target not in src:
    print("MUTATION DID NOT APPLY -- role-switcher v-if shape changed", file=sys.stderr)
    sys.exit(2)
new_src = src.replace(target, replacement, 1)

if 'v-if="false" class="role-switcher"' not in new_src:
    print("MUTATION INCOMPLETE -- role-switcher v-if not forced false", file=sys.stderr)
    sys.exit(3)

path.write_text(new_src)
print("ui-role-switch-hidden applied to", path)
