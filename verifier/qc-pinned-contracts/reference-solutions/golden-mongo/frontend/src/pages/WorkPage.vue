<script setup lang="ts">
import AssignmentProgressPanel from "../components/AssignmentProgressPanel.vue";
import CollectionPanel from "../components/CollectionPanel.vue";
import HandlingLocationPanel from "../components/HandlingLocationPanel.vue";
import HandoffPanel from "../components/HandoffPanel.vue";
import LaboratoryPanel from "../components/LaboratoryPanel.vue";
import OfferPanel from "../components/OfferPanel.vue";
import RequestPanel from "../components/RequestPanel.vue";
import WorkQueue from "../components/WorkQueue.vue";
import { useWorkspaceStore } from "../stores/workspace";

const store = useWorkspaceStore();
const isAdmin = () => store.activeRole === "administrator";
</script>

<template>
  <div class="page-grid page-grid-2 align-start">
    <WorkQueue />
    <OfferPanel />
    <AssignmentProgressPanel />
    <RequestPanel
      v-if="isAdmin() || store.activeRole === 'node_handler'"
      presealed-intake
    />
    <CollectionPanel
      v-if="isAdmin() || store.activeRole === 'collection_professional'"
    />
    <HandlingLocationPanel
      v-if="
        isAdmin() ||
        ['collection_professional', 'delivery_partner', 'node_handler'].includes(
          store.activeRole ?? '',
        )
      "
    />
    <LaboratoryPanel
      v-if="isAdmin() || store.activeRole === 'laboratory_team'"
      class="grid-span-2"
    />
    <HandoffPanel
      v-if="
        isAdmin() ||
        ['collection_professional', 'delivery_partner', 'node_handler'].includes(
          store.activeRole ?? '',
        )
      "
      class="grid-span-2"
    />
  </div>
</template>
