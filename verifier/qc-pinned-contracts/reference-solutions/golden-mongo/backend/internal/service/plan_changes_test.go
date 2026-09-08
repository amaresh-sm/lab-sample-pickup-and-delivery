package service

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func TestActiveExpectedJourneyRejectsNonpositiveVersion(t *testing.T) {
	state := mongostore.NewState()
	state.Journeys["journey"] = &mongostore.Journey{
		ID: "journey", Status: "active", PlanVersion: 1,
	}

	for _, expected := range []int{-1, 0} {
		_, err := activeExpectedJourney(state, "journey", expected)
		assertHTTPErrorCode(t, err, "invalid_plan_version")
	}
}

func TestConnectionDisruptionRevalidatesPlanWithoutDirectReservation(t *testing.T) {
	state := mongostore.NewState()
	journey := &mongostore.Journey{
		ID: "journey-1", SampleIDs: []string{"sample-1"}, Status: "active",
	}
	state.Reservations["multi-stop-edge"] = &mongostore.Reservation{
		ID: "multi-stop-edge", JourneyID: journey.ID,
		ResourceID: "selected-connection", Status: "active",
	}
	state.Assignments["unfinished-leg"] = &mongostore.Assignment{
		ID: "unfinished-leg", JourneyID: journey.ID, Kind: "transport", Status: "pending",
	}

	if !journeyAffected(state, journey, "connection", "alternate-direct-connection") {
		t.Fatal("connection disruption did not invalidate the unfinished plan")
	}
	state.Assignments["unfinished-leg"].Status = "delivered"
	if journeyAffected(state, journey, "connection", "alternate-direct-connection") {
		t.Fatal("connection disruption invalidated a journey with no unfinished movement")
	}
}

func TestWorkerDisruptionRemainsScopedToAssignedWork(t *testing.T) {
	state := mongostore.NewState()
	journey := &mongostore.Journey{
		ID: "journey-1", SampleIDs: []string{"sample-1"}, Status: "active",
	}
	state.Assignments["transport-1"] = &mongostore.Assignment{
		ID: "transport-1", JourneyID: journey.ID, SampleID: "sample-1",
		Kind: "transport", WorkerID: "assigned-worker", Status: "pending",
	}

	if journeyAffected(state, journey, "worker", "unrelated-worker") {
		t.Fatal("unrelated worker disruption invalidated the plan")
	}
	if !journeyAffected(state, journey, "worker", "assigned-worker") {
		t.Fatal("assigned worker disruption did not invalidate the plan")
	}
}

func TestActiveExpectedJourneyRequiresPositiveExactVersion(t *testing.T) {
	state := mongostore.NewState()
	state.Journeys["journey-1"] = &mongostore.Journey{
		ID: "journey-1", Status: "active", PlanVersion: 3,
	}
	for _, expected := range []int{0, -1, 2} {
		_, err := activeExpectedJourney(state, "journey-1", expected)
		if err == nil {
			t.Fatalf("expected version %d was accepted", expected)
		}
	}
	if _, err := activeExpectedJourney(state, "journey-1", 3); err != nil {
		t.Fatalf("exact positive version rejected: %v", err)
	}
}

func TestValidateJourneyPlanRejectsDisconnectedCandidate(t *testing.T) {
	state, journey := validCandidatePlan()
	if err := validateJourneyPlan(state, journey); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}
	state.Connections["connection"].Active = false
	if err := validateJourneyPlan(state, journey); err == nil {
		t.Fatal("candidate using an inactive connection was accepted")
	}
}

func TestValidateJourneyPlanRejectsMismatchedAssignmentSequence(t *testing.T) {
	state, journey := validCandidatePlan()
	state.Assignments["transport"].ToLocationID = "origin"
	if err := validateJourneyPlan(state, journey); err == nil {
		t.Fatal("candidate with mismatched transport endpoints was accepted")
	}
}

