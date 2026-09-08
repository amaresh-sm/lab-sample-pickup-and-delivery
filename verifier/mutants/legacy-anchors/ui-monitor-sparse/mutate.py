#!/usr/bin/env python3
"""UI MUTANT: monitor-sparse.

Collapses the dispatcher/admin operations MONITOR in a COPY of golden's
frontend down to a SINGLE monitorable category: in ``pages/OperationsPage.vue``
the metric row is reduced to just the "Samples" card, and the alerts/routes
summary panels and the disruption panel are removed. The remaining page shows
only one category (samples), so the monitor scenario's requirement of >= 3
distinct categories fails.

Usage: python3 mutate.py <path-to-OperationsPage.vue>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly (nonzero exit + stderr) if any target block can't be found, so a silent
no-op can never masquerade as a passing mutant.
"""
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
src = path.read_text()

edits = [
    # Reduce the metric row to the single "Samples" card.
    (
        re.compile(r'<div class="metrics">.*?</div>', re.S),
        '<div class="metrics">\n'
        '      <MetricCard\n'
        '        label="Samples"\n'
        '        :value="store.operations.samples.length"\n'
        '        detail="Role-visible journeys"\n'
        '      />\n'
        '    </div>',
    ),
    # Remove the alerts + routes summary panels.
    (re.compile(r'<div class="page-grid page-grid-2 align-start">.*?</div>', re.S),
     '<!-- alerts/routes summary removed by ui-monitor-sparse mutant -->'),
    # Remove the disruption panel.
    (re.compile(r'<DisruptionPanel\s*/>'),
     '<!-- disruption panel removed by ui-monitor-sparse mutant -->'),
]

for i, (pat, repl) in enumerate(edits, start=1):
    src, n = pat.subn(repl, src, count=1)
    if n == 0:
        print(f"MUTATION DID NOT APPLY -- edit {i} target not found (source shape changed)", file=sys.stderr)
        sys.exit(1 + i)

# Sanity: only one MetricCard should remain, and no summary panel is rendered.
if src.count("<MetricCard") != 1:
    print(f"MUTATION INCOMPLETE -- expected 1 MetricCard, found {src.count('<MetricCard')}", file=sys.stderr)
    sys.exit(6)
if "<OperationsSummary" in src:
    print("MUTATION INCOMPLETE -- an OperationsSummary panel still rendered", file=sys.stderr)
    sys.exit(7)

path.write_text(src)
print("ui-monitor-sparse applied to", path)
