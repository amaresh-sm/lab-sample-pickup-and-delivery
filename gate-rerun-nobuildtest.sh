#!/usr/bin/env bash
# Re-run the mutants whose BUILD failed in gate-isolated.sh.
#
# Those builds failed because golden's own `go test ./...` (run by
# verification/build.sh) correctly detects the injected defect -- e.g.
# fairness-ignored trips TestRecentDeclineLowersOfferPriority. That is the unit
# suite working, but it aborts the build, so the HTTP verifier never gets to
# grade the mutant, and the gate cannot answer its actual question: does the
# VERIFIER still catch this?
#
# So for these runs only, the test step is lifted -- which is exactly what each
# mutant's own RUNBOOK "Rebuild + restart" recipe does (bare `go build`, no
# `go test`). The edit is reverted with the mutation afterwards.
#
# Pass mutant names as arguments.
set -uo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"; cd "$root"
GOLDEN=verifier/qc-pinned-contracts/reference-solutions/golden-mongo
MUTANTS=verifier/qc-pinned-contracts/anchors/mutants
OUT=/tmp/gate-iso; mkdir -p "$OUT"
(($#)) || { echo "usage: $0 <mutant-name>..." >&2; exit 2; }

for m in "$@"; do
  patch="$MUTANTS/$m/mutation.patch"
  [[ -f "$patch" ]] || { echo "  $m: no mutation.patch"; continue; }
  git -C "$GOLDEN" apply "$root/$patch" || { echo "  $m: patch failed"; continue; }
  # lift the unit-test gate (reverted below with everything else)
  sed -i '' 's|^go test \./\.\.\.$|: "SKIPPED for mutant grading -- see gate-rerun-nobuildtest.sh"|' \
    "$GOLDEN/verification/build.sh"
  grep -q 'SKIPPED for mutant grading' "$GOLDEN/verification/build.sh" \
    || { echo "  $m: could not lift the test gate -- build.sh changed shape"; git -C "$GOLDEN" checkout -- .; continue; }

  rm -rf "$OUT/$m"
  ./verify-isolated --workspace "$GOLDEN" --logs-dir "$OUT/$m" >"$OUT/$m-run.log" 2>&1
  python3 - "$OUT/$m/reward.json" "$m" <<'PY' 2>/dev/null || echo "  $m: ERR (see $OUT/$m-run.log)"
import json, sys
d = json.load(open(sys.argv[1])); caps = d["capabilities"]
low = [(c["id"], c["credit"]) for c in caps if (c.get("credit") or 0) < 1.0]
print(f"  {sys.argv[2]}: overall={d['overall']:.4f}  graded={len(caps)}  "
      f"{'CAUGHT' if low else '!! NOT CAUGHT'} ({len(low)} cap(s) below 1.0)")
for cid, cr in sorted(low, key=lambda x: x[1]):
    print(f"        {cid:<34} {cr:.3f}")
PY

  git -C "$GOLDEN" apply -R "$root/$patch" 2>/dev/null || true
  git -C "$GOLDEN" checkout -- "$root/$GOLDEN" 2>/dev/null || true
  leaked=$(git -C "$root" status --porcelain -- "$GOLDEN")
  [[ -n "$leaked" ]] && { echo "  !! $m LEAKED into golden -- ABORTING:"; echo "$leaked"; exit 1; }
done
echo "RERUN_DONE"
