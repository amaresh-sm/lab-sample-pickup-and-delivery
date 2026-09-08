<script setup lang="ts">
import AdminConfiguration from "../components/AdminConfiguration.vue";
import LocationStatusPanel from "../components/LocationStatusPanel.vue";
import PanelCard from "../components/PanelCard.vue";
import ServiceabilitySnapshotPanel from "../components/ServiceabilitySnapshotPanel.vue";
</script>

<template>
  <div class="page-stack">
    <div class="page-grid page-grid-2 align-start">
      <LocationStatusPanel />
      <PanelCard
        eyebrow="Configuration integrity"
        title="Safe administration"
        description="Every configuration write is audited and active work is revalidated."
      >
        <dl class="principle-list">
          <div>
            <dt>Service boundary</dt>
            <dd>≤ 5.000 km from an active collection point</dd>
          </div>
          <div>
            <dt>Capacity</dt>
            <dd>Regular/cooled units reserved before planning completes</dd>
          </div>
          <div>
            <dt>Worker access</dt>
            <dd>Roles and active certifications are enforced server-side</dd>
          </div>
          <div>
            <dt>Network changes</dt>
            <dd>Only unfinished work may be replanned</dd>
          </div>
        </dl>
      </PanelCard>
    </div>
    <ServiceabilitySnapshotPanel />
    <AdminConfiguration />
  </div>
</template>
