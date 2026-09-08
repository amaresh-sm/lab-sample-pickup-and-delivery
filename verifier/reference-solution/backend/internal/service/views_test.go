package service

import (
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func TestSampleTimelineContainsOnlyRelatedEvents(t *testing.T) {
	state := mongostore.NewState()
	state.Assignments["assignment-a"] = &mongostore.Assignment{
		ID: "assignment-a", SampleID: "sample-a",
	}
	state.Assignments["assignment-b"] = &mongostore.Assignment{
		ID: "assignment-b", SampleID: "sample-b",
	}
	at := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	state.AuditEvents = []*mongostore.AuditEvent{
		{ID: 1, EntityType: "sample", EntityID: "sample-a", Action: "created", OccurredAt: at, Details: core.Map{}},
		{ID: 2, EntityType: "assignment", EntityID: "assignment-a", Action: "accepted", OccurredAt: at, Details: core.Map{}},
		{ID: 3, EntityType: "assignment", EntityID: "assignment-b", Action: "accepted", OccurredAt: at, Details: core.Map{}},
	}

	got := sampleTimeline(state, "sample-a")
	if len(got) != 2 {
		t.Fatalf("timeline length = %d, want 2", len(got))
	}
	if got[0]["action"] != "created" || got[1]["action"] != "accepted" {
		t.Fatalf("unexpected timeline: %#v", got)
	}
}

func TestFacilityRolesCannotReadUnassignedSamples(t *testing.T) {
	state := mongostore.NewState()
	state.Samples["sample-a"] = &mongostore.Sample{
		ID: "sample-a", RequesterID: "requester-a",
	}
	nodePrincipal := core.Principal{ID: "node-user", Roles: []string{"node_handler"}}
	if err := canReadSampleState(state, nodePrincipal, "sample-a"); err == nil {
		t.Fatal("unassigned node handler could read a sample")
	}
	labPrincipal := core.Principal{ID: "lab-user", Roles: []string{"laboratory_team"}}
	if err := canReadSampleState(state, labPrincipal, "sample-a"); err == nil {
		t.Fatal("unassigned laboratory operator could read a sample")
	}

	state.Assignments["node-work"] = &mongostore.Assignment{
		ID: "node-work", SampleID: "sample-a", Kind: "node", WorkerID: "node-user",
	}
	if err := canReadSampleState(state, nodePrincipal, "sample-a"); err != nil {
		t.Fatalf("assigned node handler was denied: %v", err)
	}
	state.Assignments["lab-work"] = &mongostore.Assignment{
		ID: "lab-work", SampleID: "sample-a", Kind: "laboratory", WorkerID: "lab-user",
	}
	if err := canReadSampleState(state, labPrincipal, "sample-a"); err != nil {
		t.Fatalf("assigned laboratory operator was denied: %v", err)
	}
}

func TestCapacityMapReportsRegularAndCooledAvailability(t *testing.T) {
	got := capacityMap(&mongostore.Capacity{
		CapacityUnits: 10, ReservedUnits: 3,
		CooledUnits: 4, CooledReserved: 1,
	})
	if got["availableUnits"] != 7 || got["cooledAvailableUnits"] != 3 {
		t.Fatalf("unexpected capacity view: %#v", got)
	}
}

func TestRequesterSampleSummariesReturnOnlyOwnedSamples(t *testing.T) {
	state := mongostore.NewState()
	at := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	state.Requests["request-owned"] = &mongostore.CollectionRequest{
		ID: "request-owned", RequesterID: "requester-a",
		RequestedAt: at, DeliveryDeadline: at.Add(4 * time.Hour),
		Serviceability: "serviceable",
	}
	state.Requests["request-other"] = &mongostore.CollectionRequest{
		ID: "request-other", RequesterID: "requester-b",
		RequestedAt: at, DeliveryDeadline: at.Add(4 * time.Hour),
		Serviceability: "serviceable",
	}
	state.Samples["sample-owned"] = &mongostore.Sample{
		ID: "sample-owned", RequestID: "request-owned",
		RequesterID: "requester-a", Status: "planned", UpdatedAt: at,
	}
	state.Samples["sample-other"] = &mongostore.Sample{
		ID: "sample-other", RequestID: "request-other",
		RequesterID: "requester-b", Status: "planned", UpdatedAt: at,
	}

	got := requesterSampleSummaries(state, "requester-a")
	if len(got) != 1 || got[0]["sampleId"] != "sample-owned" {
		t.Fatalf("requester summaries leaked or omitted samples: %#v", got)
	}
}

func TestRequesterJourneyViewRemovesOtherSamplesAndWorkerIdentifiers(t *testing.T) {
	full := core.Map{
		"journeyId":         "journey-a",
		"sampleIds":         []string{"sample-owned", "sample-other"},
		"assignedWorkerIds": []string{"worker-secret"},
		"legs": []core.Map{{
			"assignmentId":   "assignment-secret",
			"workerId":       "worker-secret",
			"sequence":       1,
			"status":         "in_progress",
			"fromLocationId": "node-a",
			"toLocationId":   "lab-a",
		}},
	}

	got := requesterJourneyView(full, []string{"sample-owned"})
	sampleIDs, ok := got["sampleIds"].([]string)
	if !ok || len(sampleIDs) != 1 || sampleIDs[0] != "sample-owned" {
		t.Fatalf("unexpected requester sample IDs: %#v", got["sampleIds"])
	}
	if _, exists := got["assignedWorkerIds"]; exists {
		t.Fatal("requester view exposed assigned worker IDs")
	}
	legs := got["legs"].([]core.Map)
	if _, exists := legs[0]["assignmentId"]; exists {
		t.Fatal("requester view exposed an assignment ID")
	}
	if _, exists := legs[0]["workerId"]; exists {
		t.Fatal("requester view exposed a worker ID")
	}
}
