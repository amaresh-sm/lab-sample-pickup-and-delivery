package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

type Service struct{ Store *mongostore.Store }

type txResult struct {
	body    core.Map
	eventID int64
}

// Handle runs one operation and, when it is rejected, records the rejection in
// the audit trail. The rejection audit is written by recordRejection as its own
// committed state transition, because the failing command rolled its whole
// compare-and-swap transaction back and would have discarded an audit row
// written inside it.
func (s *Service) Handle(ctx context.Context, operation string, in core.Map, p core.Principal) (core.Map, error) {
	out, err := s.dispatch(ctx, operation, in, p)
	if err != nil {
		s.recordRejection(ctx, p, operation, in, err)
	}
	return out, err
}

func (s *Service) dispatch(ctx context.Context, operation string, in core.Map, p core.Principal) (core.Map, error) {
	switch operation {
	case "authenticate_user":
		return s.Store.Login(ctx, core.String(in["username"]), core.String(in["password"]))
	case "select_active_role":
		return s.selectActiveRole(ctx, p, in)
	case "create_user":
		return s.createUser(ctx, p, in)
	case "read_user":
		return s.readUser(ctx, p, core.String(in["userId"]))
	case "update_user_access":
		return s.updateUserAccess(ctx, p, in)
	case "create_location":
		return s.createLocation(ctx, p, in)
	case "update_location":
		return s.updateLocation(ctx, p, in)
	case "set_location_status":
		return s.setActive(ctx, p, "location", core.String(in["locationId"]), core.Bool(in["active"]))
	case "set_location_function_status":
		return s.setLocationFunctionStatus(ctx, p, in)
	case "create_sample_type":
		return s.createSampleType(ctx, p, in)
	case "update_sample_type":
		return s.updateSampleType(ctx, p, in)
	case "configure_laboratory_acceptance":
		return s.configureAcceptance(ctx, p, in)
	case "create_connection":
		return s.createConnection(ctx, p, in)
	case "set_connection_status":
		return s.setActive(ctx, p, "connection", core.String(in["connectionId"]), core.Bool(in["active"]))
	case "configure_capacity":
		return s.configureCapacity(ctx, p, in)
	case "set_worker_availability":
		return s.setWorkerAvailability(ctx, p, in)
	case "check_serviceability":
		return s.checkServiceability(ctx, p, in)
	case "read_serviceability_snapshot":
		return s.readServiceabilitySnapshot(ctx, p)
	case "create_collection_request":
		return s.createCollectionRequest(ctx, p, in)
	case "ingest_presealed_sample":
		return s.ingestPresealedSample(ctx, p, in)
	case "read_sample_state":
		return s.readSampleState(ctx, p, core.String(in["sampleId"]))
	case "read_sample_lifecycle":
		return s.readSampleLifecycle(ctx, p, core.String(in["sampleId"]))
	case "read_journey_plan":
		return s.readJourneyPlan(ctx, p, core.String(in["journeyId"]))
	case "edit_active_plan":
		return s.editPlan(ctx, p, in)
	case "split_journey":
		return s.splitJourney(ctx, p, in)
	case "merge_journeys":
		return s.mergeJourneys(ctx, p, in)
	case "cancel_journey":
		return s.cancelJourney(ctx, p, in)
	case "reroute_journey":
		return s.rerouteJourney(ctx, p, in)
	case "accept_assignment":
		return s.acceptAssignment(ctx, p, in)
	case "decline_assignment":
		return s.declineAssignment(ctx, p, in)
	case "collect_and_seal_sample":
		return s.collectAndSeal(ctx, p, in)
	case "initiate_handoff":
		return s.initiateHandoff(ctx, p, in)
	case "confirm_handoff":
		return s.confirmHandoff(ctx, p, in)
	case "update_assignment_progress":
		return s.updateProgress(ctx, p, in)
	case "record_handling_checkpoint":
		return s.recordHandling(ctx, p, in)
	case "record_worker_location":
		return s.recordLocation(ctx, p, in)
	case "confirm_laboratory_receipt":
		return s.confirmReceipt(ctx, p, in)
	case "record_laboratory_exception":
		return s.recordLabException(ctx, p, in)
	case "report_disruption":
		return s.reportDisruption(ctx, p, in)
	case "list_role_work":
		return s.listRoleWork(ctx, p, in)
	case "read_operations_state":
		return s.readOperations(ctx, p, in)
	case "search_audit_trail":
		return s.searchAudit(ctx, p, in)
	default:
		return nil, core.Fail(http.StatusNotFound, "not_found", "operation not found")
	}
}

