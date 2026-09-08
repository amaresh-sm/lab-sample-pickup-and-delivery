package service

import (
	"errors"
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func TestResolveHandlingAssignmentAllowsUnscopedFieldObservation(t *testing.T) {
	state := mongostore.NewState()
	state.Samples["sample-1"] = &mongostore.Sample{
		ID: "sample-1", Status: "planned", ActiveJourneyID: "journey-1",
		CurrentHolderID: "observing-worker",
	}
	state.Journeys["journey-1"] = &mongostore.Journey{ID: "journey-1", Status: "active"}
	state.Assignments["transport-1"] = &mongostore.Assignment{
		ID: "transport-1", SampleID: "sample-1", JourneyID: "journey-1", Kind: "transport",
		LegSequence: 1, WorkerID: "assigned-worker", Status: "pending",
	}

	assignment, err := resolveHandlingAssignment(state, "sample-1", "", "observing-worker")
	if err != nil {
		t.Fatalf("resolve omitted assignment: %v", err)
	}
	if assignment.ID != "transport-1" {
		t.Fatalf("assignment=%q, want transport-1", assignment.ID)
	}
}

func TestResolveHandlingAssignmentRejectsUnrelatedUnscopedActor(t *testing.T) {
	state := mongostore.NewState()
	state.Samples["sample-1"] = &mongostore.Sample{
		ID: "sample-1", Status: "planned", ActiveJourneyID: "journey-1",
		CurrentHolderID: "current-holder",
	}
	state.Journeys["journey-1"] = &mongostore.Journey{ID: "journey-1", Status: "active"}
	state.Assignments["transport-1"] = &mongostore.Assignment{
		ID: "transport-1", SampleID: "sample-1", JourneyID: "journey-1", Kind: "transport",
		LegSequence: 1, WorkerID: "assigned-worker", Status: "pending",
	}

	_, err := resolveHandlingAssignment(state, "sample-1", "", "unrelated-worker")
	assertHTTPErrorCode(t, err, "forbidden")
}

func TestResolveHandlingAssignmentRequiresOwnerWhenExplicit(t *testing.T) {
	state := mongostore.NewState()
	state.Samples["sample-1"] = &mongostore.Sample{
		ID: "sample-1", Status: "planned", ActiveJourneyID: "journey-1",
	}
	state.Journeys["journey-1"] = &mongostore.Journey{ID: "journey-1", Status: "active"}
	state.Assignments["transport-1"] = &mongostore.Assignment{
		ID: "transport-1", SampleID: "sample-1", JourneyID: "journey-1", Kind: "transport",
		LegSequence: 1, WorkerID: "assigned-worker", Status: "pending",
	}

	_, err := resolveHandlingAssignment(state, "sample-1", "transport-1", "observing-worker")
	var apiErr *core.HTTPError
	if !errors.As(err, &apiErr) {
		t.Fatalf("explicit non-owner error=%v, want HTTPError", err)
	}
	if apiErr.Status != 403 || apiErr.Code != "forbidden" {
		t.Fatalf("explicit non-owner error=%+v, want 403 forbidden", apiErr)
	}
}

func TestResolveHandlingAssignmentRejectsInactiveExplicitAssignment(t *testing.T) {
	for _, test := range []struct {
		name, status, journeyID string
	}{
		{name: "completed", status: "completed", journeyID: "journey-1"},
		{name: "cancelled", status: "cancelled", journeyID: "journey-1"},
		{name: "superseded journey", status: "accepted", journeyID: "journey-old"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := mongostore.NewState()
			state.Samples["sample-1"] = &mongostore.Sample{
				ID: "sample-1", Status: "in_transit", ActiveJourneyID: "journey-1",
			}
			state.Journeys["journey-1"] = &mongostore.Journey{ID: "journey-1", Status: "active"}
			state.Assignments["transport-1"] = &mongostore.Assignment{
				ID: "transport-1", SampleID: "sample-1", JourneyID: test.journeyID,
				Kind: "transport", WorkerID: "assigned-worker", Status: test.status,
			}

			_, err := resolveHandlingAssignment(
				state, "sample-1", "transport-1", "assigned-worker",
			)
			assertHTTPErrorCode(t, err, "assignment_inactive")
		})
	}
}

