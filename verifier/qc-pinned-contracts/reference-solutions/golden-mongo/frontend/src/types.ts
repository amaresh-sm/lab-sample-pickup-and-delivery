export type Role =
  | "requester"
  | "collection_professional"
  | "delivery_partner"
  | "node_handler"
  | "laboratory_team"
  | "dispatcher"
  | "administrator";

export type ConnectionState = "connecting" | "live" | "polling" | "offline";

export interface Coordinate {
  latitude: number;
  longitude: number;
}

export interface UserIdentity {
  userId: string;
  username: string;
  fullName: string;
  roles: Role[];
}

/**
 * One signed-in session. `activeRole` is the role the SERVER says this session
 * is working under; it is never chosen locally. A user holding several roles has
 * `activeRole: null` until they select one, and until then every role-scoped
 * request is refused with `role_not_selected`.
 */
export interface Session extends UserIdentity {
  token: string;
  activeRole: Role | null;
}

export const ROLE_LABELS: Record<Role, string> = {
  requester: "Requester",
  collection_professional: "Collection professional",
  delivery_partner: "Delivery partner",
  node_handler: "Node handler",
  laboratory_team: "Laboratory team",
  dispatcher: "Dispatcher",
  administrator: "Administrator",
};

/** Every role, in the order they appear in a role picker. */
export const ROLE_ORDER: Role[] = [
  "requester",
  "collection_professional",
  "delivery_partner",
  "node_handler",
  "laboratory_team",
  "dispatcher",
  "administrator",
];

export const WORK_ROLES: Role[] = [
  "collection_professional",
  "delivery_partner",
  "node_handler",
  "laboratory_team",
];

export interface EntityRecord {
  id?: string;
  [key: string]: unknown;
}

export interface SampleRecord extends EntityRecord {
  sampleId?: string;
  requestId?: string;
  journeyId?: string;
  status?: string;
  serviceability?: string;
  requestedAt?: string;
  deliveryDeadline?: string;
  sampleTypeName?: string;
  currentHolderId?: string;
  origin?: Coordinate;
  coordinate?: Coordinate;
  latestLocation?: Coordinate;
  locationRecordedAt?: string;
  locationStale?: boolean;
  activeJourneyId?: string;
}

export interface WorkerRecord extends EntityRecord {
  workerId?: string;
  fullName?: string;
  role?: string;
  status?: string;
  available?: boolean;
  assignmentCount?: number;
  coordinate?: Coordinate;
  latestLocation?: Coordinate;
  locationRecordedAt?: string;
  locationStale?: boolean;
}

export interface RouteRecord extends EntityRecord {
  journeyId?: string;
  routeMode?: string;
  status?: string;
  orderedStops?: Array<EntityRecord>;
  coordinates?: Coordinate[];
  planVersion?: number;
}

/**
 * A location may provide any combination of these functions. Each one can be
 * activated or deactivated independently of the location itself.
 */
export type LocationFunction =
  | "collection_point"
  | "transfer_point"
  | "hub"
  | "laboratory";

export const LOCATION_FUNCTIONS: LocationFunction[] = [
  "collection_point",
  "transfer_point",
  "hub",
  "laboratory",
];

export const LOCATION_FUNCTION_LABELS: Record<LocationFunction, string> = {
  collection_point: "Collection point",
  transfer_point: "Transfer point",
  hub: "Hub",
  laboratory: "Laboratory",
};

/**
 * One network location. `functions` maps a function name to whether that
 * function is currently active; a key's presence means the location provides
 * the function at all.
 */
export interface LocationRecord extends EntityRecord {
  locationId?: string;
  name?: string;
  active?: boolean;
  coordinate?: Coordinate;
  coverageKm?: number;
  handlingMinutes?: number;
  functions?: Partial<Record<LocationFunction, boolean>>;
  acceptedSampleTypeIds?: string[];
  capacity?: EntityRecord;
}

/**
 * One active collection point and the effective coverage the published
 * serviceability calculation used for it.
 */
export interface ServiceRegion {
  locationId?: string;
  name?: string;
  latitude?: number;
  longitude?: number;
  coverageKm?: number;
}

/**
 * The published, complete serviceability calculation. A calculation being
 * computed is never served, so `status` is always `complete` here.
 */
export interface ServiceabilitySnapshot {
  version?: number;
  status?: string;
  computedAt?: string;
  networkRevision?: number;
  regions?: ServiceRegion[];
  activeCollectionPointCount?: number;
}

/** The dry-run serviceability answer. Reading it creates nothing. */
export interface ServiceabilityVerdict {
  serviceable?: boolean;
  reason?: string;
  targetLaboratoryId?: string;
  estimatedArrivalAt?: string;
  serviceabilityVersion?: number;
}

export const SERVICEABILITY_REASON_LABELS: Record<string, string> = {
  serviceable: "Serviceable",
  outside_service_coverage: "Origin is beyond 5 km of an active collection point",
  no_accepting_laboratory: "No active laboratory accepts this sample type",
  no_route: "No valid route reaches an accepting laboratory",
  insufficient_capacity: "Required regular or cooled capacity is unavailable",
  deadline_unreachable: "The sample cannot reach a laboratory before the deadline",
  handling_rules_unsatisfiable:
    "Handling and container rules cannot be satisfied by this network",
};

export interface HandoffRecord extends EntityRecord {
  handoffId?: string;
  sampleId?: string;
  senderId?: string;
  receiverId?: string;
  currentHolderId?: string;
  status?: string;
  senderConfirmed?: boolean;
  receiverConfirmed?: boolean;
}

export interface OperationsState {
  samples: SampleRecord[];
  workers: WorkerRecord[];
  locations: LocationRecord[];
  routes: RouteRecord[];
  handoffs: HandoffRecord[];
  delays: EntityRecord[];
  exceptions: EntityRecord[];
  assignmentCountSpread: number;
  ineligibleAssignmentCount: number;
  eventId?: string;
}

export interface RoleWork {
  pending: EntityRecord[];
  inProgress: EntityRecord[];
  completed: EntityRecord[];
}

export interface AuditEntry extends EntityRecord {
  auditId?: string;
  action?: string;
  actorId?: string;
  actorName?: string;
  /** The role the actor was working under when they took the action. */
  role?: string;
  actorRole?: string;
  entityType?: string;
  entityId?: string;
  occurredAt?: string;
  decision?: string;
  /** False for a refused or failed action. */
  success?: boolean;
}

export interface BootstrapPayload {
  user?: Partial<UserIdentity> & {
    activeRole?: string | null;
    roleSelectionRequired?: boolean;
  };
  operations?: Partial<OperationsState>;
  roleWork?: Partial<RoleWork>;
  requesterSamples?: SampleRecord[];
}

/** The server's answer to a role selection. */
export interface ActiveRoleSelection {
  activeRole?: string;
  roles?: string[];
}

export interface RealtimeEvent {
  id?: string | number;
  eventId?: string | number;
  type?: string;
  entityType?: string;
  entityId?: string;
  occurredAt?: string;
  createdAt?: string;
  payload?: EntityRecord;
  data?: unknown;
}

export interface MutationResult {
  [key: string]: unknown;
}

export const EMPTY_OPERATIONS: OperationsState = {
  samples: [],
  workers: [],
  locations: [],
  routes: [],
  handoffs: [],
  delays: [],
  exceptions: [],
  assignmentCountSpread: 0,
  ineligibleAssignmentCount: 0,
};

export const EMPTY_ROLE_WORK: RoleWork = {
  pending: [],
  inProgress: [],
  completed: [],
};