// require authorizes an action against the caller's SELECTED role only. Holding
// a role is not enough: an administrator working as a requester is refused every
// administrator-only action, and a user holding several roles must select one
// before any role-scoped action is permitted.
func require(p core.Principal, roles ...string) error {
	if p.RoleSelectionRequired() {
		return core.Fail(
			409,
			"role_not_selected",
			"this account holds more than one role; select the active role before acting",
		)
	}
	if !p.Has(roles...) {
		return core.Fail(403, "forbidden", "the selected role does not permit this action")
	}
	return nil
}

// selectActiveRole records, on the caller's session, which of their roles they
// are working under. The user's own role list is never touched, so switching
// context can neither add nor remove a role from the account.
func (s *Service) selectActiveRole(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	role := strings.TrimSpace(core.String(in["role"]))
	if role == "" {
		return nil, core.Fail(400, "invalid_role", "role is required")
	}
	var out core.Map
	err := s.Store.Update(ctx, func(state *mongostore.State) error {
		user := state.Users[p.ID]
		if user == nil {
			return notFound("user")
		}
		if !stringSliceContains(user.Roles, role) {
			return core.Fail(403, "role_not_held", "this account does not hold the requested role")
		}
		session := state.Sessions[p.SessionID]
		if session == nil {
			return core.Fail(401, "unauthorized", "invalid or expired bearer token")
		}
		session.SelectedRole = role
		out = core.Map{"activeRole": role, "roles": append([]string{}, user.Roles...)}
		state.AddAudit(p.ID, role, "user", p.ID, "active_role_selected", true, core.Map{
			"activeRole": role, "outcome": "selected",
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// mutate executes a complete command against one versioned MongoDB aggregate.
// Critical retries return the original response for the same input and reject a
// reused key with a different request.
func (s *Service) mutate(
	ctx context.Context,
	p core.Principal,
	operation, key string,
	input core.Map,
	fn func(*mongostore.State) (txResult, error),
) (core.Map, error) {
	raw, _ := json.Marshal(input)
	digest := sha256.Sum256(raw)
	requestHash := hex.EncodeToString(digest[:])
	var output core.Map
	err := s.Store.Update(ctx, func(state *mongostore.State) error {
		if key != "" {
			id := mongostore.IdempotencyID(p.ID, operation, key)
			if record := state.Idempotency[id]; record != nil {
				if record.RequestHash != requestHash {
					return core.Fail(409, "idempotency_conflict", "idempotency key was used with a different request")
				}
				output = cloneMap(record.Response)
				return nil
			}
		}
		result, err := fn(state)
		if err != nil {
			return err
		}
		output = cloneMap(result.body)
		if key != "" {
			id := mongostore.IdempotencyID(p.ID, operation, key)
			state.Idempotency[id] = &mongostore.IdempotencyRecord{
				ActorID: p.ID, Operation: operation, Key: key,
				RequestHash: requestHash, Status: http.StatusOK,
				Response: cloneMap(result.body), CreatedAt: core.Now(),
			}
		}
		return nil
	})
	return output, err
}

func cloneMap(value core.Map) core.Map {
	if value == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	var copied core.Map
	_ = json.Unmarshal(raw, &copied)
	return copied
}

// audit records a successful action, attributed to the role the actor is
// working under rather than to every role they hold.
func audit(state *mongostore.State, p core.Principal, entityType, entityID, action string, details core.Map) {
	state.AddAudit(p.ID, p.AuditRole(), entityType, entityID, action, true, cloneMap(details))
}

// rejectionAuditAction names the audit category a refusal belongs to. A refused
// permission check is its own category so "permission checks" is searchable
// independently of every other rejection.
func rejectionAuditAction(status int) string {
	if status == http.StatusForbidden {
		return "permission_denied"
	}
	return "action_rejected"
}

// rejectedEntityKeys maps a request field to the entity type it identifies, in
// the order a rejection should be attributed. It lets a refusal name the item it
// affected rather than only the operation.
var rejectedEntityKeys = []struct{ field, entityType string }{
	{"sampleId", "sample"},
	{"journeyId", "journey"},
	{"assignmentId", "assignment"},
	{"handoffId", "handoff"},
	{"checkpointId", "sample"},
	{"laboratoryId", "location"},
	{"locationId", "location"},
	{"connectionId", "connection"},
	{"sampleTypeId", "sample_type"},
	{"resourceId", "capacity"},
	{"workerId", "user"},
	{"userId", "user"},
	{"requestId", "collection_request"},
}

// rejectedSelfEntities names the affected item for operations whose subject is
// the caller's own account rather than an identifier in the request.
var rejectedSelfEntities = map[string]string{
	"select_active_role": "user",
	"list_role_work":     "user",
}

// rejectedEntity names the affected item of a refused action, falling back to
// the operation itself when the request identified nothing.
func rejectedEntity(operation, actorID string, in core.Map) (string, string) {
	for _, candidate := range rejectedEntityKeys {
		if value := strings.TrimSpace(core.String(in[candidate.field])); value != "" {
			return candidate.entityType, value
		}
	}
	if entityType, ok := rejectedSelfEntities[operation]; ok && actorID != "" {
		return entityType, actorID
	}
	return "operation", operation
}

// recordRejection commits one audit record describing a refused action.
//
// A rejected command returns its error from inside Store.Update, which discards
// the whole compare-and-swap transaction; an audit row appended in that
// transaction would be rolled back with everything else. The rejection is
// therefore written afterwards, in a separate transaction that appends nothing
// but the audit event, so a refused action is visible in the audit trail with a
// failure outcome while leaving no other state behind.
func (s *Service) recordRejection(
	ctx context.Context,
	p core.Principal,
	operation string,
	in core.Map,
	cause error,
) {
	var apiErr *core.HTTPError
	if !errors.As(cause, &apiErr) {
		return
	}
	// Sign-in outcomes are recorded by the credential check itself, which is the
	// only place that knows whether the account exists.
	if operation == "authenticate_user" {
		return
	}
	entityType, entityID := rejectedEntity(operation, p.ID, in)
	details := core.Map{
		"operation": operation, "code": apiErr.Code, "status": apiErr.Status,
		"message": apiErr.Error(), "outcome": "rejected", "role": p.AuditRole(),
	}
	_ = s.Store.Update(ctx, func(state *mongostore.State) error {
		state.AddAudit(
			p.ID, p.AuditRole(), entityType, entityID,
			rejectionAuditAction(apiErr.Status), false, cloneMap(details),
		)
		return nil
	})
}

func emit(state *mongostore.State, audience []string, eventType, entityType, entityID string, payload core.Map) int64 {
	return state.AddEvent(audience, eventType, entityType, entityID, cloneMap(payload))
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func pointerTime(t time.Time) *time.Time {
	value := t.UTC()
	return &value
}

func pointerFloat(value float64) *float64 { return &value }

func notFound(entity string) error {
	return core.Fail(404, "not_found", fmt.Sprintf("%s not found", entity))
}

func stringSliceContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sorted(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
