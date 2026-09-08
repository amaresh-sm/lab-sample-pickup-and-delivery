<script setup lang="ts">
import { computed } from "vue";
import { displayId, formatTime, titleCase } from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { EntityRecord } from "../types";
import EmptyState from "./EmptyState.vue";
import PanelCard from "./PanelCard.vue";
import StatusBadge from "./StatusBadge.vue";

defineProps<{ kind: "alerts" | "routes" }>();
const store = useWorkspaceStore();
const alerts = computed<EntityRecord[]>(() => [
  ...store.operations.exceptions.map((entry) => ({
    ...entry,
    alertKind: "Exception",
  })),
  ...store.operations.delays.map((entry) => ({
    ...entry,
    alertKind: "Delay",
  })),
]);
</script>

<template>
  <PanelCard
    v-if="kind === 'routes'"
    eyebrow="Journey execution"
    title="Active routes"
    description="Current direct, batched, and multi-stop movement plans."
  >
    <ul v-if="store.operations.routes.length" class="record-list route-list">
      <li
        v-for="(route, index) in store.operations.routes.slice(0, 12)"
        :key="String(route.journeyId ?? route.id ?? index)"
      >
        <div>
          <strong>Journey {{ displayId(route.journeyId ?? route.id) }}</strong>
          <small>
            {{ titleCase(route.routeMode ?? "route") }} ·
            {{ (route.orderedStops ?? []).length }} stops · v{{ route.planVersion ?? "—" }}
          </small>
        </div>
        <StatusBadge :value="route.status ?? 'active'" />
      </li>
    </ul>
    <EmptyState
      v-else
      title="No active routes"
      description="Planned journeys appear when serviceable requests are created."
    />
  </PanelCard>

  <PanelCard
    v-else
    eyebrow="Attention required"
    title="Alerts & exceptions"
    description="Deadline, handling, route, and receipt issues requiring intervention."
  >
    <ul v-if="alerts.length" class="alert-list">
      <li
        v-for="(alert, index) in alerts.slice(0, 12)"
        :key="String(alert.alertId ?? alert.id ?? index)"
      >
        <span aria-hidden="true">!</span>
        <div>
          <strong>
            {{
              String(
                alert.reason ??
                alert.message ??
                alert.alertKind ??
                "Operational alert",
              )
            }}
          </strong>
          <small>
            {{ displayId(alert.sampleId ?? alert.journeyId ?? alert.resourceId) }}
            · {{ formatTime(alert.occurredAt ?? alert.createdAt) }}
          </small>
        </div>
        <StatusBadge :value="alert.status ?? alert.alertKind ?? 'open'" />
      </li>
    </ul>
    <EmptyState
      v-else
      title="Operation is within plan"
      description="No active delays or sample exceptions need attention."
    />
  </PanelCard>
</template>
