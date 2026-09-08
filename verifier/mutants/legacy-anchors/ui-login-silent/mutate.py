#!/usr/bin/env python3
"""UI MUTANT: login-silent.

Neutralises the login form's error announcement in a COPY of golden's
`LoginPage.vue`: the error paragraph `<p ref="errorElement" class="login-error"
role="alert" tabindex="-1">{{ error }}</p>` is changed to always render an
EMPTY string, regardless of what `error.value` actually holds. The backend
still genuinely refuses invalid credentials (the app never authenticates), but
the failure becomes SILENT -- exactly the near-miss risk the login scenario's
own author had to guard against (golden's error paragraph has no `v-if`, so it
is structurally always present; this mutant proves the scenario is actually
checking for TEXT, not bare presence).

The element has no sibling `v-else`/`v-else-if`, so replacing its interpolation
expression is a safe, surgical edit that cannot break the Vue build.

Usage: python3 mutate.py <path-to-LoginPage.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly (nonzero exit + stderr) if the target can't be found, so a silent no-op
can never masquerade as a passing mutant.
"""
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

target = (
    '      <p\n'
    '        ref="errorElement"\n'
    '        class="login-error"\n'
    '        role="alert"\n'
    '        tabindex="-1"\n'
    '      >\n'
    '        {{ error }}\n'
    '      </p>'
)
replacement = (
    '      <p\n'
    '        ref="errorElement"\n'
    '        class="login-error"\n'
    '        role="alert"\n'
    '        tabindex="-1"\n'
    '      >\n'
    '        {{ "" }}\n'
    '      </p>'
)
if target not in src:
    print("MUTATION DID NOT APPLY -- login-error paragraph shape changed", file=sys.stderr)
    sys.exit(2)
new_src = src.replace(target, replacement, 1)

if replacement not in new_src:
    print("MUTATION INCOMPLETE -- login-error text was not forced empty", file=sys.stderr)
    sys.exit(3)

path.write_text(new_src)
print("ui-login-silent applied to", path)