func TestAddDuplicateHubStopPreservesValidMovementPlan(t *testing.T) {
	state, journey := validCandidatePlan()
	now := journey.CreatedAt
	delete(state.Connections, "connection")
	delete(state.Capacities, "connection")
	delete(state.Reservations, "connection")
	state.Locations["hub"] = transferPointLocation("hub")
	state.Connections["first-hop"] = &mongostore.Connection{
		ID: "first-hop", FromLocationID: "origin", ToLocationID: "hub",
		TravelMinutes: 15, Active: true,
	}
	state.Connections["second-hop"] = &mongostore.Connection{
		ID: "second-hop", FromLocationID: "hub", ToLocationID: "laboratory",
		TravelMinutes: 15, Active: true,
	}
	for _, resourceID := range []string{"hub", "first-hop", "second-hop"} {
		state.Capacities[resourceID] = &mongostore.Capacity{
			ResourceID: resourceID, CapacityUnits: 4, ReservedUnits: 1,
		}
		state.Reservations[resourceID] = &mongostore.Reservation{
			ID: resourceID, JourneyID: journey.ID, ResourceID: resourceID,
			Units: 1, Status: "active",
		}
	}
	state.Stops["hub"] = &mongostore.JourneyStop{
		ID: "hub", JourneyID: journey.ID, LocationID: "hub",
		StopType: "handoff", Sequence: 2, EstimatedAt: now.Add(15 * time.Minute),
	}
	state.Stops["delivery"].Sequence = 3
	state.Assignments["transport"].ToLocationID = "hub"
	state.Assignments["transport-2"] = newAssignment(
		"transport-2", journey.ID, "sample", "transport", 2,
		"hub", "laboratory", "delivery", now,
	)
	journey.RouteMode = "multi_stop"

	if err := addPlanStop(state, journey, core.Map{
		"locationId": "hub", "position": 1,
	}); err != nil {
		t.Fatalf("add canonical duplicate hub stop: %v", err)
	}
	if err := rebuildEditedJourneyRoute(state, journey); err != nil {
		t.Fatalf("rebuild canonical duplicate hub route: %v", err)
	}
	if err := validateJourneyPlan(state, journey); err != nil {
		t.Fatalf("canonical duplicate hub candidate rejected: %v", err)
	}
	if got := len(stopsForJourney(state, journey.ID)); got != 4 {
		t.Fatalf("stop count=%d, want 4", got)
	}
	if reservation := state.Reservations["hub"]; reservation.Units != 1 {
		t.Fatalf("hub reservation units=%d, want unchanged 1", reservation.Units)
	}
}

func TestAddNewConnectedStopRebuildsMovementsAndReservations(t *testing.T) {
	state, journey := validCandidatePlan()
	addRouteNodeAndConnection(state, "hub", "origin", "hub", "origin-hub", 10, 2)
	addRouteConnection(state, "hub", "laboratory", "hub-laboratory", 12, 3)

	if err := addPlanStop(state, journey, core.Map{
		"locationId": "hub", "position": 1, "dwellMinutes": 4,
	}); err != nil {
		t.Fatalf("add stop: %v", err)
	}
	if err := rebuildEditedJourneyRoute(state, journey); err != nil {
		t.Fatalf("rebuild edited route: %v", err)
	}
	if err := validateJourneyPlan(state, journey); err != nil {
		t.Fatalf("rebuilt add-stop candidate invalid: %v", err)
	}
	assertSampleTransportPath(t, state, journey.ID, "sample", []string{"origin", "hub", "laboratory"})
	if journey.TotalCostUnits != 5 {
		t.Fatalf("route cost=%v, want 5", journey.TotalCostUnits)
	}
	if journey.RouteMode != "multi_stop" {
		t.Fatalf("route mode=%q, want multi_stop", journey.RouteMode)
	}
}

func TestReorderStopsRebuildsAConnectedAlternative(t *testing.T) {
	state, journey := connectedFourStopCandidate()
	if err := reorderPlanStops(state, journey, core.Map{
		"locationIds": []string{"origin", "node-b", "node-a", "laboratory"},
	}); err != nil {
		t.Fatalf("reorder stops: %v", err)
	}
	if err := rebuildEditedJourneyRoute(state, journey); err != nil {
		t.Fatalf("rebuild reordered route: %v", err)
	}
	if err := validateJourneyPlan(state, journey); err != nil {
		t.Fatalf("rebuilt reordered candidate invalid: %v", err)
	}
	assertSampleTransportPath(
		t, state, journey.ID, "sample",
		[]string{"origin", "node-b", "node-a", "laboratory"},
	)
}

