#!/usr/bin/env python3
"""UI MUTANT: exception-hidden.

Neutralises the sample-EXCEPTION presentation in a COPY of golden's tracking
view: an ``exception`` status is remapped to a generic/neutral label ("active")
BOTH where the canonical status badge renders it (StatusBadge.vue) AND where the
tracking view echoes the status as plain text in its "My requests" list
(TrackingPanel.vue). Every OTHER status is left untouched, so ONLY the exception
scenario should regress.

Two edits are required because golden surfaces the exception in two places in
the tracking view; patching only the badge would leave the plain-text echo, and
the exception scenario's owner region would still read "Exception".

Usage: python3 mutate.py <path-to-frontend-src-or-components-dir>

Applied to a throwaway golden copy only; real golden is never touched. Fails
loudly (nonzero exit + stderr) if either target's source shape changed, so a
silent no-op can never masquerade as a passing mutant.
"""
import sys
from pathlib import Path


def _resolve(root: Path, name: str) -> Path:
    for candidate in (root / name, root / "components" / name, root / "src" / "components" / name):
        if candidate.exists():
            return candidate
    matches = list(root.rglob(name))
    if matches:
        return matches[0]
    print(f"MUTATION TARGET NOT FOUND -- {name} under {root}", file=sys.stderr)
    sys.exit(2)


root = Path(sys.argv[1])

# ---- edit 1: StatusBadge.vue -- remap the normalized status value -----------
badge_path = _resolve(root, "StatusBadge.vue")
badge = badge_path.read_text()
badge_target = 'String(props.value ?? "unknown").toLowerCase()'
badge_patched = (
    '((v) => (v === "exception" ? "active" : v))'
    '(String(props.value ?? "unknown").toLowerCase())'
)
if badge_target not in badge:
    print("MUTATION DID NOT APPLY -- StatusBadge normalization shape changed", file=sys.stderr)
    sys.exit(3)
badge = badge.replace(badge_target, badge_patched, 1)

# ---- edit 2: TrackingPanel.vue -- remap the "My requests" plain-text status -
tracking_path = _resolve(root, "TrackingPanel.vue")
tracking = tracking_path.read_text()
tracking_target = 'titleCase(item.status ?? "planned")'
tracking_patched = "titleCase((item.status === 'exception' ? 'active' : item.status) ?? \"planned\")"
if tracking_target not in tracking:
    print("MUTATION DID NOT APPLY -- TrackingPanel status echo shape changed", file=sys.stderr)
    sys.exit(4)
tracking = tracking.replace(tracking_target, tracking_patched, 1)

badge_path.write_text(badge)
tracking_path.write_text(tracking)
if 'exception' not in badge or "'exception'" not in tracking:
    print("MUTATION INCOMPLETE -- remap marker missing after write", file=sys.stderr)
    sys.exit(5)
print("ui-exception-hidden applied to", badge_path, "and", tracking_path)
