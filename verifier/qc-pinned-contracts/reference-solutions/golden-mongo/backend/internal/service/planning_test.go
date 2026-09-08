package service

import (
	"container/heap"
	"errors"
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func TestParetoFrontierKeepsFasterCostlierRoute(t *testing.T) {
	labels := []routeLabel{{cost: 10, minutes: 60}}
	candidate := routeLabel{cost: 15, minutes: 20}
	if dominated(labels, candidate, false) {
		t.Fatal("costlier but faster route was incorrectly pruned")
	}
	labels = addLabel(labels, candidate)
	if len(labels) != 2 {
		t.Fatalf("frontier has %d labels, want 2", len(labels))
	}
	if !dominated(labels, routeLabel{cost: 20, minutes: 70}, false) {
		t.Fatal("strictly worse route was not pruned")
	}
}

func TestRouteQueueOrdersCostThenTime(t *testing.T) {
	q := &routeQueue{}
	heap.Init(q)
	heap.Push(q, &routeItem{cost: 20, minutes: 10})
	heap.Push(q, &routeItem{cost: 10, minutes: 50})
	heap.Push(q, &routeItem{cost: 10, minutes: 20})
	first := heap.Pop(q).(*routeItem)
	if first.cost != 10 || first.minutes != 20 {
		t.Fatalf("first route=%+v", first)
	}
}

func TestSortStringsUnique(t *testing.T) {
	got := sortStringsUnique([]string{"b", "a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v", got)
	}
}

func TestUniqueStringsPreservesInsertionOrder(t *testing.T) {
	got := uniqueStrings([]string{"requester", "collection_professional", "requester"})
	want := []string{"requester", "collection_professional"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got %v, want insertion order %v", got, want)
		}
	}
}

// require authorizes against the SELECTED role. This replaces the earlier
// behaviour, where holding any one of the permitted roles was enough regardless
// of the role the user was working under.
func TestRequireUsesTheSelectedRoleNotTheUnion(t *testing.T) {
	multi := core.Principal{
		ID:    "00000000-0000-4000-8000-000000000001",
		Roles: []string{"requester", "dispatcher"},
	}

	selection := multi
	selection.SelectedRole = "dispatcher"
	if err := require(selection, "administrator", "dispatcher"); err != nil {
		t.Fatalf("selected dispatcher rejected: %v", err)
	}

	selection.SelectedRole = "requester"
	err := require(selection, "administrator", "dispatcher")
	var apiErr *core.HTTPError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 || apiErr.Code != "forbidden" {
		t.Fatalf("held-but-unselected dispatcher role authorized the action: %v", err)
	}

	err = require(multi, "administrator", "dispatcher")
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "role_not_selected" {
		t.Fatalf("unselected multi-role principal error=%v, want 409 role_not_selected", err)
	}

	if err := require(structPrincipal("dispatcher"), "administrator", "dispatcher"); err != nil {
		t.Fatalf("single-role principal was not auto-selected: %v", err)
	}
}

// An administrator working under another role is that role and nothing more.
func TestSelectedRoleGivesAdministratorsNoBypass(t *testing.T) {
	administrator := core.Principal{
		ID:           "00000000-0000-4000-8000-000000000002",
		Roles:        []string{"administrator", "requester"},
		SelectedRole: "requester",
	}
	err := require(administrator, "administrator")
	var apiErr *core.HTTPError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 {
		t.Fatalf("administrator acting as requester kept administrator powers: %v", err)
	}
	if err := require(administrator, "requester"); err != nil {
		t.Fatalf("administrator acting as requester was denied requester work: %v", err)
	}
	administrator.SelectedRole = "administrator"
	if err := require(administrator, "administrator"); err != nil {
		t.Fatalf("administrator acting as administrator was denied: %v", err)
	}
}

