package mongo

import (
	"time"

	"labgolden/internal/core"
)

// State is the application's transactional aggregate. The target environment
// runs a standalone MongoDB, where multi-document transactions are unavailable.
// Keeping invariant-related state in one versioned document lets every command
// use a single atomic compare-and-swap while audit, outbox, custody, capacity,
// and idempotency changes commit together.
type State struct {
	ID              string                         `bson:"_id" json:"id"`
	Version         int64                          `bson:"version" json:"version"`
	Users           map[string]*User               `bson:"users" json:"users"`
	Sessions        map[string]*Session            `bson:"sessions" json:"sessions"`
	Locations       map[string]*Location           `bson:"locations" json:"locations"`
	SampleTypes     map[string]*SampleType         `bson:"sampleTypes" json:"sampleTypes"`
	Acceptances     map[string]*Acceptance         `bson:"acceptances" json:"acceptances"`
	Connections     map[string]*Connection         `bson:"connections" json:"connections"`
	Capacities      map[string]*Capacity           `bson:"capacities" json:"capacities"`
	Requests        map[string]*CollectionRequest  `bson:"requests" json:"requests"`
	Samples         map[string]*Sample             `bson:"samples" json:"samples"`
	Journeys        map[string]*Journey            `bson:"journeys" json:"journeys"`
	Stops           map[string]*JourneyStop        `bson:"stops" json:"stops"`
	Assignments     map[string]*Assignment         `bson:"assignments" json:"assignments"`
	Reservations    map[string]*Reservation        `bson:"reservations" json:"reservations"`
	Handoffs        map[string]*Handoff            `bson:"handoffs" json:"handoffs"`
	Checkpoints     map[string]*HandlingCheckpoint `bson:"checkpoints" json:"checkpoints"`
	WorkerLocations map[string][]*WorkerLocation   `bson:"workerLocations" json:"workerLocations"`
	Disruptions     map[string]*Disruption         `bson:"disruptions" json:"disruptions"`
	Idempotency     map[string]*IdempotencyRecord  `bson:"idempotency" json:"idempotency"`
	AuditEvents     []*AuditEvent                  `bson:"auditEvents" json:"auditEvents"`
	OutboxEvents    []*OutboxEvent                 `bson:"outboxEvents" json:"outboxEvents"`
	NextAuditID     int64                          `bson:"nextAuditId" json:"nextAuditId"`
	NextOutboxID    int64                          `bson:"nextOutboxId" json:"nextOutboxId"`

	// NetworkRevision is bumped by every structural or policy change that can
	// alter the serviceability calculation: locations, their functions and
	// activation, connections, laboratory acceptance, and capacity.
	NetworkRevision int64 `bson:"networkRevision" json:"networkRevision"`
	// Serviceability is the published, complete serviceability calculation. It
	// is the only calculation ever served to a caller.
	Serviceability *ServiceabilityCalculation `bson:"serviceability,omitempty" json:"serviceability,omitempty"`
	// ServiceabilityDraft holds a calculation while it is being computed. It is
	// never served, so no request is ever checked against a partial result.
	ServiceabilityDraft *ServiceabilityCalculation `bson:"serviceabilityDraft,omitempty" json:"serviceabilityDraft,omitempty"`

	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

// ServiceRegion is one active collection point and the effective coverage the
// serviceability calculation used for it.
type ServiceRegion struct {
	LocationID string  `bson:"locationId" json:"locationId"`
	Name       string  `bson:"name" json:"name"`
	Latitude   float64 `bson:"latitude" json:"latitude"`
	Longitude  float64 `bson:"longitude" json:"longitude"`
	CoverageKM float64 `bson:"coverageKm" json:"coverageKm"`
}

// ServiceabilityCalculation is one complete version of the network's
// serviceability calculation. Versions are published whole: a calculation is
// built entirely under Status "computing" and only then swapped into place as
// Status "complete".
type ServiceabilityCalculation struct {
	Version         int64            `bson:"version" json:"version"`
	Status          string           `bson:"status" json:"status"`
	NetworkRevision int64            `bson:"networkRevision" json:"networkRevision"`
	ComputedAt      time.Time        `bson:"computedAt" json:"computedAt"`
	Regions         []*ServiceRegion `bson:"regions" json:"regions"`
}

// Serviceability calculation statuses.
const (
	ServiceabilityComplete  = "complete"
	ServiceabilityComputing = "computing"
)

type User struct {
	ID             string          `bson:"id" json:"id"`
	Username       string          `bson:"username" json:"username"`
	PasswordHash   string          `bson:"passwordHash" json:"-"`
	Email          string          `bson:"email" json:"email"`
	FullName       string          `bson:"fullName" json:"fullName"`
	Phone          string          `bson:"phone" json:"phone"`
	Roles          []string        `bson:"roles" json:"roles"`
	Qualifications []string        `bson:"qualifications" json:"qualifications"`
	Certifications []Certification `bson:"certifications,omitempty" json:"certifications,omitempty"`
	// AssignedLocationID is the node or laboratory a node_handler or
	// laboratory_team user is assigned to. It has no meaning for any other
	// role. An administrator's own capacity access stays unscoped regardless
	// of this field.
	AssignedLocationID string     `bson:"assignedLocationId,omitempty" json:"assignedLocationId,omitempty"`
	Active             bool       `bson:"active" json:"active"`
	Available          bool       `bson:"available" json:"available"`
	CapacityUnits      int        `bson:"capacityUnits" json:"capacityUnits"`
	AssignmentCount    int        `bson:"assignmentCount" json:"assignmentCount"`
	LastAssignedAt     *time.Time `bson:"lastAssignedAt,omitempty" json:"lastAssignedAt,omitempty"`
	LastOfferedAt      *time.Time `bson:"lastOfferedAt,omitempty" json:"lastOfferedAt,omitempty"`
	CreatedAt          time.Time  `bson:"createdAt" json:"createdAt"`
	UpdatedAt          time.Time  `bson:"updatedAt" json:"updatedAt"`
}

type Certification struct {
	Code      string     `bson:"code" json:"code"`
	Active    bool       `bson:"active" json:"active"`
	ExpiresAt *time.Time `bson:"expiresAt,omitempty" json:"expiresAt,omitempty"`
}

// Session carries the role context of one sign-in. SelectedRole lives here and
// never on User: switching the role a user is working under must not add or
// remove a role from the account.
type Session struct {
	TokenHash    string    `bson:"tokenHash" json:"tokenHash"`
	UserID       string    `bson:"userId" json:"userId"`
	SelectedRole string    `bson:"selectedRole,omitempty" json:"selectedRole,omitempty"`
	ExpiresAt    time.Time `bson:"expiresAt" json:"expiresAt"`
	CreatedAt    time.Time `bson:"createdAt" json:"createdAt"`
}

// Location function names. A location may provide any combination of these.
const (
	FunctionCollectionPoint = "collection_point"
	FunctionTransferPoint   = "transfer_point"
	FunctionHub             = "hub"
	FunctionLaboratory      = "laboratory"
)

// LocationFunctions lists every function a location may provide, in canonical
// order. Configuration input is validated against this set.
func LocationFunctions() []string {
	return []string{
		FunctionCollectionPoint,
		FunctionTransferPoint,
		FunctionHub,
		FunctionLaboratory,
	}
}

// IsLocationFunction reports whether the name is one of the four canonical
// location functions.
func IsLocationFunction(name string) bool {
	switch name {
	case FunctionCollectionPoint, FunctionTransferPoint, FunctionHub, FunctionLaboratory:
		return true
	}
	return false
}

type Location struct {
	ID              string  `bson:"id" json:"id"`
	Name            string  `bson:"name" json:"name"`
	Latitude        float64 `bson:"latitude" json:"latitude"`
	Longitude       float64 `bson:"longitude" json:"longitude"`
	CoverageKM      float64 `bson:"coverageKm" json:"coverageKm"`
	HandlingMinutes int     `bson:"handlingMinutes" json:"handlingMinutes"`
	Active          bool    `bson:"active" json:"active"`
	// Functions maps a function name to whether that function is currently active.
	// A key's presence means the location provides the function at all; the value
	// means it is enabled right now, so an administrator can disable a single
	// function without deactivating the whole site.
	Functions map[string]bool `bson:"functions" json:"functions"`
	CreatedAt time.Time       `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time       `bson:"updatedAt" json:"updatedAt"`
}

// Provides reports whether the location offers the function at all.
func (l *Location) Provides(fn string) bool {
	if l == nil {
		return false
	}
	_, ok := l.Functions[fn]
	return ok
}

// Serves reports whether the location is active and the given function is active.
func (l *Location) Serves(fn string) bool {
	if l == nil || !l.Active {
		return false
	}
	return l.Functions[fn]
}

type SampleType struct {
	ID                         string    `bson:"id" json:"id"`
	Name                       string    `bson:"name" json:"name"`
	QualifiedCollectorRequired bool      `bson:"qualifiedCollectorRequired" json:"qualifiedCollectorRequired"`
	RequiredQualification      string    `bson:"requiredQualification" json:"requiredQualification"`
	ContainerType              string    `bson:"containerType" json:"containerType"`
	TemperatureMinC            float64   `bson:"temperatureMinC" json:"temperatureMinC"`
	TemperatureMaxC            float64   `bson:"temperatureMaxC" json:"temperatureMaxC"`
	IncompatibleSampleTypes    []string  `bson:"incompatibleSampleTypes" json:"incompatibleSampleTypes"`
	RequiresCooledRoute        bool      `bson:"requiresCooledRoute" json:"requiresCooledRoute"`
	BatchingAllowed            bool      `bson:"batchingAllowed" json:"batchingAllowed"`
	CreatedAt                  time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt                  time.Time `bson:"updatedAt" json:"updatedAt"`
}

type Acceptance struct {
	ID           string    `bson:"id" json:"id"`
	LaboratoryID string    `bson:"laboratoryId" json:"laboratoryId"`
	SampleTypeID string    `bson:"sampleTypeId" json:"sampleTypeId"`
	Accepts      bool      `bson:"accepts" json:"accepts"`
	UpdatedAt    time.Time `bson:"updatedAt" json:"updatedAt"`
}

type Connection struct {
	ID             string    `bson:"id" json:"id"`
	FromLocationID string    `bson:"fromLocationId" json:"fromLocationId"`
	ToLocationID   string    `bson:"toLocationId" json:"toLocationId"`
	TravelMinutes  int       `bson:"travelMinutes" json:"travelMinutes"`
	CostUnits      float64   `bson:"costUnits" json:"costUnits"`
	Cooled         bool      `bson:"cooled" json:"cooled"`
	Active         bool      `bson:"active" json:"active"`
	CreatedAt      time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt      time.Time `bson:"updatedAt" json:"updatedAt"`
}

type Capacity struct {
	ResourceID     string    `bson:"resourceId" json:"resourceId"`
	CapacityUnits  int       `bson:"capacityUnits" json:"capacityUnits"`
	ReservedUnits  int       `bson:"reservedUnits" json:"reservedUnits"`
	CooledUnits    int       `bson:"cooledUnits" json:"cooledUnits"`
	CooledReserved int       `bson:"cooledReserved" json:"cooledReserved"`
	UpdatedAt      time.Time `bson:"updatedAt" json:"updatedAt"`
}

type CollectionRequest struct {
	ID          string    `bson:"id" json:"id"`
	RequesterID string    `bson:"requesterId" json:"requesterId"`
	Latitude    float64   `bson:"latitude" json:"latitude"`
	Longitude   float64   `bson:"longitude" json:"longitude"`
	OriginType  string    `bson:"originType" json:"originType"`
	RequestedAt time.Time `bson:"requestedAt" json:"requestedAt"`
	// EarliestCollectionAt and LatestCollectionAt bound the collection window.
	// The window governs when pickup may happen; DeliveryDeadline governs when
	// laboratory receipt must happen. They are separate constraints.
	EarliestCollectionAt time.Time `bson:"earliestCollectionAt" json:"earliestCollectionAt"`
	LatestCollectionAt   time.Time `bson:"latestCollectionAt" json:"latestCollectionAt"`
	DeliveryDeadline     time.Time `bson:"deliveryDeadline" json:"deliveryDeadline"`
	Serviceability       string    `bson:"serviceability" json:"serviceability"`
	CreatedAt            time.Time `bson:"createdAt" json:"createdAt"`
}

type Sample struct {
	ID                      string     `bson:"id" json:"id"`
	RequestID               string     `bson:"requestId" json:"requestId"`
	RequesterID             string     `bson:"requesterId" json:"requesterId"`
	SampleTypeID            string     `bson:"sampleTypeId" json:"sampleTypeId"`
	ContainerType           string     `bson:"containerType" json:"containerType"`
	TemperatureMinC         float64    `bson:"temperatureMinC" json:"temperatureMinC"`
	TemperatureMaxC         float64    `bson:"temperatureMaxC" json:"temperatureMaxC"`
	IncompatibleSampleTypes []string   `bson:"incompatibleSampleTypes" json:"incompatibleSampleTypes"`
	Status                  string     `bson:"status" json:"status"`
	ActiveJourneyID         string     `bson:"activeJourneyId,omitempty" json:"activeJourneyId,omitempty"`
	TargetLaboratoryID      string     `bson:"targetLaboratoryId,omitempty" json:"targetLaboratoryId,omitempty"`
	SealedAt                *time.Time `bson:"sealedAt,omitempty" json:"sealedAt,omitempty"`
	CurrentHolderID         string     `bson:"currentHolderId,omitempty" json:"currentHolderId,omitempty"`
	ReceivedLaboratoryID    string     `bson:"receivedLaboratoryId,omitempty" json:"receivedLaboratoryId,omitempty"`
	LatestLatitude          *float64   `bson:"latestLatitude,omitempty" json:"latestLatitude,omitempty"`
	LatestLongitude         *float64   `bson:"latestLongitude,omitempty" json:"latestLongitude,omitempty"`
	Exception               core.Map   `bson:"exception,omitempty" json:"exception,omitempty"`
	Version                 int64      `bson:"version" json:"version"`
	CreatedAt               time.Time  `bson:"createdAt" json:"createdAt"`
	UpdatedAt               time.Time  `bson:"updatedAt" json:"updatedAt"`
}

type Journey struct {
	ID                  string    `bson:"id" json:"id"`
	PrimarySampleID     string    `bson:"primarySampleId" json:"primarySampleId"`
	SampleIDs           []string  `bson:"sampleIds" json:"sampleIds"`
	ParentJourneyID     string    `bson:"parentJourneyId,omitempty" json:"parentJourneyId,omitempty"`
	IsReplacement       bool      `bson:"isReplacement" json:"isReplacement"`
	PlanVersion         int64     `bson:"planVersion" json:"planVersion"`
	RouteMode           string    `bson:"routeMode" json:"routeMode"`
	Status              string    `bson:"status" json:"status"`
	EstimatedDeliveryAt time.Time `bson:"estimatedDeliveryAt" json:"estimatedDeliveryAt"`
	TotalCostUnits      float64   `bson:"totalCostUnits" json:"totalCostUnits"`
	CapacityUsed        int       `bson:"capacityUsed" json:"capacityUsed"`
	CapacityAvailable   int       `bson:"capacityAvailable" json:"capacityAvailable"`
	CreatedAt           time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt           time.Time `bson:"updatedAt" json:"updatedAt"`
}

type JourneyStop struct {
	ID           string     `bson:"id" json:"id"`
	JourneyID    string     `bson:"journeyId" json:"journeyId"`
	LocationID   string     `bson:"locationId" json:"locationId"`
	StopType     string     `bson:"stopType" json:"stopType"`
	Sequence     int        `bson:"sequence" json:"sequence"`
	EstimatedAt  time.Time  `bson:"estimatedAt" json:"estimatedAt"`
	DwellMinutes int        `bson:"dwellMinutes" json:"dwellMinutes"`
	CompletedAt  *time.Time `bson:"completedAt,omitempty" json:"completedAt,omitempty"`
}

type AssignmentDecline struct {
	WorkerID string    `bson:"workerId" json:"workerId"`
	Reason   string    `bson:"reason" json:"reason"`
	At       time.Time `bson:"at" json:"at"`
}

type Assignment struct {
	ID             string              `bson:"id" json:"id"`
	JourneyID      string              `bson:"journeyId" json:"journeyId"`
	SampleID       string              `bson:"sampleId" json:"sampleId"`
	Kind           string              `bson:"kind" json:"kind"`
	LegSequence    int                 `bson:"legSequence" json:"legSequence"`
	FromLocationID string              `bson:"fromLocationId,omitempty" json:"fromLocationId,omitempty"`
	ToLocationID   string              `bson:"toLocationId,omitempty" json:"toLocationId,omitempty"`
	WorkerID       string              `bson:"workerId,omitempty" json:"workerId,omitempty"`
	Status         string              `bson:"status" json:"status"`
	AcceptedAt     *time.Time          `bson:"acceptedAt,omitempty" json:"acceptedAt,omitempty"`
	CompletedAt    *time.Time          `bson:"completedAt,omitempty" json:"completedAt,omitempty"`
	OfferExpiresAt *time.Time          `bson:"offerExpiresAt,omitempty" json:"offerExpiresAt,omitempty"`
	Declines       []AssignmentDecline `bson:"declines" json:"declines"`
	CreatedAt      time.Time           `bson:"createdAt" json:"createdAt"`
}

type Reservation struct {
	ID         string `bson:"id" json:"id"`
	JourneyID  string `bson:"journeyId" json:"journeyId"`
	ResourceID string `bson:"resourceId" json:"resourceId"`
	Units      int    `bson:"units" json:"units"`
	Cooled     bool   `bson:"cooled" json:"cooled"`
	Status     string `bson:"status" json:"status"`
}

type Handoff struct {
	ID                  string     `bson:"id" json:"id"`
	SampleID            string     `bson:"sampleId" json:"sampleId"`
	FromAssignmentID    string     `bson:"fromAssignmentId,omitempty" json:"fromAssignmentId,omitempty"`
	ToAssignmentID      string     `bson:"toAssignmentId,omitempty" json:"toAssignmentId,omitempty"`
	SenderID            string     `bson:"senderId" json:"senderId"`
	ReceiverID          string     `bson:"receiverId" json:"receiverId"`
	SenderConfirmedAt   *time.Time `bson:"senderConfirmedAt,omitempty" json:"senderConfirmedAt,omitempty"`
	ReceiverConfirmedAt *time.Time `bson:"receiverConfirmedAt,omitempty" json:"receiverConfirmedAt,omitempty"`
	Status              string     `bson:"status" json:"status"`
	InitiatedAt         time.Time  `bson:"initiatedAt" json:"initiatedAt"`
	CompletedAt         *time.Time `bson:"completedAt,omitempty" json:"completedAt,omitempty"`
}

type HandlingCheckpoint struct {
	ID                   string    `bson:"id" json:"id"`
	SampleID             string    `bson:"sampleId" json:"sampleId"`
	AssignmentID         string    `bson:"assignmentId" json:"assignmentId"`
	LocationID           string    `bson:"locationId,omitempty" json:"locationId,omitempty"`
	ContainerType        string    `bson:"containerType" json:"containerType"`
	ObservedTemperatureC float64   `bson:"observedTemperatureC" json:"observedTemperatureC"`
	Compliant            bool      `bson:"compliant" json:"compliant"`
	RecordedAt           time.Time `bson:"recordedAt" json:"recordedAt"`
}

type WorkerLocation struct {
	AssignmentID string    `bson:"assignmentId" json:"assignmentId"`
	Sequence     int64     `bson:"sequence" json:"sequence"`
	WorkerID     string    `bson:"workerId" json:"workerId"`
	Latitude     float64   `bson:"latitude" json:"latitude"`
	Longitude    float64   `bson:"longitude" json:"longitude"`
	RecordedAt   time.Time `bson:"recordedAt" json:"recordedAt"`
}

type Disruption struct {
	ID                 string    `bson:"id" json:"id"`
	ResourceType       string    `bson:"resourceType" json:"resourceType"`
	ResourceID         string    `bson:"resourceId" json:"resourceId"`
	Reason             string    `bson:"reason" json:"reason"`
	OccurredAt         time.Time `bson:"occurredAt" json:"occurredAt"`
	ReplanStatus       string    `bson:"replanStatus" json:"replanStatus"`
	AffectedSampleIDs  []string  `bson:"affectedSampleIds" json:"affectedSampleIds"`
	ReplacementPlanIDs []string  `bson:"replacementPlanIds" json:"replacementPlanIds"`
	CreatedAt          time.Time `bson:"createdAt" json:"createdAt"`
}

type IdempotencyRecord struct {
	ActorID     string    `bson:"actorId" json:"actorId"`
	Operation   string    `bson:"operation" json:"operation"`
	Key         string    `bson:"key" json:"key"`
	RequestHash string    `bson:"requestHash" json:"requestHash"`
	Status      int       `bson:"status" json:"status"`
	Response    core.Map  `bson:"response" json:"response"`
	CreatedAt   time.Time `bson:"createdAt" json:"createdAt"`
}

// SystemActorID and SystemRole attribute the actions taken by the application's
// own durable timers, so an audit record never lacks an actor or a role.
const (
	SystemActorID = "system"
	SystemRole    = "system"
)

// AuditEvent is one immutable accountability record. It carries all seven
// required facts: the actor (ActorID), the role they acted under (Role), the
// action taken (Action), the affected item (EntityType/EntityID), the time
// (OccurredAt), the outcome (Success) and the relevant changes (Details).
type AuditEvent struct {
	ID         int64     `bson:"id" json:"id"`
	ActorID    string    `bson:"actorId,omitempty" json:"actorId,omitempty"`
	Role       string    `bson:"role" json:"role"`
	EntityType string    `bson:"entityType" json:"entityType"`
	EntityID   string    `bson:"entityId" json:"entityId"`
	Action     string    `bson:"action" json:"action"`
	Success    bool      `bson:"success" json:"success"`
	Details    core.Map  `bson:"details" json:"details"`
	OccurredAt time.Time `bson:"occurredAt" json:"occurredAt"`
}

type OutboxEvent struct {
	ID         int64     `bson:"id" json:"id"`
	Audience   []string  `bson:"audience" json:"audience"`
	EventType  string    `bson:"eventType" json:"eventType"`
	EntityType string    `bson:"entityType" json:"entityType"`
	EntityID   string    `bson:"entityId" json:"entityId"`
	Payload    core.Map  `bson:"payload" json:"payload"`
	CreatedAt  time.Time `bson:"createdAt" json:"createdAt"`
}

// NewState returns a fully initialized state document.
func NewState() *State {
	// The empty bootstrap state is byte-stable so two resets produce identical
	// datastore fingerprints. Normal commands replace UpdatedAt with core.Now().
	state := &State{ID: StateDocumentID, Version: 1, UpdatedAt: time.Unix(0, 0).UTC()}
	state.ensureMaps()
	return state
}

func (s *State) ensureMaps() {
	if s.Users == nil {
		s.Users = map[string]*User{}
	}
	if s.Sessions == nil {
		s.Sessions = map[string]*Session{}
	}
	if s.Locations == nil {
		s.Locations = map[string]*Location{}
	}
	if s.SampleTypes == nil {
		s.SampleTypes = map[string]*SampleType{}
	}
	if s.Acceptances == nil {
		s.Acceptances = map[string]*Acceptance{}
	}
	if s.Connections == nil {
		s.Connections = map[string]*Connection{}
	}
	if s.Capacities == nil {
		s.Capacities = map[string]*Capacity{}
	}
	if s.Requests == nil {
		s.Requests = map[string]*CollectionRequest{}
	}
	if s.Samples == nil {
		s.Samples = map[string]*Sample{}
	}
	if s.Journeys == nil {
		s.Journeys = map[string]*Journey{}
	}
	if s.Stops == nil {
		s.Stops = map[string]*JourneyStop{}
	}
	if s.Assignments == nil {
		s.Assignments = map[string]*Assignment{}
	}
	if s.Reservations == nil {
		s.Reservations = map[string]*Reservation{}
	}
	if s.Handoffs == nil {
		s.Handoffs = map[string]*Handoff{}
	}
	if s.Checkpoints == nil {
		s.Checkpoints = map[string]*HandlingCheckpoint{}
	}
	if s.WorkerLocations == nil {
		s.WorkerLocations = map[string][]*WorkerLocation{}
	}
	if s.Disruptions == nil {
		s.Disruptions = map[string]*Disruption{}
	}
	if s.Idempotency == nil {
		s.Idempotency = map[string]*IdempotencyRecord{}
	}
	if s.AuditEvents == nil {
		s.AuditEvents = []*AuditEvent{}
	}
	if s.OutboxEvents == nil {
		s.OutboxEvents = []*OutboxEvent{}
	}
}

// AddAudit appends an immutable domain audit event to the transaction state.
// The outcome is explicit: a rejected or denied action is recorded with
// success=false, so "outcome" is a real fact and not a constant.
func (s *State) AddAudit(
	actorID, role, entityType, entityID, action string,
	success bool,
	details core.Map,
) int64 {
	if actorID == "" {
		actorID = SystemActorID
	}
	if role == "" {
		role = SystemRole
	}
	if details == nil {
		details = core.Map{}
	}
	s.NextAuditID++
	event := &AuditEvent{
		ID: s.NextAuditID, ActorID: actorID, Role: role, EntityType: entityType,
		EntityID: entityID, Action: action, Success: success,
		Details: details, OccurredAt: core.Now(),
	}
	s.AuditEvents = append(s.AuditEvents, event)
	return event.ID
}

// AddEvent appends a durable realtime outbox event to the same atomic state
// transition as the domain mutation.
func (s *State) AddEvent(audience []string, eventType, entityType, entityID string, payload core.Map) int64 {
	s.NextOutboxID++
	event := &OutboxEvent{
		ID: s.NextOutboxID, Audience: append([]string{}, audience...),
		EventType: eventType, EntityType: entityType, EntityID: entityID,
		Payload: payload, CreatedAt: core.Now(),
	}
	s.OutboxEvents = append(s.OutboxEvents, event)
	return event.ID
}
