package service

import (
	"context"
	"math"
	"testing"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// standardRequest is the collection-request payload buildServiceableNetwork's
// Amber sample type accepts, so a probe only has to override the fields it is
// actually testing.
func standardRequest(network networkFixture, key string) core.Map {
	return core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": network.sampleTypeID,
		"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T12:00:00Z",
		"containerType": "amber-container", "temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "idempotencyKey": key,
	}
}

// AC-2: RBAC on data. A requester may read their own sample and nobody else's,
// so a second requester asking for the first requester's sample is refused.
func TestAC2RequesterCannotReadAnotherRequestersSample(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	owner := createTestUser(t, service, admin, "ac2-owner", []string{"requester"}, nil)
	intruder := createTestUser(t, service, admin, "ac2-intruder", []string{"requester"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac2")

	created, err := service.createCollectionRequest(ctx, owner, standardRequest(network, "ac2-request"))
	if err != nil {
		t.Fatal(err)
	}
	sampleID := core.String(created["sampleId"])

	// Positive control: the owning requester can read their own sample.
	own, err := service.Handle(ctx, "read_sample_state", core.Map{"sampleId": sampleID}, owner)
	if err != nil {
		t.Fatalf("owner was refused their own sample: %v", err)
	}
	if core.String(own["requesterId"]) != owner.ID {
		t.Fatalf("owner's sample view = %v", own)
	}

	// The second requester holds a real role and names a real sample, and is
	// still refused because the sample is outside their own work.
	out, err := service.Handle(ctx, "read_sample_state", core.Map{"sampleId": sampleID}, intruder)
	assertHTTPErrorStatus(t, err, 403)
	assertHTTPErrorCode(t, err, "forbidden")
	if out != nil {
		t.Fatalf("a refused sample read still returned data: %v", out)
	}
	// The lifecycle view is the same sample data by another name and must be
	// scoped identically.
	_, err = service.Handle(ctx, "read_sample_lifecycle", core.Map{"sampleId": sampleID}, intruder)
	assertHTTPErrorStatus(t, err, 403)
	assertHTTPErrorCode(t, err, "forbidden")
}

// AC-8: an origin may be a home, a clinic, or a network node. All three are
// accepted and stored; anything else is refused with invalid_origin_type.
func TestAC8OriginMayBeHomeClinicOrNodeAndNothingElse(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac8-requester", []string{"requester"}, nil)
	createTestUser(t, service, admin, "ac8-collector", []string{"collection_professional"}, []string{"LX-A"})
	network := buildServiceableNetwork(t, service, admin, "ac8")

	for _, originType := range []string{"home", "clinic", "node"} {
		request := standardRequest(network, "ac8-"+originType)
		request["originType"] = originType
		created, err := service.createCollectionRequest(ctx, requester, request)
		if err != nil {
			t.Fatalf("originType %q was refused: %v", originType, err)
		}
		if created["serviceability"] != "serviceable" {
			t.Fatalf("originType %q serviceability = %v", originType, created["serviceability"])
		}
		// The classification is not merely accepted, it is stored and echoed back.
		state, err := service.Handle(
			ctx, "read_sample_state",
			core.Map{"sampleId": core.String(created["sampleId"])}, requester,
		)
		if err != nil {
			t.Fatal(err)
		}
		if core.String(state["originType"]) != originType {
			t.Fatalf("stored originType = %v, want %q", state["originType"], originType)
		}
	}

	// Case and surrounding whitespace are normalised, so these still name one of
	// the three permitted origins.
	for index, spelled := range []string{" Clinic ", "NODE", "HoMe"} {
		request := standardRequest(network, "ac8-spelled-"+core.String(index))
		request["originType"] = spelled
		if _, err := service.createCollectionRequest(ctx, requester, request); err != nil {
			t.Fatalf("originType %q was refused: %v", spelled, err)
		}
	}

	for _, bogus := range []string{"warehouse", "hospital", "collection_point", "0"} {
		request := standardRequest(network, "ac8-bogus-"+bogus)
		request["originType"] = bogus
		_, err := service.createCollectionRequest(ctx, requester, request)
		assertHTTPErrorStatus(t, err, 400)
		assertHTTPErrorCode(t, err, "invalid_origin_type")
	}

	// The dry run must refuse the same value for the same reason.
	dryRun := standardRequest(network, "ac8-dry-run")
	dryRun["originType"] = "warehouse"
	_, err := service.checkServiceability(ctx, requester, dryRun)
	assertHTTPErrorStatus(t, err, 400)
	assertHTTPErrorCode(t, err, "invalid_origin_type")
}

// AC-23: laboratory acceptance is configured against a location that actually
// provides the laboratory function. A location that does not is refused.
func TestAC23AcceptanceRequiresALaboratoryFunctionLocation(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	network := buildServiceableNetwork(t, service, admin, "ac23")

	// A real, active site that is a collection point and a hub but is not a
	// laboratory. Both identifiers below exist, so the only clause that can fail
	// is the laboratory-function requirement.
	notALaboratory, err := service.createLocation(ctx, admin, core.Map{
		"name": "ac23 depot", "active": true,
		"coordinate": core.Map{"latitude": 12.9616, "longitude": 77.5846},
		"functions": []string{
			mongostore.FunctionCollectionPoint, mongostore.FunctionHub,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	depotID := core.String(notALaboratory["locationId"])

	_, err = service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": depotID, "sampleTypeId": network.sampleTypeID, "accepts": true,
	})
	assertHTTPErrorStatus(t, err, 400)
	assertHTTPErrorCode(t, err, "invalid_acceptance")

	// Nothing was recorded for the non-laboratory site.
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		for _, acceptance := range state.Acceptances {
			if acceptance.LaboratoryID == depotID {
				t.Fatalf("acceptance was stored against a non-laboratory: %+v", acceptance)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Positive control: the same call against a laboratory-function location is
	// accepted, so the refusal above is about the function and nothing else.
	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": network.laboratoryID,
		"sampleTypeId": network.sampleTypeID, "accepts": true,
	}); err != nil {
		t.Fatalf("acceptance against a laboratory location was refused: %v", err)
	}

	// Adding the laboratory function to the depot makes the same call succeed,
	// without restarting anything.
	if _, err := service.updateLocation(ctx, admin, core.Map{
		"locationId": depotID, "name": "ac23 depot",
		"coordinate": core.Map{"latitude": 12.9616, "longitude": 77.5846},
		"functions": []string{
			mongostore.FunctionCollectionPoint, mongostore.FunctionHub,
			mongostore.FunctionLaboratory,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": depotID, "sampleTypeId": network.sampleTypeID, "accepts": true,
	}); err != nil {
		t.Fatalf("acceptance after gaining the laboratory function was refused: %v", err)
	}
}

// AC-24: an administrator deactivates a whole location through the real
// operation and the very same live process stops counting it as coverage. No
// restart, no reload: the serviceability check below runs against the same
// Service instance that answered "serviceable" a moment earlier.
func TestAC24DeactivatingAWholeLocationRemovesCoverageWithoutRestart(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac24-requester", []string{"requester"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac24")

	probe := core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": network.sampleTypeID,
		"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T12:00:00Z",
	}
	before, err := service.checkServiceability(ctx, requester, probe)
	if err != nil {
		t.Fatal(err)
	}
	if !core.Bool(before["serviceable"]) {
		t.Fatalf("origin was not serviceable before deactivation: %v", before)
	}
	snapshotBefore, err := service.readServiceabilitySnapshot(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotListsRegion(snapshotBefore, network.nodeID) {
		t.Fatalf("collection point is missing from the coverage snapshot: %v", snapshotBefore)
	}

	// The whole site goes down, through the real administrative operation.
	status, err := service.Handle(ctx, "set_location_status", core.Map{
		"locationId": network.nodeID, "active": false,
	}, admin)
	if err != nil {
		t.Fatalf("deactivating the location was refused: %v", err)
	}
	if core.Bool(status["active"]) {
		t.Fatalf("set_location_status reported the site still active: %v", status)
	}

	after, err := service.checkServiceability(ctx, requester, probe)
	if err != nil {
		t.Fatal(err)
	}
	if core.Bool(after["serviceable"]) {
		t.Fatalf("a deactivated collection point still provided coverage: %v", after)
	}
	if core.String(after["reason"]) != reasonOutsideServiceCoverage {
		t.Fatalf("reason after deactivation = %v, want %q", after["reason"], reasonOutsideServiceCoverage)
	}
	snapshotAfter, err := service.readServiceabilitySnapshot(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotListsRegion(snapshotAfter, network.nodeID) {
		t.Fatalf("a deactivated site is still published as a coverage region: %v", snapshotAfter)
	}

	// A real request for the same origin agrees with the check.
	created, err := service.createCollectionRequest(ctx, requester, standardRequest(network, "ac24-after"))
	if err != nil {
		t.Fatal(err)
	}
	if created["serviceability"] != "unserviceable" {
		t.Fatalf("request after deactivation = %v", created)
	}

	// Reactivating the same site in the same process restores coverage.
	if _, err := service.Handle(ctx, "set_location_status", core.Map{
		"locationId": network.nodeID, "active": true,
	}, admin); err != nil {
		t.Fatal(err)
	}
	restored, err := service.checkServiceability(ctx, requester, probe)
	if err != nil {
		t.Fatal(err)
	}
	if !core.Bool(restored["serviceable"]) {
		t.Fatalf("reactivating the site did not restore coverage: %v", restored)
	}
}

func snapshotListsRegion(snapshot core.Map, locationID string) bool {
	regions, ok := snapshot["regions"].([]core.Map)
	if !ok {
		return false
	}
	for _, region := range regions {
		if core.String(region["locationId"]) == locationID {
			return true
		}
	}
	return false
}

// latitudeAtDistanceKM returns the latitude that is exactly wanted kilometres
// north of base, measured by the application's own distance function. For a
// pure latitude offset the distance is linear in the offset, so one scaling
// step lands on the target; the caller asserts the result rather than trusting
// any hard-coded earth radius.
func latitudeAtDistanceKM(base core.Map, wanted float64) float64 {
	baseLatitude := core.Float(base["latitude"])
	probeDelta := 0.05
	probe := core.Map{
		"latitude": baseLatitude + probeDelta, "longitude": core.Float(base["longitude"]),
	}
	measured := core.DistanceKM(base, probe)
	return baseLatitude + probeDelta*wanted/measured
}

// AC-35: the five-kilometre service boundary is inclusive. An origin at exactly
// 5.0 km from an active collection point is serviceable; a farther origin is
// not. This is the real probe: it calls the serviceability decision, unlike the
// distance-only assertion in TestDistanceKMMeasuresTheFiveKilometreCoordinate.
func TestAC35FiveKilometreServiceBoundaryIsInclusive(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac35-requester", []string{"requester"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac35")

	collectionPoint := core.Map{
		"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
	}
	onTheBoundary := core.Map{
		"latitude":  latitudeAtDistanceKM(collectionPoint, 5.0),
		"longitude": fixtureOriginLongitude,
	}
	justOutside := core.Map{
		"latitude":  latitudeAtDistanceKM(collectionPoint, 5.05),
		"longitude": fixtureOriginLongitude,
	}
	// The test coordinates are what the test claims they are.
	if distance := core.DistanceKM(collectionPoint, onTheBoundary); math.Abs(distance-5.0) > 1e-9 {
		t.Fatalf("boundary origin is %.12f km from the collection point, want exactly 5.0", distance)
	}
	if distance := core.DistanceKM(collectionPoint, justOutside); distance <= 5.0 {
		t.Fatalf("control origin is %.6f km away, want more than 5.0", distance)
	}

	probe := func(origin core.Map) core.Map {
		t.Helper()
		out, err := service.checkServiceability(ctx, requester, core.Map{
			"origin": origin, "originType": "home", "sampleTypeId": network.sampleTypeID,
			"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T12:00:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	boundary := probe(onTheBoundary)
	if !core.Bool(boundary["serviceable"]) {
		t.Fatalf(
			"an origin exactly 5.0 km from an active collection point was unserviceable: %v",
			boundary,
		)
	}
	outside := probe(justOutside)
	if core.Bool(outside["serviceable"]) {
		t.Fatalf("an origin beyond 5 km was serviceable: %v", outside)
	}
	if core.String(outside["reason"]) != reasonOutsideServiceCoverage {
		t.Fatalf("reason beyond the boundary = %v", outside["reason"])
	}

	// A real request on the boundary plans, so the inclusive boundary is not a
	// dry-run-only nicety.
	request := standardRequest(network, "ac35-boundary")
	request["origin"] = onTheBoundary
	created, err := service.createCollectionRequest(ctx, requester, request)
	if err != nil {
		t.Fatal(err)
	}
	if created["serviceability"] != "serviceable" {
		t.Fatalf("boundary request = %v", created)
	}
}

// AC-63: a plan update that names an outdated plan version is refused with a
// conflict, so a stale client cannot overwrite a newer plan.
func TestAC63StalePlanVersionIsRefusedWithConflict(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac63-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac63-dispatcher", []string{"dispatcher"}, nil)
	first := createTestUser(t, service, admin, "ac63-collector-a", []string{"collection_professional"}, []string{"LX-A"})
	second := createTestUser(t, service, admin, "ac63-collector-b", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "ac63-courier", []string{"delivery_partner"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac63")

	created, err := service.createCollectionRequest(ctx, requester, standardRequest(network, "ac63-request"))
	if err != nil {
		t.Fatal(err)
	}
	journeyID := core.String(created["journeyId"])
	plan, err := service.readJourneyPlan(ctx, admin, journeyID)
	if err != nil {
		t.Fatal(err)
	}
	if core.Int(plan["planVersion"]) != 1 {
		t.Fatalf("new journey planVersion = %v, want 1", plan["planVersion"])
	}
	collectionID := core.String(plan["collectionAssignmentId"])

	// Whichever collector the fair offer picked, the other one is the override
	// target, so the successful edit below never re-offers to the same worker.
	var offered string
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		offered = state.Assignments[collectionID].WorkerID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	replacement := first.ID
	if offered == first.ID {
		replacement = second.ID
	}

	// One legitimate edit takes the plan to version 2.
	edited, err := service.editPlan(ctx, dispatcher, core.Map{
		"journeyId": journeyID, "expectedPlanVersion": 1,
		"changeType": "replace_worker",
		"changes": core.Map{
			"assignmentId": collectionID, "workerId": replacement,
			"reason": "ac63 baseline edit",
		},
		"idempotencyKey": "ac63-edit-v1",
	})
	if err != nil {
		t.Fatalf("baseline plan edit: %v", err)
	}
	if core.Int(edited["planVersion"]) != 2 {
		t.Fatalf("planVersion after one edit = %v, want 2", edited["planVersion"])
	}

	// A client that still believes the plan is at version 1 is one behind.
	_, err = service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": journeyID, "expectedPlanVersion": 1,
		"changeType": "replace_worker",
		"changes": core.Map{
			"assignmentId": collectionID, "workerId": offered,
			"reason": "ac63 stale edit",
		},
		"idempotencyKey": "ac63-edit-stale",
	}, dispatcher)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "stale_plan")

	// reroute_journey shares the same guard and must report it the same way.
	_, err = service.Handle(ctx, "reroute_journey", core.Map{
		"journeyId": journeyID, "expectedPlanVersion": 1,
		"reason": "ac63 stale reroute", "idempotencyKey": "ac63-reroute-stale",
	}, dispatcher)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "stale_plan")

	// The refusals changed nothing: the plan is still at version 2 and still
	// offered to the worker the successful edit chose.
	stillCurrent, err := service.readJourneyPlan(ctx, admin, journeyID)
	if err != nil {
		t.Fatal(err)
	}
	if core.Int(stillCurrent["planVersion"]) != 2 {
		t.Fatalf("planVersion after stale attempts = %v, want 2", stillCurrent["planVersion"])
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if state.Assignments[collectionID].WorkerID != replacement {
			t.Fatalf(
				"a stale edit changed the collection worker to %q, want %q",
				state.Assignments[collectionID].WorkerID, replacement,
			)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Naming the current version still works, so the guard rejects staleness
	// rather than all edits.
	current, err := service.editPlan(ctx, dispatcher, core.Map{
		"journeyId": journeyID, "expectedPlanVersion": 2,
		"changeType": "replace_worker",
		"changes": core.Map{
			"assignmentId": collectionID, "workerId": offered,
			"reason": "ac63 current edit",
		},
		"idempotencyKey": "ac63-edit-v2",
	})
	if err != nil {
		t.Fatalf("edit naming the current plan version: %v", err)
	}
	if core.Int(current["planVersion"]) != 3 {
		t.Fatalf("planVersion after the accepted edit = %v, want 3", current["planVersion"])
	}
}
