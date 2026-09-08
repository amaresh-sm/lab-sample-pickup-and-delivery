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
const receiptAction = useAction();
const exceptionAction = useAction();
const receiptResult = ref<MutationResult | null>(null);
const exceptionResult = ref<MutationResult | null>(null);
const receipt = reactive({
  sampleId: "",
  laboratoryId: "",
  receivedAt: localDateTime(),
});
const exception = reactive({
  sampleId: "",
  laboratoryId: "",
  reason: "",
  observedAt: localDateTime(),
});

async function confirmReceipt(): Promise<void> {
  const next = await receiptAction.run(() =>
    request<MutationResult>(
      routes.confirmLaboratoryReceipt,
      {
        method: "POST",
        body: JSON.stringify({
          sampleId: receipt.sampleId,
          laboratoryId: receipt.laboratoryId,
          receivedAt: toIso(receipt.receivedAt),
          idempotencyKey: idempotencyKey("lab-receipt"),
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  receiptResult.value = next;
  store.showNotice("Laboratory receipt confirmed; journey completed.");
  await store.refresh().catch(() => undefined);
}

async function recordException(): Promise<void> {
  const next = await exceptionAction.run(() =>
    request<MutationResult>(
      routes.recordLaboratoryException,
      {
        method: "POST",
        body: JSON.stringify({
          sampleId: exception.sampleId,
          laboratoryId: exception.laboratoryId,
          reason: exception.reason,
          observedAt: toIso(exception.observedAt),
          idempotencyKey: idempotencyKey("lab-exception"),
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  exceptionResult.value = next;
  store.showNotice("Abnormal laboratory receipt recorded as an exception.");
  await store.refresh().catch(() => undefined);
}
</script>

<template>
  <PanelCard
    eyebrow="Laboratory intake"
    title="Receive or flag sample"
    description="Confirm normal receipt against reserved capacity or record an abnormal intake."
  >
    <div class="split-forms">
      <form class="stack-form" @submit.prevent="confirmReceipt">
        <h3>Confirm receipt</h3>
        <FieldGroup label="Receipt sample ID" input-id="receipt-sample">
          <input id="receipt-sample" v-model="receipt.sampleId" required />
        </FieldGroup>
        <FieldGroup label="Receipt laboratory ID" input-id="receipt-lab">
          <input id="receipt-lab" v-model="receipt.laboratoryId" required />
        </FieldGroup>
        <FieldGroup label="Received at" input-id="received-at">
          <input
            id="received-at"
            v-model="receipt.receivedAt"
            type="datetime-local"
            required
          />
        </FieldGroup>
        <button class="primary-button" :disabled="receiptAction.busy.value">
          Confirm laboratory receipt
        </button>
        <InlineResult
          v-if="receiptAction.error.value || receiptResult"
          :kind="receiptAction.error.value ? 'error' : 'success'"
          test-id="laboratory-receipt"
        >
          {{ receiptAction.error.value || titleCase(receiptResult?.status) }}
        </InlineResult>
      </form>
      <form class="stack-form" @submit.prevent="recordException">
        <h3>Abnormal receipt</h3>
        <FieldGroup label="Exception sample ID" input-id="exception-sample">
          <input id="exception-sample" v-model="exception.sampleId" required />
        </FieldGroup>
        <FieldGroup
          label="Exception laboratory ID"
          input-id="exception-lab"
        >
          <input
            id="exception-lab"
            v-model="exception.laboratoryId"
            required
          />
        </FieldGroup>
        <FieldGroup label="Exception reason" input-id="lab-exception-reason">
          <textarea
            id="lab-exception-reason"
            v-model="exception.reason"
            rows="3"
            required
          />
        </FieldGroup>
        <FieldGroup label="Observed at" input-id="exception-observed">
          <input
            id="exception-observed"
            v-model="exception.observedAt"
            type="datetime-local"
            required
          />
        </FieldGroup>
        <button class="danger-button" :disabled="exceptionAction.busy.value">
          Record laboratory exception
        </button>
        <InlineResult
          v-if="exceptionAction.error.value || exceptionResult"
          :kind="exceptionAction.error.value ? 'error' : 'success'"
          test-id="laboratory-exception"
        >
          {{ exceptionAction.error.value || titleCase(exceptionResult?.status) }}
        </InlineResult>
      </form>
    </div>
  </PanelCard>
</template>
