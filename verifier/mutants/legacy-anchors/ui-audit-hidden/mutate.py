#!/usr/bin/env python3
"""UI MUTANT: audit-hidden.

Removes the rendered audit ENTRIES list from a COPY of golden's audit view
(`components/AuditPanel.vue`): the `<ol class="audit-list">` that renders each
audit record is deleted, so a search returns results the machine-readable output
still reflects but the human audit LIST never renders. The scenario's
entry-rendering and search-filtering assertions therefore fail, while the rest
of the app is untouched.

Usage: python3 mutate.py <path-to-AuditPanel.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly (nonzero exit + stderr) if the source shape changed and the list can't be
found, so a silent no-op can never masquerade as a passing mutant.
"""
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

# The rendered audit entries list, matched on the visible list class a user
# reads. It is EMPTIED rather than deleted: the list carries a `v-if` that a
# sibling empty-state `v-else-if` depends on, so removing the element outright
# orphans that `v-else-if` and breaks the Vue build. We keep the element and its
# `v-if` (so the template still compiles) but strip its `<li>` entries, so a
# search returns results the human LIST never renders -- the scenario's
# entry-rendering / search-filtering assertions fail while the app stays valid.
open_m = re.search(r'<ol\b[^>]*class="audit-list"[^>]*>', src, re.S)
if not open_m:
    print("MUTATION DID NOT APPLY -- audit-list source shape changed", file=sys.stderr)
    sys.exit(2)
vif_m = re.search(r'v-if="[^"]*"', open_m.group(0))
vif = vif_m.group(0) if vif_m else ""
replacement = f'<ol {vif} class="audit-entries-removed-by-mutant"></ol>'
pattern = re.compile(r'<ol\b[^>]*class="audit-list"[^>]*>.*?</ol>', re.S)
new_src, n = pattern.subn(replacement, src, count=1)
if n == 0:
    print("MUTATION DID NOT APPLY -- audit-list body not matched", file=sys.stderr)
    sys.exit(2)
if 'class="audit-list"' in new_src:
    print("MUTATION INCOMPLETE -- an audit-list still present", file=sys.stderr)
    sys.exit(3)
path.write_text(new_src)
print("ui-audit-hidden applied to", path)