func TestRemoveStopRebuildsShorterRoute(t *testing.T) {
	state, journey := connectedFourStopCandidate()
	addRouteConnection(state, "node-a", "laboratory", "node-a-laboratory", 9, 1)
	if err := removePlanStop(state, journey, core.Map{"locationId": "node-b"}); err != nil {
		t.Fatalf("remove stop: %v", err)
	}
	if err := rebuildEditedJourneyRoute(state, journey); err != nil {
		t.Fatalf("rebuild shortened route: %v", err)
	}
	if err := validateJourneyPlan(state, journey); err != nil {
		t.Fatalf("rebuilt remove-stop candidate invalid: %v", err)
	}
	assertSampleTransportPath(
		t, state, journey.ID, "sample",
		[]string{"origin", "node-a", "laboratory"},
	)
}

func TestMarkCompletedStopsDerivesOriginFromTransportAndCustody(t *testing.T) {
	state, journey := validCandidatePlan()
	sealedAt := time.Date(2026, 7, 26, 9, 5, 0, 0, time.UTC)
	completedAt := sealedAt.Add(20 * time.Minute)
	state.Samples["sample"].SealedAt = &sealedAt
	state.Samples["sample"].CurrentHolderID = "delivery"
	state.Assignments["transport"].Status = "delivered"
	state.Assignments["transport"].CompletedAt = &completedAt

	origin := markCompletedStopsAndCurrentOrigin(state, journey)
	if origin != "laboratory" {
		t.Fatalf("current origin=%q, want laboratory", origin)
	}
	stops := stopsForJourney(state, journey.ID)
	if stops[0].CompletedAt == nil || stops[1].CompletedAt == nil {
		t.Fatal("completed pickup and movement were not reflected on the old route")
	}
}

func addRouteNodeAndConnection(
	state *mongostore.State,
	nodeID, from, to, connectionID string,
	minutes int,
	cost float64,
) {
	state.Locations[nodeID] = transferPointLocation(nodeID)
	state.Capacities[nodeID] = &mongostore.Capacity{ResourceID: nodeID, CapacityUnits: 4}
	addRouteConnection(state, from, to, connectionID, minutes, cost)
}

func addRouteConnection(
	state *mongostore.State,
	from, to, connectionID string,
	minutes int,
	cost float64,
) {
	state.Connections[connectionID] = &mongostore.Connection{
		ID: connectionID, FromLocationID: from, ToLocationID: to,
		TravelMinutes: minutes, CostUnits: cost, Active: true,
	}
	state.Capacities[connectionID] = &mongostore.Capacity{
		ResourceID: connectionID, CapacityUnits: 4,
	}
}

func connectedFourStopCandidate() (*mongostore.State, *mongostore.Journey) {
	state, journey := validCandidatePlan()
	now := journey.CreatedAt
	delete(state.Connections, "connection")
	delete(state.Capacities, "connection")
	delete(state.Reservations, "connection")
	addRouteNodeAndConnection(state, "node-a", "origin", "node-a", "origin-a", 5, 1)
	addRouteNodeAndConnection(state, "node-b", "node-a", "node-b", "a-b", 5, 1)
	addRouteConnection(state, "node-b", "laboratory", "b-laboratory", 5, 1)
	addRouteConnection(state, "origin", "node-b", "origin-b", 6, 1)
	addRouteConnection(state, "node-b", "node-a", "b-a", 6, 1)
	addRouteConnection(state, "node-a", "laboratory", "a-laboratory", 6, 1)

	state.Stops["node-a"] = &mongostore.JourneyStop{
		ID: "node-a", JourneyID: journey.ID, LocationID: "node-a",
		StopType: "handoff", Sequence: 2, EstimatedAt: now.Add(5 * time.Minute),
	}
	state.Stops["node-b"] = &mongostore.JourneyStop{
		ID: "node-b", JourneyID: journey.ID, LocationID: "node-b",
		StopType: "handoff", Sequence: 3, EstimatedAt: now.Add(10 * time.Minute),
	}
	state.Stops["delivery"].Sequence = 4
	state.Stops["delivery"].EstimatedAt = now.Add(15 * time.Minute)
	journey.EstimatedDeliveryAt = now.Add(15 * time.Minute)
	journey.RouteMode = "multi_stop"

	delete(state.Assignments, "transport")
	delete(state.Assignments, "laboratory-work")
	for index, endpoints := range [][2]string{
		{"origin", "node-a"}, {"node-a", "node-b"}, {"node-b", "laboratory"},
	} {
		id := "transport-" + core.String(index+1)
		state.Assignments[id] = newAssignment(
			id, journey.ID, "sample", "transport", index+1,
			endpoints[0], endpoints[1], "delivery", now,
		)
	}
	for _, specification := range []struct {
		id, kind, from, to, worker string
		sequence                   int
	}{
		{"node-a-work", "node", "origin", "node-a", "", 1},
		{"node-b-work", "node", "node-a", "node-b", "", 2},
		{"lab-work", "laboratory", "node-b", "laboratory", "laboratory-worker", 4},
	} {
		state.Assignments[specification.id] = newAssignment(
			specification.id, journey.ID, "sample", specification.kind,
			specification.sequence, specification.from, specification.to,
			specification.worker, now,
		)
	}
	for _, resourceID := range []string{
		"origin-a", "a-b", "b-laboratory", "node-a", "node-b",
	} {
		state.Reservations[resourceID] = &mongostore.Reservation{
			ID: resourceID, JourneyID: journey.ID, ResourceID: resourceID,
			Units: 1, Status: "active",
		}
		state.Capacities[resourceID].ReservedUnits = 1
	}
	return state, journey
}

