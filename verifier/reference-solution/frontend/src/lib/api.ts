import type {
  ActiveRoleSelection,
  BootstrapPayload,
  OperationsState,
  Role,
  RoleWork,
  SampleRecord,
  Session,
} from "../types";
import {
  EMPTY_OPERATIONS,
  EMPTY_ROLE_WORK,
} from "../types";

const SESSION_KEY = "labflow.session.v1";

export const routes = {
  authenticateUser: "/api/authenticate-user",
  selectActiveRole: "/api/select-active-role",
  createUser: "/api/create-user",
  readUser: (userId: string) =>
    `/api/read-user/${encodeURIComponent(userId)}`,
  updateUserAccess: "/api/update-user-access",
  createLocation: "/api/create-location",
  updateLocation: "/api/update-location",
  setLocationStatus: "/api/set-location-status",
  setLocationFunctionStatus: "/api/set-location-function-status",
  createSampleType: "/api/create-sample-type",
  updateSampleType: "/api/update-sample-type",
  configureLaboratoryAcceptance: "/api/configure-laboratory-acceptance",
  createConnection: "/api/create-connection",
  setConnectionStatus: "/api/set-connection-status",
  configureCapacity: "/api/configure-capacity",
  setWorkerAvailability: "/api/set-worker-availability",
  bootstrap: "/api/ui/bootstrap",
  checkServiceability: "/api/check-serviceability",
  readServiceabilitySnapshot: "/api/read-serviceability-snapshot",
  createCollectionRequest: "/api/create-collection-request",
  ingestPresealedSample: "/api/ingest-presealed-sample",
  readSampleState: (sampleId: string) =>
    `/api/read-sample-state/${encodeURIComponent(sampleId)}`,
  readSampleLifecycle: "/api/read-sample-lifecycle",
  listRoleWork: "/api/list-role-work",
  readOperationsState: "/api/read-operations-state",
  readJourneyPlan: "/api/read-journey-plan",
  editActivePlan: "/api/edit-active-plan",
  splitJourney: "/api/split-journey",
  mergeJourneys: "/api/merge-journeys",
  cancelJourney: "/api/cancel-journey",
  rerouteJourney: "/api/reroute-journey",
  acceptAssignment: "/api/accept-assignment",
  declineAssignment: "/api/decline-assignment",
  collectAndSealSample: "/api/collect-and-seal-sample",
  updateAssignmentProgress: "/api/update-assignment-progress",
  initiateHandoff: "/api/initiate-handoff",
  confirmHandoff: "/api/confirm-handoff",
  recordHandlingCheckpoint: "/api/record-handling-checkpoint",
  confirmLaboratoryReceipt: "/api/confirm-laboratory-receipt",
  recordLaboratoryException: "/api/record-laboratory-exception",
  recordWorkerLocation: "/api/record-worker-location",
  reportDisruption: "/api/report-disruption",
  searchAuditTrail: "/api/search-audit-trail",
} as const;

const roleAliases: Record<string, Role> = {
  admin: "administrator",
  administrator: "administrator",
  requester: "requester",
  collector: "collection_professional",
  collection_professional: "collection_professional",
  delivery: "delivery_partner",
  delivery_partner: "delivery_partner",
  node: "node_handler",
  node_handler: "node_handler",
  lab: "laboratory_team",
  laboratory_team: "laboratory_team",
  dispatcher: "dispatcher",
};

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly details?: unknown,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** Returns a collision-resistant key for an exactly-once logical UI action. */
export function idempotencyKey(prefix = "ui"): string {
  const random =
    typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `${prefix}-${random}`;
}

/** Converts a datetime-local value to the UTC timestamp expected by the API. */
export function toIso(value: string): string {
  if (!value) return new Date().toISOString();
  const parsed = new Date(value);
  return Number.isNaN(parsed.valueOf()) ? value : parsed.toISOString();
}

/** Reads the bearer session from this browser tab only. */
export function readSession(): Session | null {
  try {
    const raw = window.sessionStorage.getItem(SESSION_KEY);
    if (!raw) return null;
    const stored = JSON.parse(raw) as Session;
    // A stored session is only a cache. The active role is re-confirmed by the
    // server on the next bootstrap, so an absent value is simply "unselected".
    return { ...stored, activeRole: normalizeRole(stored.activeRole) };
  } catch {
    return null;
  }
}

/** Replaces or clears the current tab's bearer session. */
export function writeSession(session: Session | null): void {
  if (session) {
    window.sessionStorage.setItem(SESSION_KEY, JSON.stringify(session));
  } else {
    window.sessionStorage.removeItem(SESSION_KEY);
  }
}

/** Normalizes role spelling used by legacy and canonical API implementations. */
export function normalizeRoles(value: unknown): Role[] {
  if (!Array.isArray(value)) return [];
  const roles = value
    .map((role) => roleAliases[String(role).toLowerCase()])
    .filter((role): role is Role => Boolean(role));
  return Array.from(new Set(roles));
}

/**
 * Normalizes the server's active role. An absent or unrecognized value means
 * "no role selected yet", which is a real state for a multi-role account.
 */
export function normalizeRole(value: unknown): Role | null {
  if (value === null || value === undefined || value === "") return null;
  return roleAliases[String(value).toLowerCase()] ?? null;
}

/** Asks the server to make `role` the active role for this session. */
export async function selectActiveRole(
  role: Role,
  session: Session,
): Promise<Session> {
  const payload = await request<ActiveRoleSelection>(
    routes.selectActiveRole,
    {
      method: "POST",
      body: JSON.stringify({ role }),
    },
    session,
  );
  const updated: Session = {
    ...session,
    activeRole: normalizeRole(payload.activeRole) ?? session.activeRole,
    roles:
      normalizeRoles(payload.roles).length > 0
        ? normalizeRoles(payload.roles)
        : session.roles,
  };
  writeSession(updated);
  return updated;
}