func TestHandlingCheckpointTerminalSampleStates(t *testing.T) {
	for _, status := range []string{"received", "cancelled", "expired", "exception"} {
		if !handlingCheckpointTerminalSample(&mongostore.Sample{Status: status}) {
			t.Errorf("status %q should be terminal for handling", status)
		}
	}
	for _, status := range []string{"planned", "sealed", "in_transit", "delivered"} {
		if handlingCheckpointTerminalSample(&mongostore.Sample{Status: status}) {
			t.Errorf("status %q should permit an active-journey handling checkpoint", status)
		}
	}
}

func TestLegalAssignmentTransitionRejectsSkippedAndTerminalTransitions(t *testing.T) {
	tests := []struct {
		name, kind, current, wanted string
		directFinal                 bool
		want                        bool
	}{
		{name: "accepted pickup", kind: "transport", current: "accepted", wanted: "picked_up", want: true},
		{name: "pickup progress", kind: "transport", current: "picked_up", wanted: "in_progress", want: true},
		{name: "progress arrival", kind: "transport", current: "in_progress", wanted: "arrived", want: true},
		{name: "arrival delivery", kind: "transport", current: "arrived", wanted: "delivered", want: true},
		{name: "direct final compatibility", kind: "transport", current: "accepted", wanted: "delivered", directFinal: true, want: true},
		{name: "non-direct skip", kind: "transport", current: "accepted", wanted: "delivered", want: false},
		{name: "reverse", kind: "transport", current: "arrived", wanted: "picked_up", want: false},
		{name: "terminal replay", kind: "transport", current: "delivered", wanted: "delivered", want: false},
		{name: "node starts", kind: "node", current: "accepted", wanted: "in_progress", want: true},
		{name: "node skips", kind: "node", current: "accepted", wanted: "completed", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := legalAssignmentTransition(
				test.kind, test.current, test.wanted, test.directFinal,
			)
			if got != test.want {
				t.Fatalf("legalAssignmentTransition()=%t, want %t", got, test.want)
			}
		})
	}
}

func TestPrepareTransportReleaseRequiresBoundaryETAAndAcceptance(t *testing.T) {
	state := mongostore.NewState()
	acceptedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	state.Users["courier"] = &mongostore.User{
		ID: "courier", Roles: []string{"delivery_partner"}, Active: true,
	}
	state.Samples["sample"] = &mongostore.Sample{
		ID: "sample", Status: "sealed", ActiveJourneyID: "journey", CurrentHolderID: "courier",
	}
	state.Assignments["leg-1"] = &mongostore.Assignment{
		ID: "leg-1", SampleID: "sample", JourneyID: "journey", Kind: "transport",
		LegSequence: 1, ToLocationID: "node", WorkerID: "courier",
		Status: "accepted", AcceptedAt: &acceptedAt,
	}
	state.Assignments["leg-2"] = &mongostore.Assignment{
		ID: "leg-2", SampleID: "sample", JourneyID: "journey", Kind: "transport",
		LegSequence: 2, FromLocationID: "node", Status: "pending",
	}
	state.Stops["wrong-boundary"] = &mongostore.JourneyStop{
		ID: "wrong-boundary", JourneyID: "journey", LocationID: "somewhere-else",
		Sequence: 2, EstimatedAt: acceptedAt.Add(5 * time.Minute),
	}
	state.Stops["node-boundary"] = &mongostore.JourneyStop{
		ID: "node-boundary", JourneyID: "journey", LocationID: "node",
		Sequence: 2, EstimatedAt: acceptedAt.Add(30 * time.Minute),
	}
	principal := core.Principal{ID: "courier", Roles: []string{"delivery_partner"}}

	err := prepareTransportReleaseForHandoff(
		state, state.Samples["sample"], principal,
		state.Assignments["leg-1"], state.Assignments["leg-2"],
		acceptedAt.Add(-time.Minute),
	)
	assertHTTPErrorCode(t, err, "invalid_handoff_time")

	err = prepareTransportReleaseForHandoff(
		state, state.Samples["sample"], principal,
		state.Assignments["leg-1"], state.Assignments["leg-2"],
		acceptedAt.Add(29*time.Minute),
	)
	assertHTTPErrorCode(t, err, "transport_leg_incomplete")
	if state.Assignments["leg-1"].Status != "accepted" {
		t.Fatalf("premature handoff advanced assignment: %+v", state.Assignments["leg-1"])
	}

	if err := prepareTransportReleaseForHandoff(
		state, state.Samples["sample"], principal,
		state.Assignments["leg-1"], state.Assignments["leg-2"],
		acceptedAt.Add(30*time.Minute),
	); err != nil {
		t.Fatalf("boundary-time handoff: %v", err)
	}
	if state.Assignments["leg-1"].Status != "arrived" {
		t.Fatalf("assignment status=%q, want arrived", state.Assignments["leg-1"].Status)
	}
}

