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
  resourceType: "worker",
  resourceId: "",
  reason: "",
  occurredAt: localDateTime(),
});

async function submit(): Promise<void> {
  const next = await action.run(() =>
    request<MutationResult>(
      routes.reportDisruption,
      {
        method: "POST",
        body: JSON.stringify({
          resourceType: model.resourceType,
          resourceId: model.resourceId,
          reason: model.reason,
          occurredAt: toIso(model.occurredAt),
          idempotencyKey: idempotencyKey("disruption"),
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  result.value = next;
  store.showNotice(
    "Disruption saved. Completed work and current custody were preserved.",
  );
  await store.refresh().catch(() => undefined);
}
</script>

<template>
  <PanelCard
    eyebrow="Recovery"
    title="Report disruption"
    description="Completed custody is preserved while at most one replacement plan is evaluated."
  >
    <form class="stack-form disruption-form" @submit.prevent="submit">
      <FieldGroup
        label="Unavailable resource type"
        input-id="resource-type"
      >
        <select id="resource-type" v-model="model.resourceType">
          <option value="worker">Worker</option>
          <option value="route">Route/shuttle</option>
          <option value="connection">Connection</option>
          <option value="location">Location</option>
          <option value="capacity">Capacity</option>
        </select>
      </FieldGroup>
      <FieldGroup label="Resource ID" input-id="resource-id">
        <input id="resource-id" v-model="model.resourceId" required />
      </FieldGroup>
      <FieldGroup label="Reason" input-id="disruption-reason">
        <textarea
          id="disruption-reason"
          v-model="model.reason"
          rows="3"
          required
        />
      </FieldGroup>
      <FieldGroup label="Occurred at" input-id="disruption-time">
        <input
          id="disruption-time"
          v-model="model.occurredAt"
          type="datetime-local"
          required
        />
      </FieldGroup>
      <button class="danger-button" :disabled="action.busy.value">
        Report and replan
      </button>
      <InlineResult
        v-if="action.error.value || result"
        :kind="action.error.value ? 'error' : 'success'"
        test-id="disruption-result"
      >
        {{
          action.error.value ||
          `Outcome: ${titleCase(result?.replanStatus ?? result?.status ?? result?.outcome ?? "replanned")}`
        }}
      </InlineResult>
    </form>
  </PanelCard>
</template>
