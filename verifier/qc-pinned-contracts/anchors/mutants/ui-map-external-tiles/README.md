# UI mutant: `ui-map-external-tiles`

**Target scenario:** `map_renders_local_tiles_only` (AC-83).

**Mutation:** injects an EXTERNAL raster tile source
(`https://tile.openstreetmap.org/{z}/{x}/{y}.png`) into a throwaway copy of the
map style. The local basemap stays, so the canvas still draws — only the "no
external tile service" property (AC-83) is violated.

`mutate.py <path-to-style.json>` applies it (no frontend rebuild needed; only
the served map assets change).

## How it was applied (real golden untouched)

```bash
# inside the app container, on a COPY of the map assets
cp -r /opt/arena/map-assets /tmp/mut-map-assets
python3 mutate.py /tmp/mut-map-assets/style.json
APP_ADDR=127.0.0.1:18085 FRONTEND_DIR=/workspace/frontend/dist \
  OFFLINE_MAP_ASSET_DIR=/tmp/mut-map-assets /workspace/bin/lab-golden-server &
```

## Measured result (gate)

| scenario                            | golden | this mutant |
|-------------------------------------|:------:|:-----------:|
| `map_renders_local_tiles_only`      | 1.000  | **0.500 (CAUGHT)** — 48 external egress attempts to tile.openstreetmap.org |
| `sample_freshness_visible_to_owner` | 1.000  | 1.000 (isolated, unaffected) |

The map scenario's positive verdict flips to fail because the verifier records
external egress attempts (captured via CSP-refused console violations — golden
ships `connect-src 'self'` — as well as by the route interceptor for apps with
no CSP). Freshness is untouched.