function findErrorMessage(payload: unknown, status: number): string {
  if (payload && typeof payload === "object") {
    const record = payload as Record<string, unknown>;
    const candidate =
      record.message ?? record.detail ?? record.error ?? record.title;
    if (typeof candidate === "string" && candidate.trim()) return candidate;
  }
  return `Request failed (${status})`;
}

/** Sends an authenticated same-origin API request and unwraps a `{data}` result. */
export async function request<T>(
  path: string,
  init: RequestInit = {},
  session: Session | null = readSession(),
): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (session?.token) headers.set("Authorization", `Bearer ${session.token}`);

  const response = await fetch(path, {
    ...init,
    headers,
    credentials: "same-origin",
  });
  const isJson = response.headers
    .get("content-type")
    ?.toLowerCase()
    .includes("application/json");
  const payload: unknown =
    response.status === 204
      ? undefined
      : isJson
        ? await response.json()
        : await response.text();

  if (!response.ok) {
    if (response.status === 401 && path !== routes.authenticateUser) {
      window.dispatchEvent(new CustomEvent("labflow:unauthorized"));
    }
    throw new ApiError(
      findErrorMessage(payload, response.status),
      response.status,
      payload,
    );
  }
  if (payload && typeof payload === "object" && "data" in payload) {
    return (payload as { data: T }).data;
  }
  return payload as T;
}

/** Logs a user in and tolerates both canonical camelCase and legacy snake_case. */
export async function authenticate(
  username: string,
  password: string,
): Promise<Session> {
  const payload = await request<Record<string, unknown>>(
    routes.authenticateUser,
    {
      method: "POST",
      body: JSON.stringify({ username, password }),
    },
    null,
  );
  const token = String(payload.sessionToken ?? payload.session_token ?? "");
  const userId = String(payload.userId ?? payload.user_id ?? "");
  if (!token || !userId) {
    throw new ApiError("The server returned an incomplete session.", 502, payload);
  }
  const session: Session = {
    token,
    userId,
    username: String(payload.username ?? username),
    fullName: String(payload.fullName ?? payload.full_name ?? username),
    roles: normalizeRoles(payload.roles),
    // A single-role account is placed in its only role by the server; a
    // multi-role account has no active role until it selects one.
    activeRole: normalizeRole(payload.activeRole ?? payload.active_role),
  };
  writeSession(session);
  return session;
}

function normalizedOperations(
  value?: Partial<OperationsState>,
): OperationsState {
  return {
    ...EMPTY_OPERATIONS,
    ...value,
    samples: value?.samples ?? [],
    workers: value?.workers ?? [],
    locations: value?.locations ?? [],
    routes: value?.routes ?? [],
    handoffs: value?.handoffs ?? [],
    delays: value?.delays ?? [],
    exceptions: value?.exceptions ?? [],
  };
}

export function normalizeOperations(
  value?: Partial<OperationsState>,
): OperationsState {
  return normalizedOperations(value);
}

function normalizedWork(value?: Partial<RoleWork>): RoleWork {
  const legacy = value as
    | (Partial<RoleWork> & { in_progress?: RoleWork["inProgress"] })
    | undefined;
  return {
    ...EMPTY_ROLE_WORK,
    ...value,
    pending: value?.pending ?? [],
    inProgress: value?.inProgress ?? legacy?.in_progress ?? [],
    completed: value?.completed ?? [],
  };
}

export function normalizeWork(value?: Partial<RoleWork>): RoleWork {
  return normalizedWork(value);
}

/** Loads identity and initial screen data, with standard operations as fallback. */
export async function bootstrap(
  session: Session,
): Promise<{
  session: Session;
  operations: OperationsState;
  roleWork: RoleWork;
  requesterSamples: SampleRecord[];
}> {
  try {
    const payload = await request<BootstrapPayload>(
      routes.bootstrap,
      {},
      session,
    );
    const updated: Session = {
      ...session,
      userId: String(payload.user?.userId ?? session.userId),
      username: String(payload.user?.username ?? session.username),
      fullName: String(
        payload.user?.fullName ?? payload.user?.username ?? session.fullName,
      ),
      roles:
        normalizeRoles(payload.user?.roles).length > 0
          ? normalizeRoles(payload.user?.roles)
          : session.roles,
      // The server is the authority on which role this session is acting under.
      activeRole: normalizeRole(payload.user?.activeRole),
    };
    writeSession(updated);
    return {
      session: updated,
      operations: normalizedOperations(payload.operations),
      roleWork: normalizedWork(payload.roleWork),
      requesterSamples: payload.requesterSamples ?? [],
    };
  } catch (error) {
    if (error instanceof ApiError && ![404, 405].includes(error.status)) {
      throw error;
    }
    const [operations, roleWork] = await Promise.all([
      request<OperationsState>(
        routes.readOperationsState,
        {
          method: "POST",
          body: JSON.stringify({ filters: {} }),
        },
        session,
      ).catch(() => EMPTY_OPERATIONS),
      // Work is only ever listed for the role the session is acting under.
      request<RoleWork>(
        routes.listRoleWork,
        {
          method: "POST",
          body: JSON.stringify({ role: session.activeRole, status: "all" }),
        },
        session,
      ).catch(() => EMPTY_ROLE_WORK),
    ]);
    return {
      session,
      operations: normalizedOperations(operations),
      roleWork: normalizedWork(roleWork),
      requesterSamples: [],
    };
  }
}

/** Formats unknown API errors for an accessible status message. */
export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "Something went wrong.";
}