func TestNodeSettingsPreserveFiveKilometreServiceBoundary(t *testing.T) {
	coverage, handling, err := locationSettings(
		core.Map{"coverageKm": 4.5, "handlingMinutes": 12},
		5,
		0,
	)
	if err != nil || coverage != 4.5 || handling != 12 {
		t.Fatalf("node settings = (%v, %v, %v)", coverage, handling, err)
	}
	if _, _, err := locationSettings(core.Map{"coverageKm": 5.1}, 5, 0); err == nil {
		t.Fatal("coverage beyond the fixed 5 km boundary was accepted")
	}
}

func TestConfiguredNodeHandlingTimeContributesToRouteDuration(t *testing.T) {
	state := mongostore.NewState()
	state.Locations["hub"] = &mongostore.Location{ID: "hub", HandlingMinutes: 9}

	if got := locationHandlingMinutes(state, "hub"); got != 9 {
		t.Fatalf("handling minutes = %d, want 9", got)
	}
}

func TestCanonicalSamplePoliciesCannotBeWeakened(t *testing.T) {
	base := sampleTypeRules{
		containerType: "configured-container", temperatureMinC: 2, temperatureMaxC: 8,
		batchingAllowed: true,
	}
	tests := []struct {
		name          string
		qualification string
		cooled        bool
		batching      bool
	}{
		{name: "Amber", qualification: "LX-A", batching: true},
		{name: "Indigo", qualification: "LX-I", batching: true},
		{name: "Frost", qualification: "LX-F", cooled: true, batching: true},
		{name: "Pulse", qualification: "LX-P", batching: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := enforceNamedSamplePolicy(test.name, base)
			if !got.qualifiedCollectorRequired || got.requiredQualification != test.qualification {
				t.Fatalf("qualification policy = (%v, %q)", got.qualifiedCollectorRequired, got.requiredQualification)
			}
			if got.requiresCooledRoute != test.cooled {
				t.Fatalf("cooled policy = %v, want %v", got.requiresCooledRoute, test.cooled)
			}
			if got.batchingAllowed != test.batching {
				t.Fatalf("batching policy = %v, want %v", got.batchingAllowed, test.batching)
			}
			if got.containerType != base.containerType ||
				got.temperatureMinC != base.temperatureMinC ||
				got.temperatureMaxC != base.temperatureMaxC {
				t.Fatalf("configured handling constraints changed: %+v", got)
			}
		})
	}
}

func TestFrostRejectsContainerWithoutCoolingCapability(t *testing.T) {
	rules := enforceNamedSamplePolicy("Frost", sampleTypeRules{
		containerType: "standard_vial", temperatureMinC: 2, temperatureMaxC: 8,
		batchingAllowed: true,
	})
	if err := validateNamedSamplePolicy("Frost", rules); err == nil {
		t.Fatal("Frost accepted a standard container")
	}
	for _, containerType := range []string{"cooled_vial", "refrigerated-vial", "cryo_shipper"} {
		rules.containerType = containerType
		if err := validateNamedSamplePolicy("Frost", rules); err != nil {
			t.Fatalf("cooling-capable container %q rejected: %v", containerType, err)
		}
	}
}

func TestNamedAmberIndigoCompatibilityIsSymmetricAndCannotBeRemoved(t *testing.T) {
	state := mongostore.NewState()
	state.SampleTypes["amber"] = &mongostore.SampleType{ID: "amber", Name: "Amber"}
	state.SampleTypes["indigo"] = &mongostore.SampleType{ID: "indigo", Name: "Indigo"}

	applyNamedCompatibilityRules(state, state.SampleTypes["amber"])
	if !stringSliceContains(state.SampleTypes["amber"].IncompatibleSampleTypes, "indigo") {
		t.Fatal("Amber does not reject Indigo")
	}
	if !stringSliceContains(state.SampleTypes["indigo"].IncompatibleSampleTypes, "amber") {
		t.Fatal("Indigo does not reject Amber")
	}

	state.SampleTypes["amber"].IncompatibleSampleTypes = nil
	applyNamedCompatibilityRules(state, state.SampleTypes["amber"])
	if !stringSliceContains(state.SampleTypes["amber"].IncompatibleSampleTypes, "indigo") {
		t.Fatal("an update removed canonical Amber/Indigo separation")
	}
}

