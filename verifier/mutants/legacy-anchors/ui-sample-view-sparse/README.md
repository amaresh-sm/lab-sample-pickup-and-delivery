# UI mutant: `ui-sample-view-sparse`

**Target scenario:** `sample_view_shows_location_custodian_progress`
(AC-65 / AC-66 / AC-67).

**Mutation:** removes the **current custodian** (AC-66) and the **route
progress** (AC-67) presentation from a throwaway copy of golden's sample
tracking view — the "Current holder" summary row, the "Route progress"
(`N / M legs`) summary row, and the "Ordered journey" stop list. The
current/last **location** (AC-65), status, deadline and freshness rows are left
intact, so the mutation is surgical: only the sample-view scenario should
regress, while freshness (AC-68/69) and the map (AC-83) stay green.

`mutate.py <path-to-TrackingPanel.vue>` applies it; it fails loudly if fewer
than all three removals match (so a silent no-op can never masquerade as a
passing mutant).

## How it was applied (real golden untouched)

```bash
# inside the app container, on a COPY of the frontend at /tmp/mut-sv
cp -r /workspace/frontend /tmp/mut-sv
python3 mutate.py /tmp/mut-sv/src/components/TrackingPanel.vue
( cd /tmp/mut-sv && npm run build )
APP_ADDR=127.0.0.1:18092 FRONTEND_DIR=/tmp/mut-sv/dist \
  OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets /workspace/bin/lab-golden-server &
```

## Measured result (gate)

| scenario                                          | golden | this mutant |
|---------------------------------------------------|:------:|:-----------:|
| `sample_view_shows_location_custodian_progress`   | 1.000  | **0.500 (CAUGHT)** — location(AC-65)=True, custodian(AC-66)=**False**, route-progress(AC-67)=**False** |
| `sample_freshness_visible_to_owner`               | 1.000  | 1.000 (isolated, unaffected) |
| `map_renders_local_tiles_only`                    | 1.000  | 1.000 (isolated, unaffected) |

The sample-view positive flips to fail because the owner's detail region no
longer surfaces the custodian or the route progress, while the location row and
the freshness/map scenarios are untouched — the mutation is caught and
localised.
