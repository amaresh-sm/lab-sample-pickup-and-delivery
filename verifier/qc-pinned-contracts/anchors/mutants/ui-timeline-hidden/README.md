# UI mutant: `ui-timeline-hidden`

**Target scenario:** `sample_view_shows_ordered_status_history` (AC-4).

**Mutation:** removes the ordered **status-change history** from a throwaway
copy of golden's sample tracking view (`TrackingPanel.vue`) — the `<ol>` bound
to `timeline` plus its `<EmptyState v-else>` fallback. The current-status
summary AND the route-progress "Ordered journey" stop list (AC-67) are left
intact, so the sample still shows its *current* status and its route progress,
but no history of how it got there — the AC-4 regression the scenario must
catch. Every other scenario stays green.

`mutate.py <path-to-TrackingPanel.vue>` applies it; it exits non-zero unless
both removals match (so a silent no-op can never masquerade as a passing
mutant).

## How it was applied (real golden untouched)

```bash
# inside the app container, on a COPY of the frontend at /tmp/mut2
cp -r /workspace/frontend /tmp/mut2
python3 mutate.py /tmp/mut2/src/components/TrackingPanel.vue
( cd /tmp/mut2 && npm run build )
APP_ADDR=127.0.0.1:18085 FRONTEND_DIR=/tmp/mut2/dist \
  OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets /workspace/bin/lab-golden-server &
```

## Measured result (gate)

| scenario                                          | golden | this mutant |
|---------------------------------------------------|:------:|:-----------:|
| `sample_view_shows_ordered_status_history`        | 1.000  | **0.500 (CAUGHT)** — history entries=1 (a lone current-status chip), distinct transitions=1 < 2 |
| `role_work_view_shows_current_and_past`           | 1.000  | 1.000 (isolated) |
| `sample_freshness_visible_to_owner`               | 1.000  | 1.000 (isolated) |
| `sample_view_shows_location_custodian_progress`   | 1.000  | 1.000 (isolated) |
| `map_renders_local_tiles_only`                    | 1.000  | 1.000 (isolated) |

The owner's detail region still resolves (via the stable "Pending handoff"
summary label) but now holds no multi-event history — only the lone current
status remains — so the AC-4 positive drops below the "≥2 distinct ordered
transitions" bar while the control (non-owner sees nothing) stays green: caught
at 0.500 and localised. The route-progress list is deliberately preserved to
prove the catch is specific to the STATUS HISTORY, not route progress (AC-67).
