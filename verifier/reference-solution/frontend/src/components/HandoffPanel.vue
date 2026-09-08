<script setup lang="ts">
import { reactive, ref } from "vue";
import {
  idempotencyKey,
  request,
  routes,
  toIso,
} from "../lib/api";
import { useAction } from "../lib/action";
import { displayId, localDateTime } from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { MutationResult } from "../types";
import FieldGroup from "./FieldGroup.vue";
import InlineResult from "./InlineResult.vue";
import PanelCard from "./PanelCard.vue";

const store = useWorkspaceStore();
const initiateAction = useAction();
const confirmAction = useAction();
const initiated = ref<MutationResult | null>(null);
const result = ref<MutationResult | null>(null);
const initiation = reactive({
  sampleId: "",
  receiverId: "",
  fromAssignmentId: "",
  toAssignmentId: "",
  initiatedAt: localDateTime(),
});
const confirmation = reactive({
  handoffId: "",
  confirmationSide: "sender",
  confirmedAt: localDateTime(),
  idempotencyKey: idempotencyKey("handoff-confirm"),
});

async function initiate(): Promise<void> {
  const next = await initiateAction.run(() =>
    request<MutationResult>(
      routes.initiateHandoff,
      {
        method: "POST",
        body: JSON.stringify({
          sampleId: initiation.sampleId,
          receiverId: initiation.receiverId,
          fromAssignmentId: initiation.fromAssignmentId.trim(),
          toAssignmentId: initiation.toAssignmentId.trim(),
          initiatedAt: toIso(initiation.initiatedAt),
          idempotencyKey: idempotencyKey("handoff-init"),
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  initiated.value = next;
  confirmation.handoffId = String(next.handoffId ?? "");
  store.showNotice("Handoff opened. The sender remains responsible.");
  await store.refresh().catch(() => undefined);
}

async function confirm(): Promise<void> {
  result.value = null;
  const next = await confirmAction.run(() =>
    request<MutationResult>(
      routes.confirmHandoff,
      {
        method: "POST",
        body: JSON.stringify({
          handoffId: confirmation.handoffId,
          confirmationSide: confirmation.confirmationSide,
          confirmedAt: toIso(confirmation.confirmedAt),
          idempotencyKey: confirmation.idempotencyKey,
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  result.value = next;
  confirmation.idempotencyKey = idempotencyKey("handoff-confirm");
  store.showNotice(
    next.status === "confirmed"
      ? "Both sides confirmed. Custody transferred."
      : "Confirmation saved. Custody has not transferred yet.",
  );
  await store.refresh().catch(() => undefined);
}
</script>

<template>
  <PanelCard
    eyebrow="Chain of custody"
    title="Handoff"
    description="The sender stays responsible until both named participants confirm."
    class-name="handoff-panel"
  >
    <div class="split-forms">
      <form class="stack-form" @submit.prevent="initiate">
        <h3>Initiate handoff</h3>
        <FieldGroup label="Handoff sample ID" input-id="handoff-sample">
          <input
            id="handoff-sample"
            v-model="initiation.sampleId"
            required
          />
        </FieldGroup>
        <FieldGroup label="Receiver ID" input-id="handoff-receiver">
          <input
            id="handoff-receiver"
            v-model="initiation.receiverId"
            required
          />
        </FieldGroup>
        <FieldGroup label="From assignment" input-id="handoff-from">
          <input
            id="handoff-from"
            v-model="initiation.fromAssignmentId"
            required
          />
        </FieldGroup>
        <FieldGroup label="To assignment" input-id="handoff-to">
          <input
            id="handoff-to"
            v-model="initiation.toAssignmentId"
            required
          />
        </FieldGroup>
        <FieldGroup label="Initiated at" input-id="handoff-initiated">
          <input
            id="handoff-initiated"
            v-model="initiation.initiatedAt"
            type="datetime-local"
            required
          />
        </FieldGroup>
        <button class="secondary-button" :disabled="initiateAction.busy.value">
          Initiate handoff
        </button>
        <InlineResult
          v-if="initiateAction.error.value || initiated"
          :kind="initiateAction.error.value ? 'error' : 'success'"
        >
          {{
            initiateAction.error.value ||
            `Handoff ${displayId(initiated?.handoffId)} is pending`
          }}
        </InlineResult>
      </form>

      <form class="stack-form" @submit.prevent="confirm">
        <h3>Confirm participation</h3>
        <FieldGroup label="Handoff ID" input-id="handoff-id">
          <input
            id="handoff-id"
            v-model="confirmation.handoffId"
            name="handoffId"
            required
          />
        </FieldGroup>
        <FieldGroup label="Confirmation side" input-id="handoff-side">
          <select
            id="handoff-side"
            v-model="confirmation.confirmationSide"
            name="confirmationSide"
            data-testid="handoff-side"
          >
            <option value="sender">Sender</option>
            <option value="receiver">Receiver</option>
          </select>
        </FieldGroup>
        <FieldGroup label="Confirmed at" input-id="handoff-time">
          <input
            id="handoff-time"
            v-model="confirmation.confirmedAt"
            type="text"
            placeholder="2026-07-22T09:30:00Z"
            required
          />
        </FieldGroup>
        <FieldGroup label="Action key" input-id="handoff-key">
          <input
            id="handoff-key"
            v-model="confirmation.idempotencyKey"
            required
          />
        </FieldGroup>
        <button
          id="confirm-handoff"
          class="primary-button"
          :disabled="confirmAction.busy.value"
        >
          Confirm handoff
        </button>
        <div
          v-show="confirmAction.error.value || result?.status"
          class="result-pair"
        >
          <InlineResult
            :kind="confirmAction.error.value ? 'error' : 'success'"
            test-id="handoff-status"
          >
            {{ confirmAction.error.value || result?.status }}
          </InlineResult>
          <InlineResult
            :kind="result?.currentHolderId ? 'success' : 'neutral'"
            test-id="handoff-holder"
          >
            {{ result?.currentHolderId ?? result?.current_holder_id ?? "" }}
          </InlineResult>
        </div>
      </form>
    </div>
  </PanelCard>
</template>
