<script setup lang="ts">
import { reactive, ref } from "vue";
import {
  idempotencyKey,
  request,
  routes,
  toIso,
} from "../lib/api";
import { useAction } from "../lib/action";
import { localDateTime, titleCase } from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { MutationResult } from "../types";
import FieldGroup from "./FieldGroup.vue";
import InlineResult from "./InlineResult.vue";
import PanelCard from "./PanelCard.vue";

const store = useWorkspaceStore();
const action = useAction();
const result = ref<MutationResult | null>(null);
const model = reactive({
  sampleId: "",
  assignmentId: "",
  containerType: "refrigerated_vial",
  observedTemperatureC: "4",
  collectedAt: localDateTime(),
});

async function submit(): Promise<void> {
  const next = await action.run(() =>
    request<MutationResult>(
      routes.collectAndSealSample,
      {
        method: "POST",
        body: JSON.stringify({
          sampleId: model.sampleId,
          assignmentId: model.assignmentId,
          containerType: model.containerType,
          observedTemperatureC: Number(model.observedTemperatureC),
          collectedAt: toIso(model.collectedAt),
          idempotencyKey: idempotencyKey("collect-seal"),
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  result.value = next;
  store.showNotice("Sample collected, sealed, and placed in explicit custody.");
  await store.refresh().catch(() => undefined);
}
</script>

<template>
  <PanelCard
    eyebrow="Certified collection"
    title="Collect and seal"
    description="Home samples enter transport only after qualification, container, and temperature checks."
  >
    <form class="stack-form" @submit.prevent="submit">
      <FieldGroup label="Collection sample ID" input-id="collection-sample">
        <input id="collection-sample" v-model="model.sampleId" required />
      </FieldGroup>
      <FieldGroup
        label="Collection assignment ID"
        input-id="collection-assignment"
      >
        <input
          id="collection-assignment"
          v-model="model.assignmentId"
          required
        />
      </FieldGroup>
      <FieldGroup label="Sealed container" input-id="sealed-container">
        <input
          id="sealed-container"
          v-model="model.containerType"
          required
        />
      </FieldGroup>
      <FieldGroup
        label="Observed temperature °C"
        input-id="collection-temperature"
      >
        <input
          id="collection-temperature"
          v-model="model.observedTemperatureC"
          type="number"
          step="0.1"
          required
        />
      </FieldGroup>
      <FieldGroup label="Collected at" input-id="collected-at">
        <input
          id="collected-at"
          v-model="model.collectedAt"
          type="datetime-local"
          required
        />
      </FieldGroup>
      <button class="primary-button" :disabled="action.busy.value">
        Record collection and seal
      </button>
      <InlineResult
        v-if="action.error.value || result"
        :kind="action.error.value ? 'error' : 'success'"
        test-id="collection-result"
      >
        {{ action.error.value || titleCase(result?.status) }}
      </InlineResult>
    </form>
  </PanelCard>
</template>
