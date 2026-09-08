# UI mutant: `ui-exception-hidden`

**Target scenario:** `sample_view_surfaces_exception_flag` (AC-70).

**Mutation:** in a throwaway copy of golden's frontend, remaps an `exception`
status to the neutral label `active` in the two places the tracking view
surfaces it:

- `components/StatusBadge.vue` -- the canonical status badge (`Exception` ->
  `Active`);
- `components/TrackingPanel.vue` -- the "My requests" list echoes the status as
  plain text (`titleCase(item.status)`), remapped for `exception` only.

Every other status renders unchanged, so the mutation is surgical: only the
exception scenario should regress. `mutate.py <path-to-frontend-src>` applies it
and fails loudly (nonzero exit + stderr) if either target's source shape changed
and the anchors can't be found, so a silent no-op can never masquerade as a
passing mutant.

## How it was applied (real golden untouched)

```bash
# inside the app container, on a COPY of the frontend at /tmp/mut-exc
cp -r /workspace/frontend /tmp/mut-exc
python3 mutate.py /tmp/mut-exc/src
( cd /tmp/mut-exc && npm run build )
APP_ADDR=127.0.0.1:18084 FRONTEND_DIR=/tmp/mut-exc/dist \
  OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets /workspace/bin/lab-golden-server &
```

## Measured result (gate)

| scenario                                | golden | this mutant |
|-----------------------------------------|:------:|:-----------:|
| `sample_view_surfaces_exception_flag`   | pending validation run | pending validation run (expected: CAUGHT — exception indicator gone) |
| `sample_view_shows_location_custodian_progress` | pending validation run | pending validation run (expected: isolated, unaffected) |
| `map_renders_local_tiles_only`          | pending validation run | pending validation run (expected: isolated, unaffected) |

Expected: the exception scenario's positive flips to fail (no exception word in
the owner's sample region) while other scenarios stay green. Numbers pending the
separate validation step.
