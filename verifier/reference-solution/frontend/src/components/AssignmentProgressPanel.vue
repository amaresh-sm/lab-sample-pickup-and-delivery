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
const status = ref("");
const model = reactive({
  assignmentId: "",
  status: "in_progress",
  occurredAt: localDateTime(),
  idempotencyKey: idempotencyKey("assignment"),
});

async function submit(): Promise<void> {
  const next = await action.run(() =>
    request<MutationResult>(
      routes.updateAssignmentProgress,
      {
        method: "POST",
        body: JSON.stringify({
          assignmentId: model.assignmentId,
          status: model.status,
          occurredAt: toIso(model.occurredAt),
          idempotencyKey: model.idempotencyKey,
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  status.value = String(next.status ?? model.status);
  model.idempotencyKey = idempotencyKey("assignment");
  store.showNotice(`Assignment marked ${titleCase(status.value)}.`);
  await store.refresh().catch(() => undefined);
}
</script>

<template>
  <PanelCard
    eyebrow="Worker action"
    title="Update work"
    description="Transitions are checked against collection, custody, and stop order."
  >
    <form class="stack-form" @submit.prevent="submit">
      <FieldGroup label="Assignment ID" input-id="assignment-id">
        <input
          id="assignment-id"
          v-model="model.assignmentId"
          name="assignmentId"
          required
          autocomplete="off"
        />
      </FieldGroup>
      <FieldGroup label="Work status" input-id="assignment-status">
        <select
          id="assignment-status"
          v-model="model.status"
          name="status"
          data-testid="assignment-status"
        >
          <option value="at_pickup">At pickup</option>
          <option value="picked_up">Picked up</option>
          <option value="in_progress">In progress</option>
          <option value="arrived">Arrived</option>
          <option value="delivered">Delivered</option>
          <option value="completed">Completed</option>
        </select>
      </FieldGroup>
      <FieldGroup label="Occurred at" input-id="assignment-time">
        <input
            id="assignment-time"
            v-model="model.occurredAt"
            name="occurredAt"
            type="text"
            placeholder="2026-07-22T09:30:00Z"
            required
        />
      </FieldGroup>
      <FieldGroup label="Update key" input-id="assignment-key">
        <input
          id="assignment-key"
          v-model="model.idempotencyKey"
          name="idempotencyKey"
          required
        />
      </FieldGroup>
      <button
        id="update-assignment"
        class="primary-button"
        :disabled="action.busy.value"
      >
        Update status
      </button>
      <InlineResult
        :kind="action.error.value ? 'error' : status ? 'success' : 'neutral'"
        test-id="updated-assignment-status"
      >
        {{ action.error.value || status }}
      </InlineResult>
    </form>
  </PanelCard>
</template>
