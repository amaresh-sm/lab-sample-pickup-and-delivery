<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { request, routes, toIso } from "../lib/api";
import { useAction } from "../lib/action";
import { localDateTime, titleCase } from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { ServiceabilityVerdict } from "../types";
import { SERVICEABILITY_REASON_LABELS } from "../types";
import FieldGroup from "./FieldGroup.vue";
import InlineResult from "./InlineResult.vue";
import PanelCard from "./PanelCard.vue";

const store = useWorkspaceStore();
const action = useAction();
const verdict = ref<ServiceabilityVerdict | null>(null);
const model = reactive({
  originType: "home",
  latitude: "12.9716",
  longitude: "77.5946",
  sampleTypeId: "",
  requestedAt: localDateTime(),
  earliestCollectionAt: "",
  latestCollectionAt: "",
  deliveryDeadline: localDateTime(4),
});

const reasonLabel = computed(() => {
  const reason = String(verdict.value?.reason ?? "");
  return SERVICEABILITY_REASON_LABELS[reason] ?? titleCase(reason);
});

/**
 * Runs the dry run only. The server answers from the published serviceability
 * calculation and writes nothing: no request, sample, journey, assignment or
 * capacity reservation is created.
 */
async function check(): Promise<void> {
  const payload: Record<string, unknown> = {
    originType: model.originType,
    origin: {
      latitude: Number(model.latitude),
      longitude: Number(model.longitude),
    },
    sampleTypeId: model.sampleTypeId,
    requestedAt: toIso(model.requestedAt),
    deliveryDeadline: toIso(model.deliveryDeadline),
  };
  if (model.earliestCollectionAt) {
    payload.earliestCollectionAt = toIso(model.earliestCollectionAt);
  }
  if (model.latestCollectionAt) {
    payload.latestCollectionAt = toIso(model.latestCollectionAt);
  }
  const next = await action.run(() =>
    request<ServiceabilityVerdict>(
      routes.checkServiceability,
      { method: "POST", body: JSON.stringify(payload) },
      store.session,
    ),
  );
  if (!next) return;
  verdict.value = next;
}
</script>

<template>
  <PanelCard
    eyebrow="Requester"
    title="Serviceability pre-check"
    description="Check an origin and sample type before creating anything. This is a dry run: no request, sample, journey, assignment or capacity reservation is created."
  >
    <form class="stack-form" @submit.prevent="check">
      <div class="form-grid form-grid-3">
        <FieldGroup label="Collection origin" input-id="precheck-origin-type">
          <select
            id="precheck-origin-type"
            v-model="model.originType"
            name="originType"
          >
            <option value="home">Home collection</option>
            <option value="clinic">Clinic</option>
            <option value="node">Collection node</option>
          </select>
        </FieldGroup>
        <FieldGroup label="Origin latitude" input-id="precheck-lat">
          <input
            id="precheck-lat"
            v-model="model.latitude"
            name="latitude"
            inputmode="decimal"
            required
          />
        </FieldGroup>
        <FieldGroup label="Origin longitude" input-id="precheck-lng">
          <input
            id="precheck-lng"
            v-model="model.longitude"
            name="longitude"
            inputmode="decimal"
            required
          />
        </FieldGroup>
        <FieldGroup label="Sample type" input-id="precheck-sample-type">
          <input
            id="precheck-sample-type"
            v-model="model.sampleTypeId"
            name="sampleTypeId"
            placeholder="Sample type ID"
            required
          />
        </FieldGroup>
        <FieldGroup label="Requested at" input-id="precheck-requested">
          <input
            id="precheck-requested"
            v-model="model.requestedAt"
            name="requestedAt"
            type="text"
            required
          />
        </FieldGroup>
        <FieldGroup label="Delivery deadline" input-id="precheck-deadline">
          <input
            id="precheck-deadline"
            v-model="model.deliveryDeadline"
            name="deliveryDeadline"
            type="text"
            required
          />
        </FieldGroup>
        <FieldGroup
          label="Earliest collection"
          input-id="precheck-window-open"
          hint="Optional. Defaults to the requested time."
        >
          <input
            id="precheck-window-open"
            v-model="model.earliestCollectionAt"
            name="earliestCollectionAt"
            type="text"
          />
        </FieldGroup>
        <FieldGroup
          label="Latest collection"
          input-id="precheck-window-close"
          hint="Optional. Defaults to the delivery deadline."
        >
          <input
            id="precheck-window-close"
            v-model="model.latestCollectionAt"
            name="latestCollectionAt"
            type="text"
          />
        </FieldGroup>
      </div>
      <button
        id="check-serviceability"
        class="secondary-button"
        :disabled="action.busy.value"
      >
        {{ action.busy.value ? "Checking coverage…" : "Check serviceability" }}
      </button>
      <InlineResult v-if="action.error.value" kind="error">
        {{ action.error.value }}
      </InlineResult>
    </form>
    <div
      v-if="verdict"
      class="tracking-summary"
      data-testid="serviceability-verdict"
      aria-live="polite"
    >
      <div>
        <span>Verdict</span>
        <strong
          :class="[
            'status-badge',
            verdict.serviceable ? 'status-valid' : 'status-invalid',
          ]"
          data-testid="serviceability-decision"
        >
          {{ verdict.serviceable ? "Serviceable" : "Not serviceable" }}
        </strong>
      </div>
      <div>
        <span>Reason</span>
        <strong data-testid="serviceability-reason">{{ reasonLabel }}</strong>
      </div>
      <div>
        <span>Target laboratory</span>
        <strong>{{ verdict.targetLaboratoryId || "—" }}</strong>
      </div>
      <div>
        <span>Estimated arrival</span>
        <strong>{{ verdict.estimatedArrivalAt || "—" }}</strong>
      </div>
      <div>
        <span>Calculation version</span>
        <strong data-testid="serviceability-version">
          {{ verdict.serviceabilityVersion ?? "—" }}
        </strong>
      </div>
    </div>
  </PanelCard>
</template>
