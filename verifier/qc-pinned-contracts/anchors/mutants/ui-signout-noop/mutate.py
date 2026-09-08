#!/usr/bin/env python3
"""UI MUTANT: signout-noop.

Neutralises BOTH sign-out buttons in a COPY of golden's `AppShell.vue`: the
`@click="store.logout"` handler on the mobile sign-out button and the sidebar
"quiet-button" sign-out button are both replaced with a no-op
(`@click="() => {}"`). The buttons still render with their original "Sign out"
text -- only the click behaviour is gutted -- so the positive assertion
(clicking sign-out returns the app to the public login form) must flip to
fail while every other control on the page is untouched.

Usage: python3 mutate.py <path-to-AppShell.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly (nonzero exit + stderr) if either target can't be found, so a silent
no-op can never masquerade as a passing mutant.
"""
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

target = '@click="store.logout"'
replacement = '@click="() => {}"'
count = src.count(target)
if count != 2:
    print(f"MUTATION DID NOT APPLY -- expected 2 occurrences of {target!r}, found {count}", file=sys.stderr)
    sys.exit(2)
new_src = src.replace(target, replacement)

if new_src.count(replacement) != 2 or target in new_src:
    print("MUTATION INCOMPLETE -- not all sign-out handlers were neutralised", file=sys.stderr)
    sys.exit(3)

path.write_text(new_src)
print("ui-signout-noop applied to", path)
