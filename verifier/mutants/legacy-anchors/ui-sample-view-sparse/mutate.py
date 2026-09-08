#!/usr/bin/env python3
"""UI MUTANT: sample-view-sparse.

Removes the CURRENT CUSTODIAN (AC-66) and ROUTE PROGRESS (AC-67) presentation
from a COPY of golden's sample tracking view, while leaving the current/last
location (AC-65), status, deadline and freshness intact. So ONLY the
`sample_view_shows_location_custodian_progress` scenario should regress; the
freshness (AC-68/69) and map (AC-83) scenarios must stay green.

Three surgical removals on the visible labels a user reads:
  * the "Current holder" summary row (custodian),
  * the "Route progress" summary row (the "N / M legs" ratio), and
  * the "Ordered journey" route-progress list.

Usage: python3 mutate.py <path-to-TrackingPanel.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly if the source shape changed so a silent no-op can never masquerade as a
passing mutant.
"""
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

patterns = [
    r'<div>\s*<span>Current holder</span>.*?</div>',
    r'<div>\s*<span>Route progress</span>.*?</strong>\s*</div>',
    r'<div v-if="routeStops\.length"[^>]*>.*?</ol>\s*</div>',
]
before = src
applied = 0
for pat in patterns:
    new = re.sub(pat, '<!-- custodian/progress removed by ui-sample-view-sparse mutant -->', src, flags=re.S)
    if new != src:
        applied += 1
        src = new

if applied < len(patterns):
    print(f"MUTATION INCOMPLETE -- only {applied}/{len(patterns)} removals applied (source shape changed)", file=sys.stderr)
    sys.exit(2)
if "Current holder" in src or "Route progress" in src or "Ordered journey" in src:
    print("MUTATION INCOMPLETE -- a custodian/progress label still present", file=sys.stderr)
    sys.exit(3)
path.write_text(src)
print("ui-sample-view-sparse applied to", path)
