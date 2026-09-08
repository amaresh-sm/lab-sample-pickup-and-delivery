#!/usr/bin/env bash
# sweep-isolated.sh -- score every benchmark candidate with the ISOLATED verifier.
#
# Every score in benchmark.md predates the isolation change and is therefore NOT
# comparable to these: they were produced in "shared" mode, where all 52
# capabilities ran against one accumulating app instance and leftover field
# workers from earlier capabilities could win offers the current capability's own
# worker needed. Those runs also predate the park_field_workers fix, which was
# silently parking ZERO workers on 8 of 8 candidates while parking golden
# correctly -- so the old table compared golden-under-clean-conditions against
# candidates-under-dirty-conditions. This sweep re-measures all of them on equal
# footing.
#
# 3 runs per candidate: the point is not just the score but the SPREAD. Isolation
# is only worth its runtime cost if the previously-flaky candidates (opus 0.1265,
# luna-xhigh 0.1282, terra 0.1094, sonnet 0.0325) collapse toward 0.0000.
#
# 2 concurrent lanes, deliberately. 4 lanes inflated a single run from ~20 to 82
# minutes and produced a score that later proved non-credible; 2 lanes was
# measured clean. Lanes do not contend for host ports -- verify-isolated
# publishes none, the engine reaches the app on 127.0.0.1 INSIDE each container.
set -uo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"; cd "$root"
RUNS="${RUNS:-3}"
LANES="${LANES:-2}"

CANDIDATES=(
  v3-opus-4.8-max v3-sol-xhigh luna-xhigh-fresh v3-terra-high v3-sonnet-medium
  oc-glm52-v2 v3-portkey-low v3-luna-high v3-haiku-low
)

jobs=()
for p in "${CANDIDATES[@]}"; do
  d=$(ls -d runs/${p}* 2>/dev/null | head -1)
  [[ -n "$d" && -f "$d/solution/workspace/verification/manifest.json" ]] \
    || { echo "SKIP $p -- no built workspace" >&2; continue; }
  for ((i=1; i<=RUNS; i++)); do jobs+=("$d|$i"); done
done
echo "[sweep] ${#jobs[@]} runs (${#CANDIDATES[@]} candidates x $RUNS), $LANES lanes"

run_one() {
  local d="${1%%|*}" i="${1##*|}"
  local out="$d/verifier-logs-iso-$i"
  rm -rf "$out"
  ./verify-isolated --workspace "$d/solution/workspace" --logs-dir "$out" \
    >"$d/iso-run-$i.log" 2>&1
  local s
  s=$(python3 -c "
import json;print(f\"{json.load(open('$out/reward.json'))['overall']:.4f}\")" 2>/dev/null || echo ERR)
  local n
  n=$(python3 -c "
import json;print(len(json.load(open('$out/reward.json'))['capabilities']))" 2>/dev/null || echo 0)
  printf '[sweep] %-34s run %s  overall=%s  graded=%s\n' "$(basename "$d")" "$i" "$s" "$n"
}

# Lane limiter: keep at most $LANES background jobs alive. Polls rather than
# using `wait -n`, which is bash 4.3+ (macOS ships bash 3.2 -- the same trap
# `mapfile` fell into in verify-isolated).
for j in "${jobs[@]}"; do
  while (( $(jobs -rp | wc -l) >= LANES )); do sleep 5; done
  run_one "$j" &
done
wait
echo "SWEEP_DONE"
