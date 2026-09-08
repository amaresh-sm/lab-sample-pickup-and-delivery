package service

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func integrationService(t *testing.T) (*Service, core.Principal) {
	t.Helper()
	baseURI := os.Getenv("TEST_MONGODB_URI")
	if baseURI == "" {
		t.Skip("TEST_MONGODB_URI is not set")
	}
	uri := baseURI + "_service"
	ctx := context.Background()
	store, err := mongostore.Open(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(ctx); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.EnsureInitialAdmin(ctx, "integration-admin", "password-123456"); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	var admin core.Principal
	if err := store.View(ctx, func(state *mongostore.State) error {
		for _, user := range state.Users {
			if user.Username == "integration-admin" {
				admin = core.Principal{ID: user.ID, Roles: append([]string{}, user.Roles...)}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Service{Store: store}, admin
}

func createTestUser(t *testing.T, service *Service, admin core.Principal, username string, roles, qualifications []string) core.Principal {
	t.Helper()
	return createTestUserWithLocation(t, service, admin, username, roles, qualifications, "")
}

// createTestUserWithLocation is the same as createTestUser but also sets
// AssignedLocationID (via the "assignedLocationId" field on the create-user
// call) when locationID is non-empty. This is required for any
// laboratory_team/node_handler worker that is expected to actually be
// eligible for (and receive) a lab/node assignment: fairWorker in
// planning.go requires an exact match between the worker's
// AssignedLocationID and the assignment's location.
func createTestUserWithLocation(t *testing.T, service *Service, admin core.Principal, username string, roles, qualifications []string, locationID string) core.Principal {
	t.Helper()
	body := core.Map{
		"username": username, "initialPassword": "password-123456",
		"email": username + "@example.test", "fullName": username,
		"phone": "555-0100", "roles": roles, "qualifications": qualifications,
		"active": true,
	}
	if locationID != "" {
		body["assignedLocationId"] = locationID
	}
	out, err := service.createUser(context.Background(), admin, body)
	if err != nil {
		t.Fatal(err)
	}
	return core.Principal{ID: core.String(out["userId"]), Roles: roles}
}

func TestDirectJourneyCustodyAndReceipt(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "requester", []string{"requester"}, nil)
	collectorA := createTestUser(t, service, admin, "collector-a", []string{"collection_professional"}, []string{"LX-A"})
	collectorB := createTestUser(t, service, admin, "collector-b", []string{"collection_professional"}, []string{"LX-A"})
	courier := createTestUser(t, service, admin, "courier", []string{"delivery_partner"}, nil)

	node, err := service.createLocation(ctx, admin, core.Map{
		"name": "North node", "active": true,
		"coordinate": core.Map{"latitude": 12.9716, "longitude": 77.5946},
		"functions": []string{
			mongostore.FunctionCollectionPoint,
			mongostore.FunctionTransferPoint,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	laboratory, err := service.createLocation(ctx, admin, core.Map{
		"name": "Central lab", "active": true,
		"coordinate": core.Map{"latitude": 12.9816, "longitude": 77.6046},
		"functions":  []string{mongostore.FunctionLaboratory},
	})
	if err != nil {
		t.Fatal(err)
	}
	sampleType, err := service.createSampleType(ctx, admin, core.Map{
		"name": "Amber", "containerType": "amber-container",
		"temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes":    []string{},
		"qualifiedCollectorRequired": true, "requiredQualification": "LX-A",
		"batchingAllowed": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeID, laboratoryID, sampleTypeID := core.String(node["locationId"]), core.String(laboratory["locationId"]), core.String(sampleType["sampleTypeId"])
	laboratoryUser := createTestUserWithLocation(t, service, admin, "laboratory-user", []string{"laboratory_team"}, nil, laboratoryID)
	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": laboratoryID, "sampleTypeId": sampleTypeID, "accepts": true,
	}); err != nil {
		t.Fatal(err)
	}
	connection, err := service.createConnection(ctx, admin, core.Map{
		"fromLocationId": nodeID, "toLocationId": laboratoryID,
		"travelMinutes": 30, "costUnits": 5.0, "active": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, resourceID := range []string{nodeID, laboratoryID, core.String(connection["connectionId"])} {
		if _, err := service.configureCapacity(ctx, admin, core.Map{"resourceId": resourceID, "capacityUnits": 10}); err != nil {
			t.Fatal(err)
		}
	}

	requestInput := core.Map{
		"requesterId": requester.ID, "origin": core.Map{"latitude": 12.9716, "longitude": 77.5946},
		"originType": "home", "requestedAt": "2026-07-26T09:00:00Z",
		"deliveryDeadline": "2026-07-26T12:00:00Z", "sampleTypeId": sampleTypeID,
		"containerType": "amber-container", "temperatureMinC": 2.0,
		"temperatureMaxC": 8.0, "incompatibleSampleTypes": []string{},
		"idempotencyKey": "create-direct-journey",
	}
	created, err := service.createCollectionRequest(ctx, requester, requestInput)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := service.createCollectionRequest(ctx, requester, requestInput)
	if err != nil {
		t.Fatalf("idempotent create retry: %v", err)
	}
	if retried["requestId"] != created["requestId"] ||
		retried["sampleId"] != created["sampleId"] ||
		retried["journeyId"] != created["journeyId"] {
		t.Fatalf("create retry changed identity: first=%v retry=%v", created, retried)
	}
	if created["serviceability"] != "serviceable" {
		t.Fatalf("serviceability=%v", created["serviceability"])
	}
	journeyID, sampleID := core.String(created["journeyId"]), core.String(created["sampleId"])
	journey, err := service.readJourneyPlan(ctx, admin, journeyID)
	if err != nil {
		t.Fatal(err)
	}
	if journey["routeMode"] != "direct" {
		t.Fatalf("routeMode=%v", journey["routeMode"])
	}
	collectionID := core.String(journey["collectionAssignmentId"])

	var successes int
	var winner core.Principal
	var mutex sync.Mutex
	var wait sync.WaitGroup
	for index, collector := range []core.Principal{collectorA, collectorB} {
		wait.Add(1)
		go func(index int, collector core.Principal) {
			defer wait.Done()
			_, acceptErr := service.acceptAssignment(ctx, collector, core.Map{
				"assignmentId": collectionID, "idempotencyKey": "accept-collection-" + core.String(index),
			})
			if acceptErr == nil {
				mutex.Lock()
				successes++
				winner = collector
				mutex.Unlock()
				return
			}
			var apiErr *core.HTTPError
			if !errors.As(acceptErr, &apiErr) ||
				apiErr.Code != "assignment_taken" && apiErr.Code != "offer_not_available" {
				t.Errorf("unexpected concurrent accept error: %v", acceptErr)
			}
		}(index, collector)
	}
	wait.Wait()
	if successes != 1 {
		t.Fatalf("successful concurrent acceptances=%d, want 1", successes)
	}
	if _, err := service.collectAndSeal(ctx, winner, core.Map{
		"sampleId": sampleID, "assignmentId": collectionID,
		"containerType": "amber-container", "observedTemperatureC": 5.0,
		"collectedAt": "2026-07-26T09:10:00Z", "idempotencyKey": "seal-direct-sample",
	}); err != nil {
		t.Fatal(err)
	}

	transportID := core.String(journey["firstTransportAssignmentId"])
	if _, err := service.acceptAssignment(ctx, courier, core.Map{
		"assignmentId": transportID, "idempotencyKey": "accept-transport",
	}); err != nil {
		t.Fatal(err)
	}
	handoff, err := service.initiateHandoff(ctx, winner, core.Map{
		"sampleId": sampleID, "receiverId": courier.ID,
		"fromAssignmentId": collectionID, "toAssignmentId": transportID,
		"initiatedAt": "2026-07-26T09:12:00Z", "idempotencyKey": "handoff-direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	handoffID := core.String(handoff["handoffId"])
	if _, err := service.confirmHandoff(ctx, winner, core.Map{
		"handoffId": handoffID, "confirmationSide": "sender",
		"confirmedAt": "2026-07-26T09:13:00Z", "idempotencyKey": "handoff-sender",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.confirmHandoff(ctx, courier, core.Map{
		"handoffId": handoffID, "confirmationSide": "receiver",
		"confirmedAt": "2026-07-26T09:14:00Z", "idempotencyKey": "handoff-receiver",
	}); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := service.readSampleLifecycle(ctx, admin, sampleID)
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle["currentHolderId"] != courier.ID {
		t.Fatalf("holder=%v, want courier", lifecycle["currentHolderId"])
	}
	progress := []struct {
		status string
		at     string
	}{
		{"at_pickup", "2026-07-26T09:15:00Z"},
		{"picked_up", "2026-07-26T09:16:00Z"},
		{"in_progress", "2026-07-26T09:17:00Z"},
		{"arrived", "2026-07-26T09:40:00Z"},
		{"delivered", "2026-07-26T09:41:00Z"},
	}
	for index, update := range progress {
		if _, err := service.updateProgress(ctx, courier, core.Map{
			"assignmentId": transportID, "status": update.status, "occurredAt": update.at,
			"idempotencyKey": "progress-" + core.String(index),
		}); err != nil {
			t.Fatalf("progress %s: %v", update.status, err)
		}
	}
	receiptInput := core.Map{
		"sampleId": sampleID, "laboratoryId": laboratoryID,
		"receivedAt": "2026-07-26T09:42:00Z", "idempotencyKey": "receipt-direct",
	}
	receipt, err := service.confirmReceipt(ctx, laboratoryUser, receiptInput)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := service.confirmReceipt(ctx, laboratoryUser, receiptInput)
	if err != nil {
		t.Fatalf("idempotent receipt retry: %v", err)
	}
	if receipt["status"] != "received" || retry["status"] != "received" {
		t.Fatalf("receipt=%v retry=%v", receipt, retry)
	}
}

// This is a distance-helper test only: it checks that the equatorial test
// coordinate really is within five kilometres, and it does NOT exercise
// serviceability. The inclusive 5 km service boundary of AC-35 is probed by
// TestAC35FiveKilometreServiceBoundaryIsInclusive, which calls the
// serviceability decision itself.
func TestDistanceKMMeasuresTheFiveKilometreCoordinate(t *testing.T) {
	origin := core.Map{"latitude": 0.0, "longitude": 0.0}
	// At the equator this longitude delta is approximately five kilometres.
	node := core.Map{"latitude": 0.0, "longitude": 0.044966}
	distance := core.DistanceKM(origin, node)
	if distance > 5.0001 {
		t.Fatalf("test coordinate distance=%f", distance)
	}
	if time.Duration(distance*1000) <= 0 {
		t.Fatal("invalid distance")
	}
}

func TestCanonicalPoliciesSurviveUpdateWithoutName(t *testing.T) {
	service, admin := integrationService(t)
	ctx := context.Background()
	create := func(name, container string, minimum, maximum float64) core.Map {
		t.Helper()
		out, err := service.createSampleType(ctx, admin, core.Map{
			"name": name, "containerType": container,
			"temperatureMinC": minimum, "temperatureMaxC": maximum,
			"qualifiedCollectorRequired": false, "requiredQualification": "untrusted",
			"incompatibleSampleTypes": []string{}, "requiresCooledRoute": false,
			"batchingAllowed": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	updateWithoutName := func(item core.Map) core.Map {
		t.Helper()
		out, err := service.updateSampleType(ctx, admin, core.Map{
			"sampleTypeId":               core.String(item["sampleTypeId"]),
			"containerType":              core.String(item["containerType"]),
			"temperatureMinC":            core.Float(item["temperatureMinC"]),
			"temperatureMaxC":            core.Float(item["temperatureMaxC"]),
			"qualifiedCollectorRequired": false, "requiredQualification": "weakened",
			"incompatibleSampleTypes": []string{}, "requiresCooledRoute": false,
			"batchingAllowed": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	amber := create("Amber", "standard_vial", 2, 8)
	indigo := create("Indigo", "standard_vial", 2, 8)
	frost := create("Frost", "cooled_vial", -20, -10)
	pulse := create("Pulse", "rapid_vial", 2, 8)

	amber = updateWithoutName(amber)
	indigo = updateWithoutName(indigo)
	frost = updateWithoutName(frost)
	pulse = updateWithoutName(pulse)

	assertQualification := func(item core.Map, want string) {
		t.Helper()
		if !core.Bool(item["qualifiedCollectorRequired"]) || core.String(item["requiredQualification"]) != want {
			t.Fatalf("policy = %v, want qualification %s", item, want)
		}
	}
	assertQualification(amber, "LX-A")
	assertQualification(indigo, "LX-I")
	assertQualification(frost, "LX-F")
	assertQualification(pulse, "LX-P")
	if !core.Bool(frost["requiresCooledRoute"]) {
		t.Fatal("Frost update disabled continuous cooling")
	}
	if core.Bool(pulse["batchingAllowed"]) {
		t.Fatal("Pulse update enabled batching")
	}

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		amberState := state.SampleTypes[core.String(amber["sampleTypeId"])]
		indigoState := state.SampleTypes[core.String(indigo["sampleTypeId"])]
		if !stringSliceContains(amberState.IncompatibleSampleTypes, indigoState.ID) ||
			!stringSliceContains(indigoState.IncompatibleSampleTypes, amberState.ID) {
			t.Fatalf(
				"canonical incompatibility was weakened: amber=%v indigo=%v",
				amberState.IncompatibleSampleTypes,
				indigoState.IncompatibleSampleTypes,
			)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStructuredCertificationsPersistAcrossUserUpdates(t *testing.T) {
	service, admin := integrationService(t)
	ctx := context.Background()
	created, err := service.createUser(ctx, admin, core.Map{
		"username": "certified-user", "initialPassword": "password-123456",
		"email": "certified@example.test", "fullName": "Certified User",
		"phone": "555-0140", "roles": []string{"collection_professional"},
		"qualifications": []string{"legacy-code"}, "active": true,
		"certifications": []any{
			core.Map{"code": "LX-F", "active": true, "expiresAt": "2027-01-01T00:00:00Z"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	userID := core.String(created["userId"])
	read, err := service.readUser(ctx, admin, userID)
	if err != nil {
		t.Fatal(err)
	}
	certifications := read["certifications"].([]core.Map)
	if len(certifications) != 1 || core.String(certifications[0]["code"]) != "LX-F" {
		t.Fatalf("stored certifications = %v", certifications)
	}

	if _, err := service.updateUserAccess(ctx, admin, core.Map{
		"userId": userID, "roles": []string{"collection_professional"},
		"qualifications": []string{"legacy-code"}, "active": true,
	}); err != nil {
		t.Fatal(err)
	}
	read, err = service.readUser(ctx, admin, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(read["certifications"].([]core.Map)) != 1 {
		t.Fatal("omitting certifications from a legacy access update erased structured records")
	}

	if _, err := service.updateUserAccess(ctx, admin, core.Map{
		"userId": userID, "roles": []string{"collection_professional"},
		"qualifications": []string{"legacy-code"}, "active": true,
		"certifications": []any{core.Map{"code": "LX-F", "active": false}},
	}); err != nil {
		t.Fatal(err)
	}
	read, err = service.readUser(ctx, admin, userID)
	if err != nil {
		t.Fatal(err)
	}
	updated := read["certifications"].([]core.Map)[0]
	if core.Bool(updated["active"]) {
		t.Fatalf("updated certifications = %v", read["certifications"])
	}
}
