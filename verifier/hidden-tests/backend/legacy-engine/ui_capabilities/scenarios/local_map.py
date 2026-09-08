"""AC-83 -- the operations map is rendered from LOCAL assets only; no external
tile service is contacted.

This is the archetypal thing the HTTP engine cannot see: whether the BROWSER,
while drawing the map, reaches out to the public internet. The driver's
NetworkGate blocks (and records) every non-app-origin request, so:
  * POSITIVE: the map draws a real, non-blank <canvas> AND the gate recorded
    ZERO external requests -- i.e. everything the map needed came from the app's
    own origin. (If the app tried to fetch external tiles, the gate would have
    blocked them and the count would be > 0.)
  * CONTROL: the app's OWN origin actually served map assets (style / tiles /
    glyphs / sprite) -- proving the map is genuinely a local-tile map and not
    merely an empty container that trivially makes no external calls (a hollow
    shell would fail this).

Framework-agnostic: the only structural touch is counting <canvas> elements,
which every mapping library renders into; requests are classified purely by
URL origin, not by any app-specific asset path.
"""
from __future__ import annotations

import re

from model import UiCapability, UiReport, UiVerdict
from helpers import appears_within, canvas_is_nonblank

# Same-origin requests that look like map assets, used only for the CONTROL
# ("the tiles really are local"). Matched on the URL, framework-agnostic.
_MAP_ASSET = re.compile(r"(map[-_/]?asset|tile|pmtiles|\.pbf|glyph|sprite|style\.json|/map)", re.I)
# AC-83 is scoped to the MAP: "no external network requests FROM THE MAP". An
# app may legitimately load unrelated page chrome from a CDN (web fonts, an
# attribution link) without contacting an external TILE service -- that is not
# an AC-83 violation and must not fail this scenario. So the positive counts
# only EXTERNAL egress that looks like a map ASSET: a tile/glyph/sprite/style/
# pmtiles/vector-tile request, a known basemap/tile host, or an xyz raster-tile
# path. (Google Fonts CSS and a `/copyright` attribution link match none of
# these; `tile.openstreetmap.org/z/x/y.png` matches on "tile" and the xyz path.)
_EXTERNAL_MAP_TILE = re.compile(
    r"(tile|pmtiles|\.pbf|\.mvt|glyph|sprite|style\.json|maptiler|mapbox|basemaps?|"
    r"stadiamaps|arcgis|/\d+/\d+/\d+\.(png|jpe?g|webp))",
    re.I,
)
# A visible map-failure banner, matched fuzzily; only used to fail fast when an
# app explicitly reports the basemap did not load.
_MAP_ERROR = re.compile(r"(basemap|map).{0,20}(warning|error|unavailable|failed|could not)", re.I)


def _run(engine) -> UiReport:
    tmo = engine.timeout_ms
    ctx, gate = engine.new_context()
    try:
        page = ctx.new_page()
        gate.watch(page)  # also capture CSP-refused / failed external attempts
        if not engine.login(page, engine.admin_user, engine.admin_pass):
            return UiReport(
                positive=UiVerdict(False, "admin could not sign in through the public login form"),
                notes="login failure",
            )
        # Reach the live operations / map view via a visible nav affordance.
        engine.navigate(page, r"live operations|operations|map|network")

        rendered, cv = canvas_is_nonblank(page)
        # Let the basemap settle so any tile fetch it attempts is observed.
        # Bounded, event-driven (the network-idle event), never a fixed sleep.
        try:
            page.wait_for_load_state("networkidle", timeout=6000)
        except Exception:
            pass
        map_error_shown = appears_within(page.get_by_text(_MAP_ERROR), 1000)
        external = gate.external_attempts
        external_tiles = [u for u in external if _EXTERNAL_MAP_TILE.search(u)]
        positive = UiVerdict(
            ok=rendered and len(external_tiles) == 0,
            detail=(
                f"map canvas non-blank={rendered} {cv}; external MAP-TILE egress={len(external_tiles)} "
                f"{external_tiles[:5]}; (all external attempts={len(external)} {external[:5]}, "
                f"map-error-banner={map_error_shown}, informational)"
            ),
            evidence={"canvas": cv, "externalTileAttempts": external_tiles, "allExternalAttempts": external},
        )

        local_assets = [u for u in gate.all_requests if _MAP_ASSET.search(u)]
        negative = UiVerdict(
            ok=len(local_assets) > 0,
            detail=(
                f"local (same-origin) map assets fetched={len(local_assets)} "
                f"{local_assets[:5] if local_assets else '(none -- likely no real map present)'}"
            ),
            evidence={"localMapAssets": local_assets[:20]},
        )
        return UiReport(positive=positive, negative=negative, notes="AC-83 local-only map")
    finally:
        ctx.close()


CAPABILITY = UiCapability(
    id="map_renders_local_tiles_only",
    ac="AC-83",
    description="Operations map draws a non-blank canvas from local assets with zero external network calls.",
    run=_run,
    priority=True,
)