func assertSampleTransportPath(
	t *testing.T,
	state *mongostore.State,
	journeyID, sampleID string,
	locations []string,
) {
	t.Helper()
	assignments := []*mongostore.Assignment{}
	for _, assignment := range state.Assignments {
		if assignment.JourneyID == journeyID && assignment.SampleID == sampleID &&
			assignment.Kind == "transport" && assignment.Status != "cancelled" {
			assignments = append(assignments, assignment)
		}
	}
	sort.Slice(assignments, func(i, j int) bool {
		return assignments[i].LegSequence < assignments[j].LegSequence
	})
	if len(assignments) != len(locations)-1 {
		t.Fatalf("transport count=%d, want %d", len(assignments), len(locations)-1)
	}
	for index, assignment := range assignments {
		if assignment.FromLocationID != locations[index] ||
			assignment.ToLocationID != locations[index+1] {
			t.Fatalf("leg %d=%s→%s, want %s→%s", index+1,
				assignment.FromLocationID, assignment.ToLocationID,
				locations[index], locations[index+1])
		}
	}
}

func TestReserveRouteResourcesPreflightsWholeReplacement(t *testing.T) {
	state := mongostore.NewState()
	state.Capacities["available"] = &mongostore.Capacity{
		ResourceID: "available", CapacityUnits: 2,
	}
	state.Capacities["exhausted"] = &mongostore.Capacity{
		ResourceID: "exhausted", CapacityUnits: 1, ReservedUnits: 1,
	}
	err := reserveRouteResources(state, "replacement", []string{"available", "exhausted"}, false, 1)
	if err == nil {
		t.Fatal("replacement reservation unexpectedly succeeded")
	}
	if state.Capacities["available"].ReservedUnits != 0 {
		t.Fatal("replacement partially reserved capacity before discovering a later failure")
	}
	if len(state.Reservations) != 0 {
		t.Fatal("replacement left partial reservations behind")
	}
}