func TestPrepareTransportReleaseRequiresPreviousLegCompletion(t *testing.T) {
	state := mongostore.NewState()
	acceptedAt := time.Date(2026, 7, 26, 9, 20, 0, 0, time.UTC)
	priorCompletedAt := time.Date(2026, 7, 26, 9, 40, 0, 0, time.UTC)
	state.Users["courier"] = &mongostore.User{
		ID: "courier", Roles: []string{"delivery_partner"}, Active: true,
	}
	state.Samples["sample"] = &mongostore.Sample{
		ID: "sample", Status: "in_transit", ActiveJourneyID: "journey", CurrentHolderID: "courier",
	}
	state.Assignments["leg-1"] = &mongostore.Assignment{
		ID: "leg-1", SampleID: "sample", JourneyID: "journey", Kind: "transport",
		LegSequence: 1, Status: "delivered", CompletedAt: &priorCompletedAt,
	}
	state.Assignments["leg-2"] = &mongostore.Assignment{
		ID: "leg-2", SampleID: "sample", JourneyID: "journey", Kind: "transport",
		LegSequence: 2, FromLocationID: "node", ToLocationID: "lab",
		WorkerID: "courier", Status: "accepted", AcceptedAt: &acceptedAt,
	}
	state.Assignments["lab-work"] = &mongostore.Assignment{
		ID: "lab-work", SampleID: "sample", JourneyID: "journey", Kind: "laboratory",
		LegSequence: 3, ToLocationID: "lab", Status: "pending",
	}
	state.Stops["lab-boundary"] = &mongostore.JourneyStop{
		ID: "lab-boundary", JourneyID: "journey", LocationID: "lab",
		Sequence: 3, EstimatedAt: acceptedAt.Add(15 * time.Minute),
	}
	principal := core.Principal{ID: "courier", Roles: []string{"delivery_partner"}}

	err := prepareTransportReleaseForHandoff(
		state, state.Samples["sample"], principal,
		state.Assignments["leg-2"], state.Assignments["lab-work"],
		priorCompletedAt.Add(-time.Minute),
	)
	assertHTTPErrorCode(t, err, "prior_leg_incomplete")

	if err := prepareTransportReleaseForHandoff(
		state, state.Samples["sample"], principal,
		state.Assignments["leg-2"], state.Assignments["lab-work"],
		priorCompletedAt,
	); err != nil {
		t.Fatalf("handoff after prior completion: %v", err)
	}
}

