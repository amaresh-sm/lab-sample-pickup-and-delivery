# UI mutant: `ui-monitor-sparse`

**Target scenario:** `monitor_dashboard_renders_categories` (AC-76).

**Mutation:** in a throwaway copy of golden's frontend, collapses the
dispatcher/admin operations monitor (`pages/OperationsPage.vue`) down to a
single monitorable category — the metric row is reduced to just the "Samples"
card, and the "Alerts & exceptions" / "Active routes" summary panels and the
disruption panel are removed. The remaining view exposes only one category
(samples), so the scenario's requirement of >= 3 distinct categories fails.

> Note: the scenario grades the **Live operations** view (`OperationsPage`),
> not the Overview/dashboard, because the operations view is the surface that
> renders the monitorable categories as first-class MetricCards + panel
> headings ("Samples", "Workers", "Collection points", "Laboratories",
> "Active routes", "Alerts & exceptions"); the Overview exposes only ~2 of
> those as clean metrics/headings for an admin. The mutant therefore targets
> `OperationsPage.vue`.

`mutate.py <path-to-OperationsPage.vue>` applies it and fails loudly (nonzero
exit + stderr) if any target block can't be found, so a silent no-op can never
masquerade as a passing mutant.

## How it was applied (real golden untouched)

```bash
# inside the app container, on a COPY of the frontend at /tmp/mut-mon
cp -r /workspace/frontend /tmp/mut-mon
python3 mutate.py /tmp/mut-mon/src/pages/OperationsPage.vue
( cd /tmp/mut-mon && npm run build )
APP_ADDR=127.0.0.1:18084 FRONTEND_DIR=/tmp/mut-mon/dist \
  OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets /workspace/bin/lab-golden-server &
```

## Measured result (gate)

| scenario                                | golden | this mutant |
|-----------------------------------------|:------:|:-----------:|
| `monitor_dashboard_renders_categories`  | pending validation run | pending validation run (expected: CAUGHT — categories drop below 3) |
| `map_renders_local_tiles_only`          | pending validation run | pending validation run (expected: isolated, unaffected) |
| `audit_view_renders_and_searches`       | pending validation run | pending validation run (expected: isolated, unaffected) |

Expected: the monitor scenario's positive flips to fail (only one category
renders) while other scenarios stay green. Numbers pending the separate
validation step.
