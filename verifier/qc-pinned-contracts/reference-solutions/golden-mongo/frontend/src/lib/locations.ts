import type { LocationFunction, LocationRecord } from "../types";
import { LOCATION_FUNCTIONS, LOCATION_FUNCTION_LABELS } from "../types";

/** Renders the location's identifier from either canonical key. */
export function locationId(location: LocationRecord): string {
  return String(location.locationId ?? location.id ?? "");
}

/** Returns every function the location provides, whether enabled or not. */
export function providedFunctions(
  location: LocationRecord,
): LocationFunction[] {
  const functions = location.functions ?? {};
  return LOCATION_FUNCTIONS.filter((name) => name in functions);
}

/**
 * Returns the functions the location is serving right now: the location itself
 * must be active and the individual function must be enabled.
 */
export function servedFunctions(location: LocationRecord): LocationFunction[] {
  if (!location.active) return [];
  const functions = location.functions ?? {};
  return LOCATION_FUNCTIONS.filter((name) => functions[name] === true);
}

/** Reports whether the location provides the function at all. */
export function provides(
  location: LocationRecord,
  name: LocationFunction,
): boolean {
  return name in (location.functions ?? {});
}

/** Reports whether the location is active and the function is enabled. */
export function serves(
  location: LocationRecord,
  name: LocationFunction,
): boolean {
  return Boolean(location.active) && (location.functions ?? {})[name] === true;
}

// Laboratories are the most specific role a site can hold, so they win when a
// single marker has to stand for several functions.
const FUNCTION_PRIORITY: LocationFunction[] = [
  "laboratory",
  "hub",
  "transfer_point",
  "collection_point",
];

/**
 * Picks the function that represents the location on the map. Served functions
 * are preferred so an inactive function never overrides a working one.
 */
export function primaryFunction(
  location: LocationRecord,
): LocationFunction | "" {
  const served = servedFunctions(location);
  const provided = providedFunctions(location);
  return (
    FUNCTION_PRIORITY.find((name) => served.includes(name)) ??
    FUNCTION_PRIORITY.find((name) => provided.includes(name)) ??
    ""
  );
}

/** Formats a function list for a label or tooltip. */
export function functionLabels(names: LocationFunction[]): string {
  return names.map((name) => LOCATION_FUNCTION_LABELS[name]).join(", ");
}

/**
 * Describes each provided function and whether it is currently working, for
 * example "Hub, Laboratory (off)".
 */
export function functionSummary(location: LocationRecord): string {
  const provided = providedFunctions(location);
  if (provided.length === 0) return "No functions";
  return provided
    .map((name) => {
      const label = LOCATION_FUNCTION_LABELS[name];
      return serves(location, name) ? label : `${label} (off)`;
    })
    .join(", ");
}