func TestValidateHandoffRouteRequiresSameSampleAndAdjacentAssignments(t *testing.T) {
	state := mongostore.NewState()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	state.Users["collector"] = &mongostore.User{
		ID: "collector", Roles: []string{"collection_professional"}, Active: true,
	}
	state.Users["courier"] = &mongostore.User{
		ID: "courier", Roles: []string{"delivery_partner"}, Active: true, Available: true, CapacityUnits: 1,
	}
	state.Samples["sample"] = &mongostore.Sample{
		ID: "sample", ActiveJourneyID: "journey", CurrentHolderID: "collector",
	}
	state.Assignments["collection"] = &mongostore.Assignment{
		ID: "collection", SampleID: "sample", JourneyID: "journey",
		Kind: "collection", WorkerID: "collector", Status: "completed",
	}
	state.Assignments["transport"] = &mongostore.Assignment{
		ID: "transport", SampleID: "sample", JourneyID: "journey",
		Kind: "transport", LegSequence: 1, WorkerID: "other-offered-courier", Status: "pending",
		CreatedAt: now,
	}

	err := validateHandoffRoute(
		state, state.Samples["sample"], "collector", "courier", "collection", "transport",
	)
	assertHTTPErrorCode(t, err, "receiver_assignment_mismatch")
	err = acceptHandoffReceiverAssignment(state, handoffActor("courier"), "transport", "courier", now)
	assertHTTPErrorCode(t, err, "offer_not_available")

	state.Assignments["transport"].WorkerID = ""
	if err := validateHandoffRoute(
		state, state.Samples["sample"], "collector", "courier", "collection", "transport",
	); err != nil {
		t.Fatalf("valid adjacent handoff: %v", err)
	}
	if err := acceptHandoffReceiverAssignment(
		state, handoffActor("courier"), "transport", "courier", now,
	); err != nil {
		t.Fatalf("accept handoff receiver assignment: %v", err)
	}
	if assignment := state.Assignments["transport"]; assignment.WorkerID != "courier" ||
		assignment.Status != "accepted" ||
		assignment.AcceptedAt == nil {
		t.Fatalf("receiver did not own accepted next assignment: %+v", assignment)
	}

	state.Assignments["transport"].LegSequence = 2
	err = validateHandoffRoute(
		state, state.Samples["sample"], "collector", "courier", "collection", "transport",
	)
	assertHTTPErrorCode(t, err, "nonadjacent_handoff")

	state.Assignments["transport"].LegSequence = 1
	state.Assignments["transport"].SampleID = "another-sample"
	err = validateHandoffRoute(
		state, state.Samples["sample"], "collector", "courier", "collection", "transport",
	)
	assertHTTPErrorCode(t, err, "invalid_handoff_route")
}

func TestValidateHandoffRouteAllowsNodeCustodyBridgeWithoutReassigningLeg(t *testing.T) {
	state := mongostore.NewState()
	acceptedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	state.Locations["node"] = transferPointLocation("node")
	state.Users["courier-a"] = &mongostore.User{
		ID: "courier-a", Roles: []string{"delivery_partner"}, Active: true,
	}
	state.Users["courier-b"] = &mongostore.User{
		ID: "courier-b", Roles: []string{"delivery_partner"}, Active: true,
	}
	state.Users["node-handler"] = &mongostore.User{
		ID: "node-handler", Roles: []string{"node_handler"}, Active: true,
	}
	state.Samples["sample"] = &mongostore.Sample{
		ID: "sample", ActiveJourneyID: "journey", CurrentHolderID: "courier-a",
	}
	state.Assignments["leg-1"] = &mongostore.Assignment{
		ID: "leg-1", SampleID: "sample", JourneyID: "journey", Kind: "transport",
		LegSequence: 1, ToLocationID: "node", WorkerID: "courier-a",
		Status: "arrived", AcceptedAt: &acceptedAt,
	}
	state.Assignments["leg-2"] = &mongostore.Assignment{
		ID: "leg-2", SampleID: "sample", JourneyID: "journey", Kind: "transport",
		LegSequence: 2, FromLocationID: "node", WorkerID: "courier-b", Status: "pending",
	}

	if err := validateHandoffRoute(
		state, state.Samples["sample"], "courier-a", "node-handler", "leg-1", "leg-2",
	); err != nil {
		t.Fatalf("valid node bridge: %v", err)
	}
	if err := acceptHandoffReceiverAssignment(
		state, handoffActor("node-handler"), "leg-2", "node-handler", acceptedAt.Add(time.Minute),
	); err != nil {
		t.Fatalf("node bridge assignment handling: %v", err)
	}
	if state.Assignments["leg-2"].WorkerID != "courier-b" ||
		state.Assignments["leg-2"].Status != "pending" {
		t.Fatalf("node bridge reassigned next leg: %+v", state.Assignments["leg-2"])
	}
}