func TestBatchLaboratoryExceptionPreservesRemainingActiveSample(t *testing.T) {
	state, journey := validCandidatePlan()
	now := journey.CreatedAt
	state.Requests["request-2"] = &mongostore.CollectionRequest{
		ID: "request-2", RequestedAt: now, DeliveryDeadline: now.Add(2 * time.Hour),
	}
	state.Samples["sample-2"] = &mongostore.Sample{
		ID: "sample-2", RequestID: "request-2", SampleTypeID: "type",
		ContainerType: "vial", Status: "planned", ActiveJourneyID: journey.ID,
		TargetLaboratoryID: "laboratory",
	}
	state.Assignments["collection-2"] = newAssignment(
		"collection-2", journey.ID, "sample-2", "collection", 0,
		"", "", "collector", now,
	)
	state.Assignments["transport-2"] = newAssignment(
		"transport-2", journey.ID, "sample-2", "transport", 1,
		"origin", "laboratory", "delivery", now,
	)
	state.Assignments["laboratory-2"] = newAssignment(
		"laboratory-2", journey.ID, "sample-2", "laboratory", 2,
		"origin", "laboratory", "laboratory-worker", now,
	)
	journey.SampleIDs = []string{"sample", "sample-2"}
	journey.CapacityUsed = 2
	journey.RouteMode = "batched"
	for _, reservation := range state.Reservations {
		reservation.Units = 2
		state.Capacities[reservation.ResourceID].ReservedUnits = 2
	}
	completedAt := now.Add(time.Minute)
	state.Assignments["collection"].Status = "completed"
	state.Assignments["collection"].CompletedAt = &completedAt

	if err := removeSampleFromActiveBatch(state, journey, "sample"); err != nil {
		t.Fatalf("remove exception sample: %v", err)
	}
	state.Samples["sample"].Status = "exception"
	state.Samples["sample"].ActiveJourneyID = ""

	if journey.Status != "active" || journey.PrimarySampleID != "sample-2" ||
		journey.CapacityUsed != 1 || len(journey.SampleIDs) != 1 {
		t.Fatalf("remaining journey is incoherent: %+v", journey)
	}
	for _, reservation := range state.Reservations {
		if reservation.Status != "active" || reservation.Units != 1 {
			t.Fatalf("remaining reservation=%+v, want one active unit", reservation)
		}
	}
	for _, assignment := range assignmentsForSample(state, "sample") {
		if assignment.JourneyID != journey.ID {
			t.Fatalf("exception assignment lost historical journey linkage: %+v", assignment)
		}
		if assignment.ID == "collection" && assignment.Status != "completed" {
			t.Fatalf("completed exception assignment was mutated: %+v", assignment)
		}
		if assignment.ID != "collection" && assignment.Status != "cancelled" {
			t.Fatalf("unfinished exception assignment was not cancelled: %+v", assignment)
		}
	}
	if err := validateJourneyPlan(state, journey); err != nil {
		t.Fatalf("remaining batch plan invalid after exception: %v", err)
	}
}

func TestRemovingOneSampleFromThreeSampleBatchRemainsBatched(t *testing.T) {
	state := mongostore.NewState()
	journey := &mongostore.Journey{
		ID: "journey", PrimarySampleID: "sample-1",
		SampleIDs: []string{"sample-1", "sample-2", "sample-3"},
		RouteMode: "batched", Status: "active", CapacityUsed: 3,
	}

	if err := removeSampleFromActiveBatch(state, journey, "sample-1"); err != nil {
		t.Fatalf("remove sample from three-sample batch: %v", err)
	}
	if journey.RouteMode != "batched" || journey.CapacityUsed != 2 ||
		len(journey.SampleIDs) != 2 || journey.PrimarySampleID != "sample-2" {
		t.Fatalf("remaining two-sample journey is not a coherent batch: %+v", journey)
	}
}

