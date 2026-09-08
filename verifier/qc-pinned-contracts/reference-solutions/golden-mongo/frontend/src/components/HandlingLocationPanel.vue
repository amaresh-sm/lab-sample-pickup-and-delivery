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
const checkpointAction = useAction();
const locationAction = useAction();
const checkpointResult = ref<MutationResult | null>(null);
const locationResult = ref<MutationResult | null>(null);
const checkpoint = reactive({
  sampleId: "",
  assignmentId: "",
  locationId: "",
  containerType: "refrigerated_vial",
  observedTemperatureC: "4",
  recordedAt: localDateTime(),
});
const location = reactive({
  assignmentId: "",
  latitude: "12.9716",
  longitude: "77.5946",
  sequence: "1",
  recordedAt: localDateTime(),
});

async function recordCheckpoint(): Promise<void> {
  const next = await checkpointAction.run(() =>
    request<MutationResult>(
      routes.recordHandlingCheckpoint,
      {
        method: "POST",
        body: JSON.stringify({
          sampleId: checkpoint.sampleId,
          ...(checkpoint.assignmentId.trim()
            ? { assignmentId: checkpoint.assignmentId.trim() }
            : {}),
          locationId: checkpoint.locationId.trim(),
          containerType: checkpoint.containerType,
          observedTemperatureC: Number(checkpoint.observedTemperatureC),
          recordedAt: toIso(checkpoint.recordedAt),
          idempotencyKey: idempotencyKey("handling"),
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  checkpointResult.value = next;
  store.showNotice(
    next.compliant === false
      ? "Handling exception recorded."
      : "Handling checkpoint recorded.",
  );
  await store.refresh().catch(() => undefined);
}

async function recordLocation(): Promise<void> {
  const next = await locationAction.run(() =>
    request<MutationResult>(
      routes.recordWorkerLocation,
      {
        method: "POST",
        body: JSON.stringify({
          assignmentId: location.assignmentId,
          coordinate: {
            latitude: Number(location.latitude),
            longitude: Number(location.longitude),
          },
          sequence: Number(location.sequence),
          recordedAt: toIso(location.recordedAt),
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  locationResult.value = next;
  location.sequence = String(Number(location.sequence) + 1);
  store.showNotice("Location saved with a strictly increasing sequence.");
  await store.refresh().catch(() => undefined);
}
</script>

<template>
  <div class="page-grid page-grid-2 align-start grid-span-2">
    <PanelCard
      eyebrow="Handling integrity"
      title="Record checkpoint"
      description="Register sealed sample intake and verify container and temperature compliance."
    >
      <form class="stack-form" @submit.prevent="recordCheckpoint">
        <FieldGroup label="Checkpoint sample ID" input-id="checkpoint-sample">
          <input id="checkpoint-sample" v-model="checkpoint.sampleId" required />
        </FieldGroup>
        <FieldGroup
          label="Checkpoint assignment ID"
          input-id="checkpoint-assignment"
          hint="May be omitted for the next active transport leg."
        >
          <input
            id="checkpoint-assignment"
            v-model="checkpoint.assignmentId"
          />
        </FieldGroup>
        <FieldGroup label="Checkpoint location ID" input-id="checkpoint-location">
          <input
            id="checkpoint-location"
            v-model="checkpoint.locationId"
            required
          />
        </FieldGroup>
        <FieldGroup label="Checkpoint container" input-id="checkpoint-container">
          <input
            id="checkpoint-container"
            v-model="checkpoint.containerType"
            required
          />
        </FieldGroup>
        <FieldGroup
          label="Checkpoint temperature °C"
          input-id="checkpoint-temperature"
        >
          <input
            id="checkpoint-temperature"
            v-model="checkpoint.observedTemperatureC"
            type="number"
            step="0.1"
            required
          />
        </FieldGroup>
        <FieldGroup label="Checkpoint time" input-id="checkpoint-time">
          <input
            id="checkpoint-time"
            v-model="checkpoint.recordedAt"
            type="datetime-local"
            required
          />
        </FieldGroup>
        <button class="primary-button" :disabled="checkpointAction.busy.value">
          Record handling checkpoint
        </button>
        <InlineResult
          v-if="checkpointAction.error.value || checkpointResult"
          :kind="checkpointAction.error.value ? 'error' : checkpointResult?.compliant === false ? 'error' : 'success'"
          test-id="checkpoint-result"
        >
          {{
            checkpointAction.error.value ||
            (checkpointResult?.compliant === false
              ? "Non-compliant"
              : titleCase(checkpointResult?.sampleStatus ?? "compliant"))
          }}
        </InlineResult>
      </form>
    </PanelCard>

    <PanelCard
      eyebrow="Realtime movement"
      title="Share location"
      description="Out-of-order or duplicate sequence numbers are rejected as stale."
    >
      <form class="stack-form" @submit.prevent="recordLocation">
        <FieldGroup
          label="Location assignment ID"
          input-id="location-assignment"
        >
          <input
            id="location-assignment"
            v-model="location.assignmentId"
            required
          />
        </FieldGroup>
        <FieldGroup label="Worker latitude" input-id="worker-latitude">
          <input
            id="worker-latitude"
            v-model="location.latitude"
            inputmode="decimal"
            required
          />
        </FieldGroup>
        <FieldGroup label="Worker longitude" input-id="worker-longitude">
          <input
            id="worker-longitude"
            v-model="location.longitude"
            inputmode="decimal"
            required
          />
        </FieldGroup>
        <FieldGroup label="Location sequence" input-id="location-sequence">
          <input
            id="location-sequence"
            v-model="location.sequence"
            type="number"
            min="1"
            required
          />
        </FieldGroup>
        <FieldGroup label="Location recorded at" input-id="location-time">
          <input
            id="location-time"
            v-model="location.recordedAt"
            type="datetime-local"
            required
          />
        </FieldGroup>
        <button class="primary-button" :disabled="locationAction.busy.value">
          Save location
        </button>
        <InlineResult
          v-if="locationAction.error.value || locationResult"
          :kind="locationAction.error.value ? 'error' : 'success'"
          test-id="location-result"
        >
          {{
            locationAction.error.value ||
            `Saved sequence ${locationResult?.sequence}`
          }}
        </InlineResult>
      </form>
    </PanelCard>
  </div>
</template>