func TestNodeBridgeRequiresDurablyAssignedHandlerWhenAssignmentExists(t *testing.T) {
	state := mongostore.NewState()
	acceptedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	state.Locations["node"] = transferPointLocation("node")
	state.Users["courier-a"] = &mongostore.User{
		ID: "courier-a", Roles: []string{"delivery_partner"}, Active: true,
	}
	state.Users["courier-b"] = &mongostore.User{
		ID: "courier-b", Roles: []string{"delivery_partner"}, Active: true,
	}
	state.Users["assigned-handler"] = &mongostore.User{
		ID: "assigned-handler", Roles: []string{"node_handler"},
		Active: true, Available: true, CapacityUnits: 1,
	}
	state.Users["unrelated-handler"] = &mongostore.User{
		ID: "unrelated-handler", Roles: []string{"node_handler"},
		Active: true, Available: true, CapacityUnits: 1,
	}
	state.Samples["sample"] = &mongostore.Sample{
		ID: "sample", ActiveJourneyID: "journey", CurrentHolderID: "courier-a",
	}
	state.Assignments["leg-1"] = &mongostore.Assignment{
		ID: "leg-1", SampleID: "sample", JourneyID: "journey", Kind: "transport",
		LegSequence: 1, ToLocationID: "node", WorkerID: "courier-a",
		Status: "arrived", AcceptedAt: &acceptedAt,
	}
	state.Assignments["leg-2"] = &mongostore.Assignment{
		ID: "leg-2", SampleID: "sample", JourneyID: "journey", Kind: "transport",
		LegSequence: 2, FromLocationID: "node", WorkerID: "courier-b", Status: "pending",
	}
	state.Assignments["node-work"] = &mongostore.Assignment{
		ID: "node-work", SampleID: "sample", JourneyID: "journey", Kind: "node",
		LegSequence: 1, ToLocationID: "node", WorkerID: "assigned-handler", Status: "pending",
	}

	err := validateHandoffRoute(
		state, state.Samples["sample"], "courier-a", "unrelated-handler", "leg-1", "leg-2",
	)
	assertHTTPErrorCode(t, err, "receiver_assignment_mismatch")

	if err := validateHandoffRoute(
		state, state.Samples["sample"], "courier-a", "assigned-handler", "leg-1", "leg-2",
	); err != nil {
		t.Fatalf("assigned node handler rejected: %v", err)
	}
	if err := acceptNodeBridgeAssignment(
		state, handoffActor("assigned-handler"),
		state.Assignments["leg-1"], state.Assignments["leg-2"],
		"assigned-handler", acceptedAt.Add(time.Minute),
	); err != nil {
		t.Fatalf("accept node bridge assignment: %v", err)
	}
	nodeWork := state.Assignments["node-work"]
	if nodeWork.Status != "accepted" || nodeWork.AcceptedAt == nil {
		t.Fatalf("node assignment not durably accepted: %+v", nodeWork)
	}

	state.Samples["sample"].CurrentHolderID = "unrelated-handler"
	err = validateHandoffRoute(
		state, state.Samples["sample"], "unrelated-handler", "courier-b", "leg-1", "leg-2",
	)
	assertHTTPErrorCode(t, err, "sender_assignment_mismatch")

	state.Samples["sample"].CurrentHolderID = "assigned-handler"
	if err := validateHandoffRoute(
		state, state.Samples["sample"], "assigned-handler", "courier-b", "leg-1", "leg-2",
	); err != nil {
		t.Fatalf("assigned node sender rejected: %v", err)
	}
	completeNodeBridgeAssignment(
		state, handoffActor("assigned-handler"),
		state.Assignments["leg-1"], state.Assignments["leg-2"],
		"assigned-handler", acceptedAt.Add(2*time.Minute),
	)
	if nodeWork.Status != "completed" || nodeWork.CompletedAt == nil {
		t.Fatalf("node assignment not completed on custody release: %+v", nodeWork)
	}
}

