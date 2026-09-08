<script setup lang="ts">
import RequestPanel from "../components/RequestPanel.vue";
import ServiceabilityCheckPanel from "../components/ServiceabilityCheckPanel.vue";
import TrackingPanel from "../components/TrackingPanel.vue";
</script>

<template>
  <div class="page-stack">
    <ServiceabilityCheckPanel />
    <div class="page-grid page-grid-2 align-start">
      <RequestPanel />
      <TrackingPanel />
    </div>
  </div>
</template>
