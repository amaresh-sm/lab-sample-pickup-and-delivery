package service

import (
	"context"
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

var (
	windowRequestedAt = time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	windowDeadline    = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
)

// serviceableNetwork is the smallest network that makes the origin serviceable:
// one active collection point at the origin, one active laboratory that accepts
// the sample type, and one active connection between them.
func serviceableNetwork() *mongostore.State {
	state := mongostore.NewState()
	state.SampleTypes["type"] = &mongostore.SampleType{ID: "type", Name: "Amber"}
	state.Locations["node"] = collectionPointLocation("node", 0, 0)
	state.Locations["lab"] = laboratoryLocation("lab")
	state.Acceptances["lab|type"] = &mongostore.Acceptance{
		LaboratoryID: "lab", SampleTypeID: "type", Accepts: true,
	}
	state.Connections["node-lab"] = &mongostore.Connection{
		ID: "node-lab", FromLocationID: "node", ToLocationID: "lab",
		TravelMinutes: 30, CostUnits: 5, Active: true,
	}
	return state
}

func serviceabilityCheck(requestedAt, deadline time.Time) serviceabilityInputs {
	return serviceabilityInputs{
		origin:        core.Map{"latitude": 0.0, "longitude": 0.0},
		sampleTypeID:  "type",
		requestedAt:   requestedAt,
		deadline:      deadline,
		window:        collectionWindow{earliestAt: requestedAt, latestAt: deadline},
		requiredUnits: 1,
	}
}

func TestCollectionWindowDefaultsToRequestedAtAndDeadlineWhenAbsent(t *testing.T) {
	window, err := parseCollectionWindow(core.Map{}, windowRequestedAt, windowDeadline)
	if err != nil {
		t.Fatalf("absent collection window rejected: %v", err)
	}
	if !window.earliestAt.Equal(windowRequestedAt) || !window.latestAt.Equal(windowDeadline) {
		t.Fatalf("default window = [%s, %s]", window.earliestAt, window.latestAt)
	}

	window, err = parseCollectionWindow(core.Map{
		"earliestCollectionAt": "2026-07-30T09:30:00Z",
		"latestCollectionAt":   "2026-07-30T10:00:00Z",
	}, windowRequestedAt, windowDeadline)
	if err != nil {
		t.Fatalf("explicit collection window rejected: %v", err)
	}
	if formatTime(window.earliestAt) != "2026-07-30T09:30:00Z" ||
		formatTime(window.latestAt) != "2026-07-30T10:00:00Z" {
		t.Fatalf("explicit window = [%s, %s]", window.earliestAt, window.latestAt)
	}
}

func TestCollectionWindowRejectsInvertedWindowAndWindowPastTheDeadline(t *testing.T) {
	for _, test := range []struct {
		name string
		in   core.Map
	}{
		{
			name: "inverted",
			in: core.Map{
				"earliestCollectionAt": "2026-07-30T10:00:00Z",
				"latestCollectionAt":   "2026-07-30T09:30:00Z",
			},
		},
		{
			name: "empty",
			in: core.Map{
				"earliestCollectionAt": "2026-07-30T10:00:00Z",
				"latestCollectionAt":   "2026-07-30T10:00:00Z",
			},
		},
		{
			name: "ends after the delivery deadline",
			in: core.Map{
				"earliestCollectionAt": "2026-07-30T09:30:00Z",
				"latestCollectionAt":   "2026-07-30T12:30:00Z",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCollectionWindow(test.in, windowRequestedAt, windowDeadline)
			assertHTTPErrorStatus(t, err, 400)
			assertHTTPErrorCode(t, err, "invalid_collection_window")
		})
	}

	_, err := parseCollectionWindow(
		core.Map{"earliestCollectionAt": "not-a-timestamp"},
		windowRequestedAt,
		windowDeadline,
	)
	assertHTTPErrorCode(t, err, "invalid_time")
}

func TestStoredCollectionWindowDefaultsForRecordsWrittenBeforeTheWindowExisted(t *testing.T) {
	legacy := &mongostore.CollectionRequest{
		RequestedAt: windowRequestedAt, DeliveryDeadline: windowDeadline,
	}
	earliest, latest := requestCollectionWindow(legacy)
	if !earliest.Equal(windowRequestedAt) || !latest.Equal(windowDeadline) {
		t.Fatalf("legacy window = [%s, %s]", earliest, latest)
	}

	stored := &mongostore.CollectionRequest{
		RequestedAt:          windowRequestedAt,
		EarliestCollectionAt: windowRequestedAt.Add(30 * time.Minute),
		LatestCollectionAt:   windowRequestedAt.Add(time.Hour),
		DeliveryDeadline:     windowDeadline,
	}
	earliest, latest = requestCollectionWindow(stored)
	if !earliest.Equal(stored.EarliestCollectionAt) || !latest.Equal(stored.LatestCollectionAt) {
		t.Fatalf("stored window = [%s, %s]", earliest, latest)
	}
}

func TestServiceabilityVerdictNamesTargetLaboratoryAndArrival(t *testing.T) {
	verdict, err := evaluateServiceability(
		serviceableNetwork(),
		serviceabilityCheck(windowRequestedAt, windowDeadline),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.serviceable || verdict.reason != serviceableReason {
		t.Fatalf("verdict = %+v", verdict)
	}
	if verdict.targetLaboratoryID != "lab" {
		t.Fatalf("target laboratory = %q", verdict.targetLaboratoryID)
	}
	if formatTime(verdict.estimatedArrivalAt) != "2026-07-30T09:30:00Z" {
		t.Fatalf("estimated arrival = %s", formatTime(verdict.estimatedArrivalAt))
	}
}

func TestServiceabilityVerdictReportsEachNegativeReasonCode(t *testing.T) {
	for _, test := range []struct {
		name    string
		arrange func(*mongostore.State) serviceabilityInputs
		reason  string
	}{
		{
			name: "origin beyond the 5 km collection-point coverage",
			arrange: func(state *mongostore.State) serviceabilityInputs {
				state.Locations["node"].Latitude = 1
				return serviceabilityCheck(windowRequestedAt, windowDeadline)
			},
			reason: reasonOutsideServiceCoverage,
		},
		{
			name: "no laboratory accepts the sample type",
			arrange: func(state *mongostore.State) serviceabilityInputs {
				state.Acceptances["lab|type"].Accepts = false
				return serviceabilityCheck(windowRequestedAt, windowDeadline)
			},
			reason: reasonNoAcceptingLaboratory,
		},
		{
			name: "no connection reaches the laboratory",
			arrange: func(state *mongostore.State) serviceabilityInputs {
				delete(state.Connections, "node-lab")
				return serviceabilityCheck(windowRequestedAt, windowDeadline)
			},
			reason: reasonNoRoute,
		},
		{
			name: "laboratory capacity pool is exhausted",
			arrange: func(state *mongostore.State) serviceabilityInputs {
				state.Capacities["lab"] = &mongostore.Capacity{
					ResourceID: "lab", CapacityUnits: 1, ReservedUnits: 1,
				}
				return serviceabilityCheck(windowRequestedAt, windowDeadline)
			},
			reason: reasonInsufficientCapacity,
		},
		{
			name: "route cannot reach the laboratory before the deadline",
			arrange: func(state *mongostore.State) serviceabilityInputs {
				return serviceabilityCheck(
					windowRequestedAt, windowRequestedAt.Add(10*time.Minute),
				)
			},
			reason: reasonDeadlineUnreachable,
		},
		{
			name: "cooled handling rule cannot be satisfied by the network",
			arrange: func(state *mongostore.State) serviceabilityInputs {
				state.SampleTypes["type"].Name = "Frost"
				state.SampleTypes["type"].RequiresCooledRoute = true
				return serviceabilityCheck(windowRequestedAt, windowDeadline)
			},
			reason: reasonHandlingRulesUnsatisfiable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := serviceableNetwork()
			inputs := test.arrange(state)
			verdict, err := evaluateServiceability(state, inputs)
			if err != nil {
				t.Fatal(err)
			}
			if verdict.serviceable {
				t.Fatalf("verdict is serviceable, want %q", test.reason)
			}
			if verdict.reason != test.reason {
				t.Fatalf("reason = %q, want %q", verdict.reason, test.reason)
			}
		})
	}
}

func TestServiceabilityVerdictRejectsAnUnknownSampleType(t *testing.T) {
	inputs := serviceabilityCheck(windowRequestedAt, windowDeadline)
	inputs.sampleTypeID = "missing"
	_, err := evaluateServiceability(serviceableNetwork(), inputs)
	assertHTTPErrorStatus(t, err, 404)
}

// A collection window that opens too late for the route to beat the delivery
// deadline makes the request unserviceable, even though the window itself is
// valid and the same request without the window would be serviceable.
func TestCollectionWindowThatMakesTheDeadlineUnreachableIsUnserviceable(t *testing.T) {
	state := serviceableNetwork()
	deadline := windowRequestedAt.Add(time.Hour)

	open, err := evaluateServiceability(state, serviceabilityCheck(windowRequestedAt, deadline))
	if err != nil {
		t.Fatal(err)
	}
	if !open.serviceable {
		t.Fatalf("request without a narrowed window = %+v, want serviceable", open)
	}

	late := serviceabilityCheck(windowRequestedAt, deadline)
	late.window = collectionWindow{
		earliestAt: windowRequestedAt.Add(45 * time.Minute),
		latestAt:   windowRequestedAt.Add(55 * time.Minute),
	}
	verdict, err := evaluateServiceability(state, late)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.serviceable || verdict.reason != reasonDeadlineUnreachable {
		t.Fatalf("late-window verdict = %+v, want %q", verdict, reasonDeadlineUnreachable)
	}
}

// Serviceability depends only on network structure. Making every worker
// unavailable must not move the serviceable region.
func TestServiceabilityVerdictIgnoresWorkerAvailability(t *testing.T) {
	state := serviceableNetwork()
	for _, id := range []string{"collector", "courier", "handler", "operator"} {
		state.Users[id] = &mongostore.User{
			ID: id, Roles: []string{"collection_professional", "delivery_partner"},
			Active: true, Available: true, CapacityUnits: 4,
		}
	}
	inputs := serviceabilityCheck(windowRequestedAt, windowDeadline)
	staffed, err := evaluateServiceability(state, inputs)
	if err != nil {
		t.Fatal(err)
	}

	for _, user := range state.Users {
		user.Available = false
	}
	unstaffed, err := evaluateServiceability(state, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if staffed.serviceable != unstaffed.serviceable ||
		staffed.reason != unstaffed.reason ||
		staffed.targetLaboratoryID != unstaffed.targetLaboratoryID ||
		!staffed.estimatedArrivalAt.Equal(unstaffed.estimatedArrivalAt) {
		t.Fatalf(
			"verdict changed with worker availability: staffed=%+v unstaffed=%+v",
			staffed, unstaffed,
		)
	}
	if !unstaffed.serviceable {
		t.Fatal("network with no available worker became unserviceable")
	}
}

func TestPublishServiceabilityIncrementsVersionByOneAndCompletes(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	state := mongostore.NewState()
	state.Locations["node"] = collectionPointLocation("node", 0, 0)
	bumpNetworkRevision(state)

	first := publishServiceability(state)
	if first.Version != 1 {
		t.Fatalf("first published version = %d, want 1", first.Version)
	}
	if first.Status != mongostore.ServiceabilityComplete {
		t.Fatalf("first published status = %q", first.Status)
	}
	if first.NetworkRevision != state.NetworkRevision {
		t.Fatalf("published revision = %d, state revision = %d", first.NetworkRevision, state.NetworkRevision)
	}
	if state.ServiceabilityDraft != nil {
		t.Fatalf("a draft survived publication: %+v", state.ServiceabilityDraft)
	}

	bumpNetworkRevision(state)
	second := publishServiceability(state)
	if second.Version != first.Version+1 {
		t.Fatalf("second published version = %d, want %d", second.Version, first.Version+1)
	}
	if second.Status != mongostore.ServiceabilityComplete || state.ServiceabilityDraft != nil {
		t.Fatalf("second publication = %+v draft=%+v", second, state.ServiceabilityDraft)
	}

	if again := ensureServiceabilityPublished(state); again != state.Serviceability ||
		again.Version != second.Version {
		t.Fatalf("an up-to-date calculation was republished: %+v", again)
	}
}

// While a new version is being computed the previous complete version must keep
// being served, so no request is ever checked against a partial calculation.
func TestInProgressServiceabilityDraftIsNeverServed(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	state := mongostore.NewState()
	state.Locations["published-node"] = collectionPointLocation("published-node", 0, 0)
	bumpNetworkRevision(state)
	published := publishServiceability(state)

	state.ServiceabilityDraft = &mongostore.ServiceabilityCalculation{
		Version:         published.Version + 1,
		Status:          mongostore.ServiceabilityComputing,
		NetworkRevision: state.NetworkRevision,
		ComputedAt:      core.Now(),
		Regions: []*mongostore.ServiceRegion{
			{LocationID: "half-computed-node", CoverageKM: 5},
		},
	}

	served := servedServiceability(state)
	if served.Version != published.Version {
		t.Fatalf("served version = %d, want the published %d", served.Version, published.Version)
	}
	if served.Status != mongostore.ServiceabilityComplete {
		t.Fatalf("served status = %q, want complete", served.Status)
	}
	if len(served.Regions) != 1 || served.Regions[0].LocationID != "published-node" {
		t.Fatalf("served regions = %+v; the in-progress draft leaked", served.Regions)
	}

	view := serviceabilitySnapshotView(served)
	if view["version"] != published.Version ||
		view["status"] != mongostore.ServiceabilityComplete ||
		view["activeCollectionPointCount"] != 1 {
		t.Fatalf("snapshot view = %#v", view)
	}
	regions := view["regions"].([]core.Map)
	if len(regions) != 1 || regions[0]["locationId"] != "published-node" {
		t.Fatalf("snapshot regions = %#v; the in-progress draft leaked", regions)
	}
	if state.ServiceabilityDraft == nil {
		t.Fatal("serving the published version discarded the in-progress draft")
	}
}

func TestServiceabilityRegionsMatchActiveCollectionPointsWithCappedCoverage(t *testing.T) {
	state := mongostore.NewState()
	state.Locations["a-legacy-wide"] = &mongostore.Location{
		ID: "a-legacy-wide", Name: "Legacy wide", Active: true, CoverageKM: 50,
		Latitude: 1, Longitude: 2,
		Functions: map[string]bool{mongostore.FunctionCollectionPoint: true},
	}
	state.Locations["b-narrow"] = &mongostore.Location{
		ID: "b-narrow", Name: "Narrow", Active: true, CoverageKM: 3,
		Functions: map[string]bool{
			mongostore.FunctionCollectionPoint: true,
			mongostore.FunctionHub:             true,
		},
	}
	state.Locations["c-unset-coverage"] = &mongostore.Location{
		ID: "c-unset-coverage", Name: "Unset", Active: true,
		Functions: map[string]bool{mongostore.FunctionCollectionPoint: true},
	}
	state.Locations["d-function-disabled"] = &mongostore.Location{
		ID: "d-function-disabled", Active: true, CoverageKM: 4,
		Functions: map[string]bool{mongostore.FunctionCollectionPoint: false},
	}
	state.Locations["e-inactive"] = &mongostore.Location{
		ID: "e-inactive", Active: false, CoverageKM: 4,
		Functions: map[string]bool{mongostore.FunctionCollectionPoint: true},
	}
	state.Locations["f-laboratory-only"] = laboratoryLocation("f-laboratory-only")

	regions := serviceabilityRegions(state)
	want := []struct {
		id       string
		coverage float64
	}{
		{"a-legacy-wide", 5},
		{"b-narrow", 3},
		{"c-unset-coverage", 5},
	}
	if len(regions) != len(want) {
		t.Fatalf("regions = %+v, want exactly the active collection points %+v", regions, want)
	}
	for index, expected := range want {
		if regions[index].LocationID != expected.id {
			t.Fatalf("region %d = %q, want %q", index, regions[index].LocationID, expected.id)
		}
		if regions[index].CoverageKM != expected.coverage {
			t.Fatalf(
				"region %q coverage = %v, want the planning cap %v",
				expected.id, regions[index].CoverageKM, expected.coverage,
			)
		}
	}
	if regions[0].Name != "Legacy wide" || regions[0].Latitude != 1 || regions[0].Longitude != 2 {
		t.Fatalf("region descriptor = %+v", regions[0])
	}
}

func TestServiceabilityReadsRefuseUnauthorizedRoles(t *testing.T) {
	service := &Service{}
	ctx := context.Background()

	_, err := service.readServiceabilitySnapshot(ctx, structPrincipal("requester"))
	assertHTTPErrorStatus(t, err, 403)
	assertHTTPErrorCode(t, err, "forbidden")

	_, err = service.checkServiceability(ctx, structPrincipal("collection_professional"), core.Map{})
	assertHTTPErrorStatus(t, err, 403)
	assertHTTPErrorCode(t, err, "forbidden")
}

// The dry run has to be usable by a requester, so its input validation must run
// before any datastore access.
func TestCheckServiceabilityValidatesInputBeforeReadingState(t *testing.T) {
	service := &Service{}
	ctx := context.Background()

	_, err := service.checkServiceability(ctx, structPrincipal("requester"), core.Map{})
	assertHTTPErrorStatus(t, err, 400)
	assertHTTPErrorCode(t, err, "invalid_origin")

	_, err = service.checkServiceability(ctx, structPrincipal("requester"), core.Map{
		"origin":               core.Map{"latitude": 0.0, "longitude": 0.0},
		"originType":           "home",
		"sampleTypeId":         "type",
		"requestedAt":          "2026-07-30T09:00:00Z",
		"deliveryDeadline":     "2026-07-30T12:00:00Z",
		"earliestCollectionAt": "2026-07-30T11:00:00Z",
		"latestCollectionAt":   "2026-07-30T10:00:00Z",
	})
	assertHTTPErrorStatus(t, err, 400)
	assertHTTPErrorCode(t, err, "invalid_collection_window")
}

func TestCreateLocationRequiresAtLeastOneFunction(t *testing.T) {
	service := &Service{}
	admin := core.Principal{
		ID:    "00000000-0000-4000-8000-0000000000ad",
		Roles: []string{"administrator"},
	}
	base := core.Map{
		"name": "Inert site", "active": true,
		"coordinate": core.Map{"latitude": 12.9716, "longitude": 77.5946},
	}
	for _, test := range []struct {
		name      string
		functions any
		present   bool
	}{
		{name: "absent"},
		{name: "empty list", functions: []string{}, present: true},
		{name: "empty json array", functions: []any{}, present: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := cloneMap(base)
			if test.present {
				in["functions"] = test.functions
			}
			_, err := service.createLocation(context.Background(), admin, in)
			assertHTTPErrorStatus(t, err, 400)
			assertHTTPErrorCode(t, err, "invalid_location_function")
		})
	}
}
