#!/usr/bin/env python3
"""UI MUTANT: map-external-tiles.

Injects an EXTERNAL raster tile source (a public OpenStreetMap tile host) into
a COPY of the map style, so the map now reaches for tiles off the app's own
origin -- violating AC-83 ("Map assets are local only -- no external tile
service"). The local basemap is left in place so the canvas still draws; ONLY
the "no external calls" property should regress.

Usage: python3 mutate.py <path-to-style.json>

Applied to a throwaway copy of the map assets only; real golden assets are
never touched.
"""
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
style = json.loads(path.read_text())

style.setdefault("sources", {})["external-osm"] = {
    "type": "raster",
    "tiles": ["https://tile.openstreetmap.org/{z}/{x}/{y}.png"],
    "tileSize": 256,
    "attribution": "external",
}
style.setdefault("layers", [])
# Insert the external raster just above the background so it actually requests
# tiles on load.
style["layers"].insert(1, {"id": "external-basemap", "type": "raster", "source": "external-osm"})

path.write_text(json.dumps(style, indent=2))
print("ui-map-external-tiles applied to", path)
