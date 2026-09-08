<script setup lang="ts">
import { computed } from "vue";
import { displayId } from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import EmptyState from "./EmptyState.vue";
import PanelCard from "./PanelCard.vue";

const store = useWorkspaceStore();
const pending = computed(() =>
  store.operations.handoffs.filter(
    (handoff) => handoff.status === "pending",
  ),
);
</script>

<template>
  <PanelCard
    eyebrow="Live custody"
    title="Pending handoffs"
    description="A pending exchange never creates a second current holder."
  >
    <ul v-if="pending.length" class="handoff-list">
      <li
        v-for="(handoff, index) in pending"
        :key="String(handoff.handoffId ?? handoff.id ?? index)"
      >
        <div class="handoff-people">
          <div>
            <span>Current holder</span>
            <strong>{{ displayId(handoff.currentHolderId) }}</strong>
          </div>
          <span aria-hidden="true">→</span>
          <div>
            <span>Receiver</span>
            <strong>{{ displayId(handoff.receiverId) }}</strong>
          </div>
        </div>
        <div class="confirmation-progress">
          <span :class="{ confirmed: handoff.senderConfirmed }">
            Sender {{ handoff.senderConfirmed ? "confirmed" : "waiting" }}
          </span>
          <span :class="{ confirmed: handoff.receiverConfirmed }">
            Receiver {{ handoff.receiverConfirmed ? "confirmed" : "waiting" }}
          </span>
        </div>
      </li>
    </ul>
    <EmptyState
      v-else
      title="No pending handoffs"
      description="Every active sealed sample has one unambiguous holder."
    />
  </PanelCard>
</template>
