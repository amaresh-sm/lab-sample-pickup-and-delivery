<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { request, routes } from "../lib/api";
import { useAction } from "../lib/action";
import { displayId, formatTime } from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { ServiceabilitySnapshot } from "../types";
import EmptyState from "./EmptyState.vue";
import InlineResult from "./InlineResult.vue";
import PanelCard from "./PanelCard.vue";

const store = useWorkspaceStore();
const action = useAction();
const snapshot = ref<ServiceabilitySnapshot | null>(null);

const regions = computed(() => snapshot.value?.regions ?? []);

/**
 * Reads the published serviceability calculation. Only a complete version is
 * ever served, so a calculation still being computed can never appear here.
 */
async function load(): Promise<void> {
  const next = await action.run(() =>
    request<ServiceabilitySnapshot>(
      routes.readServiceabilitySnapshot,
      { method: "POST", body: JSON.stringify({}) },
      store.session,
    ),
  );
  if (!next) return;
  snapshot.value = next;
}

onMounted(() => {
  void load();
});
</script>

<template>
  <PanelCard
    eyebrow="Administrator"
    title="Serviceability calculation"
    description="The active collection points and coverage regions the current calculation uses. One complete version is published at a time; an in-progress calculation is never served."
  >
    <template #actions>
      <button
        id="reload-serviceability-snapshot"
        class="quiet-button"
        type="button"
        :disabled="action.busy.value"
        @click="load"
      >
        {{ action.busy.value ? "Loading…" : "Refresh" }}
      </button>
    </template>
    <InlineResult v-if="action.error.value" kind="error">
      {{ action.error.value }}
    </InlineResult>
    <div
      v-if="snapshot"
      class="tracking-summary"
      data-testid="serviceability-snapshot"
    >
      <div>
        <span>Published version</span>
        <strong data-testid="serviceability-snapshot-version">
          {{ snapshot.version ?? "—" }}
        </strong>
      </div>
      <div>
        <span>Status</span>
        <strong class="status-badge status-completed">
          {{ snapshot.status ?? "—" }}
        </strong>
      </div>
      <div>
        <span>Computed at</span>
        <strong>{{ formatTime(snapshot.computedAt) }}</strong>
      </div>
      <div>
        <span>Network revision</span>
        <strong>{{ snapshot.networkRevision ?? "—" }}</strong>
      </div>
      <div>
        <span>Active collection points</span>
        <strong>{{ snapshot.activeCollectionPointCount ?? regions.length }}</strong>
      </div>
    </div>
    <div v-if="regions.length" class="location-inventory">
      <table>
        <caption class="sr-only">
          Coverage regions used by the published serviceability calculation
        </caption>
        <thead>
          <tr>
            <th scope="col">Collection point</th>
            <th scope="col">Location ID</th>
            <th scope="col">Latitude</th>
            <th scope="col">Longitude</th>
            <th scope="col">Effective coverage</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="region in regions"
            :key="region.locationId ?? region.name"
            data-testid="serviceability-region"
          >
            <td>{{ region.name || "Unnamed" }}</td>
            <td>{{ displayId(region.locationId) }}</td>
            <td>{{ region.latitude ?? "—" }}</td>
            <td>{{ region.longitude ?? "—" }}</td>
            <td>{{ region.coverageKm ?? "—" }} km</td>
          </tr>
        </tbody>
      </table>
    </div>
    <EmptyState
      v-else-if="snapshot"
      title="No active collection points"
      description="Activate a location's collection-point function to add it to the serviceable region."
    />
  </PanelCard>
</template>