func TestRequestIncompatibilitiesCanOnlyStrengthenSamplePolicy(t *testing.T) {
	got := effectiveSampleIncompatibilities([]string{"canonical"}, []string{"request", "canonical"})
	if len(got) != 2 ||
		!stringSliceContains(got, "canonical") ||
		!stringSliceContains(got, "request") {
		t.Fatalf("effective incompatibilities = %v", got)
	}
}

func TestServiceabilitySearchesAllInRangeNodesAndChoosesCheapestFeasibleRoute(t *testing.T) {
	state := mongostore.NewState()
	state.SampleTypes["type"] = &mongostore.SampleType{ID: "type"}
	state.Locations["nearest-dead-end"] = collectionPointLocation("nearest-dead-end", 0, 0)
	state.Locations["costly-node"] = collectionPointLocation("costly-node", 0, 0.01)
	state.Locations["cheap-node"] = collectionPointLocation("cheap-node", 0, 0.02)
	state.Locations["costly-lab"] = laboratoryLocation("costly-lab")
	state.Locations["cheap-lab"] = laboratoryLocation("cheap-lab")
	state.Acceptances["costly"] = &mongostore.Acceptance{
		LaboratoryID: "costly-lab", SampleTypeID: "type", Accepts: true,
	}
	state.Acceptances["cheap"] = &mongostore.Acceptance{
		LaboratoryID: "cheap-lab", SampleTypeID: "type", Accepts: true,
	}
	state.Connections["costly"] = &mongostore.Connection{
		ID: "costly", FromLocationID: "costly-node", ToLocationID: "costly-lab",
		TravelMinutes: 10, CostUnits: 50, Active: true,
	}
	state.Connections["cheap"] = &mongostore.Connection{
		ID: "cheap", FromLocationID: "cheap-node", ToLocationID: "cheap-lab",
		TravelMinutes: 20, CostUnits: 10, Active: true,
	}

	requestedAt := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	got, err := findCheapestServiceRoute(
		state,
		core.Map{"latitude": 0.0, "longitude": 0.0},
		"",
		"type",
		requestedAt,
		requestedAt.Add(2*time.Hour),
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.origin.id != "cheap-node" || got.route.labID != "cheap-lab" || got.route.cost != 10 {
		t.Fatalf("route = %+v", got)
	}
}

func TestServiceabilityPrefersFeasibleExactOriginOverCheaperCoveredNode(t *testing.T) {
	state := mongostore.NewState()
	state.SampleTypes["type"] = &mongostore.SampleType{ID: "type"}
	state.Locations["origin"] = collectionPointLocation("origin", 0, 0)
	state.Locations["covered-hub"] = &mongostore.Location{
		ID: "covered-hub", Active: true, CoverageKM: 5,
		Latitude: 0, Longitude: 0.02,
		Functions: map[string]bool{
			mongostore.FunctionCollectionPoint: true,
			mongostore.FunctionHub:             true,
		},
	}
	state.Locations["lab"] = laboratoryLocation("lab")
	state.Acceptances["lab|type"] = &mongostore.Acceptance{
		LaboratoryID: "lab", SampleTypeID: "type", Accepts: true,
	}
	state.Connections["origin-hub"] = &mongostore.Connection{
		ID: "origin-hub", FromLocationID: "origin", ToLocationID: "covered-hub",
		TravelMinutes: 15, CostUnits: 20, Active: true,
	}
	state.Connections["hub-lab"] = &mongostore.Connection{
		ID: "hub-lab", FromLocationID: "covered-hub", ToLocationID: "lab",
		TravelMinutes: 15, CostUnits: 20, Active: true,
	}

	requestedAt := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	got, err := findCheapestServiceRoute(
		state,
		core.Map{"latitude": 0.0, "longitude": 0.0},
		"",
		"type",
		requestedAt,
		requestedAt.Add(2*time.Hour),
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.origin.id != "origin" || len(got.route.edges) != 2 || got.route.cost != 40 {
		t.Fatalf("route = %+v", got)
	}
}

func TestServiceabilityHardCapsLegacyCoverageAtFiveKilometres(t *testing.T) {
	state := mongostore.NewState()
	state.Locations["legacy-wide-node"] = &mongostore.Location{
		ID: "legacy-wide-node", Active: true, CoverageKM: 50,
		Latitude: 0, Longitude: 0.046,
		Functions: map[string]bool{mongostore.FunctionCollectionPoint: true},
	}
	nodes, _ := serviceNodesInRange(
		state,
		core.Map{"latitude": 0.0, "longitude": 0.0},
		"",
	)
	if len(nodes) != 0 {
		t.Fatalf("nodes = %v; a point beyond 5 km was covered", nodes)
	}
}

func TestFrostRouteRequiresCooledTransportAndStorageThroughout(t *testing.T) {
	state := mongostore.NewState()
	state.SampleTypes["frost"] = &mongostore.SampleType{
		ID: "frost", Name: "Frost", RequiresCooledRoute: true,
	}
	state.Locations["node"] = collectionPointLocation("node", 0, 0)
	state.Locations["lab"] = laboratoryLocation("lab")
	state.Acceptances["lab|frost"] = &mongostore.Acceptance{
		LaboratoryID: "lab", SampleTypeID: "frost", Accepts: true,
	}
	state.Connections["route"] = &mongostore.Connection{
		ID: "route", FromLocationID: "node", ToLocationID: "lab",
		TravelMinutes: 10, CostUnits: 1, Active: true,
	}
	requestedAt := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	find := func() error {
		_, err := findRoute(state, "node", "frost", requestedAt, requestedAt.Add(time.Hour), 1)
		return err
	}
	if err := find(); err == nil {
		t.Fatal("Frost used a non-cooled connection")
	}

	state.Connections["route"].Cooled = true
	state.Capacities["node"] = &mongostore.Capacity{ResourceID: "node", CapacityUnits: 1}
	state.Capacities["lab"] = &mongostore.Capacity{ResourceID: "lab", CapacityUnits: 1, CooledUnits: 1}
	state.Capacities["route"] = &mongostore.Capacity{ResourceID: "route", CapacityUnits: 1, CooledUnits: 1}
	if err := find(); err == nil {
		t.Fatal("Frost used node storage without cooled capacity")
	}

	state.Capacities["node"].CooledUnits = 1
	state.Capacities["lab"].CooledUnits = 0
	if err := find(); err == nil {
		t.Fatal("Frost used laboratory storage without cooled capacity")
	}

	state.Capacities["lab"].CooledUnits = 1
	state.Capacities["route"].CooledUnits = 0
	if err := find(); err == nil {
		t.Fatal("Frost used transport capacity without cooled capacity")
	}
	state.Capacities["route"].CooledUnits = 1
	if err := find(); err != nil {
		t.Fatalf("fully cooled Frost route rejected: %v", err)
	}
}

func TestCreateFacilityAssignmentsBuildsNodeAndLaboratoryWork(t *testing.T) {
	state := mongostore.NewState()
	state.Locations["origin"] = collectionPointLocation("origin", 0, 0)
	state.Locations["hub"] = transferPointLocation("hub")
	state.Locations["lab"] = laboratoryLocation("lab")
	state.Users["handler"] = &mongostore.User{
		ID: "handler", Roles: []string{"node_handler"}, Active: true, Available: true, CapacityUnits: 2,
		AssignedLocationID: "hub",
	}
	state.Users["operator"] = &mongostore.User{
		ID: "operator", Roles: []string{"laboratory_team"}, Active: true, Available: true, CapacityUnits: 2,
		AssignedLocationID: "lab",
	}

	assignmentIDs, workerIDs, err := createFacilityAssignments(
		state,
		"journey",
		"sample",
		[]string{"origin", "hub", "lab"},
		&mongostore.SampleType{Name: "Amber"},
		time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(assignmentIDs) != 2 || len(workerIDs) != 2 {
		t.Fatalf("assignments=%v workers=%v", assignmentIDs, workerIDs)
	}
	kinds := map[string]*mongostore.Assignment{}
	for _, assignmentID := range assignmentIDs {
		assignment := state.Assignments[assignmentID]
		kinds[assignment.Kind] = assignment
	}
	if node := kinds["node"]; node == nil ||
		node.WorkerID != "handler" ||
		node.FromLocationID != "origin" ||
		node.ToLocationID != "hub" {
		t.Fatalf("node assignment = %+v", node)
	}
	if laboratory := kinds["laboratory"]; laboratory == nil ||
		laboratory.WorkerID != "operator" ||
		laboratory.FromLocationID != "hub" ||
		laboratory.ToLocationID != "lab" {
		t.Fatalf("laboratory assignment = %+v", laboratory)
	}
}

func TestFairWorkerBalancesOffersWithinTheRequestedRole(t *testing.T) {
	state := mongostore.NewState()
	state.Users["multi"] = &mongostore.User{
		ID: "multi", Roles: []string{"collection_professional", "delivery_partner"},
		Qualifications: []string{"LX-A"}, Active: true, Available: true,
		CapacityUnits: 10,
	}
	state.Users["collector"] = &mongostore.User{
		ID: "collector", Roles: []string{"collection_professional"},
		Qualifications: []string{"LX-A"}, Active: true, Available: true,
		CapacityUnits: 10,
	}
	state.Assignments["transport-1"] = &mongostore.Assignment{
		ID: "transport-1", Kind: "transport", WorkerID: "multi", Status: "pending",
	}
	state.Assignments["transport-2"] = &mongostore.Assignment{
		ID: "transport-2", Kind: "transport", WorkerID: "multi", Status: "pending",
	}
	state.Assignments["collection-1"] = &mongostore.Assignment{
		ID: "collection-1", Kind: "collection", WorkerID: "multi", Status: "pending",
	}

	if got := fairWorker(state, "collection_professional", "LX-A", true, nil, ""); got != "collector" {
		t.Fatalf("selected %q, want collector with fewer collection-role offers", got)
	}
}

// declineFixture is two equally-loaded couriers where only one has declined.
func declineFixture(declinedAt time.Time) *mongostore.State {
	state := mongostore.NewState()
	for _, id := range []string{"decliner", "steady"} {
		state.Users[id] = &mongostore.User{
			ID: id, Username: id, Roles: []string{"delivery_partner"},
			Active: true, Available: true, CapacityUnits: 10,
		}
	}
	state.Assignments["declined-leg"] = &mongostore.Assignment{
		ID: "declined-leg", Kind: "transport", Status: "pending",
		Declines: []mongostore.AssignmentDecline{
			{WorkerID: "decliner", Reason: "busy", At: declinedAt},
		},
	}
	return state
}

// AC-33: a recent decline lowers a worker's priority for future offers.
func TestRecentDeclineLowersOfferPriority(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T12:00:00Z")
	t.Setenv("DECLINE_PENALTY_WINDOW_SECONDS", "3600")
	state := declineFixture(time.Date(2026, 7, 30, 11, 30, 0, 0, time.UTC))

	if got := workerRoleOfferCount(state, "decliner", "delivery_partner", core.Now()); got != 1 {
		t.Fatalf("recent decline penalty = %d, want 1", got)
	}
	if got := fairWorker(state, "delivery_partner", "", false, nil, ""); got != "steady" {
		t.Fatalf("selected %q, want the worker who did not just decline", got)
	}
}

// AC-33: a decline outside the window carries no penalty at all, so a worker who
// declined long ago ranks exactly as if they never had.
func TestDeclineOutsideTheWindowCarriesNoPenalty(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T12:00:00Z")
	t.Setenv("DECLINE_PENALTY_WINDOW_SECONDS", "3600")
	state := declineFixture(time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))

	if got := workerRoleOfferCount(state, "decliner", "delivery_partner", core.Now()); got != 0 {
		t.Fatalf("expired decline penalty = %d, want 0", got)
	}
	// With the penalty expired the tie is broken by the stable ordering rules, not
	// by the old decline: the worker who declined is once again first in line.
	if got := fairWorker(state, "delivery_partner", "", false, nil, ""); got != "decliner" {
		t.Fatalf("selected %q, want the un-penalised earlier candidate", got)
	}
}

// AC-33: repeated declines must never permanently exclude a worker. Once the
// window has passed, a worker who declined many times is offered work again even
// when they are the only eligible worker left.
func TestRepeatedDeclinesNeverPermanentlyExcludeAWorker(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T12:00:00Z")
	t.Setenv("DECLINE_PENALTY_WINDOW_SECONDS", "3600")
	oldDecline := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	state := mongostore.NewState()
	state.Users["only-courier"] = &mongostore.User{
		ID: "only-courier", Username: "only-courier", Roles: []string{"delivery_partner"},
		Active: true, Available: true, CapacityUnits: 10,
	}
	state.Journeys["journey"] = &mongostore.Journey{ID: "journey", Status: "active"}
	state.Samples["sample"] = &mongostore.Sample{ID: "sample", SampleTypeID: "type"}
	state.SampleTypes["type"] = &mongostore.SampleType{ID: "type"}
	declines := []mongostore.AssignmentDecline{}
	for index := 0; index < 5; index++ {
		declines = append(declines, mongostore.AssignmentDecline{
			WorkerID: "only-courier", Reason: "busy",
			At: oldDecline.Add(time.Duration(index) * time.Minute),
		})
	}
	state.Assignments["stalled-leg"] = &mongostore.Assignment{
		ID: "stalled-leg", JourneyID: "journey", SampleID: "sample",
		Kind: "transport", LegSequence: 1, Status: "pending",
		Declines: declines, CreatedAt: oldDecline,
	}

	if got := workerRoleOfferCount(state, "only-courier", "delivery_partner", core.Now()); got != 0 {
		t.Fatalf("expired repeated declines still penalise the worker: %d", got)
	}
	if len(recentlyDeclinedWorkers(state.Assignments["stalled-leg"], core.Now())) != 0 {
		t.Fatal("expired declines still exclude the worker from being offered work")
	}
	offerUnofferedAssignments(state, core.Now())
	if got := state.Assignments["stalled-leg"].WorkerID; got != "only-courier" {
		t.Fatalf("stalled work was offered to %q, want the worker whose declines expired", got)
	}

	// Inside the window the same worker is still excluded from that one offer, so
	// the penalty is real while it lasts.
	fresh := mongostore.NewState()
	fresh.Users["only-courier"] = state.Users["only-courier"]
	fresh.Assignments["fresh-leg"] = &mongostore.Assignment{
		ID: "fresh-leg", Kind: "transport", Status: "pending",
		Declines: []mongostore.AssignmentDecline{
			{WorkerID: "only-courier", At: time.Date(2026, 7, 30, 11, 59, 0, 0, time.UTC)},
		},
	}
	if len(recentlyDeclinedWorkers(fresh.Assignments["fresh-leg"], core.Now())) != 1 {
		t.Fatal("a decline made a minute ago carried no exclusion")
	}
}

func structPrincipal(roles ...string) core.Principal {
	return core.Principal{ID: "00000000-0000-4000-8000-000000000001", Roles: roles}
}

// collectionPointLocation builds an active location whose only function is the
// 5 km collection point used by the serviceability scan.
func collectionPointLocation(id string, latitude, longitude float64) *mongostore.Location {
	return &mongostore.Location{
		ID: id, Active: true, CoverageKM: 5,
		Latitude: latitude, Longitude: longitude,
		Functions: map[string]bool{mongostore.FunctionCollectionPoint: true},
	}
}

// transferPointLocation builds an active intermediate stop.
func transferPointLocation(id string) *mongostore.Location {
	return &mongostore.Location{
		ID: id, Active: true, CoverageKM: 5,
		Functions: map[string]bool{mongostore.FunctionTransferPoint: true},
	}
}

// laboratoryLocation builds an active laboratory destination.
func laboratoryLocation(id string) *mongostore.Location {
	return &mongostore.Location{
		ID: id, Active: true, CoverageKM: 5,
		Functions: map[string]bool{mongostore.FunctionLaboratory: true},
	}
}
