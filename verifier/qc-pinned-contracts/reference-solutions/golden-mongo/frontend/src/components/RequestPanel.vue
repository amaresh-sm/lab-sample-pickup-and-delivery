<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import {
  idempotencyKey,
  request,
  routes,
  toIso,
} from "../lib/api";
import { useAction } from "../lib/action";
import {
  displayId,
  localDateTime,
  strings,
  titleCase,
} from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { MutationResult } from "../types";
import FieldGroup from "./FieldGroup.vue";
import InlineResult from "./InlineResult.vue";
import PanelCard from "./PanelCard.vue";

const props = withDefaults(defineProps<{ presealedIntake?: boolean }>(), {
  presealedIntake: false,
});
const store = useWorkspaceStore();
const action = useAction();
const result = ref<MutationResult | null>(null);
const model = reactive({
  requesterId: store.session?.userId ?? "",
  originType: props.presealedIntake ? "node" : "home",
  latitude: "12.9716",
  longitude: "77.5946",
  requestedAt: localDateTime(),
  // The collection window is optional. Left blank, it defaults server-side to
  // [requestedAt, deliveryDeadline].
  earliestCollectionAt: "",
  latestCollectionAt: "",
  deliveryDeadline: localDateTime(4),
  sampleTypeId: "",
  containerType: "refrigerated_vial",
  temperatureMinC: "2",
  temperatureMaxC: "8",
  incompatibleSampleTypes: "",
  alreadySealed: props.presealedIntake,
});

const sampleId = computed(() =>
  String(result.value?.sampleId ?? result.value?.sample_id ?? ""),
);
const journeyId = computed(() =>
  String(result.value?.journeyId ?? result.value?.journey_id ?? ""),
);

async function submit(): Promise<void> {
  const usePresealedIntake =
    props.presealedIntake ||
    (model.originType !== "home" && model.alreadySealed);
  const payload: Record<string, unknown> = {
    requesterId: model.requesterId,
    originType: model.originType,
    origin: {
      latitude: Number(model.latitude),
      longitude: Number(model.longitude),
    },
    requestedAt: toIso(model.requestedAt),
    deliveryDeadline: toIso(model.deliveryDeadline),
    sampleTypeId: model.sampleTypeId,
    containerType: model.containerType,
    temperatureMinC: Number(model.temperatureMinC),
    temperatureMaxC: Number(model.temperatureMaxC),
    incompatibleSampleTypes: strings(model.incompatibleSampleTypes),
    idempotencyKey: idempotencyKey("request"),
  };
  if (model.earliestCollectionAt) {
    payload.earliestCollectionAt = toIso(model.earliestCollectionAt);
  }
  if (model.latestCollectionAt) {
    payload.latestCollectionAt = toIso(model.latestCollectionAt);
  }
  const next = await action.run(() =>
    request<MutationResult>(
      usePresealedIntake
        ? routes.ingestPresealedSample
        : routes.createCollectionRequest,
      {
        method: "POST",
        body: JSON.stringify(payload),
      },
      store.session,
    ),
  );
  if (!next) return;
  result.value = next;
  store.showNotice(
    next.serviceability === "unserviceable"
      ? "Request recorded as outside service coverage."
      : "Collection request created and planned.",
  );
  await store.refresh().catch(() => undefined);
}
</script>

