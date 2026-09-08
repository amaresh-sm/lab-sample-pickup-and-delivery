#!/usr/bin/env python3
"""UI MUTANT: worklist-no-completed.

Removes the PAST/COMPLETED work bucket from a COPY of golden's role-work view,
leaving the current/active buckets (pending + in progress) intact. So a role
still sees its live work but never its completed/past work -- exactly the
AC-3 regression the `role_work_view_shows_current_and_past` scenario must
catch, while every OTHER scenario (freshness AC-68/69, sample view AC-65/66/67,
status history AC-4, map AC-83) stays green (they never read the work view).

The mutation drops the `completed` entry from the WorkQueue tab list, so the
completed bucket has no affordance to reveal it and its items never render.

Usage: python3 mutate.py <path-to-WorkQueue.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly if the source shape changed so a silent no-op can never masquerade as a
passing mutant.
"""
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

# The tab-list entry that exposes the completed/past bucket, matched on the
# visible label + bucket key a user reads (robust to minor formatting edits).
pattern = r'\n\s*\{\s*key:\s*"completed",\s*label:\s*"Completed"\s*\},'
new = re.sub(pattern, '', src, count=1)

if new == src:
    print("MUTATION DID NOT APPLY -- source shape changed (no completed tab entry found)", file=sys.stderr)
    sys.exit(2)
if re.search(r'key:\s*"completed"', new):
    print("MUTATION INCOMPLETE -- a completed bucket entry still present", file=sys.stderr)
    sys.exit(3)
path.write_text(new)
print("ui-worklist-no-completed applied to", path)
