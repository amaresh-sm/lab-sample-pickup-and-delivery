<script setup lang="ts">
import { computed, ref } from "vue";
import { displayId, formatTime } from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { EntityRecord, RoleWork } from "../types";
import EmptyState from "./EmptyState.vue";
import PanelCard from "./PanelCard.vue";
import StatusBadge from "./StatusBadge.vue";

const store = useWorkspaceStore();
const tab = ref<keyof RoleWork>("pending");
const tabs: Array<{ key: keyof RoleWork; label: string }> = [
  { key: "pending", label: "Pending" },
  { key: "inProgress", label: "In progress" },
  { key: "completed", label: "Completed" },
];
const items = computed(() => store.roleWork[tab.value] ?? []);

function itemId(item: EntityRecord, index: number): unknown {
  return item.assignmentId ?? item.sampleId ?? item.id ?? index;
}

function latestHistory(item: EntityRecord): EntityRecord | null {
  if (!Array.isArray(item.history) || item.history.length === 0) return null;
  return item.history[item.history.length - 1] as EntityRecord;
}

function capacityText(item: EntityRecord): string {
  const value = item.destinationCapacity;
  if (!value || typeof value !== "object") return "";
  const capacity = value as EntityRecord;
  return `Capacity ${String(capacity.availableUnits ?? 0)} regular · ${String(
    capacity.cooledAvailableUnits ?? 0,
  )} cooled`;
}
</script>

<template>
  <PanelCard
    eyebrow="Role workspace"
    title="My work"
    description="Only assignments authorized for the selected active role are shown."
  >
    <div
      class="segmented"
      role="tablist"
      aria-label="Work status"
      data-testid="work-history"
    >
      <button
        v-for="item in tabs"
        :key="item.key"
        type="button"
        role="tab"
        :aria-selected="tab === item.key"
        @click="tab = item.key"
      >
        {{ item.label }} <span>{{ store.roleWork[item.key].length }}</span>
      </button>
    </div>
    <ul v-if="items.length" class="record-list">
      <li
        v-for="(item, index) in items.slice(0, 20)"
        :key="String(itemId(item, index))"
      >
        <div>
          <strong>
            {{ String(item.title ?? item.kind ?? `Work item ${index + 1}`) }}
          </strong>
          <small>
            {{ displayId(itemId(item, index)) }} · Sample
            {{ displayId(item.sampleId) }} ·
            {{ formatTime(item.deliveryDeadline ?? item.updatedAt) }}
          </small>
          <small v-if="item.fromLocationId || item.toLocationId">
            {{ displayId(item.fromLocationId) }} →
            {{ displayId(item.toLocationId) }}
          </small>
          <small v-if="item.incomingFromLocationId || item.outgoingToLocationId">
            Incoming {{ displayId(item.incomingFromLocationId) }} · Outgoing
            {{ displayId(item.outgoingToLocationId) }}
          </small>
          <small v-if="capacityText(item)">
            {{ capacityText(item) }}
          </small>
          <small v-if="latestHistory(item)" class="history-line">
            Last change:
            {{
              String(
                latestHistory(item)?.action ??
                latestHistory(item)?.status ??
                "updated",
              ).replaceAll("_", " ")
            }}
            · {{ formatTime(latestHistory(item)?.occurredAt) }}
          </small>
        </div>
        <StatusBadge :value="item.status ?? 'pending'" />
      </li>
    </ul>
    <EmptyState
      v-else
      :title="`No ${tabs.find((item) => item.key === tab)?.label.toLowerCase()} work`"
      description="New offers and saved status changes appear here automatically."
    />
  </PanelCard>
</template>