func TestAdjacentAssignmentsSupportsExplicitFacilityWork(t *testing.T) {
	state := mongostore.NewState()
	state.Assignments["leg-1"] = &mongostore.Assignment{
		ID: "leg-1", JourneyID: "journey", Kind: "transport",
		LegSequence: 1, FromLocationID: "origin", ToLocationID: "node",
	}
	state.Assignments["node-work"] = &mongostore.Assignment{
		ID: "node-work", JourneyID: "journey", Kind: "node",
		LegSequence: 1, FromLocationID: "origin", ToLocationID: "node",
	}
	state.Assignments["leg-2"] = &mongostore.Assignment{
		ID: "leg-2", JourneyID: "journey", Kind: "transport",
		LegSequence: 2, FromLocationID: "node", ToLocationID: "lab",
	}
	state.Assignments["lab-work"] = &mongostore.Assignment{
		ID: "lab-work", JourneyID: "journey", Kind: "laboratory",
		LegSequence: 3, FromLocationID: "node", ToLocationID: "lab",
	}

	pairs := [][2]string{
		{"leg-1", "node-work"},
		{"node-work", "leg-2"},
		{"leg-2", "lab-work"},
	}
	for _, pair := range pairs {
		if !adjacentAssignments(state, state.Assignments[pair[0]], state.Assignments[pair[1]]) {
			t.Errorf("%s -> %s should be adjacent", pair[0], pair[1])
		}
	}
	state.Assignments["lab-work"].LegSequence = 4
	if adjacentAssignments(state, state.Assignments["leg-2"], state.Assignments["lab-work"]) {
		t.Fatal("out-of-order laboratory work was adjacent")
	}
}

func TestLaboratoryOperatorAuthorizationUsesFacilityAssignmentWhenPresent(t *testing.T) {
	state := mongostore.NewState()
	acceptedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	if !laboratoryOperatorAuthorized(state, "sample", "lab", "operator-a") {
		t.Fatal("legacy journey without lab assignment should permit role-scoped receipt")
	}
	state.Assignments["lab-work"] = &mongostore.Assignment{
		ID: "lab-work", SampleID: "sample", Kind: "laboratory",
		ToLocationID: "lab", WorkerID: "operator-b", Status: "accepted",
		AcceptedAt: &acceptedAt,
	}
	if laboratoryOperatorAuthorized(state, "sample", "lab", "operator-a") {
		t.Fatal("unrelated operator was authorized despite facility assignment")
	}
	if !laboratoryOperatorAuthorized(state, "sample", "lab", "operator-b") {
		t.Fatal("accepted assigned laboratory operator was rejected")
	}
}

func TestFinalTransportAssignmentUsesGreatestRouteSequence(t *testing.T) {
	state := mongostore.NewState()
	state.Assignments["leg-1"] = &mongostore.Assignment{
		ID: "leg-1", JourneyID: "journey", Kind: "transport", LegSequence: 1,
	}
	state.Assignments["leg-3"] = &mongostore.Assignment{
		ID: "leg-3", JourneyID: "journey", Kind: "transport", LegSequence: 3,
	}
	state.Assignments["other"] = &mongostore.Assignment{
		ID: "other", JourneyID: "other-journey", Kind: "transport", LegSequence: 9,
	}
	final := finalTransportAssignment(state, "journey")
	if final == nil || final.ID != "leg-3" {
		t.Fatalf("final transport=%v, want leg-3", final)
	}
}

// handoffActor is the worker whose custody step is being exercised. Facility work
// that advances through a handoff is audited against the actor who moved it, so
// these helpers need a principal even in a unit test.
func handoffActor(id string) core.Principal {
	return core.Principal{ID: id, Roles: []string{"node_handler"}}
}

func assertHTTPErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var apiErr *core.HTTPError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error=%v, want HTTPError %q", err, want)
	}
	if apiErr.Code != want {
		t.Fatalf("error code=%q, want %q", apiErr.Code, want)
	}
}
