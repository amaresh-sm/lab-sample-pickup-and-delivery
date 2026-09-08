# UI mutant: `ui-freshness-hidden`

**Target scenario:** `sample_freshness_visible_to_owner` (AC-68 / AC-69).

**Mutation:** removes the "Last update" (AC-68) and "Location freshness"
(AC-69) rows from a throwaway copy of golden's sample tracking view. All other
tracking fields (status, holder, deadline, route progress, ordered journey)
stay intact, so the mutation is surgical: only the freshness scenario should
regress.

`mutate.py <path-to-TrackingPanel.vue>` applies it; it fails loudly if the
source shape changed and the labels can't be found (so a silent no-op can never
masquerade as a passing mutant).

## How it was applied (real golden untouched)

```bash
# inside the app container, on a COPY of the frontend at /tmp/mut-fresh
cp -r /workspace/frontend /tmp/mut-fresh
python3 mutate.py /tmp/mut-fresh/src/components/TrackingPanel.vue
( cd /tmp/mut-fresh && npm run build )
APP_ADDR=127.0.0.1:18084 FRONTEND_DIR=/tmp/mut-fresh/dist \
  OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets /workspace/bin/lab-golden-server &
```

## Measured result (gate)

| scenario                            | golden | this mutant |
|-------------------------------------|:------:|:-----------:|
| `sample_freshness_visible_to_owner` | 1.000  | **0.500 (CAUGHT)** — staleness indicator gone |
| `map_renders_local_tiles_only`      | 1.000  | 1.000 (isolated, unaffected) |

The freshness scenario's positive verdict flips to fail (the AC-69 indicator no
longer renders in the owner's tracking region) while the map scenario is
untouched — the mutation is caught and localised.
