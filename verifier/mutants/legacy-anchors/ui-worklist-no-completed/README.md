# UI mutant: `ui-worklist-no-completed`

**Target scenario:** `role_work_view_shows_current_and_past` (AC-3).

**Mutation:** drops the **completed / past** work bucket from a throwaway copy
of golden's role-work view (`WorkQueue.vue`) by removing the `completed` entry
from its tab list. The current/active buckets (pending + in progress) stay
intact, so the worker still sees their live work but never their completed/past
work — the AC-3 regression the scenario must catch. Every other scenario stays
green (none reads the work view).

`mutate.py <path-to-WorkQueue.vue>` applies it; it exits non-zero if the
`completed` tab entry is not found (so a silent no-op can never masquerade as a
passing mutant).

## How it was applied (real golden untouched)

```bash
# inside the app container, on a COPY of the frontend at /tmp/mut1
cp -r /workspace/frontend /tmp/mut1
python3 mutate.py /tmp/mut1/src/components/WorkQueue.vue
( cd /tmp/mut1 && npm run build )
APP_ADDR=127.0.0.1:18084 FRONTEND_DIR=/tmp/mut1/dist \
  OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets /workspace/bin/lab-golden-server &
```

## Measured result (gate)

| scenario                                          | golden | this mutant |
|---------------------------------------------------|:------:|:-----------:|
| `role_work_view_shows_current_and_past`           | 1.000  | **0.000 (CAUGHT)** — active item rendered=True, completed item rendered=**False** |
| `sample_view_shows_ordered_status_history`        | 1.000  | 1.000 (isolated) |
| `sample_freshness_visible_to_owner`               | 1.000  | 1.000 (isolated) |
| `sample_view_shows_location_custodian_progress`   | 1.000  | 1.000 (isolated) |
| `map_renders_local_tiles_only`                    | 1.000  | 1.000 (isolated) |

The completed sample becomes unreachable (no bucket affordance reveals it), so
the AC-3 positive flips to fail while every other scenario is untouched — the
mutation is caught and localised.
