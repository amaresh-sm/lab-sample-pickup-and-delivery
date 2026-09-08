<script setup lang="ts">
import { computed } from "vue";
import AssignmentProgressPanel from "../components/AssignmentProgressPanel.vue";
import AuditPanel from "../components/AuditPanel.vue";
import HandoffPanel from "../components/HandoffPanel.vue";
import LiveMap from "../components/LiveMap.vue";
import MetricCard from "../components/MetricCard.vue";
import LocationStatusPanel from "../components/LocationStatusPanel.vue";
import OperationsSummary from "../components/OperationsSummary.vue";
import PanelCard from "../components/PanelCard.vue";
import RequestPanel from "../components/RequestPanel.vue";
import WorkQueue from "../components/WorkQueue.vue";
import { useWorkspaceStore } from "../stores/workspace";
import type { Role } from "../types";

const store = useWorkspaceStore();
const activeSamples = computed(() =>
  store.operations.samples.filter(
    (sample) =>
      !["received", "expired", "cancelled"].includes(sample.status ?? ""),
  ).length,
);
function isAny(...roles: Role[]): boolean {
  return (
    store.activeRole === "administrator" ||
    Boolean(store.activeRole && roles.includes(store.activeRole))
  );
}
</script>

<template>
  <div class="page-stack">
    <section class="welcome-band">
      <div>
        <span class="eyebrow">Today’s network</span>
        <h2>Good operations start with clear responsibility.</h2>
        <p>
          Live updates are ordered, custody is explicit, and every critical
          action is safe to retry.
        </p>
      </div>
      <div class="network-date">
        <span>
          {{ new Intl.DateTimeFormat("en-IN", { weekday: "long" }).format(new Date()) }}
        </span>
        <strong>
          {{
            new Intl.DateTimeFormat("en-IN", {
              day: "2-digit",
              month: "short",
            }).format(new Date())
          }}
        </strong>
      </div>
    </section>

    <div class="metrics">
      <MetricCard
        label="Active samples"
        :value="activeSamples"
        :detail="`${store.operations.samples.length} visible to this role`"
      />
      <MetricCard
        label="Work in progress"
        :value="store.roleWork.inProgress.length"
        :detail="`${store.roleWork.pending.length} waiting`"
        tone="blue"
      />
      <MetricCard
        label="Pending handoffs"
        :value="store.operations.handoffs.filter((item) => item.status === 'pending').length"
        detail="Two-sided confirmation"
        tone="amber"
      />
      <MetricCard
        label="Open alerts"
        :value="store.operations.delays.length + store.operations.exceptions.length"
        detail="Delays and exceptions"
        tone="coral"
      />
    </div>

    <div v-if="isAny('dispatcher')" class="dashboard-map-grid">
      <PanelCard
        eyebrow="Realtime operations"
        title="Live map"
        description="Samples, workers, and routes use the supplied local PMTiles basemap."
        class-name="map-panel"
      >
        <LiveMap compact />
      </PanelCard>
      <OperationsSummary kind="alerts" />
    </div>

    <RequestPanel v-if="isAny('requester', 'dispatcher')" />

    <div
      v-if="isAny('collection_professional', 'delivery_partner', 'node_handler', 'laboratory_team')"
      class="page-grid page-grid-2 align-start"
    >
      <WorkQueue />
      <AssignmentProgressPanel />
    </div>

    <HandoffPanel
      v-if="isAny('collection_professional', 'delivery_partner', 'node_handler')"
    />

    <LocationStatusPanel v-if="isAny()" />
    <AuditPanel v-if="isAny('dispatcher')" />
  </div>
</template>