// A replan of an unsealed sample cannot schedule collection before the sample's
// collection window opens, so the replacement plan's stops and arrival estimate
// depart from the window opening rather than from the disruption time.
func TestReplanDepartsNoEarlierThanTheCollectionWindow(t *testing.T) {
	occurredAt := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	windowOpensAt := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)

	state := mongostore.NewState()
	state.Requests["request"] = &mongostore.CollectionRequest{
		ID: "request", RequestedAt: occurredAt,
		EarliestCollectionAt: windowOpensAt,
		LatestCollectionAt:   windowOpensAt.Add(time.Hour),
		DeliveryDeadline:     windowOpensAt.Add(3 * time.Hour),
	}
	state.Samples["unsealed"] = &mongostore.Sample{ID: "unsealed", RequestID: "request"}
	journey := &mongostore.Journey{
		ID: "journey", PrimarySampleID: "unsealed", SampleIDs: []string{"unsealed"},
	}

	if got := replanDepartureTime(state, journey, occurredAt); !got.Equal(windowOpensAt) {
		t.Fatalf(
			"replan departure = %s, want the collection window opening %s",
			formatTime(got), formatTime(windowOpensAt),
		)
	}

	// A window that has already opened imposes no bound: the replan departs at the
	// disruption.
	state.Requests["request"].EarliestCollectionAt = occurredAt.Add(-time.Hour)
	if got := replanDepartureTime(state, journey, occurredAt); !got.Equal(occurredAt) {
		t.Fatalf("replan departure = %s, want the disruption time", formatTime(got))
	}

	// A sealed sample is already in custody, so its window no longer constrains
	// the replan at all.
	state.Requests["request"].EarliestCollectionAt = windowOpensAt
	state.Samples["unsealed"].SealedAt = pointerTime(occurredAt)
	if got := replanDepartureTime(state, journey, occurredAt); !got.Equal(occurredAt) {
		t.Fatalf(
			"a sealed sample's window constrained the replan: %s",
			formatTime(got),
		)
	}

	// In a batch the latest still-uncollected window wins, so no sample is
	// scheduled for collection before it can be collected.
	state.Samples["unsealed"].SealedAt = nil
	state.Requests["later"] = &mongostore.CollectionRequest{
		ID: "later", RequestedAt: occurredAt,
		EarliestCollectionAt: windowOpensAt.Add(30 * time.Minute),
		LatestCollectionAt:   windowOpensAt.Add(90 * time.Minute),
		DeliveryDeadline:     windowOpensAt.Add(3 * time.Hour),
	}
	state.Samples["later"] = &mongostore.Sample{ID: "later", RequestID: "later"}
	journey.SampleIDs = append(journey.SampleIDs, "later")
	want := windowOpensAt.Add(30 * time.Minute)
	if got := replanDepartureTime(state, journey, occurredAt); !got.Equal(want) {
		t.Fatalf("batched replan departure = %s, want %s", formatTime(got), formatTime(want))
	}
}

