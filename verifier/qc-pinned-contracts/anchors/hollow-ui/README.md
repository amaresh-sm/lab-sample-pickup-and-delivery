# Hollow-UI anchor

A deliberately empty frontend (`index.html`) served by golden's REAL backend,
used to prove the UI verifier scores a non-functional presentation layer at ~0.

It even renders a login-SHAPED form that does nothing, to demonstrate the
verifier requires a genuine authenticated session and real rendered data values
— not merely the presence of form controls — before awarding credit.

## How it was run

```bash
# golden's real backend, but its frontend swapped for this shell, on a new port
APP_ADDR=127.0.0.1:18083 \
  FRONTEND_DIR=/opt/gym/verifier/qc-pinned-contracts/anchors/hollow-ui \
  OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets /workspace/bin/lab-golden-server &
```

## Measured result (gate)

| scenario                            | golden | hollow-ui |
|-------------------------------------|:------:|:---------:|
| `map_renders_local_tiles_only`      | 1.000  | **0.000** |
| `sample_freshness_visible_to_owner` | 1.000  | **0.000** |

HTTP seeding still succeeds (the backend is real), but login never reaches an
authenticated state, no map canvas ever draws, and no freshness is ever
rendered — so every scenario scores 0.
