#!/usr/bin/env python3
"""UI MUTANT: freshness-hidden.

Removes the sample-freshness presentation (the "Last update" time -- AC-68 --
and the "Location freshness" Current/Stale indicator -- AC-69) from a COPY of
golden's tracking view. Everything else (deadline, status, holder, route
progress) is left intact, so ONLY the freshness scenario should regress.

Usage: python3 mutate.py <path-to-TrackingPanel.vue>

Applied to a throwaway golden copy only; real golden is never touched.
"""
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

# The two summary rows that render the freshness data values. Matched on the
# visible labels a user reads, so this stays robust to minor markup edits.
patterns = [
    r'<div>\s*<span>Last update</span>.*?</div>',
    r'<div>\s*<span>Location freshness</span>.*?</strong>\s*</div>',
]
before = src
for pat in patterns:
    src = re.sub(pat, '<!-- freshness removed by ui-freshness-hidden mutant -->', src, flags=re.S)

if src == before:
    print("MUTATION DID NOT APPLY -- source shape changed", file=sys.stderr)
    sys.exit(2)
if "Location freshness" in src or "Last update" in src:
    print("MUTATION INCOMPLETE -- a freshness label still present", file=sys.stderr)
    sys.exit(3)
path.write_text(src)
print("ui-freshness-hidden applied to", path)
