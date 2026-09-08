#!/usr/bin/env python3
"""UI MUTANT: accept-noop.

Neutralises the accept-assignment form's submit handler in a COPY of golden's
`OfferPanel.vue`: `<form class="stack-form" @submit.prevent="accept">` has its
handler replaced with a no-op (`@submit.prevent="() => {}"`). The "Accept
assignment" button still renders, is still clickable, and the form still
submits (prevented from a real page reload) -- only the ACTUAL
`accept_assignment` network call never fires, so the assignment never moves
out of the pending bucket server-side. This is exactly the WRITE-THROUGH gap
the scenario's own author must catch: a UI that looks fully functional (button
present, click succeeds, no JS error) but never persists the write.

The form element has no sibling `v-else`/`v-else-if`, so replacing its
`@submit.prevent` handler is a safe, surgical edit that cannot break the Vue
build.

Usage: python3 mutate.py <path-to-OfferPanel.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly (nonzero exit + stderr) if the target can't be found, so a silent no-op
can never masquerade as a passing mutant.
"""
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

target = '<form class="stack-form" @submit.prevent="accept">'
replacement = '<form class="stack-form" @submit.prevent="() => {}">'
count = src.count(target)
if count != 1:
    print(f"MUTATION DID NOT APPLY -- expected 1 occurrence of {target!r}, found {count}", file=sys.stderr)
    sys.exit(2)
new_src = src.replace(target, replacement)

if new_src.count(replacement) != 1 or target in new_src:
    print("MUTATION INCOMPLETE -- the accept form's submit handler was not neutralised", file=sys.stderr)
    sys.exit(3)

path.write_text(new_src)
print("ui-accept-noop applied to", path)
