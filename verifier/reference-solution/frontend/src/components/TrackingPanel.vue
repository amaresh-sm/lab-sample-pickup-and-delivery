<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { request, routes } from "../lib/api";
import { useAction } from "../lib/action";
import { displayId, formatTime, titleCase } from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { EntityRecord, MutationResult } from "../types";
import EmptyState from "./EmptyState.vue";
import FieldGroup from "./FieldGroup.vue";
import InlineResult from "./InlineResult.vue";
import PanelCard from "./PanelCard.vue";
import StatusBadge from "./StatusBadge.vue";

const store = useWorkspaceStore();
const action = useAction();
const sampleId = ref("");
const sample = ref<MutationResult | null>(null);
const plan = ref<MutationResult | null>(null);

const state = computed(() => {
  const nested = sample.value?.state;
  return nested && typeof nested === "object"
    ? (nested as EntityRecord)
    : sample.value;
});
const timeline = computed<EntityRecord[]>(() => {
  const value = sample.value?.timeline ?? sample.value?.events;
  return Array.isArray(value) ? value as EntityRecord[] : [];
});
const routeStops = computed<EntityRecord[]>(() =>
  Array.isArray(plan.value?.orderedStops)
    ? plan.value.orderedStops as EntityRecord[]
    : [],
);
const routeLegCount = computed(() =>
  Array.isArray(plan.value?.legs) ? plan.value.legs.length : 0,
);
const completedLegCount = computed(() =>
  Array.isArray(sample.value?.completedLegIds)
    ? sample.value.completedLegIds.length
    : 0,
);
/** When the sample's location was last reported, as the server recorded it. */
const locationRecordedAt = computed(() =>
  sample.value?.locationRecordedAt
    ? String(sample.value.locationRecordedAt)
    : "",
);
/**
 * Whether that reading is now stale. The server decides this with the same
 * threshold the dispatcher's operations view uses, so the two never disagree.
 */
const locationStale = computed(() => Boolean(sample.value?.locationStale));

async function submit(): Promise<void> {
  const next = await action.run(async () => {
    const [stateResult, lifecycle] = await Promise.all([
      request<MutationResult>(
        routes.readSampleState(sampleId.value),
        {},
        store.session,
      ),
      request<MutationResult>(
        routes.readSampleLifecycle,
        {
          method: "POST",
          body: JSON.stringify({ sampleId: sampleId.value }),
        },
        store.session,
      ),
    ]);
    const combined = {
      ...stateResult,
      ...lifecycle,
      state: stateResult.state ?? lifecycle.state,
    };
    const journeyId = String(combined.activeJourneyId ?? "");
    const journeyPlan = journeyId
      ? await request<MutationResult>(
          routes.readJourneyPlan,
          {
            method: "POST",
            body: JSON.stringify({ journeyId }),
          },
          store.session,
        )
      : null;
    return { sample: combined, plan: journeyPlan };
  },
  );
  if (!next) return;
  sample.value = next.sample;
  plan.value = next.plan;
}

async function selectSample(id: unknown): Promise<void> {
  sampleId.value = String(id ?? "");
  if (sampleId.value) await submit();
}

watch(
  () => store.lastEvent,
  (event) => {
    if (!event || !sample.value || !sampleId.value || action.busy.value) return;
    const payload = event.payload ?? (
      event.data && typeof event.data === "object"
        ? event.data as EntityRecord
        : undefined
    );
    const relatedId = String(
      payload?.sampleId ??
      payload?.sample_id ??
      event.entityId ??
      "",
    );
    if (
      relatedId === sampleId.value ||
      (
        event.entityType === "journey" &&
        event.entityId === sample.value.activeJourneyId
      )
    ) {
      void submit();
    }
  },
);
</script>

