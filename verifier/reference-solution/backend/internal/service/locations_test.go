package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func assertHTTPErrorStatus(t *testing.T, err error, want int) {
	t.Helper()
	var apiErr *core.HTTPError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error=%v, want HTTPError with status %d", err, want)
	}
	if apiErr.Status != want {
		t.Fatalf("error status=%d (%s), want %d", apiErr.Status, apiErr.Code, want)
	}
}

func combinedFunctionLocation(id string, functions ...string) *mongostore.Location {
	location := &mongostore.Location{
		ID: id, Active: true, CoverageKM: 5,
		Functions: map[string]bool{},
	}
	for _, function := range functions {
		location.Functions[function] = true
	}
	return location
}

// A single site may act as a hub and as a laboratory at the same time. The old
// disjoint Node/Laboratory model made that impossible: such a site was either
// absent from Nodes (so it could not be an intermediate stop) or absent from
// Laboratories (so it could not be a final destination).
func TestLocationServingHubAndLaboratoryIsValidFinalAndIntermediateStop(t *testing.T) {
	state := mongostore.NewState()
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	state.Locations["origin"] = collectionPointLocation("origin", 0, 0)
	state.Locations["hub-lab"] = combinedFunctionLocation(
		"hub-lab", mongostore.FunctionHub, mongostore.FunctionLaboratory,
	)
	state.Locations["lab"] = laboratoryLocation("lab")
	state.Users["handler"] = &mongostore.User{
		ID: "handler", Roles: []string{"node_handler"},
		Active: true, Available: true, CapacityUnits: 4,
	}
	state.Users["operator"] = &mongostore.User{
		ID: "operator", Roles: []string{"laboratory_team"},
		Active: true, Available: true, CapacityUnits: 4,
	}
	sampleType := &mongostore.SampleType{Name: "Amber"}

	finalIDs, _, err := createFacilityAssignments(
		state, "journey-final", "sample-final",
		[]string{"origin", "hub-lab"}, sampleType, now,
	)
	if err != nil {
		t.Fatalf("hub that also serves as a laboratory rejected as final destination: %v", err)
	}
	if len(finalIDs) != 1 ||
		state.Assignments[finalIDs[0]].Kind != "laboratory" ||
		state.Assignments[finalIDs[0]].ToLocationID != "hub-lab" {
		t.Fatalf("final laboratory work = %+v", state.Assignments[finalIDs[0]])
	}

	middleIDs, _, err := createFacilityAssignments(
		state, "journey-middle", "sample-middle",
		[]string{"origin", "hub-lab", "lab"}, sampleType, now,
	)
	if err != nil {
		t.Fatalf("hub that also serves as a laboratory rejected as intermediate stop: %v", err)
	}
	kinds := map[string]*mongostore.Assignment{}
	for _, assignmentID := range middleIDs {
		assignment := state.Assignments[assignmentID]
		kinds[assignment.Kind] = assignment
	}
	if node := kinds["node"]; node == nil || node.ToLocationID != "hub-lab" {
		t.Fatalf("intermediate node work at a hub/laboratory = %+v", node)
	}
	if laboratory := kinds["laboratory"]; laboratory == nil || laboratory.ToLocationID != "lab" {
		t.Fatalf("final laboratory work = %+v", laboratory)
	}
}

// The whole-plan validator must also accept the combination case: an
// intermediate stop that additionally serves as a laboratory.
func TestValidateJourneyPlanAcceptsIntermediateHubThatAlsoServesAsLaboratory(t *testing.T) {
	state, journey := validCandidatePlan()
	addRouteNodeAndConnection(state, "hub-lab", "origin", "hub-lab", "origin-hub-lab", 10, 2)
	addRouteConnection(state, "hub-lab", "laboratory", "hub-lab-laboratory", 12, 3)
	state.Locations["hub-lab"].Functions[mongostore.FunctionLaboratory] = true

	if err := addPlanStop(state, journey, core.Map{
		"locationId": "hub-lab", "position": 1, "dwellMinutes": 4,
	}); err != nil {
		t.Fatalf("add hub/laboratory stop: %v", err)
	}
	if err := rebuildEditedJourneyRoute(state, journey); err != nil {
		t.Fatalf("rebuild route through a hub/laboratory: %v", err)
	}
	if err := validateJourneyPlan(state, journey); err != nil {
		t.Fatalf("intermediate hub that also serves as a laboratory was rejected: %v", err)
	}
	assertSampleTransportPath(
		t, state, journey.ID, "sample",
		[]string{"origin", "hub-lab", "laboratory"},
	)
}

