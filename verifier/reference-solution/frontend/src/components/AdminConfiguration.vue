<script setup lang="ts">
import { reactive, ref } from "vue";
import { request, routes, toIso } from "../lib/api";
import { useAction } from "../lib/action";
import { displayId, strings } from "../lib/format";
import { locationId, providedFunctions, serves } from "../lib/locations";
import { useWorkspaceStore } from "../stores/workspace";
import type { LocationFunction, MutationResult } from "../types";
import { LOCATION_FUNCTIONS, LOCATION_FUNCTION_LABELS } from "../types";
import FieldGroup from "./FieldGroup.vue";
import InlineResult from "./InlineResult.vue";
import PanelCard from "./PanelCard.vue";

const store = useWorkspaceStore();
const action = useAction();
const result = ref<MutationResult | null>(null);
const resultLabel = ref("");

const user = reactive({
  username: "",
  initialPassword: "",
  fullName: "",
  email: "",
  phone: "",
  roles: "requester",
  qualifications: "",
  certificationActive: "true",
  certificationExpiresAt: "",
  active: "true",
});
const userAccess = reactive({
  userId: "",
  roles: "",
  qualifications: "",
  certificationActive: "true",
  certificationExpiresAt: "",
  active: "true",
});
const userLookup = ref("");

// One location may provide any combination of the four functions, so the form
// carries a checkbox per function rather than a facility type.
const location = reactive({
  locationId: "",
  name: "",
  latitude: "12.9716",
  longitude: "77.5946",
  active: "true",
  coverageKm: "5",
  handlingMinutes: "10",
  functions: {
    collection_point: true,
    transfer_point: true,
    hub: false,
    laboratory: false,
  } as Record<LocationFunction, boolean>,
});
const locationFunction = reactive({
  locationId: "",
  function: "collection_point" as LocationFunction,
  active: "true",
});
const locationFunctionOptions = LOCATION_FUNCTIONS.map((name) => ({
  value: name,
  label: LOCATION_FUNCTION_LABELS[name],
}));
const sampleType = reactive({
  sampleTypeId: "",
  name: "",
  qualifiedCollectorRequired: "true",
  requiredQualification: "LX-A",
  containerType: "standard_vial",
  temperatureMinC: "2",
  temperatureMaxC: "8",
  incompatibleSampleTypes: "",
});
const acceptance = reactive({
  laboratoryId: "",
  sampleTypeId: "",
  accepts: "true",
});
const connection = reactive({
  connectionId: "",
  fromLocationId: "",
  toLocationId: "",
  travelMinutes: "20",
  costUnits: "1",
  cooled: "false",
  active: "true",
});
const capacity = reactive({
  resourceId: "",
  capacityUnits: "10",
  cooledCapacityUnits: "0",
});
const worker = reactive({
  workerId: "",
  available: "true",
  capacityUnits: "1",
});

async function perform(
  path: string,
  payload: Record<string, unknown>,
  message: string,
): Promise<void> {
  result.value = null;
  const next = await action.run(() =>
    request<MutationResult>(
      path,
      { method: "POST", body: JSON.stringify(payload) },
      store.session,
    ),
  );
  if (!next) return;
  result.value = next;
  resultLabel.value = message;
  store.showNotice(message);
  await store.refresh().catch(() => undefined);
}

async function readUser(): Promise<void> {
  result.value = null;
  const next = await action.run(() =>
    request<MutationResult>(
      routes.readUser(userLookup.value),
      {},
      store.session,
    ),
  );
  if (!next) return;
  result.value = next;
  resultLabel.value = "User loaded";
}

function certificationRecords(
  codes: string,
  active: string,
  expiresAt: string,
): Array<Record<string, unknown>> {
  return strings(codes).map((code) => ({
    code,
    active: active === "true",
    ...(expiresAt ? { expiresAt: toIso(expiresAt) } : {}),
  }));
}

function createUser(): Promise<void> {
  return perform(
    routes.createUser,
    {
      username: user.username,
      initialPassword: user.initialPassword,
      fullName: user.fullName,
      email: user.email,
      phone: user.phone,
      roles: strings(user.roles),
      qualifications: strings(user.qualifications),
      certifications: certificationRecords(
        user.qualifications,
        user.certificationActive,
        user.certificationExpiresAt,
      ),
      active: user.active === "true",
    },
    "User created",
  );
}

