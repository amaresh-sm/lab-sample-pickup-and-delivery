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
const acceptAction = useAction();
const declineAction = useAction();
const result = ref<MutationResult | null>(null);
const model = reactive({
  assignmentId: "",
  reason: "",
  declinedAt: localDateTime(),
});

async function accept(): Promise<void> {
  const next = await acceptAction.run(() =>
    request<MutationResult>(
      routes.acceptAssignment,
      {
        method: "POST",
        body: JSON.stringify({
          assignmentId: model.assignmentId,
          idempotencyKey: idempotencyKey("accept"),
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  result.value = next;
  store.showNotice("Assignment accepted.");
  await store.refresh().catch(() => undefined);
}

async function decline(): Promise<void> {
  const next = await declineAction.run(() =>
    request<MutationResult>(
      routes.declineAssignment,
      {
        method: "POST",
        body: JSON.stringify({
          assignmentId: model.assignmentId,
          reason: model.reason || null,
          declinedAt: toIso(model.declinedAt),
          idempotencyKey: idempotencyKey("decline"),
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  result.value = next;
  store.showNotice("Offer declined and returned to fair assignment.");
  await store.refresh().catch(() => undefined);
}
</script>

<template>
  <PanelCard
    eyebrow="Assignment offer"
    title="Accept or decline"
    description="Offers are assigned one worker at a time and retries remain idempotent."
  >
    <form class="stack-form" @submit.prevent="accept">
      <FieldGroup label="Offer assignment ID" input-id="offer-assignment">
        <input
          id="offer-assignment"
          v-model="model.assignmentId"
          required
          autocomplete="off"
        />
      </FieldGroup>
      <FieldGroup label="Decline reason" input-id="decline-reason">
        <input
          id="decline-reason"
          v-model="model.reason"
          placeholder="Optional"
        />
      </FieldGroup>
      <FieldGroup label="Decision time" input-id="decision-time">
        <input
          id="decision-time"
          v-model="model.declinedAt"
          type="datetime-local"
          required
        />
      </FieldGroup>
      <div class="button-row">
        <button
          id="accept-assignment"
          class="primary-button"
          :disabled="acceptAction.busy.value || declineAction.busy.value"
        >
          Accept assignment
        </button>
        <button
          id="decline-assignment"
          type="button"
          class="secondary-button"
          :disabled="acceptAction.busy.value || declineAction.busy.value"
          @click="decline"
        >
          Decline offer
        </button>
      </div>
      <InlineResult
        v-if="acceptAction.error.value || declineAction.error.value || result"
        :kind="acceptAction.error.value || declineAction.error.value ? 'error' : 'success'"
        test-id="assignment-decision"
      >
        {{
          acceptAction.error.value ||
          declineAction.error.value ||
          titleCase(result?.status)
        }}
      </InlineResult>
    </form>
  </PanelCard>
</template>