// The replacement plan built by a reroute really departs from the window opening,
// so its arrival estimate is not optimistic.
func TestRerouteOfAnUnsealedSampleUsesTheCollectionWindowForItsEstimate(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "replan-requester", []string{"requester"}, nil)
	createTestUser(t, service, admin, "replan-collector", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "replan-courier", []string{"delivery_partner"}, nil)
	createTestUser(t, service, admin, "replan-operator", []string{"laboratory_team"}, nil)
	dispatcher := createTestUser(t, service, admin, "replan-dispatcher", []string{"dispatcher"}, nil)
	network := buildServiceableNetwork(t, service, admin, "replan")

	created, err := service.createCollectionRequest(ctx, requester, core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": network.sampleTypeID,
		"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T13:00:00Z",
		"earliestCollectionAt": "2026-07-30T11:00:00Z",
		"latestCollectionAt":   "2026-07-30T12:00:00Z",
		"containerType":        "amber-container", "temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "idempotencyKey": "replan-window-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	journeyID := core.String(created["journeyId"])

	// Reroute at 09:00, while the sample is still uncollected.
	rerouted, err := service.rerouteJourney(ctx, dispatcher, core.Map{
		"journeyId": journeyID, "expectedPlanVersion": 1,
		"reason": "connection maintenance", "idempotencyKey": "replan-window-reroute",
	})
	if err != nil {
		t.Fatal(err)
	}
	replacementID := core.String(rerouted["replacementPlanId"])
	if replacementID == "" {
		t.Fatalf("reroute produced no replacement plan: %v", rerouted)
	}

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		replacement := state.Journeys[replacementID]
		stops := stopsForJourney(state, replacementID)
		if len(stops) == 0 {
			t.Fatal("replacement plan has no stops")
		}
		windowOpensAt := state.Requests[core.String(created["requestId"])].EarliestCollectionAt
		if stops[0].EstimatedAt.Before(windowOpensAt) {
			t.Fatalf(
				"replan scheduled collection at %s, before the window opened at %s",
				formatTime(stops[0].EstimatedAt), formatTime(windowOpensAt),
			)
		}
		if !stops[0].EstimatedAt.Equal(windowOpensAt) {
			t.Fatalf(
				"replan pickup = %s, want the window opening %s",
				formatTime(stops[0].EstimatedAt), formatTime(windowOpensAt),
			)
		}
		if replacement.EstimatedDeliveryAt.Before(windowOpensAt) {
			t.Fatalf(
				"replan arrival estimate %s precedes the collection window",
				formatTime(replacement.EstimatedDeliveryAt),
			)
		}
		// The 30-minute route from the window opening lands at 11:30, not 09:30.
		if formatTime(replacement.EstimatedDeliveryAt) != "2026-07-30T11:30:00Z" {
			t.Fatalf(
				"replan arrival estimate = %s, want 2026-07-30T11:30:00Z",
				formatTime(replacement.EstimatedDeliveryAt),
			)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReportDisruptionRejectsOperationalWorkerRoles(t *testing.T) {
	service := &Service{}
	_, err := service.reportDisruption(
		context.Background(),
		core.Principal{ID: "courier", Roles: []string{"delivery_partner"}},
		core.Map{},
	)
	var apiErr *core.HTTPError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 {
		t.Fatalf("delivery worker disruption report error=%v, want forbidden", err)
	}
}

func validCandidatePlan() (*mongostore.State, *mongostore.Journey) {
	state := mongostore.NewState()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	state.Locations["origin"] = collectionPointLocation("origin", 0, 0)
	state.Locations["laboratory"] = laboratoryLocation("laboratory")
	state.Connections["connection"] = &mongostore.Connection{
		ID: "connection", FromLocationID: "origin", ToLocationID: "laboratory",
		TravelMinutes: 30, Active: true,
	}
	for _, resourceID := range []string{"origin", "connection", "laboratory"} {
		state.Capacities[resourceID] = &mongostore.Capacity{
			ResourceID: resourceID, CapacityUnits: 4, ReservedUnits: 1,
		}
		state.Reservations[resourceID] = &mongostore.Reservation{
			ID: resourceID, JourneyID: "journey", ResourceID: resourceID,
			Units: 1, Status: "active",
		}
	}
	state.Users["collector"] = &mongostore.User{
		ID: "collector", Roles: []string{"collection_professional"},
		Active: true, Available: true, CapacityUnits: 4,
	}
	state.Users["delivery"] = &mongostore.User{
		ID: "delivery", Roles: []string{"delivery_partner"},
		Active: true, Available: true, CapacityUnits: 4,
	}
	state.Users["laboratory-worker"] = &mongostore.User{
		ID: "laboratory-worker", Roles: []string{"laboratory_team"},
		Active: true, Available: true, CapacityUnits: 4,
	}
	state.SampleTypes["type"] = &mongostore.SampleType{
		ID: "type", ContainerType: "vial", BatchingAllowed: true,
	}
	state.Acceptances["acceptance"] = &mongostore.Acceptance{
		ID: "acceptance", LaboratoryID: "laboratory", SampleTypeID: "type", Accepts: true,
	}
	state.Requests["request"] = &mongostore.CollectionRequest{
		ID: "request", RequestedAt: now, DeliveryDeadline: now.Add(2 * time.Hour),
	}
	state.Samples["sample"] = &mongostore.Sample{
		ID: "sample", RequestID: "request", SampleTypeID: "type",
		ContainerType: "vial", Status: "planned", ActiveJourneyID: "journey",
		TargetLaboratoryID: "laboratory",
	}
	journey := &mongostore.Journey{
		ID: "journey", PrimarySampleID: "sample", SampleIDs: []string{"sample"},
		PlanVersion: 1, RouteMode: "direct", Status: "active",
		EstimatedDeliveryAt: now.Add(30 * time.Minute),
		CapacityUsed:        1, CapacityAvailable: 4, CreatedAt: now, UpdatedAt: now,
	}
	state.Journeys[journey.ID] = journey
	state.Stops["pickup"] = &mongostore.JourneyStop{
		ID: "pickup", JourneyID: journey.ID, LocationID: "origin",
		StopType: "pickup", Sequence: 1, EstimatedAt: now,
	}
	state.Stops["delivery"] = &mongostore.JourneyStop{
		ID: "delivery", JourneyID: journey.ID, LocationID: "laboratory",
		StopType: "delivery", Sequence: 2, EstimatedAt: now.Add(30 * time.Minute),
	}
	state.Assignments["collection"] = newAssignment(
		"collection", journey.ID, "sample", "collection", 0, "", "", "collector", now,
	)
	state.Assignments["transport"] = newAssignment(
		"transport", journey.ID, "sample", "transport", 1, "origin", "laboratory", "delivery", now,
	)
	state.Assignments["laboratory-work"] = newAssignment(
		"laboratory-work", journey.ID, "sample", "laboratory", 2,
		"origin", "laboratory", "laboratory-worker", now,
	)
	return state, journey
}