// Disabling one function must not disable the rest of the site.
func TestDisablingCollectionPointFunctionLeavesOtherFunctionsWorking(t *testing.T) {
	state := mongostore.NewState()
	state.Locations["site"] = combinedFunctionLocation(
		"site", mongostore.FunctionCollectionPoint,
		mongostore.FunctionHub, mongostore.FunctionLaboratory,
	)
	origin := core.Map{"latitude": 0.0, "longitude": 0.0}

	if nodes, _ := serviceNodesInRange(state, origin, ""); len(nodes) != 1 {
		t.Fatalf("in-range collection points = %v, want the site", nodes)
	}

	out, err := applyLocationFunctionStatus(
		state, "site", mongostore.FunctionCollectionPoint, false,
	)
	if err != nil {
		t.Fatalf("disable collection-point function: %v", err)
	}
	if out["active"] != false || out["function"] != mongostore.FunctionCollectionPoint {
		t.Fatalf("function status result = %#v", out)
	}

	if nodes, _ := serviceNodesInRange(state, origin, ""); len(nodes) != 0 {
		t.Fatalf("in-range collection points = %v; a disabled collection point still covered the origin", nodes)
	}
	if !servesLaboratory(state, "site") {
		t.Fatal("disabling the collection-point function also disabled the laboratory function")
	}
	if !servesTransferStop(state, "site") {
		t.Fatal("disabling the collection-point function also disabled the hub function")
	}
	if !state.Locations["site"].Active {
		t.Fatal("disabling one function deactivated the whole location")
	}
	if !state.Locations["site"].Provides(mongostore.FunctionCollectionPoint) {
		t.Fatal("disabling a function removed it from the provided set")
	}
	if state.Locations["site"].Serves(mongostore.FunctionCollectionPoint) {
		t.Fatal("the collection-point function is still being served after it was disabled")
	}
}

// Deactivating the site itself must stop every function it provides.
func TestDeactivatingWholeLocationDisablesEveryFunction(t *testing.T) {
	state := mongostore.NewState()
	state.Locations["site"] = combinedFunctionLocation(
		"site", mongostore.LocationFunctions()...,
	)
	origin := core.Map{"latitude": 0.0, "longitude": 0.0}
	for _, function := range mongostore.LocationFunctions() {
		if !state.Locations["site"].Serves(function) {
			t.Fatalf("active location does not serve %q", function)
		}
	}

	state.Locations["site"].Active = false

	for _, function := range mongostore.LocationFunctions() {
		if state.Locations["site"].Serves(function) {
			t.Fatalf("deactivated location still serves %q", function)
		}
		if !state.Locations["site"].Provides(function) {
			t.Fatalf("deactivation removed %q from the provided set", function)
		}
	}
	if nodes, _ := serviceNodesInRange(state, origin, ""); len(nodes) != 0 {
		t.Fatalf("in-range collection points = %v; a deactivated location was still serviceable", nodes)
	}
	if servesLaboratory(state, "site") {
		t.Fatal("deactivated location still accepted laboratory work")
	}
	if servesTransferStop(state, "site") {
		t.Fatal("deactivated location still accepted intermediate stops")
	}
}

func TestSetLocationFunctionStatusRejectsUnknownAndUnprovidedFunctions(t *testing.T) {
	service := &Service{}
	admin := core.Principal{
		ID:    "00000000-0000-4000-8000-0000000000ad",
		Roles: []string{"administrator"},
	}
	_, err := service.setLocationFunctionStatus(context.Background(), admin, core.Map{
		"locationId": "site", "function": "teleporter", "active": true,
	})
	assertHTTPErrorStatus(t, err, 400)
	assertHTTPErrorCode(t, err, "invalid_location_function")

	state := mongostore.NewState()
	state.Locations["site"] = laboratoryLocation("site")

	_, unknownErr := applyLocationFunctionStatus(state, "site", "teleporter", true)
	assertHTTPErrorStatus(t, unknownErr, 400)
	assertHTTPErrorCode(t, unknownErr, "invalid_location_function")

	_, unprovidedErr := applyLocationFunctionStatus(
		state, "site", mongostore.FunctionCollectionPoint, true,
	)
	assertHTTPErrorStatus(t, unprovidedErr, 404)
	if state.Locations["site"].Provides(mongostore.FunctionCollectionPoint) {
		t.Fatal("a rejected function toggle added the function to the location")
	}

	_, missingErr := applyLocationFunctionStatus(
		state, "missing", mongostore.FunctionLaboratory, true,
	)
	assertHTTPErrorStatus(t, missingErr, 404)
}

// An update may not strip a location down to no function, just as creation may
// not build one that way: a site with no function silently disappears from every
// plan while still looking active.
func TestUpdateLocationRequiresAtLeastOneFunction(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	created, err := service.createLocation(ctx, admin, core.Map{
		"name": "Combined site", "active": true,
		"coordinate": core.Map{"latitude": 12.9716, "longitude": 77.5946},
		"functions": []string{
			mongostore.FunctionCollectionPoint, mongostore.FunctionLaboratory,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	locationID := core.String(created["locationId"])

	for _, test := range []struct {
		name      string
		functions any
	}{
		{name: "empty list", functions: []string{}},
		{name: "empty json array", functions: []any{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, updateErr := service.updateLocation(ctx, admin, core.Map{
				"locationId": locationID, "name": "Combined site", "active": true,
				"coordinate": core.Map{"latitude": 12.9716, "longitude": 77.5946},
				"functions":  test.functions,
			})
			assertHTTPErrorStatus(t, updateErr, 400)
			assertHTTPErrorCode(t, updateErr, "invalid_location_function")
		})
	}

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		location := state.Locations[locationID]
		if len(location.Functions) != 2 {
			t.Fatalf("a rejected update changed the location's functions: %v", location.Functions)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Narrowing to a single remaining function is still allowed.
	updated, err := service.updateLocation(ctx, admin, core.Map{
		"locationId": locationID, "name": "Combined site", "active": true,
		"coordinate": core.Map{"latitude": 12.9716, "longitude": 77.5946},
		"functions":  []string{mongostore.FunctionLaboratory},
	})
	if err != nil {
		t.Fatalf("narrowing to one function was refused: %v", err)
	}
	if functions := core.Object(updated["functions"]); len(functions) != 1 {
		t.Fatalf("updated functions = %v", functions)
	}
}