function updateAccess(): Promise<void> {
  return perform(
    routes.updateUserAccess,
    {
      userId: userAccess.userId,
      roles: strings(userAccess.roles),
      qualifications: strings(userAccess.qualifications),
      certifications: certificationRecords(
        userAccess.qualifications,
        userAccess.certificationActive,
        userAccess.certificationExpiresAt,
      ),
      active: userAccess.active === "true",
    },
    "User roles and certifications updated",
  );
}

function selectedFunctions(): LocationFunction[] {
  return LOCATION_FUNCTIONS.filter((name) => location.functions[name]);
}

function saveLocation(update: boolean): Promise<void> {
  return perform(
    update ? routes.updateLocation : routes.createLocation,
    {
      ...(update ? { locationId: location.locationId } : {}),
      name: location.name,
      coordinate: {
        latitude: Number(location.latitude),
        longitude: Number(location.longitude),
      },
      active: location.active === "true",
      coverageKm: Number(location.coverageKm),
      handlingMinutes: Number(location.handlingMinutes),
      functions: selectedFunctions(),
    },
    update ? "Location updated" : "Location created",
  );
}

function setLocationStatus(): Promise<void> {
  return perform(
    routes.setLocationStatus,
    {
      locationId: location.locationId,
      active: location.active === "true",
    },
    "Location status updated",
  );
}

function setLocationFunctionStatus(): Promise<void> {
  return perform(
    routes.setLocationFunctionStatus,
    {
      locationId: locationFunction.locationId,
      function: locationFunction.function,
      active: locationFunction.active === "true",
    },
    "Location function status updated",
  );
}

function saveSampleType(update: boolean): Promise<void> {
  return perform(
    update ? routes.updateSampleType : routes.createSampleType,
    {
      ...(update ? { sampleTypeId: sampleType.sampleTypeId } : {}),
      name: sampleType.name,
      qualifiedCollectorRequired:
        sampleType.qualifiedCollectorRequired === "true",
      requiredQualification: sampleType.requiredQualification,
      containerType: sampleType.containerType,
      temperatureMinC: Number(sampleType.temperatureMinC),
      temperatureMaxC: Number(sampleType.temperatureMaxC),
      incompatibleSampleTypes: strings(
        sampleType.incompatibleSampleTypes,
      ),
    },
    update ? "Sample handling rule updated" : "Sample type created",
  );
}

function configureAcceptance(): Promise<void> {
  return perform(
    routes.configureLaboratoryAcceptance,
    {
      laboratoryId: acceptance.laboratoryId,
      sampleTypeId: acceptance.sampleTypeId,
      accepts: acceptance.accepts === "true",
    },
    "Laboratory capability updated",
  );
}

function createConnection(): Promise<void> {
  return perform(
    routes.createConnection,
    {
      fromLocationId: connection.fromLocationId,
      toLocationId: connection.toLocationId,
      travelMinutes: Number(connection.travelMinutes),
      costUnits: Number(connection.costUnits),
      cooled: connection.cooled === "true",
      active: connection.active === "true",
    },
    "Network connection created",
  );
}

function setConnectionStatus(): Promise<void> {
  return perform(
    routes.setConnectionStatus,
    {
      connectionId: connection.connectionId,
      active: connection.active === "true",
    },
    "Connection status updated",
  );
}

function configureCapacity(): Promise<void> {
  return perform(
    routes.configureCapacity,
    {
      resourceId: capacity.resourceId,
      capacityUnits: Number(capacity.capacityUnits),
      cooledCapacityUnits: Number(capacity.cooledCapacityUnits),
    },
    "Regular and cooled capacity updated",
  );
}

function setWorkerAvailability(): Promise<void> {
  return perform(
    routes.setWorkerAvailability,
    {
      workerId: worker.workerId,
      available: worker.available === "true",
      capacityUnits: Number(worker.capacityUnits),
    },
    "Worker availability updated",
  );
}
</script>

