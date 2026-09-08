<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import maplibregl, {
  type GeoJSONSource,
  type Map as MapLibreMap,
  type StyleSpecification,
} from "maplibre-gl";
import { Protocol } from "pmtiles";
import { useWorkspaceStore } from "../stores/workspace";
import {
  functionSummary,
  locationId,
  primaryFunction,
  providedFunctions,
  serves,
} from "../lib/locations";
import type {
  Coordinate,
  OperationsState,
  RouteRecord,
} from "../types";

withDefaults(defineProps<{ compact?: boolean }>(), { compact: false });

const store = useWorkspaceStore();
const container = ref<HTMLDivElement | null>(null);
const ready = ref(false);
const mapError = ref("");
let map: MapLibreMap | null = null;
let protocolRegistered = false;
const protocol = new Protocol();

function registerProtocol(): void {
  if (protocolRegistered) return;
  maplibregl.addProtocol("pmtiles", protocol.tile);
  protocolRegistered = true;
}

function absoluteTemplateUrl(value: string, origin: string): string {
  return new URL(value, origin).href
    .replace(/%7B/gi, "{")
    .replace(/%7D/gi, "}");
}

async function localMapStyle(): Promise<StyleSpecification> {
  const response = await fetch("/map-assets/style.json", {
    credentials: "same-origin",
  });
  if (!response.ok) {
    throw new Error(`Local map style returned HTTP ${response.status}`);
  }
  const style = await response.json() as StyleSpecification;
  const origin = window.location.origin;
  if (style.sprite && typeof style.sprite === "string") {
    style.sprite = absoluteTemplateUrl(style.sprite, origin);
  }
  if (style.glyphs) {
    style.glyphs = absoluteTemplateUrl(style.glyphs, origin);
  }
  for (const source of Object.values(style.sources ?? {})) {
    if (
      "url" in source &&
      typeof source.url === "string" &&
      source.url.startsWith("pmtiles:///")
    ) {
      source.url = `pmtiles://${origin}/${source.url.slice("pmtiles:///".length)}`;
    }
  }
  return style;
}

function validCoordinate(value: unknown): Coordinate | null {
  if (!value || typeof value !== "object") return null;
  const record = value as Record<string, unknown>;
  const latitude = Number(record.latitude ?? record.lat);
  const longitude = Number(
    record.longitude ?? record.lng ?? record.lon,
  );
  if (
    !Number.isFinite(latitude) ||
    !Number.isFinite(longitude) ||
    Math.abs(latitude) > 90 ||
    Math.abs(longitude) > 180
  ) {
    return null;
  }
  return { latitude, longitude };
}

function routeCoordinates(route: RouteRecord): Coordinate[] {
  if (Array.isArray(route.coordinates)) {
    return route.coordinates
      .map(validCoordinate)
      .filter((point): point is Coordinate => point !== null);
  }
  return (route.orderedStops ?? [])
    .map((stop) => validCoordinate(stop.coordinate ?? stop))
    .filter((point): point is Coordinate => point !== null);
}

function coverageRing(center: Coordinate, radiusKm: number): number[][] {
  const earthRadiusKm = 6371.0088;
  const angularDistance = radiusKm / earthRadiusKm;
  const latitude = center.latitude * Math.PI / 180;
  const longitude = center.longitude * Math.PI / 180;
  const points: number[][] = [];
  for (let index = 0; index <= 64; index += 1) {
    const bearing = 2 * Math.PI * index / 64;
    const pointLatitude = Math.asin(
      Math.sin(latitude) * Math.cos(angularDistance) +
      Math.cos(latitude) * Math.sin(angularDistance) * Math.cos(bearing),
    );
    const pointLongitude = longitude + Math.atan2(
      Math.sin(bearing) * Math.sin(angularDistance) * Math.cos(latitude),
      Math.cos(angularDistance) - Math.sin(latitude) * Math.sin(pointLatitude),
    );
    points.push([
      pointLongitude * 180 / Math.PI,
      pointLatitude * 180 / Math.PI,
    ]);
  }
  return points;
}