<template>
  <PanelCard
    :eyebrow="presealedIntake ? 'Node or clinic intake' : 'Requester'"
    :title="presealedIntake ? 'Register sealed sample' : 'Create request'"
    :description="
      presealedIntake
        ? 'Register an already-sealed clinic or node sample and place it into explicit custody.'
        : 'Check coverage, handling compatibility, capacity, and deadline feasibility in one action.'
    "
    class-name="request-panel"
  >
    <form @submit.prevent="submit">
      <div class="form-grid form-grid-3">
        <FieldGroup label="Requester ID" input-id="requester">
          <input
            id="requester"
            v-model="model.requesterId"
            name="requesterId"
            required
            autocomplete="off"
          />
        </FieldGroup>
        <FieldGroup label="Collection origin" input-id="origin-type">
          <select id="origin-type" v-model="model.originType" name="originType">
            <option v-if="!presealedIntake" value="home">Home collection</option>
            <option value="clinic">Clinic</option>
            <option value="node">Collection node</option>
          </select>
        </FieldGroup>
        <FieldGroup label="Sample type" input-id="sample-type">
          <input
            id="sample-type"
            v-model="model.sampleTypeId"
            name="sampleTypeId"
            placeholder="Sample type ID"
            required
          />
        </FieldGroup>
        <FieldGroup label="Origin latitude" input-id="lat">
          <input
            id="lat"
            v-model="model.latitude"
            name="latitude"
            inputmode="decimal"
            required
          />
        </FieldGroup>
        <FieldGroup label="Origin longitude" input-id="lng">
          <input
            id="lng"
            v-model="model.longitude"
            name="longitude"
            inputmode="decimal"
            required
          />
        </FieldGroup>
        <FieldGroup label="Requested at" input-id="requested">
          <input
            id="requested"
            v-model="model.requestedAt"
            name="requestedAt"
            type="text"
            placeholder="2026-07-22T09:00:00Z"
            required
          />
        </FieldGroup>
        <FieldGroup label="Delivery deadline" input-id="deadline">
          <input
            id="deadline"
            v-model="model.deliveryDeadline"
            name="deliveryDeadline"
            type="text"
            placeholder="2026-07-22T13:00:00Z"
            required
          />
        </FieldGroup>
        <FieldGroup
          label="Earliest collection"
          input-id="earliest-collection"
          hint="Collection window opens. Defaults to the requested time."
        >
          <input
            id="earliest-collection"
            v-model="model.earliestCollectionAt"
            name="earliestCollectionAt"
            type="text"
            placeholder="2026-07-22T09:30:00Z"
          />
        </FieldGroup>
        <FieldGroup
          label="Latest collection"
          input-id="latest-collection"
          hint="Collection window closes. Must not be after the deadline."
        >
          <input
            id="latest-collection"
            v-model="model.latestCollectionAt"
            name="latestCollectionAt"
            type="text"
            placeholder="2026-07-22T11:00:00Z"
          />
        </FieldGroup>
        <FieldGroup label="Container type" input-id="container">
          <input
            id="container"
            v-model="model.containerType"
            name="containerType"
            required
          />
        </FieldGroup>
        <FieldGroup label="Minimum temperature" input-id="temp-min">
          <input
            id="temp-min"
            v-model="model.temperatureMinC"
            name="temperatureMinC"
            type="number"
            step="0.1"
            required
          />
        </FieldGroup>
        <FieldGroup label="Maximum temperature" input-id="temp-max">
          <input
            id="temp-max"
            v-model="model.temperatureMaxC"
            name="temperatureMaxC"
            type="number"
            step="0.1"
            required
          />
        </FieldGroup>
        <FieldGroup
          label="Incompatible sample types"
          input-id="incompatible"
          hint="Comma-separated IDs or a JSON array."
          class-name="field-span-2"
        >
          <input
            id="incompatible"
            v-model="model.incompatibleSampleTypes"
            name="incompatibleSampleTypes"
            placeholder="sample-type-id, another-id"
          />
        </FieldGroup>
        <label
          v-if="model.originType !== 'home' && !presealedIntake"
          class="check-field"
          for="already-sealed"
        >
          <input
            id="already-sealed"
            v-model="model.alreadySealed"
            type="checkbox"
          />
          Already sealed at clinic/node
        </label>
      </div>
      <div class="rule-callout">
        <strong>Handling presets</strong>
        <span>Amber · LX-A</span>
        <span>Indigo · LX-I · separate from Amber</span>
        <span>Frost · LX-F · cooled throughout</span>
        <span>Pulse · LX-P · never batched</span>
      </div>
      <div class="form-footer">
        <button
          id="create-request"
          class="primary-button"
          :disabled="action.busy.value"
        >
          {{
            action.busy.value
              ? "Planning request…"
              : presealedIntake
                ? "Register sealed sample"
                : "Create collection"
          }}
        </button>
        <InlineResult v-if="action.error.value" kind="error">
          {{ action.error.value }}
        </InlineResult>
      </div>
    </form>
    <div
      :class="['request-progress', { 'has-result': result }]"
      data-testid="request-progress"
      aria-live="polite"
    >
      <div>
        <span>Request progress</span>
        <strong>
          {{
            result
              ? titleCase(result.serviceability ?? "planned")
              : "Ready for intake"
          }}
        </strong>
      </div>
      <div>
        <span>Sample ID</span>
        <output data-testid="sample-id">{{ sampleId }}</output>
      </div>
      <div>
        <span>Journey</span>
        <strong>{{ displayId(journeyId) }}</strong>
      </div>
    </div>
  </PanelCard>
</template>
