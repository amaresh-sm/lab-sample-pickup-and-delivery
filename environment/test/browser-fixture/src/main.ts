import maplibregl, { Map } from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import { Protocol } from "pmtiles";
import { createApp, h } from "vue";

declare global {
  interface Window {
    arenaMap: Map;
    arenaMapErrors: string[];
  }
}

const statusRoot = document.querySelector<HTMLElement>("#status-root");
if (!statusRoot) {
  throw new Error("missing Vue status root");
}
createApp({
  render: () => h("strong", { id: "render-status" }, "Local clinical sample map"),
}).mount(statusRoot);

const protocol = new Protocol();
maplibregl.addProtocol("pmtiles", protocol.tile);

const styleResponse = await fetch("/map-assets/style.json");
if (!styleResponse.ok) {
  throw new Error(`failed to load local style: ${styleResponse.status}`);
}
const style = await styleResponse.json();
const localArchiveUrl = "pmtiles:///map-assets/bangalore.pmtiles";
style.sources.basemap.url = localArchiveUrl;
style.sprite = `${window.location.origin}/map-assets/sprites/light`;
style.glyphs = `${window.location.origin}/map-assets/fonts/{fontstack}/{range}.pbf`;

const map = new maplibregl.Map({
  container: "map",
  style,
  center: [77.5946, 12.9716],
  zoom: 12,
  attributionControl: true,
});

window.arenaMap = map;
window.arenaMapErrors = [];

map.on("error", (event) => {
  const message = event.error instanceof Error ? event.error.message : String(event.error);
  window.arenaMapErrors.push(message);
  console.error(`map rendering error: ${message}`);
});

map.on("load", () => {
  map.addSource("sample-point", {
    type: "geojson",
    data: {
      type: "Feature",
      properties: { kind: "lab-sample-delivery" },
      geometry: {
        type: "Point",
        coordinates: [77.5946, 12.9716],
      },
    },
  });
  map.addLayer({
    id: "sample-point",
    type: "circle",
    source: "sample-point",
    paint: {
      "circle-color": "#d33636",
      "circle-radius": 8,
      "circle-stroke-color": "#ffffff",
      "circle-stroke-width": 3,
    },
  });

  const markerElement = document.createElement("div");
  markerElement.className = "sample-point-marker";
  markerElement.setAttribute("aria-label", "Clinical sample relay point");
  new maplibregl.Marker({ element: markerElement })
    .setLngLat([77.5946, 12.9716])
    .addTo(map);
});

map.on("idle", () => {
  if (map.getSource("sample-point") && map.getLayer("sample-point")) {
    document.documentElement.setAttribute("data-map-ready", "true");
  }
});
