<script setup lang="ts">
import { reactive, ref } from "vue";
import { request, routes, toIso } from "../lib/api";
import { useAction } from "../lib/action";
import { auditRoleLabel, displayId, formatTime, titleCase } from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { AuditEntry } from "../types";
import { ROLE_LABELS, ROLE_ORDER } from "../types";
import EmptyState from "./EmptyState.vue";
import FieldGroup from "./FieldGroup.vue";
import PanelCard from "./PanelCard.vue";

const store = useWorkspaceStore();
const action = useAction();
const entries = ref<AuditEntry[]>([]);
const searched = ref(false);
const filters = reactive({
  query: "",
  actorId: "",
  entityType: "",
  entityId: "",
  action: "",
  role: "",
  fromTime: "",
  toTime: "",
});

/** The role an actor was working under, as recorded on the audit event. */
function entryRole(entry: AuditEntry): string {
  return String(entry.role ?? entry.actorRole ?? "");
}

/** A rejected or failed action is recorded with a false outcome. */
function entryFailed(entry: AuditEntry): boolean {
  return entry.success === false;
}

async function submit(): Promise<void> {
  const next = await action.run(() =>
    request<{ entries?: AuditEntry[] }>(
      routes.searchAuditTrail,
      {
        method: "POST",
        body: JSON.stringify({
          query: filters.query || null,
          actorId: filters.actorId || null,
          entityType: filters.entityType || null,
          entityId: filters.entityId || null,
          action: filters.action || null,
          role: filters.role || null,
          fromTime: filters.fromTime ? toIso(filters.fromTime) : null,
          toTime: filters.toTime ? toIso(filters.toTime) : null,
        }),
      },
      store.session,
    ),
  );
  if (!next) return;
  entries.value = next.entries ?? [];
  searched.value = true;
}
</script>

<template>
  <PanelCard
    eyebrow="Accountability"
    title="Audit trail"
    description="Search immutable operational actions, outcomes, actors, and planning decisions."
    class-name="audit-panel"
  >
    <form class="audit-search audit-search-expanded" @submit.prevent="submit">
      <FieldGroup label="Audit query" input-id="audit-query">
        <input
          id="audit-query"
          v-model="filters.query"
          name="query"
          type="search"
          placeholder="Action, decision, or identifier"
        />
      </FieldGroup>
      <FieldGroup label="Audit entity" input-id="audit-entity">
        <input
          id="audit-entity"
          v-model="filters.entityId"
          placeholder="Entity ID"
        />
      </FieldGroup>
      <FieldGroup label="Entity type" input-id="audit-entity-type">
        <select id="audit-entity-type" v-model="filters.entityType">
          <option value="">All entity types</option>
          <option value="sample">Sample</option>
          <option value="journey">Journey</option>
          <option value="handoff">Handoff</option>
          <option value="assignment">Assignment</option>
          <option value="node">Node</option>
          <option value="user">User</option>
        </select>
      </FieldGroup>
      <FieldGroup label="Actor ID" input-id="audit-actor">
        <input id="audit-actor" v-model="filters.actorId" />
      </FieldGroup>
      <FieldGroup label="Action" input-id="audit-action">
        <input id="audit-action" v-model="filters.action" />
      </FieldGroup>
      <FieldGroup label="Actor role" input-id="audit-role">
        <select id="audit-role" v-model="filters.role">
          <option value="">All roles</option>
          <option
            v-for="role in ROLE_ORDER"
            :key="role"
            :value="role"
          >
            {{ ROLE_LABELS[role] }}
          </option>
          <option value="unselected">No role selected</option>
          <option value="system">System timers</option>
        </select>
      </FieldGroup>
      <FieldGroup label="From time" input-id="audit-from">
        <input id="audit-from" v-model="filters.fromTime" type="datetime-local" />
      </FieldGroup>
      <FieldGroup label="To time" input-id="audit-to">
        <input id="audit-to" v-model="filters.toTime" type="datetime-local" />
      </FieldGroup>
      <button
        id="search-audit"
        class="primary-button"
        :disabled="action.busy.value"
      >
        Search audit
      </button>
    </form>
    <output
      class="audit-machine-output"
      data-testid="audit-results"
      aria-live="polite"
    >
      {{ action.error.value || (searched ? JSON.stringify(entries) : "") }}
    </output>
    <ol v-if="entries.length" class="audit-list">
      <li
        v-for="(entry, index) in entries"
        :key="String(entry.auditId ?? entry.id ?? index)"
      >
        <span class="audit-mark" aria-hidden="true">
          {{ String(entry.action ?? "event").slice(0, 2).toUpperCase() }}
        </span>
        <div>
          <strong>{{ titleCase(entry.action ?? "Operational event") }}</strong>
          <span>
            {{ entry.actorName ?? displayId(entry.actorId) }}
            <template v-if="entryRole(entry)">
              (as {{ auditRoleLabel(entryRole(entry)) }})
            </template>
            ·
            {{ titleCase(entry.entityType) }}
            {{ displayId(entry.entityId) }}
          </span>
          <p v-if="entry.decision">{{ entry.decision }}</p>
        </div>
        <span
          :class="[
            'stale-badge',
            entryFailed(entry) ? 'is-stale' : 'is-current',
          ]"
          data-testid="audit-outcome"
        >
          {{ entryFailed(entry) ? "Refused" : "Applied" }}
        </span>
        <time>{{ formatTime(entry.occurredAt) }}</time>
      </li>
    </ol>
    <EmptyState
      v-else-if="searched && !action.error.value"
      title="No matching audit events"
      description="Try removing an identifier or broadening the search terms."
    />
  </PanelCard>
</template>