<template>
  <div class="admin-stack">
    <PanelCard
      eyebrow="Identity and access"
      title="Users, roles & certifications"
      description="Onboard people, assign multiple roles, maintain active certifications, and deactivate access."
    >
      <div class="admin-actions">
        <details open>
          <summary>Create user</summary>
          <form class="form-grid form-grid-3" @submit.prevent="createUser">
            <FieldGroup label="New username" input-id="admin-username">
              <input id="admin-username" v-model="user.username" required />
            </FieldGroup>
            <FieldGroup label="Initial password" input-id="admin-password">
              <input
                id="admin-password"
                v-model="user.initialPassword"
                type="password"
                minlength="8"
                required
              />
            </FieldGroup>
            <FieldGroup label="Full name" input-id="admin-full-name">
              <input id="admin-full-name" v-model="user.fullName" required />
            </FieldGroup>
            <FieldGroup label="Email" input-id="admin-email">
              <input id="admin-email" v-model="user.email" type="email" />
            </FieldGroup>
            <FieldGroup label="Phone" input-id="admin-phone">
              <input id="admin-phone" v-model="user.phone" />
            </FieldGroup>
            <FieldGroup
              label="Roles"
              input-id="admin-roles"
              hint="Comma-separated role names."
            >
              <input id="admin-roles" v-model="user.roles" required />
            </FieldGroup>
            <FieldGroup
              label="Certification codes"
              input-id="admin-qualifications"
              hint="For example LX-A, LX-I, LX-F, LX-P."
            >
              <input
                id="admin-qualifications"
                v-model="user.qualifications"
              />
            </FieldGroup>
            <FieldGroup
              label="Certification status"
              input-id="admin-certification-active"
            >
              <select
                id="admin-certification-active"
                v-model="user.certificationActive"
              >
                <option value="true">Active</option>
                <option value="false">Inactive</option>
              </select>
            </FieldGroup>
            <FieldGroup
              label="Certification expiry"
              input-id="admin-certification-expiry"
              hint="Optional; applied to the listed certification codes."
            >
              <input
                id="admin-certification-expiry"
                v-model="user.certificationExpiresAt"
                type="datetime-local"
              />
            </FieldGroup>
            <FieldGroup label="User active" input-id="admin-user-active">
              <select id="admin-user-active" v-model="user.active">
                <option value="true">Active</option>
                <option value="false">Inactive</option>
              </select>
            </FieldGroup>
            <button class="primary-button" :disabled="action.busy.value">
              Create user
            </button>
          </form>
        </details>
        <details>
          <summary>Read or update access</summary>
          <form class="search-form" @submit.prevent="readUser">
            <FieldGroup label="User lookup ID" input-id="user-lookup">
              <input id="user-lookup" v-model="userLookup" required />
            </FieldGroup>
            <button class="secondary-button" :disabled="action.busy.value">
              Read user
            </button>
          </form>
          <form class="form-grid form-grid-3 sub-form" @submit.prevent="updateAccess">
            <FieldGroup label="Access user ID" input-id="access-user-id">
              <input
                id="access-user-id"
                v-model="userAccess.userId"
                required
              />
            </FieldGroup>
            <FieldGroup label="Updated roles" input-id="access-roles">
              <input id="access-roles" v-model="userAccess.roles" required />
            </FieldGroup>
            <FieldGroup
              label="Updated certification codes"
              input-id="access-qualifications"
            >
              <input
                id="access-qualifications"
                v-model="userAccess.qualifications"
              />
            </FieldGroup>
            <FieldGroup
              label="Certification status"
              input-id="access-certification-active"
            >
              <select
                id="access-certification-active"
                v-model="userAccess.certificationActive"
              >
                <option value="true">Active</option>
                <option value="false">Inactive</option>
              </select>
            </FieldGroup>
            <FieldGroup
              label="Certification expiry"
              input-id="access-certification-expiry"
              hint="Optional; applies to all listed codes."
            >
              <input
                id="access-certification-expiry"
                v-model="userAccess.certificationExpiresAt"
                type="datetime-local"
              />
            </FieldGroup>
            <FieldGroup label="Access active" input-id="access-active">
              <select id="access-active" v-model="userAccess.active">
                <option value="true">Active</option>
                <option value="false">Inactive</option>
              </select>
            </FieldGroup>
            <button class="primary-button" :disabled="action.busy.value">
              Update access
            </button>
          </form>
        </details>
      </div>
    </PanelCard>

    <PanelCard
      eyebrow="Service network"
      title="Locations & functions"
      description="One location may act as a collection point, transfer point, hub, laboratory, or any combination. Activate or deactivate the site, or a single function, without restart."
    >
      <div class="admin-actions">
        <details open>
          <summary>Create or update location</summary>
          <form
            class="form-grid form-grid-3"
            @submit.prevent="saveLocation(false)"
          >
            <FieldGroup
              label="Configuration location ID"
              input-id="config-location-id"
            >
              <input
                id="config-location-id"
                v-model="location.locationId"
                placeholder="Required only for update or status change"
              />
            </FieldGroup>
            <FieldGroup label="Location name" input-id="config-location-name">
              <input
                id="config-location-name"
                v-model="location.name"
                required
              />
            </FieldGroup>
            <FieldGroup label="Location latitude" input-id="config-location-lat">
              <input
                id="config-location-lat"
                v-model="location.latitude"
                required
              />
            </FieldGroup>
            <FieldGroup
              label="Location longitude"
              input-id="config-location-lng"
            >
              <input
                id="config-location-lng"
                v-model="location.longitude"
                required
              />
            </FieldGroup>
            <FieldGroup label="Coverage radius km" input-id="coverage-km">
              <input
                id="coverage-km"
                v-model="location.coverageKm"
                type="number"
                step="0.001"
                min="0"
                max="5"
                required
              />
            </FieldGroup>
            <FieldGroup label="Handling minutes" input-id="handling-minutes">
              <input
                id="handling-minutes"
                v-model="location.handlingMinutes"
                type="number"
                min="0"
                required
              />
            </FieldGroup>
            <FieldGroup
              label="Location active"
              input-id="config-location-active"
            >
              <select id="config-location-active" v-model="location.active">
                <option value="true">Active</option>
                <option value="false">Inactive</option>
              </select>
            </FieldGroup>
            <fieldset class="function-set">
              <legend>Functions provided</legend>
              <label
                v-for="option in locationFunctionOptions"
                :key="option.value"
                class="function-option"
                :for="`location-function-${option.value}`"
              >
                <input
                  :id="`location-function-${option.value}`"
                  v-model="location.functions[option.value]"
                  type="checkbox"
                  :name="`function-${option.value}`"
                />
                <span>{{ option.label }}</span>
              </label>
            </fieldset>
            <div class="button-row">
              <button class="primary-button" :disabled="action.busy.value">
                Create location
              </button>
              <button
                type="button"
                class="secondary-button"
                :disabled="action.busy.value || !location.locationId"
                @click="saveLocation(true)"
              >
                Update location
              </button>
              <button
                type="button"
                class="secondary-button"
                :disabled="action.busy.value || !location.locationId"
                @click="setLocationStatus"
              >
                Set location status
              </button>
            </div>
          </form>
        </details>
        <details>
          <summary>Configured locations</summary>
          <div class="location-inventory" data-testid="location-inventory">
            <table>
              <caption class="sr-only">
                Configured network locations and the functions they provide
              </caption>
              <thead>
                <tr>
                  <th scope="col">Location</th>
                  <th scope="col">ID</th>
                  <th scope="col">Site</th>
                  <th scope="col">Functions</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="item in store.operations.locations"
                  :key="locationId(item)"
                >
                  <td>{{ item.name || "Unnamed location" }}</td>
                  <td>{{ displayId(locationId(item)) }}</td>
                  <td>{{ item.active ? "Active" : "Inactive" }}</td>
                  <td>
                    <span
                      v-for="name in providedFunctions(item)"
                      :key="name"
                      class="function-chip"
                      :class="{ 'is-off': !serves(item, name) }"
                    >
                      {{ LOCATION_FUNCTION_LABELS[name] }}{{
                        serves(item, name) ? "" : " (off)"
                      }}
                    </span>
                    <span v-if="providedFunctions(item).length === 0">
                      No functions
                    </span>
                  </td>
                </tr>
                <tr v-if="store.operations.locations.length === 0">
                  <td colspan="4">No locations configured yet.</td>
                </tr>
              </tbody>
            </table>
          </div>
        </details>
        <details>
          <summary>Single function availability</summary>
          <form
            class="form-grid form-grid-3"
            @submit.prevent="setLocationFunctionStatus"
          >
            <FieldGroup label="Function location ID" input-id="function-location-id">
              <input
                id="function-location-id"
                v-model="locationFunction.locationId"
                required
              />
            </FieldGroup>
            <FieldGroup label="Function" input-id="function-name">
              <select id="function-name" v-model="locationFunction.function">
                <option
                  v-for="option in locationFunctionOptions"
                  :key="option.value"
                  :value="option.value"
                >
                  {{ option.label }}
                </option>
              </select>
            </FieldGroup>
            <FieldGroup label="Function active" input-id="function-active">
              <select id="function-active" v-model="locationFunction.active">
                <option value="true">Active</option>
                <option value="false">Inactive</option>
              </select>
            </FieldGroup>
            <button class="primary-button" :disabled="action.busy.value">
              Set function status
            </button>
          </form>
        </details>
        <details>
          <summary>Laboratory acceptance</summary>
          <form
            class="form-grid form-grid-3"
            @submit.prevent="configureAcceptance"
          >
            <FieldGroup
              label="Capability laboratory ID"
              input-id="capability-lab"
            >
              <input
                id="capability-lab"
                v-model="acceptance.laboratoryId"
                required
              />
            </FieldGroup>
            <FieldGroup label="Accepted sample type ID" input-id="accepted-type">
              <input
                id="accepted-type"
                v-model="acceptance.sampleTypeId"
                required
              />
            </FieldGroup>
            <FieldGroup label="Accepts sample type" input-id="accepts-type">
              <select id="accepts-type" v-model="acceptance.accepts">
                <option value="true">Accepts</option>
                <option value="false">Does not accept</option>
              </select>
            </FieldGroup>
            <button class="primary-button" :disabled="action.busy.value">
              Save laboratory capability
            </button>
          </form>
        </details>
      </div>
    </PanelCard>

    <PanelCard
      eyebrow="Clinical rules"
      title="Sample types & handling"
      description="Configure professional certification, containers, temperature bounds, and incompatible batches."
    >
      <form class="form-grid form-grid-3" @submit.prevent="saveSampleType(false)">
        <FieldGroup label="Existing sample type ID" input-id="rule-type-id">
          <input
            id="rule-type-id"
            v-model="sampleType.sampleTypeId"
            placeholder="Required only for update"
          />
        </FieldGroup>
        <FieldGroup label="Sample type name" input-id="rule-name">
          <input id="rule-name" v-model="sampleType.name" required />
        </FieldGroup>
        <FieldGroup
          label="Qualified collector required"
          input-id="rule-qualified"
        >
          <select
            id="rule-qualified"
            v-model="sampleType.qualifiedCollectorRequired"
          >
            <option value="true">Required</option>
            <option value="false">Not required</option>
          </select>
        </FieldGroup>
        <FieldGroup
          label="Required certification"
          input-id="rule-qualification"
        >
          <input
            id="rule-qualification"
            v-model="sampleType.requiredQualification"
          />
        </FieldGroup>
        <FieldGroup label="Required container" input-id="rule-container">
          <input
            id="rule-container"
            v-model="sampleType.containerType"
            required
          />
        </FieldGroup>
        <FieldGroup label="Minimum temperature °C" input-id="rule-temp-min">
          <input
            id="rule-temp-min"
            v-model="sampleType.temperatureMinC"
            type="number"
            step="0.1"
            required
          />
        </FieldGroup>
        <FieldGroup label="Maximum temperature °C" input-id="rule-temp-max">
          <input
            id="rule-temp-max"
            v-model="sampleType.temperatureMaxC"
            type="number"
            step="0.1"
            required
          />
        </FieldGroup>
        <FieldGroup
          label="Incompatible type IDs"
          input-id="rule-incompatible"
        >
          <input
            id="rule-incompatible"
            v-model="sampleType.incompatibleSampleTypes"
          />
        </FieldGroup>
        <div class="button-row">
          <button class="primary-button" :disabled="action.busy.value">
            Create sample type
          </button>
          <button
            type="button"
            class="secondary-button"
            :disabled="action.busy.value || !sampleType.sampleTypeId"
            @click="saveSampleType(true)"
          >
            Update handling rule
          </button>
        </div>
      </form>
    </PanelCard>

    <PanelCard
      eyebrow="Routes and resources"
      title="Connections, capacity & workers"
      description="Configure travel cost/time, cooled links, regular/cooled capacity, and worker availability."
    >
      <div class="admin-actions">
        <details open>
          <summary>Network connection</summary>
          <form class="form-grid form-grid-3" @submit.prevent="createConnection">
            <FieldGroup
              label="Connection ID for status"
              input-id="connection-id"
            >
              <input
                id="connection-id"
                v-model="connection.connectionId"
              />
            </FieldGroup>
            <FieldGroup label="From location ID" input-id="connection-from">
              <input
                id="connection-from"
                v-model="connection.fromLocationId"
                required
              />
            </FieldGroup>
            <FieldGroup label="To location ID" input-id="connection-to">
              <input
                id="connection-to"
                v-model="connection.toLocationId"
                required
              />
            </FieldGroup>
            <FieldGroup label="Travel minutes" input-id="connection-minutes">
              <input
                id="connection-minutes"
                v-model="connection.travelMinutes"
                type="number"
                min="1"
                required
              />
            </FieldGroup>
            <FieldGroup label="Cost units" input-id="connection-cost">
              <input
                id="connection-cost"
                v-model="connection.costUnits"
                type="number"
                step="0.1"
                min="0"
                required
              />
            </FieldGroup>
            <FieldGroup label="Cooled connection" input-id="connection-cooled">
              <select id="connection-cooled" v-model="connection.cooled">
                <option value="false">Standard</option>
                <option value="true">Cooled</option>
              </select>
            </FieldGroup>
            <FieldGroup label="Connection active" input-id="connection-active">
              <select id="connection-active" v-model="connection.active">
                <option value="true">Active</option>
                <option value="false">Inactive</option>
              </select>
            </FieldGroup>
            <div class="button-row">
              <button class="primary-button" :disabled="action.busy.value">
                Create connection
              </button>
              <button
                type="button"
                class="secondary-button"
                :disabled="action.busy.value || !connection.connectionId"
                @click="setConnectionStatus"
              >
                Set connection status
              </button>
            </div>
          </form>
        </details>
        <details>
          <summary>Regular and cooled capacity</summary>
          <form class="form-grid form-grid-3" @submit.prevent="configureCapacity">
            <FieldGroup label="Capacity resource ID" input-id="capacity-resource">
              <input
                id="capacity-resource"
                v-model="capacity.resourceId"
                required
              />
            </FieldGroup>
            <FieldGroup label="Regular capacity units" input-id="regular-capacity">
              <input
                id="regular-capacity"
                v-model="capacity.capacityUnits"
                type="number"
                min="0"
                required
              />
            </FieldGroup>
            <FieldGroup label="Cooled capacity units" input-id="cooled-capacity">
              <input
                id="cooled-capacity"
                v-model="capacity.cooledCapacityUnits"
                type="number"
                min="0"
                required
              />
            </FieldGroup>
            <button class="primary-button" :disabled="action.busy.value">
              Configure capacity
            </button>
          </form>
        </details>
        <details>
          <summary>Worker availability</summary>
          <form
            class="form-grid form-grid-3"
            @submit.prevent="setWorkerAvailability"
          >
            <FieldGroup label="Worker ID" input-id="availability-worker">
              <input
                id="availability-worker"
                v-model="worker.workerId"
                required
              />
            </FieldGroup>
            <FieldGroup
              label="Worker available"
              input-id="availability-active"
            >
              <select id="availability-active" v-model="worker.available">
                <option value="true">Available</option>
                <option value="false">Unavailable</option>
              </select>
            </FieldGroup>
            <FieldGroup
              label="Worker capacity units"
              input-id="worker-capacity"
            >
              <input
                id="worker-capacity"
                v-model="worker.capacityUnits"
                type="number"
                min="0"
                required
              />
            </FieldGroup>
            <button class="primary-button" :disabled="action.busy.value">
              Save worker availability
            </button>
          </form>
        </details>
      </div>
    </PanelCard>

    <InlineResult
      v-if="action.error.value || result"
      :kind="action.error.value ? 'error' : 'success'"
      test-id="admin-result"
    >
      {{
        action.error.value ||
        `${resultLabel}: ${JSON.stringify(result)}`
      }}
    </InlineResult>
  </div>
</template>
