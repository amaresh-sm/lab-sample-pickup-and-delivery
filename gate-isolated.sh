#!/usr/bin/env bash
# gate-isolated.sh -- prove the ISOLATED verifier still discriminates.
#
# Golden scoring 1.0000 only shows isolation does not break CORRECT behaviour.
# It says nothing about whether the verifier still catches INCORRECT behaviour.
# That is the real risk of this change: trading known measurement noise for
# unknown blindness. This script is the check.
#
#   hollow anchor -> must stay near 0. A do-nothing app that fabricates
#                    plausible 2xx bodies and persists nothing must not score.
#   9 backend mutants -> each patches golden with ONE real defect (reproduced
#                    from the sibling golden-postgres app). Each must still be
#                    CAUGHT, i.e. score strictly below golden's 1.0000, and the
#                    specific capability the mutant targets must drop.
#
# `replan-copies-verbatim` is the one that matters most: it is what originally
# exposed `report_disruption`'s check as vacuous, and the FIRST version of this
# isolation change (which short-circuited park_field_workers) would have let it
# through undetected. It is the direct regression test for that mistake.
set -uo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"; cd "$root"
GOLDEN=verifier/qc-pinned-contracts/reference-solutions/golden-mongo
MUTANTS=verifier/qc-pinned-contracts/anchors/mutants
OUT=/tmp/gate-iso; mkdir -p "$OUT"

# ---------------------------------------------------------------- hollow anchor
# The hollow app is a standalone python http.server, not a workspace with a
# manifest, so verify-isolated (which resets datastores) does not apply: there is
# no datastore to reset and nothing accumulates between capabilities. Scoring it
# once in isolated MODE is the equivalent check.
echo "=== HOLLOW ANCHOR ==="
if [[ "${SKIP_HOLLOW:-0}" == 1 ]]; then
  echo "  skipped (SKIP_HOLLOW=1); previously measured 0.0737"
else
python3 "$GOLDEN/../../anchors/hollow/server.py" 8099 >"$OUT/hollow-server.log" 2>&1 &
hollow_pid=$!
sleep 3
# Probe the LISTENING PORT, not a specific path: the hollow server resolves its
# routes from the pinned contract, so hardcoding /ready here would couple this
# gate to a path the contract owns.
if ! python3 -c 'import socket,sys; s=socket.create_connection(("127.0.0.1",8099),3); s.close()' 2>/dev/null; then
  echo "  hollow server failed to start; see $OUT/hollow-server.log"
else
  VERIFIER_ISOLATED=1 python3 verifier/qc-pinned-contracts/engine/run.py \
    --base-url http://127.0.0.1:8099 \
    --contract tasks/lab-sample-pickup-and-delivery/openapi.contract.json \
    --admin-user admin --admin-pass admin123 \
    score --json-out "$OUT/hollow.json" >"$OUT/hollow.log" 2>&1
  echo "  hollow overall: $(grep -m1 'overall score' "$OUT/hollow.log" || echo NO_SCORE)"
fi
kill "$hollow_pid" 2>/dev/null; wait "$hollow_pid" 2>/dev/null
fi

# --------------------------------------------------------------------- mutants
echo
echo "=== BACKEND MUTANTS (each patches golden, scored isolated, then reverted) ==="
for m in $(ls "$MUTANTS" | grep -v '^ui-'); do
  patch="$MUTANTS/$m/mutation.patch"
  # assignment-stealing and capacity-leak-on-receipt have a RUNBOOK but no
  # patch: they are golden-POSTGRES bugs, analysed on paper and never built as
  # live mutants. Both RUNBOOKs already conclude the suite does NOT catch them
  # (accept_assignment has no negative probe; nothing reads
  # capacity.reservedUnits across a completed journey). Those are pre-existing
  # coverage gaps, unrelated to isolation -- nothing for this gate to run.
  [[ -f "$patch" ]] || { echo "  $m: analysis-only (no live mutant) -- N/A for this gate"; continue; }
  # apply to golden's working tree
  if ! git -C "$GOLDEN" apply "$root/$patch" 2>"$OUT/$m-apply.log"; then
    echo "  $m: PATCH FAILED TO APPLY (golden source drifted?) -- see $OUT/$m-apply.log"
    continue
  fi
  rm -rf "$OUT/$m"
  # A behavioural mutant can correctly break golden's OWN Go unit tests
  # (fairness-ignored breaks TestRecentDeclineLowersOfferPriority, exactly
  # because it removes the recency logic that test checks) -- that is
  # evidence the mutation is real, not a build defect. What THIS gate needs
  # is whether the HTTP-observable capability checks ALSO catch it, so skip
  # golden's own test suite here; never set this for real candidate scoring.
  SKIP_GO_TESTS=1 ./verify-isolated --workspace "$GOLDEN" --logs-dir "$OUT/$m" >"$OUT/$m-run.log" 2>&1
  # Golden scores 1.0000 on all 52 isolated, so ANY capability below 1.0 here is
  # a detection. Report which ones -- an overall drop alone would not show
  # whether the mutant was caught by the capability that targets it or by
  # unrelated collateral.
  python3 - "$OUT/$m/reward.json" "$m" <<'PY' 2>/dev/null || echo "  $m: ERR (no reward.json -- see $OUT/$m-run.log)"
import json, sys
d = json.load(open(sys.argv[1])); caps = d["capabilities"]
low = [(c["id"], c["credit"]) for c in caps if (c.get("credit") or 0) < 1.0]
print(f"  {sys.argv[2]}: overall={d['overall']:.4f}  graded={len(caps)}  "
      f"{'CAUGHT' if low else '!! NOT CAUGHT'} ({len(low)} cap(s) below 1.0)")
for cid, cr in sorted(low, key=lambda x: x[1]):
    print(f"        {cid:<34} {cr:.3f}")
PY
  # Revert: reverse-apply, then hard-restore any stragglers, so the next mutant
  # starts from pristine golden -- a leaked mutation would silently contaminate
  # every subsequent result.
  #
  # Both commands are PATHSPEC-SCOPED to golden's subtree. golden-mongo is a
  # subdirectory of this same repo, not a nested repo, so an unscoped
  # `git checkout -- .` here would also revert the engine changes under test.
  git -C "$GOLDEN" apply -R "$root/$patch" 2>/dev/null || true
  git -C "$GOLDEN" checkout -- "$root/$GOLDEN" 2>/dev/null || true
  leaked=$(git -C "$root" status --porcelain -- "$GOLDEN")
  [[ -n "$leaked" ]] && { echo "  !! $m LEAKED into golden -- ABORTING (later results would be contaminated):"; echo "$leaked"; exit 1; }
done
echo
echo "=== golden must still be pristine ==="
git -C "$root" status --porcelain -- "$GOLDEN"
echo "  (empty above = clean)"
echo "GATE_DONE"
