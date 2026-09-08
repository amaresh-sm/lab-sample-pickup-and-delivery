<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { idempotencyKey, request, routes } from "../lib/api";
import { useAction } from "../lib/action";
import { strings, titleCase } from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { MutationResult } from "../types";
import FieldGroup from "./FieldGroup.vue";
import InlineResult from "./InlineResult.vue";
import PanelCard from "./PanelCard.vue";

type PlanAction = "split" | "merge" | "cancel" | "reroute";

const store = useWorkspaceStore();
const action = useAction();
const result = ref<MutationResult | null>(null);
const model = reactive({
  action: "split" as PlanAction,
  journeyId: "",
  expectedPlanVersion: "1",
  identifiers: "",
  reason: "",
});

const labels: Record<PlanAction, string> = {
  split: "Split journey",
  merge: "Merge journeys",
  cancel: "Cancel journey",
  reroute: "Reroute journey",
};
const identifierLabel = computed(() =>
  model.action === "split"
    ? "Sample IDs to split"
    : "Source journey IDs",
);

async function submit(): Promise<void> {
  const base = {
    expectedPlanVersion: Number(model.expectedPlanVersion),
    idempotencyKey: idempotencyKey(`plan-${model.action}`),
  };
  let path: string;
  let payload: Record<string, unknown>;
  switch (model.action) {
    case "split":
      path = routes.splitJourney;
      payload = {
        ...base,
        journeyId: model.journeyId,
        sampleIds: strings(model.identifiers),
      };
      break;
    case "merge":
      path = routes.mergeJourneys;
      payload = {
        ...base,
        targetJourneyId: model.journeyId,
        sourceJourneyIds: strings(model.identifiers),
      };
      break;
    case "cancel":
      path = routes.cancelJourney;
      payload = {
        ...base,
        journeyId: model.journeyId,
        reason: model.reason || null,
      };
      break;
    case "reroute":
      path = routes.rerouteJourney;
      payload = {
        ...base,
        journeyId: model.journeyId,
        reason: model.reason || null,
      };
      break;
  }
  const next = await action.run(() =>
    request<MutationResult>(
      path,
      { method: "POST", body: JSON.stringify(payload) },
      store.session,
    ),
  );
  if (!next) return;
  result.value = next;
  store.showNotice(
    `${labels[model.action]} completed after atomic validation.`,
  );
  await store.refresh().catch(() => undefined);
}
</script>

<template>
  <PanelCard
    eyebrow="Dispatcher intervention"
    title="Split, merge, cancel or reroute"
    description="Only unfinished work changes; stale versions and invalid batches leave the active plan untouched."
  >
    <form class="disruption-form" @submit.prevent="submit">
      <FieldGroup label="Plan action" input-id="dispatcher-plan-action">
        <select id="dispatcher-plan-action" v-model="model.action">
          <option value="split">Split journey</option>
          <option value="merge">Merge journeys</option>
          <option value="cancel">Cancel journey</option>
          <option value="reroute">Reroute journey</option>
        </select>
      </FieldGroup>
      <FieldGroup
        :label="model.action === 'merge' ? 'Target journey ID' : 'Action journey ID'"
        input-id="dispatcher-action-journey"
      >
        <input
          id="dispatcher-action-journey"
          v-model="model.journeyId"
          required
        />
      </FieldGroup>
      <FieldGroup
        label="Action expected plan version"
        input-id="dispatcher-action-version"
      >
        <input
          id="dispatcher-action-version"
          v-model="model.expectedPlanVersion"
          type="number"
          min="1"
          required
        />
      </FieldGroup>
      <FieldGroup
        v-if="model.action === 'split' || model.action === 'merge'"
        :label="identifierLabel"
        input-id="dispatcher-action-identifiers"
        hint="Comma-separated identifiers."
      >
        <input
          id="dispatcher-action-identifiers"
          v-model="model.identifiers"
          required
        />
      </FieldGroup>
      <FieldGroup
        v-else
        label="Plan action reason"
        input-id="dispatcher-action-reason"
      >
        <input
          id="dispatcher-action-reason"
          v-model="model.reason"
          placeholder="Optional operational reason"
        />
      </FieldGroup>
      <button
        :class="model.action === 'cancel' ? 'danger-button' : 'primary-button'"
        :disabled="action.busy.value"
      >
        {{ labels[model.action] }}
      </button>
      <InlineResult
        v-if="action.error.value || result"
        :kind="action.error.value ? 'error' : 'success'"
        test-id="dispatcher-plan-result"
      >
        {{
          action.error.value ||
          `${titleCase(model.action)} result: ${JSON.stringify(result)}`
        }}
      </InlineResult>
    </form>
  </PanelCard>
</template>
