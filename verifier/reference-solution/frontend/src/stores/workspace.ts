import { computed, onScopeDispose, ref } from "vue";
import { defineStore } from "pinia";
import {
  authenticate,
  bootstrap,
  errorMessage,
  normalizeOperations,
  normalizeWork,
  readSession,
  request,
  routes,
  selectActiveRole,
  writeSession,
} from "../lib/api";
import { RealtimeClient } from "../lib/realtime";
import type {
  ConnectionState,
  OperationsState,
  RealtimeEvent,
  Role,
  RoleWork,
  SampleRecord,
  Session,
  BootstrapPayload,
} from "../types";
import {
  EMPTY_OPERATIONS,
  EMPTY_ROLE_WORK,
  WORK_ROLES,
} from "../types";

/**
 * The active role is server state, held on the session. The client only mirrors
 * what the server reports, so a role switch is a request and never a local
 * preference.
 */
function serverRole(session: Session | null): Role | null {
  return session?.activeRole ?? null;
}

export const useWorkspaceStore = defineStore("workspace", () => {
  const session = ref<Session | null>(readSession());
  const activeRole = ref<Role | null>(serverRole(session.value));
  const operations = ref<OperationsState>({ ...EMPTY_OPERATIONS });
  const roleWork = ref<RoleWork>({ ...EMPTY_ROLE_WORK });
  const requesterSamples = ref<SampleRecord[]>([]);
  const loading = ref(Boolean(session.value));
  const error = ref("");
  const notice = ref("");
  const connection = ref<ConnectionState>("connecting");
  const lastEvent = ref<RealtimeEvent | null>(null);
  let realtime: RealtimeClient | null = null;
  let noticeTimer: number | null = null;
  let refreshTimer: number | null = null;

  const isAuthenticated = computed(() => Boolean(session.value?.token));
  const roles = computed(() => session.value?.roles ?? []);
  const canOperate = computed(() =>
    activeRole.value === "dispatcher" ||
    activeRole.value === "administrator",
  );
  /**
   * A multi-role account must choose the role it is working under before the
   * server will permit any role-scoped action.
   */
  const roleSelectionRequired = computed(
    () => Boolean(session.value) && !activeRole.value && roles.value.length > 1,
  );

  function showNotice(message: string): void {
    notice.value = message;
    if (noticeTimer !== null) window.clearTimeout(noticeTimer);
    noticeTimer = window.setTimeout(() => {
      notice.value = "";
      noticeTimer = null;
    }, 4_500);
  }

  async function refreshOperations(): Promise<void> {
    if (!session.value || !canOperate.value) {
      operations.value = { ...EMPTY_OPERATIONS };
      return;
    }
    operations.value = normalizeOperations(
      await request<OperationsState>(
        routes.readOperationsState,
        {
          method: "POST",
          body: JSON.stringify({ filters: {} }),
        },
        session.value,
      ),
    );
  }

  async function refreshWork(): Promise<void> {
    if (
      !session.value ||
      !activeRole.value ||
      !WORK_ROLES.includes(activeRole.value)
    ) {
      roleWork.value = { ...EMPTY_ROLE_WORK };
      return;
    }
    roleWork.value = normalizeWork(
      await request<RoleWork>(
        routes.listRoleWork,
        {
          method: "POST",
          body: JSON.stringify({ role: activeRole.value, status: "all" }),
        },
        session.value,
      ),
    );
  }

  async function refreshRequesterSamples(): Promise<void> {
    if (
      !session.value ||
      activeRole.value !== "requester" ||
      !session.value.roles.includes("requester")
    ) {
      requesterSamples.value = [];
      return;
    }
    const payload = await request<BootstrapPayload>(
      routes.bootstrap,
      {},
      session.value,
    );
    requesterSamples.value = payload.requesterSamples ?? [];
  }

  async function refresh(): Promise<void> {
    const results = await Promise.allSettled([
      refreshOperations(),
      refreshWork(),
      refreshRequesterSamples(),
    ]);
    const rejected = results.find(
      (result): result is PromiseRejectedResult =>
        result.status === "rejected",
    );
    if (rejected) throw rejected.reason;
  }

  function scheduleRefresh(): void {
    if (refreshTimer !== null) return;
    refreshTimer = window.setTimeout(() => {
      refreshTimer = null;
      void refresh().catch(() => undefined);
    }, 120);
  }

  function startRealtime(): void {
    realtime?.stop();
    if (!session.value) return;
    realtime = new RealtimeClient({
      session: session.value,
      onEvent: (event) => {
        lastEvent.value = event;
        scheduleRefresh();
      },
      onPoll: refresh,
      onState: (state) => {
        connection.value = state;
      },
    });
    realtime.start();
  }

  async function initialize(): Promise<void> {
    if (!session.value) {
      loading.value = false;
      return;
    }
    loading.value = true;
    try {
      const payload = await bootstrap(session.value);
      session.value = payload.session;
      activeRole.value = serverRole(payload.session);
      operations.value = payload.operations;
      roleWork.value = payload.roleWork;
      requesterSamples.value =
        activeRole.value === "requester" ? payload.requesterSamples : [];
      await refresh();
      error.value = "";
      startRealtime();
    } catch (cause) {
      error.value = errorMessage(cause);
    } finally {
      loading.value = false;
    }
  }

  async function login(username: string, password: string): Promise<void> {
    const authenticated = await authenticate(username, password);
    session.value = authenticated;
    activeRole.value = serverRole(authenticated);
    await initialize();
  }

  function logout(): void {
    realtime?.stop();
    realtime = null;
    writeSession(null);
    session.value = null;
    activeRole.value = null;
    operations.value = { ...EMPTY_OPERATIONS };
    roleWork.value = { ...EMPTY_ROLE_WORK };
    requesterSamples.value = [];
    connection.value = "offline";
  }

  /**
   * Switches the role this session works under by asking the server to record
   * the selection. The client adopts only the role the server confirms, so
   * navigation and permissions can never disagree with what the API will allow.
   */
  async function selectRole(role: Role): Promise<void> {
    if (!session.value || activeRole.value === role) return;
    loading.value = true;
    try {
      const updated = await selectActiveRole(role, session.value);
      session.value = updated;
      activeRole.value = serverRole(updated);
      await refresh();
      error.value = "";
    } catch (cause) {
      error.value = errorMessage(cause);
    } finally {
      loading.value = false;
    }
  }

  window.addEventListener("labflow:unauthorized", logout);
  onScopeDispose(() => {
    realtime?.stop();
    window.removeEventListener("labflow:unauthorized", logout);
  });

  return {
    session,
    activeRole,
    operations,
    roleWork,
    requesterSamples,
    loading,
    error,
    notice,
    connection,
    lastEvent,
    isAuthenticated,
    roles,
    canOperate,
    roleSelectionRequired,
    initialize,
    login,
    logout,
    selectRole,
    refresh,
    showNotice,
  };
});