<template>
  <PanelCard
    eyebrow="Requester visibility"
    title="Track a sample"
    description="View the safe, role-filtered journey state, current location, and expected arrival."
  >
    <div v-if="store.requesterSamples.length" class="requester-samples">
      <span class="list-label">My requests</span>
      <ul class="record-list">
        <li
          v-for="item in store.requesterSamples"
          :key="String(item.sampleId ?? item.id)"
        >
          <div>
            <strong>Sample {{ displayId(item.sampleId ?? item.id) }}</strong>
            <small>
              {{ titleCase(item.status ?? "planned") }} ·
              {{ formatTime(item.deliveryDeadline) }}
            </small>
          </div>
          <button
            type="button"
            class="secondary-button"
            :aria-label="`Track sample ${String(item.sampleId ?? item.id)}`"
            @click="selectSample(item.sampleId ?? item.id)"
          >
            Track
          </button>
        </li>
      </ul>
    </div>
    <form class="search-form" @submit.prevent="submit">
      <FieldGroup label="Sample tracking ID" input-id="tracking-sample">
        <input
          id="tracking-sample"
          v-model="sampleId"
          name="sampleId"
          required
        />
      </FieldGroup>
      <button class="primary-button" :disabled="action.busy.value">
        View progress
      </button>
    </form>
    <InlineResult v-if="action.error.value" kind="error">
      {{ action.error.value }}
    </InlineResult>
    <template v-if="sample">
      <div class="tracking-summary">
        <div>
          <span>Status</span>
          <StatusBadge :value="state?.status ?? sample.status ?? 'planned'" />
        </div>
        <div>
          <span>Current holder</span>
          <strong>{{ displayId(sample.currentHolderId) }}</strong>
        </div>
        <div>
          <span>Deadline</span>
          <strong>{{ formatTime(sample.deliveryDeadline) }}</strong>
        </div>
        <div>
          <span>Laboratory</span>
          <strong>{{ displayId(sample.targetLaboratoryId) }}</strong>
        </div>
        <div>
          <span>Active journey</span>
          <strong>{{ displayId(sample.activeJourneyId) }}</strong>
        </div>
        <div>
          <span>Route progress</span>
          <strong>
            {{ completedLegCount }} / {{ routeLegCount }} legs
          </strong>
        </div>
        <div>
          <span>Latest location</span>
          <strong>
            {{
              sample.latestLocation
                ? JSON.stringify(sample.latestLocation)
                : "Awaiting update"
            }}
          </strong>
        </div>
        <div>
          <span>Last update</span>
          <strong data-testid="location-recorded-at">
            {{
              locationRecordedAt
                ? formatTime(locationRecordedAt)
                : "Awaiting update"
            }}
          </strong>
        </div>
        <div>
          <span>Location freshness</span>
          <strong>
            <span
              :class="['stale-badge', locationStale ? 'is-stale' : 'is-current']"
              data-testid="location-stale"
            >
              {{ locationStale ? "Stale" : "Current" }}
            </span>
          </strong>
        </div>
        <div>
          <span>Pending handoff</span>
          <strong>{{ displayId(sample.pendingHandoffId) }}</strong>
        </div>
      </div>
      <div v-if="routeStops.length" class="route-progress">
        <div class="route-progress-heading">
          <strong>Ordered journey</strong>
          <span>ETA {{ formatTime(plan?.estimatedDeliveryAt) }}</span>
        </div>
        <ol class="timeline compact-timeline">
          <li
            v-for="(stop, index) in routeStops"
            :key="String(stop.stopId ?? stop.locationId ?? index)"
          >
            <span>{{ index + 1 }}</span>
            <div>
              <strong>{{ displayId(stop.locationId) }}</strong>
              <small>
                {{ titleCase(stop.stopType ?? "transfer") }} ·
                {{ formatTime(stop.completedAt ?? stop.estimatedAt) }}
              </small>
            </div>
            <StatusBadge :value="stop.completedAt ? 'completed' : 'planned'" />
          </li>
        </ol>
      </div>
      <ol v-if="timeline.length" class="timeline compact-timeline">
        <li
          v-for="(event, index) in timeline"
          :key="String(event.eventId ?? event.id ?? index)"
        >
          <span>{{ index + 1 }}</span>
          <div>
            <strong>{{ titleCase(event.status ?? event.action) }}</strong>
            <small>{{ formatTime(event.occurredAt) }}</small>
          </div>
        </li>
      </ol>
      <EmptyState
        v-else
        title="No lifecycle events yet"
        description="Saved progress events will appear as the journey advances."
      />
    </template>
  </PanelCard>
</template>
