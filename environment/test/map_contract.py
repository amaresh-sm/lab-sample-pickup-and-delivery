#!/usr/bin/env python3
"""Validate the pinned offline Bangalore map asset bundle."""

import hashlib
import json
import struct
from pathlib import Path


SCRIPT_PATH = Path(__file__).resolve()
REPOSITORY_ROOT = SCRIPT_PATH.parents[2]
REPOSITORY_MAP_DIR = REPOSITORY_ROOT / "environment" / "assets" / "map"
if REPOSITORY_MAP_DIR.is_dir():
    ROOT = REPOSITORY_ROOT
    MAP_DIR = REPOSITORY_MAP_DIR
else:
    ROOT = SCRIPT_PATH.parents[1]
    MAP_DIR = ROOT / "map-assets"
EXPECTED_BOUNDS = [77.45, 12.8, 77.85, 13.2]
EXPECTED_CENTER = [77.5946, 12.9716]
EXPECTED_LAYERS = {
    "background",
    "water",
    "landuse",
    "buildings",
    "roads",
    "boundaries",
    "places",
}


def require(condition: bool, message: str) -> None:
    """Raise a concise contract error when a condition is false."""
    if not condition:
        raise AssertionError(message)


def read_required(relative_path: str) -> bytes:
    """Read one required map file and report its repository-relative name."""
    path = MAP_DIR / relative_path
    require(path.is_file(), f"missing required map asset: {path.relative_to(ROOT)}")
    return path.read_bytes()


def validate_pmtiles(content: bytes) -> None:
    """Validate the stable fields in a PMTiles v3 binary header."""
    require(len(content) >= 127, "bangalore.pmtiles is shorter than a PMTiles header")
    require(content[:7] == b"PMTiles", "bangalore.pmtiles has invalid magic")
    require(content[7] == 3, f"unsupported PMTiles version: {content[7]}")
    require(content[101] == 14, f"unexpected PMTiles maximum zoom: {content[101]}")

    encoded_bounds = struct.unpack_from("<iiii", content, 102)
    bounds = [round(value / 10_000_000, 7) for value in encoded_bounds]
    require(bounds == EXPECTED_BOUNDS, f"unexpected PMTiles bounds: {bounds}")


def validate_metadata(content: bytes) -> None:
    """Validate pinned source and geography metadata."""
    metadata = json.loads(content)
    expected = {
        "source_archive": "https://build.protomaps.com/20260716.pmtiles",
        "basemap_schema": "4.14.11",
        "pmtiles_cli": "1.31.1",
        "bounds": EXPECTED_BOUNDS,
        "center": EXPECTED_CENTER,
        "min_zoom": 0,
        "max_zoom": 14,
        "extracted_on": "2026-07-16",
    }
    for key, value in expected.items():
        require(metadata.get(key) == value, f"metadata field {key!r} is not pinned correctly")


def validate_style(content: bytes) -> None:
    """Validate that the MapLibre style uses only bundled map resources."""
    style = json.loads(content)
    require(style.get("version") == 8, "style must use MapLibre style version 8")
    require(style.get("center") == EXPECTED_CENTER, "style has an unexpected center")
    require(style.get("glyphs") == "/map-assets/fonts/{fontstack}/{range}.pbf", "glyph URL is not local")
    require(style.get("sprite") == "/map-assets/sprites/light", "sprite URL is not local")

    source = style.get("sources", {}).get("basemap", {})
    require(source.get("type") == "vector", "basemap source must be vector tiles")
    require(source.get("url") == "pmtiles:///map-assets/bangalore.pmtiles", "basemap PMTiles URL is not local")
    require("OpenStreetMap contributors" in source.get("attribution", ""), "style is missing OSM attribution")

    layer_ids = {layer.get("id") for layer in style.get("layers", [])}
    require(EXPECTED_LAYERS <= layer_ids, f"style is missing layers: {sorted(EXPECTED_LAYERS - layer_ids)}")


def validate_checksums(content: bytes) -> None:
    """Require a checksum entry for every public map asset except the list."""
    expected_paths = {
        path.relative_to(MAP_DIR).as_posix()
        for path in MAP_DIR.rglob("*")
        if path.is_file() and path.name != "sha256sums.txt"
    }
    recorded: dict[str, str] = {}
    for line in content.decode("utf-8").splitlines():
        checksum, relative_path = line.split(maxsplit=1)
        recorded[relative_path.removeprefix("./")] = checksum
    require(set(recorded) == expected_paths, "sha256sums.txt does not cover the complete map bundle")

    for relative_path, expected_checksum in recorded.items():
        actual_checksum = hashlib.sha256((MAP_DIR / relative_path).read_bytes()).hexdigest()
        require(actual_checksum == expected_checksum, f"checksum mismatch: {relative_path}")


def main() -> None:
    """Run all map asset contract checks."""
    archive = read_required("bangalore.pmtiles")
    metadata = read_required("metadata.json")
    style = read_required("style.json")
    notice = read_required("NOTICE.md").decode("utf-8")
    read_required("fonts/Noto Sans Regular/0-255.pbf")
    read_required("sprites/light.json")
    read_required("sprites/light.png")
    read_required("sprites/light@2x.json")
    read_required("sprites/light@2x.png")
    checksums = read_required("sha256sums.txt")

    validate_pmtiles(archive)
    validate_metadata(metadata)
    validate_style(style)
    for required_notice in ("OpenStreetMap", "ODbL", "Protomaps", "SIL Open Font License", "MIT"):
        require(required_notice in notice, f"NOTICE.md is missing {required_notice}")
    validate_checksums(checksums)
    print("offline Bangalore map contract: PASS")


if __name__ == "__main__":
    main()