function syncMapData(target: MapLibreMap, state: OperationsState): void {
  // Only a location that is actually serving as a collection point contributes
  // to the serviceable region, so disabling that one function removes its ring
  // while its other functions keep working.
  const serviceAreas = state.locations.flatMap((location) => {
    const coordinate = validCoordinate(location.coordinate);
    const coverageKm = Number(location.coverageKm);
    if (
      !serves(location, "collection_point") ||
      !coordinate ||
      !Number.isFinite(coverageKm) ||
      coverageKm <= 0
    ) {
      return [];
    }
    return [{
      type: "Feature" as const,
      geometry: {
        type: "Polygon" as const,
        coordinates: [coverageRing(coordinate, Math.min(5, coverageKm))],
      },
      properties: {
        locationId: locationId(location),
        name: String(location.name ?? "Collection point"),
        coverageKm,
      },
    }];
  });
  const pointFeatures = [
    ...state.samples.flatMap((sample) => {
      const coordinate = validCoordinate(
        sample.latestLocation ?? sample.coordinate ?? sample.origin,
      );
      return coordinate
        ? [{
            type: "Feature" as const,
            geometry: {
              type: "Point" as const,
              coordinates: [coordinate.longitude, coordinate.latitude],
            },
            properties: {
              kind: "sample",
              status: String(sample.status ?? "active"),
              stale: Boolean(sample.locationStale ?? false),
              label: String(sample.sampleId ?? sample.id ?? "Sample"),
              currentHolderId: String(sample.currentHolderId ?? ""),
            },
          }]
        : [];
    }),
    ...state.workers.flatMap((worker) => {
      const coordinate = validCoordinate(
        worker.latestLocation ?? worker.coordinate,
      );
      return coordinate
        ? [{
            type: "Feature" as const,
            geometry: {
              type: "Point" as const,
              coordinates: [coordinate.longitude, coordinate.latitude],
            },
            properties: {
              kind: "worker",
              status: String(worker.status ?? "available"),
              stale: Boolean(worker.locationStale ?? false),
              label: String(
                worker.fullName ??
                worker.workerId ??
                worker.id ??
                "Worker",
              ),
            },
          }]
        : [];
    }),
    // One marker per location. `role` colours the marker by the most specific
    // function it serves and `multiFunction` rings sites that combine several
    // functions, so the map shows what each location actually does.
    ...state.locations.flatMap((location) => {
      const coordinate = validCoordinate(location.coordinate);
      return coordinate
        ? [{
            type: "Feature" as const,
            geometry: {
              type: "Point" as const,
              coordinates: [coordinate.longitude, coordinate.latitude],
            },
            properties: {
              kind: "location",
              role: primaryFunction(location),
              multiFunction: providedFunctions(location).length > 1,
              status: location.active ? "active" : "inactive",
              stale: false,
              label: String(
                location.name ?? locationId(location) ?? "Location",
              ),
              functions: functionSummary(location),
            },
          }]
        : [];
    }),
  ];
  const routeFeatures = state.routes.flatMap((route) => {
    const coordinates = routeCoordinates(route);
    return coordinates.length >= 2
      ? [{
          type: "Feature" as const,
          geometry: {
            type: "LineString" as const,
            coordinates: coordinates.map((point) => [
              point.longitude,
              point.latitude,
            ]),
          },
          properties: {
            status: String(route.status ?? "active"),
            journeyId: String(route.journeyId ?? route.id ?? ""),
          },
        }]
      : [];
  });
  (target.getSource("service-areas") as GeoJSONSource | undefined)
    ?.setData({ type: "FeatureCollection", features: serviceAreas });
  (target.getSource("operations-points") as GeoJSONSource | undefined)
    ?.setData({ type: "FeatureCollection", features: pointFeatures });
  (target.getSource("operations-routes") as GeoJSONSource | undefined)
    ?.setData({ type: "FeatureCollection", features: routeFeatures });
}

