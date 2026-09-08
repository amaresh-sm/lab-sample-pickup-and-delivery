package service

import (
	"context"
	"sort"
	"testing"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// planShape is everything about a plan that a rejected change must leave alone.
type planShape struct {
	planVersion   int64
	status        string
	routeMode     string
	sampleIDs     []string
	estimatedAt   string
	stops         []string
	assignments   []string
	reservations  []string
	capacityState string
}

func capturePlanShape(t *testing.T, service *Service, journeyID string) planShape {
	t.Helper()
	var out planShape
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		journey := state.Journeys[journeyID]
		if journey == nil {
			t.Fatalf("journey %s is missing", journeyID)
		}
		out.planVersion = journey.PlanVersion
		out.status = journey.Status
		out.routeMode = journey.RouteMode
		out.sampleIDs = sorted(journey.SampleIDs)
		out.estimatedAt = formatTime(journey.EstimatedDeliveryAt)
		for _, stop := range stopsForJourney(state, journeyID) {
			out.stops = append(out.stops, core.String(stop.Sequence)+":"+stop.LocationID+":"+stop.StopType+":"+stop.ID)
		}
		for _, assignment := range state.Assignments {
			if assignment.JourneyID == journeyID {
				out.assignments = append(out.assignments, assignment.ID+":"+assignment.Kind+":"+assignment.Status)
			}
		}
		sort.Strings(out.assignments)
		for _, reservation := range state.Reservations {
			if reservation.JourneyID == journeyID {
				out.reservations = append(
					out.reservations,
					reservation.ResourceID+":"+core.String(reservation.Units)+":"+reservation.Status,
				)
			}
		}
		sort.Strings(out.reservations)
		out.capacityState = describeCapacities(state)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertSamePlanShape(t *testing.T, label string, before, after planShape) {
	t.Helper()
	if before.planVersion != after.planVersion {
		t.Fatalf("%s: planVersion changed %d -> %d", label, before.planVersion, after.planVersion)
	}
	if before.status != after.status || before.routeMode != after.routeMode {
		t.Fatalf("%s: journey changed %s/%s -> %s/%s", label, before.status, before.routeMode, after.status, after.routeMode)
	}
	if core.String(before.sampleIDs) != core.String(after.sampleIDs) {
		t.Fatalf("%s: sample set changed %v -> %v", label, before.sampleIDs, after.sampleIDs)
	}
	if before.estimatedAt != after.estimatedAt {
		t.Fatalf("%s: estimated delivery changed %s -> %s", label, before.estimatedAt, after.estimatedAt)
	}
	if core.String(before.stops) != core.String(after.stops) {
		t.Fatalf("%s: stop set changed\nbefore=%v\nafter =%v", label, before.stops, after.stops)
	}
	if core.String(before.assignments) != core.String(after.assignments) {
		t.Fatalf("%s: assignments changed\nbefore=%v\nafter =%v", label, before.assignments, after.assignments)
	}
	if core.String(before.reservations) != core.String(after.reservations) {
		t.Fatalf("%s: reservations changed\nbefore=%v\nafter =%v", label, before.reservations, after.reservations)
	}
	if before.capacityState != after.capacityState {
		t.Fatalf("%s: capacity pools changed\nbefore=%s\nafter =%s", label, before.capacityState, after.capacityState)
	}
}

// createRequestFor raises one collection request for a named sample type and
// returns its sample and journey identifiers.
func createRequestFor(
	t *testing.T,
	service *Service,
	requester core.Principal,
	sampleTypeID, containerType, key string,
) (string, string) {
	t.Helper()
	created, err := service.createCollectionRequest(context.Background(), requester, core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": sampleTypeID,
		"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T12:00:00Z",
		"containerType": containerType, "temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "idempotencyKey": key,
	})
	if err != nil {
		t.Fatalf("create request %s: %v", key, err)
	}
	if created["serviceability"] != "serviceable" {
		t.Fatalf("request %s = %v", key, created)
	}
	return core.String(created["sampleId"]), core.String(created["journeyId"])
}

// AC-14: Indigo must not share a container with Amber. The two are created with
// identical containers, identical temperature ranges, the same destination and
// the same cooling requirement, so the only reason a batch of them can fail is
// the Indigo/Amber separation rule itself. The Amber+Amber control in the same
// test proves the batch would otherwise be valid.
//
// AC-44 rides along: the rejected batch leaves the current plan exactly as it
// was.
func TestAC14AmberAndIndigoCannotShareABatch(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac14-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac14-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac14-collector", []string{"collection_professional"}, []string{"LX-A", "LX-I"})
	createTestUser(t, service, admin, "ac14-courier", []string{"delivery_partner"}, nil)
	createTestUser(t, service, admin, "ac14-operator", []string{"laboratory_team"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac14")

	// Indigo, deliberately identical to Amber in every batching dimension
	// except its named separation from Amber.
	indigo, err := service.createSampleType(ctx, admin, core.Map{
		"name": "Indigo", "containerType": "amber-container",
		"temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "requiresCooledRoute": false,
		"batchingAllowed": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	indigoTypeID := core.String(indigo["sampleTypeId"])
	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": network.laboratoryID, "sampleTypeId": indigoTypeID, "accepts": true,
	}); err != nil {
		t.Fatal(err)
	}

	amberSampleID, amberJourneyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac14-amber-1",
	)
	indigoSampleID, _ := createRequestFor(
		t, service, requester, indigoTypeID, "amber-container", "ac14-indigo",
	)
	secondAmberID, secondAmberJourneyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac14-amber-2",
	)

	// The separation is recorded symmetrically on both samples, so neither
	// ordering of the batch can slip through.
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		amber, indigoSample := state.Samples[amberSampleID], state.Samples[indigoSampleID]
		if !stringSliceContains(amber.IncompatibleSampleTypes, indigoTypeID) ||
			!stringSliceContains(indigoSample.IncompatibleSampleTypes, network.sampleTypeID) {
			t.Fatalf(
				"Amber/Indigo separation is not on both samples: amber=%v indigo=%v",
				amber.IncompatibleSampleTypes, indigoSample.IncompatibleSampleTypes,
			)
		}
		// The two samples really are otherwise batch-compatible.
		if amber.ContainerType != indigoSample.ContainerType ||
			amber.TargetLaboratoryID != indigoSample.TargetLaboratoryID ||
			amber.TemperatureMinC != indigoSample.TemperatureMinC ||
			amber.TemperatureMaxC != indigoSample.TemperatureMaxC {
			t.Fatalf(
				"the test samples differ outside the separation rule: %+v vs %+v",
				amber, indigoSample,
			)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	before := capturePlanShape(t, service, amberJourneyID)

	_, err = service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": amberJourneyID, "expectedPlanVersion": 1,
		"changeType":     "merge_samples",
		"changes":        core.Map{"sampleIds": []string{amberSampleID, indigoSampleID}},
		"idempotencyKey": "ac14-merge-amber-indigo",
	}, dispatcher)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "incompatible_batch")

	// AC-44: the refused change kept the current plan.
	assertSamePlanShape(t, "after a refused Amber+Indigo batch", before, capturePlanShape(t, service, amberJourneyID))
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if state.Samples[indigoSampleID].ActiveJourneyID == amberJourneyID {
			t.Fatal("a refused batch still moved the Indigo sample onto the Amber journey")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The dedicated journey-merge operation shares the batch rules and must
	// refuse the same pairing.
	indigoJourneyID := ""
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		indigoJourneyID = state.Samples[indigoSampleID].ActiveJourneyID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Handle(ctx, "merge_journeys", core.Map{
		"targetJourneyId": amberJourneyID, "expectedPlanVersion": 1,
		"sourceJourneyIds": []string{indigoJourneyID},
		"idempotencyKey":   "ac14-merge-journeys-amber-indigo",
	}, dispatcher)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "incompatible_batch")
	assertSamePlanShape(t, "after a refused Amber+Indigo journey merge", before, capturePlanShape(t, service, amberJourneyID))

	// Control: two Amber samples, identical to the pairing above in every
	// respect except the separation rule, batch successfully.
	merged, err := service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": amberJourneyID, "expectedPlanVersion": 1,
		"changeType":     "merge_samples",
		"changes":        core.Map{"sampleIds": []string{amberSampleID, secondAmberID}},
		"idempotencyKey": "ac14-merge-amber-amber",
	}, dispatcher)
	if err != nil {
		t.Fatalf("an Amber+Amber batch was refused, so the refusals above may not be about separation: %v", err)
	}
	if core.String(merged["routeMode"]) != "batched" {
		t.Fatalf("Amber+Amber merge = %v", merged)
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		journey := state.Journeys[amberJourneyID]
		if len(journey.SampleIDs) != 2 {
			t.Fatalf("batched journey carries %v", journey.SampleIDs)
		}
		if state.Journeys[secondAmberJourneyID].Status != "replaced" {
			t.Fatalf("source journey status = %q", state.Journeys[secondAmberJourneyID].Status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-18: a Pulse sample must never wait for or join a consolidation batch.
func TestAC18PulseSampleCannotJoinABatch(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac18-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac18-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac18-collector", []string{"collection_professional"}, []string{"LX-A", "LX-P"})
	createTestUser(t, service, admin, "ac18-courier", []string{"delivery_partner"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac18")
	createTestUserWithLocation(t, service, admin, "ac18-operator", []string{"laboratory_team"}, nil, network.laboratoryID)
	createTestUserWithLocation(t, service, admin, "ac18-handler", []string{"node_handler"}, nil, network.nodeID)

	pulse, err := service.createSampleType(ctx, admin, core.Map{
		"name": "Pulse", "containerType": "pulse-container",
		"temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "requiresCooledRoute": false,
		// An administrator asking for batching gets refused by the named policy.
		"batchingAllowed": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pulseTypeID := core.String(pulse["sampleTypeId"])
	if core.Bool(pulse["batchingAllowed"]) {
		t.Fatalf("Pulse was created with batching enabled: %v", pulse)
	}
	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": network.laboratoryID, "sampleTypeId": pulseTypeID, "accepts": true,
	}); err != nil {
		t.Fatal(err)
	}
	// An administrator cannot re-enable batching for Pulse afterwards either.
	updated, err := service.updateSampleType(ctx, admin, core.Map{
		"sampleTypeId": pulseTypeID, "containerType": "pulse-container",
		"temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "batchingAllowed": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if core.Bool(updated["batchingAllowed"]) {
		t.Fatalf("an administrator enabled batching for Pulse: %v", updated)
	}

	amberSampleID, amberJourneyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac18-amber",
	)
	pulseSampleID, pulseJourneyID := createRequestFor(
		t, service, requester, pulseTypeID, "pulse-container", "ac18-pulse",
	)

	amberBefore := capturePlanShape(t, service, amberJourneyID)
	pulseBefore := capturePlanShape(t, service, pulseJourneyID)

	// Pulling Pulse into somebody else's batch is refused.
	_, err = service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": amberJourneyID, "expectedPlanVersion": 1,
		"changeType":     "merge_samples",
		"changes":        core.Map{"sampleIds": []string{amberSampleID, pulseSampleID}},
		"idempotencyKey": "ac18-merge-onto-amber",
	}, dispatcher)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "pulse_no_batch")

	// Batching something onto the Pulse journey is refused just the same, so the
	// rule is about the Pulse sample rather than about which journey leads.
	_, err = service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": pulseJourneyID, "expectedPlanVersion": 1,
		"changeType":     "merge_samples",
		"changes":        core.Map{"sampleIds": []string{pulseSampleID, amberSampleID}},
		"idempotencyKey": "ac18-merge-onto-pulse",
	}, dispatcher)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "pulse_no_batch")

	// The dedicated journey-merge operation refuses it too.
	_, err = service.Handle(ctx, "merge_journeys", core.Map{
		"targetJourneyId": amberJourneyID, "expectedPlanVersion": 1,
		"sourceJourneyIds": []string{pulseJourneyID},
		"idempotencyKey":   "ac18-merge-journeys",
	}, dispatcher)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "pulse_no_batch")

	// AC-44: neither plan moved.
	assertSamePlanShape(t, "Amber journey after refused Pulse batches", amberBefore, capturePlanShape(t, service, amberJourneyID))
	assertSamePlanShape(t, "Pulse journey after refused Pulse batches", pulseBefore, capturePlanShape(t, service, pulseJourneyID))
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if state.Samples[pulseSampleID].ActiveJourneyID != pulseJourneyID {
			t.Fatal("a refused batch moved the Pulse sample off its own journey")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-44: a plan change whose revalidation fails is rejected and the current plan
// is kept — the same plan version, the same stops, the same work and the same
// capacity reservations, with nothing partially applied and no capacity leaked.
func TestAC44FailedPlanRevalidationKeepsTheCurrentPlan(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac44-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac44-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac44-collector", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "ac44-courier", []string{"delivery_partner"}, nil)
	createTestUser(t, service, admin, "ac44-handler", []string{"node_handler"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac44")

	// An active transfer point that nothing connects to. Inserting it as a stop
	// passes the stop-level checks and then fails route revalidation.
	stranded, err := service.createLocation(ctx, admin, core.Map{
		"name": "ac44 stranded hub", "active": true,
		"coordinate": core.Map{"latitude": 13.0416, "longitude": 77.6646},
		"functions":  []string{mongostore.FunctionTransferPoint},
	})
	if err != nil {
		t.Fatal(err)
	}
	strandedID := core.String(stranded["locationId"])
	if _, err := service.configureCapacity(ctx, admin, core.Map{
		"resourceId": strandedID, "capacityUnits": 10, "cooledCapacityUnits": 10,
	}); err != nil {
		t.Fatal(err)
	}

	_, journeyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac44-request",
	)
	before := capturePlanShape(t, service, journeyID)
	if before.planVersion != 1 || len(before.stops) != 2 {
		t.Fatalf("unexpected starting plan: %+v", before)
	}

	_, err = service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": journeyID, "expectedPlanVersion": 1,
		"changeType": "add_stop",
		"changes": core.Map{
			"locationId": strandedID, "position": 1, "dwellMinutes": 5,
		},
		"idempotencyKey": "ac44-add-unreachable-stop",
	}, dispatcher)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "invalid_route")

	after := capturePlanShape(t, service, journeyID)
	assertSamePlanShape(t, "after a failed route revalidation", before, after)

	// Nothing of the rejected change survived anywhere.
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		for _, stop := range state.Stops {
			if stop.JourneyID == journeyID && stop.LocationID == strandedID {
				t.Fatalf("the rejected stop was persisted: %+v", stop)
			}
		}
		for _, reservation := range state.Reservations {
			if reservation.JourneyID == journeyID && reservation.ResourceID == strandedID {
				t.Fatalf("the rejected change left a reservation behind: %+v", reservation)
			}
		}
		if capacity := state.Capacities[strandedID]; capacity != nil && capacity.ReservedUnits != 0 {
			t.Fatalf("the rejected change reserved capacity at the stranded hub: %+v", capacity)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The plan is still usable: an edit naming the unchanged version is accepted,
	// which it could not be if the rejected change had bumped the version.
	accepted, err := service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": journeyID, "expectedPlanVersion": 1,
		"changeType":     "cancel_work",
		"changes":        core.Map{"reason": "ac44 teardown"},
		"idempotencyKey": "ac44-cancel",
	}, dispatcher)
	if err != nil {
		t.Fatalf("the plan was left unusable by the rejected change: %v", err)
	}
	if core.Int(accepted["planVersion"]) != 2 {
		t.Fatalf("planVersion after the accepted change = %v, want 2", accepted["planVersion"])
	}
}
