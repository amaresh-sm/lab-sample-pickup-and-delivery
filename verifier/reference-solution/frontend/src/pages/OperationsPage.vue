<script setup lang="ts">
import DisruptionPanel from "../components/DisruptionPanel.vue";
import LiveMap from "../components/LiveMap.vue";
import MetricCard from "../components/MetricCard.vue";
import OperationsSummary from "../components/OperationsSummary.vue";
import PanelCard from "../components/PanelCard.vue";
import { computed } from "vue";
import { provides, serves } from "../lib/locations";
import { useWorkspaceStore } from "../stores/workspace";

const store = useWorkspaceStore();

const activeCollectionPoints = computed(
  () =>
    store.operations.locations.filter((location) =>
      serves(location, "collection_point"),
    ).length,
);
const activeLaboratories = computed(
  () =>
    store.operations.locations.filter((location) =>
      serves(location, "laboratory"),
    ).length,
);
const providedLaboratories = computed(
  () =>
    store.operations.locations.filter((location) =>
      provides(location, "laboratory"),
    ).length,
);
</script>

<template>
  <div class="page-stack">
    <div class="metrics">
      <MetricCard
        label="Samples"
        :value="store.operations.samples.length"
        detail="Role-visible journeys"
      />
      <MetricCard
        label="Workers"
        :value="store.operations.workers.length"
        detail="On the live board"
        tone="blue"
      />
      <MetricCard
        label="Collection points"
        :value="activeCollectionPoints"
        :detail="`${store.operations.locations.length} locations configured`"
        tone="amber"
      />
      <MetricCard
        label="Laboratories"
        :value="activeLaboratories"
        :detail="`${providedLaboratories} provide laboratory work`"
        tone="blue"
      />
      <MetricCard
        label="Fairness spread"
        :value="store.operations.assignmentCountSpread"
        detail="Assignment count difference"
        tone="amber"
      />
      <MetricCard
        label="Ineligible assignments"
        :value="store.operations.ineligibleAssignmentCount"
        detail="Should remain zero"
        tone="coral"
      />
    </div>
    <PanelCard
      eyebrow="Dispatcher view"
      title="Network map"
      description="Realtime locations, holder state, stale markers, and route status update without refresh."
      class-name="map-panel"
    >
      <LiveMap />
    </PanelCard>
    <div class="page-grid page-grid-2 align-start">
      <OperationsSummary kind="alerts" />
      <OperationsSummary kind="routes" />
    </div>
    <DisruptionPanel />
  </div>
</template>
