<script setup lang="ts">
import { computed } from "vue";
import { RouterLink, useRoute, useRouter } from "vue-router";
import { roleLabel } from "../lib/format";
import { useWorkspaceStore } from "../stores/workspace";
import type { ConnectionState, Role } from "../types";

interface NavigationItem {
  path: string;
  label: string;
  mark: string;
  roles?: Role[];
}

const navigation: NavigationItem[] = [
  { path: "/dashboard", label: "Overview", mark: "OV" },
  {
    path: "/requests",
    label: "Requests",
    mark: "RQ",
    roles: ["requester", "dispatcher", "administrator"],
  },
  {
    path: "/work",
    label: "My work",
    mark: "WK",
    roles: [
      "collection_professional",
      "delivery_partner",
      "node_handler",
      "laboratory_team",
      "administrator",
    ],
  },
  {
    path: "/operations",
    label: "Live operations",
    mark: "OP",
    roles: ["dispatcher", "administrator"],
  },
  {
    path: "/plans",
    label: "Journey plans",
    mark: "PL",
    roles: ["dispatcher", "administrator"],
  },
  {
    path: "/custody",
    label: "Custody",
    mark: "CU",
    roles: [
      "collection_professional",
      "delivery_partner",
      "node_handler",
      "laboratory_team",
      "dispatcher",
      "administrator",
    ],
  },
  {
    path: "/audit",
    label: "Audit trail",
    mark: "AU",
    roles: ["dispatcher", "administrator"],
  },
  {
    path: "/admin",
    label: "Administration",
    mark: "AD",
    roles: ["administrator"],
  },
];

const labels: Record<ConnectionState, string> = {
  connecting: "Connecting",
  live: "Live",
  polling: "Live · polling",
  offline: "Offline",
};

const store = useWorkspaceStore();
const route = useRoute();
const router = useRouter();

// Navigation follows the SELECTED role exactly, with no administrator bypass: an
// administrator working as a requester sees a requester's workspace, which is the
// only workspace the API will serve them.
const available = computed(() =>
  navigation.filter((item) => {
    if (!item.roles) return true;
    return Boolean(
      store.activeRole && item.roles.includes(store.activeRole),
    );
  }),
);
const pageTitle = computed(
  () => String(route.meta.title ?? "Overview"),
);
const initials = computed(() =>
  (store.session?.fullName || store.session?.username || "User")
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase())
    .join(""),
);

// Switching role context asks the server to record the selection; only the role
// the server confirms is adopted here.
async function changeRole(event: Event): Promise<void> {
  const value = (event.target as HTMLSelectElement).value;
  if (!value) return;
  await store.selectRole(value as Role);
  await router.push("/dashboard");
}
</script>

<template>
  <div class="app-shell">
    <a class="skip-link" href="#main-content">Skip to main content</a>
    <aside class="sidebar" aria-label="Primary navigation">
      <div class="brand">
        <span class="brand-symbol" aria-hidden="true">LF</span>
        <div>
          <strong>Labflow</strong>
          <small>Clinical logistics</small>
        </div>
        <button
          class="mobile-sign-out"
          type="button"
          @click="store.logout"
        >
          Sign out
        </button>
      </div>
      <nav>
        <RouterLink
          v-for="item in available"
          :key="item.path"
          :to="item.path"
          :class="{ active: route.path === item.path }"
          :aria-current="route.path === item.path ? 'page' : undefined"
        >
          <span class="nav-mark" aria-hidden="true">{{ item.mark }}</span>
          {{ item.label }}
        </RouterLink>
      </nav>
      <div class="sidebar-footer">
        <div class="identity">
          <span aria-hidden="true">{{ initials || "U" }}</span>
          <div>
            <strong>{{ store.session?.fullName || store.session?.username }}</strong>
            <small>{{ roleLabel(store.activeRole) }}</small>
          </div>
        </div>
        <button class="quiet-button" type="button" @click="store.logout">
          Sign out
        </button>
      </div>
    </aside>

    <div class="workspace">
      <header class="topbar">
        <div>
          <span class="eyebrow">Operations workspace</span>
          <h1>{{ pageTitle }}</h1>
        </div>
        <div class="topbar-status">
          <label v-if="store.roles.length > 1" class="role-switcher">
            <span>Active role</span>
            <select
              data-testid="active-role"
              :value="store.activeRole ?? ''"
              @change="changeRole"
            >
              <option v-if="!store.activeRole" value="">
                Select a role
              </option>
              <option
                v-for="role in store.roles"
                :key="role"
                :value="role"
              >
                {{ roleLabel(role) }}
              </option>
            </select>
          </label>
          <span
            :class="['connection', `connection-${store.connection}`]"
            aria-live="polite"
          >
            <i aria-hidden="true" />
            {{ labels[store.connection] }}
          </span>
          <span data-testid="authenticated-user" class="signed-in">
            Signed in as {{ store.session?.username }}
          </span>
        </div>
      </header>
      <main id="main-content" class="main-content" tabindex="-1">
        <div v-if="store.loading" class="workspace-loading">
          <span class="spinner" role="status">
            <i aria-hidden="true" /> Loading authorized workspace
          </span>
        </div>
        <div
          v-else-if="store.roleSelectionRequired"
          class="global-message message-error"
          data-testid="role-selection-required"
          role="alert"
        >
          <span>
            This account holds more than one role. Choose the role you are
            working under to continue.
          </span>
        </div>
        <slot v-else />

        <div
          v-if="store.error"
          class="global-message message-error"
          role="alert"
        >
          <span>{{ store.error }}</span>
          <button type="button" @click="store.error = ''">Dismiss</button>
        </div>
        <div v-if="store.notice" class="toast" role="status">
          {{ store.notice }}
        </div>
        <div class="visually-hidden" aria-live="polite">
          {{
            store.lastEvent
              ? `Updated: ${store.lastEvent.type ?? "operations event"}`
              : ""
          }}
        </div>
      </main>
    </div>
  </div>
</template>
