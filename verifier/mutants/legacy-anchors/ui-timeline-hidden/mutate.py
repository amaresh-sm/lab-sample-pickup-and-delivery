#!/usr/bin/env python3
"""UI MUTANT: timeline-hidden.

Removes the ordered STATUS-CHANGE HISTORY from a COPY of golden's sample
tracking view, leaving the current-status summary AND the route-progress
"ordered journey" stop list intact. So the sample still shows its CURRENT
status (and its route progress -- AC-67), but no HISTORY of how it got there --
exactly the AC-4 regression the `sample_view_shows_ordered_status_history`
scenario must catch, while every OTHER scenario stays green (freshness
AC-68/69, sample view AC-65/66/67, work buckets AC-3, map AC-83).

Two surgical removals on the lifecycle-timeline block a user reads:
  * the `<ol v-if="timeline.length">` list of status-change events, and
  * its `<EmptyState v-else>` fallback (removed together so no orphan v-else
    remains).

Usage: python3 mutate.py <path-to-TrackingPanel.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly if fewer than both removals match so a silent no-op can never masquerade
as a passing mutant.
"""
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

patterns = [
    # the lifecycle history list (the <ol> bound to `timeline`)
    r'<ol v-if="timeline\.length".*?</ol>',
    # its empty-state fallback (kept as a pair so no orphaned v-else remains)
    r'<EmptyState\s+v-else\b.*?/>',
]
applied = 0
for pat in patterns:
    new = re.sub(pat, '<!-- status history removed by ui-timeline-hidden mutant -->', src, count=1, flags=re.S)
    if new != src:
        applied += 1
        src = new

if applied < len(patterns):
    print(f"MUTATION INCOMPLETE -- only {applied}/{len(patterns)} removals applied (source shape changed)", file=sys.stderr)
    sys.exit(2)
if re.search(r'v-if="timeline\.length"', src) or re.search(r'No lifecycle events yet', src):
    print("MUTATION INCOMPLETE -- a timeline element still present", file=sys.stderr)
    sys.exit(3)
path.write_text(src)
print("ui-timeline-hidden applied to", path)