function addOperationsLayers(target: MapLibreMap): void {
  if (!target.getSource("service-areas")) {
    target.addSource("service-areas", {
      type: "geojson",
      data: { type: "FeatureCollection", features: [] },
    });
    target.addLayer({
      id: "service-areas-fill",
      type: "fill",
      source: "service-areas",
      paint: {
        "fill-color": "#147d70",
        "fill-opacity": 0.1,
      },
    });
    target.addLayer({
      id: "service-areas-line",
      type: "line",
      source: "service-areas",
      paint: {
        "line-color": "#147d70",
        "line-width": 1.5,
        "line-dasharray": [3, 2],
      },
    });
  }
  if (!target.getSource("operations-routes")) {
    target.addSource("operations-routes", {
      type: "geojson",
      data: { type: "FeatureCollection", features: [] },
    });
    target.addLayer({
      id: "operations-routes-line",
      type: "line",
      source: "operations-routes",
      paint: {
        "line-color": "#df714d",
        "line-width": 4,
        "line-opacity": 0.88,
      },
    });
  }
  if (!target.getSource("operations-points")) {
    target.addSource("operations-points", {
      type: "geojson",
      data: { type: "FeatureCollection", features: [] },
    });
    target.addLayer({
      id: "operations-points-halo",
      type: "circle",
      source: "operations-points",
      paint: {
        "circle-radius": 10,
        "circle-color": "#ffffff",
        "circle-opacity": 0.9,
      },
    });
    target.addLayer({
      id: "operations-points",
      type: "circle",
      source: "operations-points",
      paint: {
        "circle-radius": [
          "case",
          ["==", ["get", "kind"], "worker"],
          6,
          ["==", ["get", "role"], "laboratory"],
          9,
          ["==", ["get", "kind"], "location"],
          8,
          7,
        ],
        "circle-color": [
          "case",
          ["==", ["get", "stale"], true],
          "#73837f",
          ["==", ["get", "status"], "inactive"],
          "#a4b2ae",
          ["==", ["get", "kind"], "worker"],
          "#147d70",
          ["==", ["get", "role"], "laboratory"],
          "#635aa8",
          ["==", ["get", "role"], "hub"],
          "#b5642d",
          ["==", ["get", "role"], "transfer_point"],
          "#d69b2d",
          ["==", ["get", "role"], "collection_point"],
          "#e0b64a",
          "#df714d",
        ],
        "circle-stroke-color": [
          "case",
          ["==", ["get", "multiFunction"], true],
          "#0f2b27",
          "#173a35",
        ],
        "circle-stroke-width": [
          "case",
          ["==", ["get", "multiFunction"], true],
          3,
          1,
        ],
      },
    });
    target.on("click", "operations-points", (event) => {
      const feature = event.features?.[0];
      const coordinates = feature?.geometry.type === "Point"
        ? feature.geometry.coordinates.slice() as [number, number]
        : null;
      if (!coordinates) return;
      new maplibregl.Popup({ closeButton: false, offset: 10 })
        .setLngLat(coordinates)
        .setText(
          [
            String(feature.properties?.label ?? "Operation"),
            feature.properties?.functions
              ? String(feature.properties.functions)
              : "",
            feature.properties?.stale ? "location stale" : "location current",
            feature.properties?.currentHolderId
              ? `holder ${String(feature.properties.currentHolderId)}`
              : "",
          ].filter(Boolean).join(" · "),
        )
        .addTo(target);
    });
  }
}

const latestCoordinate = computed(() => {
  for (let index = store.operations.workers.length - 1; index >= 0; index -= 1) {
    const worker = store.operations.workers[index];
    const point = validCoordinate(
      worker.latestLocation ?? worker.coordinate,
    );
    if (point) return point;
  }
  for (let index = store.operations.samples.length - 1; index >= 0; index -= 1) {
    const sample = store.operations.samples[index];
    const point = validCoordinate(
      sample.latestLocation ?? sample.coordinate ?? sample.origin,
    );
    if (point) return point;
  }
  return null;
});

onMounted(async () => {
  if (!container.value) return;
  registerProtocol();
  let style: StyleSpecification;
  try {
    style = await localMapStyle();
  } catch (error) {
    mapError.value = error instanceof Error
      ? error.message
      : "Local map asset unavailable";
    return;
  }
  if (!container.value) return;
  map = new maplibregl.Map({
    container: container.value,
    style,
    center: [77.5946, 12.9716],
    zoom: 10.4,
    attributionControl: false,
    cooperativeGestures: true,
  });
  map.addControl(
    new maplibregl.NavigationControl({
      visualizePitch: false,
      showCompass: false,
    }),
    "bottom-right",
  );
  map.addControl(
    new maplibregl.AttributionControl({ compact: true }),
    "bottom-left",
  );
  map.on("load", () => {
    if (!map) return;
    addOperationsLayers(map);
    syncMapData(map, store.operations);
    ready.value = true;
  });
  map.on("error", (event) => {
    mapError.value = event.error?.message ?? "Local map asset unavailable";
  });
});

watch(
  () => store.operations,
  (operations) => {
    if (map && ready.value) syncMapData(map, operations);
  },
  { deep: true },
);

onBeforeUnmount(() => {
  map?.remove();
  map = null;
});
</script>

<template>
  <div :class="['map-frame', { 'map-compact': compact }]">
    <div
      ref="container"
      class="live-map"
      data-testid="live-map"
      aria-label="Live sample and worker map"
    />
    <div class="map-legend" aria-hidden="true">
      <span><i class="sample-dot" /> Samples</span>
      <span><i class="worker-dot" /> Workers</span>
      <span>● Collection points</span>
      <span><i class="coverage-area" /> Service coverage</span>
      <span>● Transfer points</span>
      <span>● Hubs</span>
      <span>● Laboratories</span>
      <span>◎ Multi-function locations</span>
      <span><i class="route-line" /> Active routes</span>
      <span><i class="stale-dot" /> Stale location</span>
    </div>
    <span
      class="coordinate-readout"
      data-testid="visible-coordinate"
      aria-live="polite"
    >
      {{ JSON.stringify(latestCoordinate ?? {}) }}
    </span>
    <span v-if="mapError" class="map-error" role="status">
      Basemap warning: {{ mapError }}
    </span>
  </div>
</template>
