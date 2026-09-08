<script setup lang="ts">
import { reactive, ref } from "vue";
import { request, routes } from "../lib/api";
import { useAction } from "../lib/action";
import { useWorkspaceStore } from "../stores/workspace";
import type { LocationFunction, MutationResult } from "../types";
import { LOCATION_FUNCTIONS, LOCATION_FUNCTION_LABELS } from "../types";
import FieldGroup from "./FieldGroup.vue";
import InlineResult from "./InlineResult.vue";
import PanelCard from "./PanelCard.vue";

const store = useWorkspaceStore();
const action = useAction();
const result = ref("");
// "Whole site" toggles the location itself; a named function toggles only that
// function, leaving the rest of the site working.
const model = reactive({
  locationId: "",
  scope: "location" as "location" | LocationFunction,
  active: "true",
});
const scopeOptions = [
  { value: "location", label: "Whole location" },
  ...LOCATION_FUNCTIONS.map((name) => ({
    value: name,
    label: LOCATION_FUNCTION_LABELS[name],
  })),
];

async function submit(): Promise<void> {
  const active = model.active === "true";
  const wholeLocation = model.scope === "location";
  const next = await action.run(() =>
    request<MutationResult>(
      wholeLocation ? routes.setLocationStatus : routes.setLocationFunctionStatus,
      {
        method: "POST",
        body: JSON.stringify(
          wholeLocation
            ? { locationId: model.locationId, active }
            : {
                locationId: model.locationId,
                function: model.scope,
                active,
              },
        ),
      },
      store.session,
    ),
  );
  if (!next) return;
  const subject = wholeLocation
    ? "Location"
    : LOCATION_FUNCTION_LABELS[model.scope as LocationFunction];
  result.value = active ? `${subject} activated` : `${subject} deactivated`;
  store.showNotice(`${result.value}. Remaining plans are being revalidated.`);
  await store.refresh().catch(() => undefined);
}
</script>

<template>
  <PanelCard
    eyebrow="Administrator"
    title="Network status"
    description="Activate or deactivate a location, or one function it provides, without restarting. Status changes trigger safe revalidation of unfinished work."
  >
    <form class="stack-form" @submit.prevent="submit">
      <FieldGroup label="Location ID" input-id="location-id">
        <input
          id="location-id"
          v-model="model.locationId"
          name="locationId"
          required
        />
      </FieldGroup>
      <FieldGroup label="Scope" input-id="location-scope">
        <select
          id="location-scope"
          v-model="model.scope"
          name="scope"
          data-testid="location-scope"
        >
          <option
            v-for="option in scopeOptions"
            :key="option.value"
            :value="option.value"
          >
            {{ option.label }}
          </option>
        </select>
      </FieldGroup>
      <FieldGroup label="Active" input-id="location-active">
        <select
          id="location-active"
          v-model="model.active"
          name="active"
          data-testid="location-active"
        >
          <option value="true">Active</option>
          <option value="false">Inactive</option>
        </select>
      </FieldGroup>
      <button
        id="update-location"
        class="primary-button"
        :disabled="action.busy.value"
      >
        Update location
      </button>
      <InlineResult
        :kind="action.error.value ? 'error' : result ? 'success' : 'neutral'"
        test-id="location-change"
      >
        {{ action.error.value || result }}
      </InlineResult>
    </form>
  </PanelCard>
</template>
