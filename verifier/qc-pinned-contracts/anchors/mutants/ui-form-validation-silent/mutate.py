#!/usr/bin/env python3
"""UI MUTANT: form-validation-silent.

Swallows the create-request form's validation-error announcement in a COPY of
golden's `RequestPanel.vue`: the `<InlineResult v-if="action.error.value"
kind="error">{{ action.error.value }}</InlineResult>` block's `v-if` is forced
to a literal `false`, so a rejected submission (e.g. the pinned contract's
`invalid_collection_window`) is still caught by `useAction()` (the error text
is still computed and stored) but never RENDERED anywhere on the page. The
element has no sibling `v-else`/`v-else-if`, so forcing it off is a safe,
surgical edit that cannot break the Vue build.

Usage: python3 mutate.py <path-to-RequestPanel.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly (nonzero exit + stderr) if the target can't be found, so a silent no-op
can never masquerade as a passing mutant.
"""
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

target = '<InlineResult v-if="action.error.value" kind="error">'
replacement = '<InlineResult v-if="false" kind="error">'
if target not in src:
    print("MUTATION DID NOT APPLY -- InlineResult error block shape changed", file=sys.stderr)
    sys.exit(2)
new_src = src.replace(target, replacement, 1)

if replacement not in new_src:
    print("MUTATION INCOMPLETE -- InlineResult v-if not forced false", file=sys.stderr)
    sys.exit(3)

path.write_text(new_src)
print("ui-form-validation-silent applied to", path)
