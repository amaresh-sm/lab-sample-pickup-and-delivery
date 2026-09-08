<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { idempotencyKey, request, routes } from "../lib/api";
import { useAction } from "../lib/action";
import {
  formatTime,
  objectJson,
  titleCase,
} from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { EntityRecord, MutationResult } from "../types";
import FieldGroup from "./FieldGroup.vue";
import InlineResult from "./InlineResult.vue";
import PanelCard from "./PanelCard.vue";
import StatusBadge from "./StatusBadge.vue";

const store = useWorkspaceStore();
const readAction = useAction();
const editAction = useAction();
const plan = ref<MutationResult | null>(null);
const readJourneyId = ref("");
const edit = reactive({
  journeyId: "",
  expectedPlanVersion: "1",
  changeType: "reorder_stops",
  changes: "{}",
});
const stops = computed<EntityRecord[]>(() =>
  Array.isArray(plan.value?.orderedStops)
    ? plan.value?.orderedStops as EntityRecord[]
    : [],
);

async function readPlan(): Promise<void> {
  const next = await readAction.run(() =>
    request<MutationResult>(
      routes.readJourneyPlan,
      {
        method: "POST",
        body: JSON.stringify({ journeyId: readJourneyId.value }),
      },
      store.session,
    ),
  );
  if (!next) return;
  plan.value = next;
  edit.journeyId = String(next.journeyId ?? readJourneyId.value);
  edit.expectedPlanVersion = String(next.planVersion ?? 1);
}

async function editPlan(): Promise<void> {
  let changes: Record<string, unknown>;
  try {
    changes = objectJson(edit.changes);
  } catch (cause) {
    await editAction.run(async () => {
      throw cause;
    });
    return;
  }
  const next = await editAction.run(() =>
    request<MutationResult>(
      routes.editActivePlan,
      {
        method: "POST",
        body: JSON.stringify({
          journeyId: edit.journeyId,
          expectedPlanVersion: Number(edit.expectedPlanVersion),
          changeType: edit.changeType,
          changes,
          idempotencyKey: idempotencyKey("plan-edit"),
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  plan.value = next;
  edit.expectedPlanVersion = String(next.planVersion ?? Number(edit.expectedPlanVersion) + 1);
  store.showNotice("Plan updated after full feasibility revalidation.");
  await store.refresh().catch(() => undefined);
}
</script>

<template>
  <div class="page-grid page-grid-2">
    <PanelCard
      eyebrow="Planning"
      title="Find journey"
      description="Inspect the active route, worker assignments, capacity, and version."
    >
      <form class="stack-form" @submit.prevent="readPlan">
        <FieldGroup label="Journey ID" input-id="plan-read-journey">
          <input
            id="plan-read-journey"
            v-model="readJourneyId"
            required
          />
        </FieldGroup>
        <button class="primary-button" :disabled="readAction.busy.value">
          Read journey plan
        </button>
        <InlineResult v-if="readAction.error.value" kind="error">
          {{ readAction.error.value }}
        </InlineResult>
      </form>
      <div v-if="plan" class="plan-summary">
        <div>
          <span>Mode</span>
          <strong>{{ titleCase(plan.routeMode) }}</strong>
        </div>
        <div>
          <span>Version</span>
          <strong>{{ String(plan.planVersion ?? "—") }}</strong>
        </div>
        <div>
          <span>Cost</span>
          <strong>{{ String(plan.totalCostUnits ?? "—") }}</strong>
        </div>
        <div>
          <span>State</span>
          <StatusBadge :value="plan.valid === false ? 'invalid' : plan.status ?? 'valid'" />
        </div>
      </div>
    </PanelCard>

    <PanelCard
      eyebrow="Optimistic concurrency"
      title="Edit active plan"
      description="Stale versions and infeasible batch or route changes are rejected atomically."
    >
      <form class="stack-form" @submit.prevent="editPlan">
        <FieldGroup label="Journey ID for edit" input-id="plan-edit-journey">
          <input id="plan-edit-journey" v-model="edit.journeyId" required />
        </FieldGroup>
        <FieldGroup
          label="Expected plan version"
          input-id="plan-version"
        >
          <input
            id="plan-version"
            v-model="edit.expectedPlanVersion"
            type="number"
            min="1"
            required
          />
        </FieldGroup>
        <FieldGroup label="Change type" input-id="plan-change-type">
          <select id="plan-change-type" v-model="edit.changeType">
            <option value="reorder_stops">Reorder stops</option>
            <option value="merge_samples">Merge samples/batch</option>
            <option value="split_samples">Split samples</option>
            <option value="replace_worker">Replace worker</option>
            <option value="replace_route">Replace route</option>
            <option value="reroute">Reroute unfinished work</option>
            <option value="cancel_work">Cancel unfinished work</option>
          </select>
        </FieldGroup>
        <FieldGroup
          label="Validated changes"
          input-id="plan-changes"
          hint='Examples: {"orderedStopIds":["node-a","lab-a"]} or {"sampleIds":["sample-a"]}'
        >
          <textarea
            id="plan-changes"
            v-model="edit.changes"
            rows="5"
            required
          />
        </FieldGroup>
        <button class="primary-button" :disabled="editAction.busy.value">
          Validate and save plan
        </button>
        <InlineResult v-if="editAction.error.value" kind="error">
          {{ editAction.error.value }}
        </InlineResult>
      </form>
    </PanelCard>

    <PanelCard
      v-if="stops.length"
      eyebrow="Ordered work"
      title="Journey timeline"
      class-name="grid-span-2"
    >
      <ol class="timeline">
        <li
          v-for="(stop, index) in stops"
          :key="String(stop.stopId ?? stop.id ?? stop.locationId ?? index)"
        >
          <span>{{ index + 1 }}</span>
          <div>
            <strong>
              {{
                String(
                  stop.name ??
                  stop.locationName ??
                  stop.locationId ??
                  `Stop ${index + 1}`,
                )
              }}
            </strong>
            <small>
              {{ titleCase(stop.type ?? stop.stopType ?? "transfer") }} ·
              {{ formatTime(stop.plannedAt ?? stop.estimatedAt ?? stop.arrivalAt) }}
            </small>
          </div>
          <StatusBadge :value="stop.status ?? 'planned'" />
        </li>
      </ol>
    </PanelCard>
  </div>
</template>
