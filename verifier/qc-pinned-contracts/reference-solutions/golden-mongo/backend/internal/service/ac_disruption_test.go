package service

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// replanNetwork is a network that survives the loss of any single resource: a
// cheap primary route, a dearer alternate hub, a second accepting laboratory
// reachable from the primary hub, and one spare location nothing routes through.
//
// Costs are chosen so planning always picks the primary route and every
// alternate is strictly dearer, which makes a redirected replan unambiguous:
//
//	node -> hubPrimary -> labPrimary        cost 2, 20 minutes  (planned)
//	node -> hubAlternate -> labPrimary      cost 7, 40 minutes
//	node -> hubPrimary -> labAlternate      cost 10, 50 minutes
type replanNetwork struct {
	nodeID          string
	hubPrimaryID    string
	hubAlternateID  string
	labPrimaryID    string
	labAlternateID  string
	spareLocationID string
	sampleTypeID    string

	primaryHopID      string // node -> hubPrimary
	primaryFinalID    string // hubPrimary -> labPrimary
	alternateHopID    string // node -> hubAlternate
	alternateFinalID  string // hubAlternate -> labPrimary
	alternateLabHopID string // hubPrimary -> labAlternate
}

// replanFixture is the replannable network plus every principal a journey over it
// needs, including a standby courier so a disrupted worker can be replaced by an
// eligible one and an idle courier who holds no work at all.
type replanFixture struct {
	network     replanNetwork
	requester   core.Principal
	dispatcher  core.Principal
	collector   core.Principal
	courier     core.Principal
	standby     core.Principal
	idleCourier core.Principal
	handler     core.Principal
	operator    core.Principal
}

func buildReplanFixture(
	t *testing.T,
	service *Service,
	admin core.Principal,
	prefix string,
) replanFixture {
	t.Helper()
	ctx := context.Background()
	fixture := replanFixture{
		requester:  createTestUser(t, service, admin, prefix+"-requester", []string{"requester"}, nil),
		dispatcher: createTestUser(t, service, admin, prefix+"-dispatcher", []string{"dispatcher"}, nil),
		collector:  createTestUser(t, service, admin, prefix+"-collector", []string{"collection_professional"}, []string{"LX-A"}),
		courier:    createTestUser(t, service, admin, prefix+"-courier", []string{"delivery_partner"}, nil),
		standby:    createTestUser(t, service, admin, prefix+"-standby", []string{"delivery_partner"}, nil),
		handler:    createTestUser(t, service, admin, prefix+"-handler", []string{"node_handler"}, nil),
		operator:   createTestUser(t, service, admin, prefix+"-operator", []string{"laboratory_team"}, nil),
	}
	// Created last so the fair-assignment order never offers this courier the
	// first plan's work: it exists only as a resource nothing depends on.
	fixture.idleCourier = createTestUser(t, service, admin, prefix+"-idle", []string{"delivery_partner"}, nil)

	newLocation := func(name string, latitude, longitude float64, functions ...string) string {
		t.Helper()
		out, err := service.createLocation(ctx, admin, core.Map{
			"name": prefix + " " + name, "active": true,
			"coordinate": core.Map{"latitude": latitude, "longitude": longitude},
			"functions":  functions,
		})
		if err != nil {
			t.Fatalf("create location %s: %v", name, err)
		}
		return core.String(out["locationId"])
	}
	network := replanNetwork{
		nodeID: newLocation(
			"node", fixtureOriginLatitude, fixtureOriginLongitude,
			mongostore.FunctionCollectionPoint, mongostore.FunctionTransferPoint,
		),
		hubPrimaryID:    newLocation("primary hub", 12.9766, 77.5996, mongostore.FunctionTransferPoint),
		hubAlternateID:  newLocation("alternate hub", 12.9800, 77.6000, mongostore.FunctionTransferPoint),
		labPrimaryID:    newLocation("primary laboratory", 12.9816, 77.6046, mongostore.FunctionLaboratory),
		labAlternateID:  newLocation("alternate laboratory", 12.9850, 77.6100, mongostore.FunctionLaboratory),
		spareLocationID: newLocation("spare hub", 13.0416, 77.6646, mongostore.FunctionTransferPoint),
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
	network.sampleTypeID = core.String(sampleType["sampleTypeId"])
	for _, laboratoryID := range []string{network.labPrimaryID, network.labAlternateID} {
		if _, err := service.configureAcceptance(ctx, admin, core.Map{
			"laboratoryId": laboratoryID, "sampleTypeId": network.sampleTypeID, "accepts": true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	newConnection := func(from, to string, minutes int, cost float64) string {
		t.Helper()
		out, err := service.createConnection(ctx, admin, core.Map{
			"fromLocationId": from, "toLocationId": to,
			"travelMinutes": minutes, "costUnits": cost, "active": true,
		})
		if err != nil {
			t.Fatalf("create connection %s -> %s: %v", from, to, err)
		}
		return core.String(out["connectionId"])
	}
	network.primaryHopID = newConnection(network.nodeID, network.hubPrimaryID, 10, 1.0)
	network.primaryFinalID = newConnection(network.hubPrimaryID, network.labPrimaryID, 10, 1.0)
	network.alternateHopID = newConnection(network.nodeID, network.hubAlternateID, 20, 4.0)
	network.alternateFinalID = newConnection(network.hubAlternateID, network.labPrimaryID, 20, 3.0)
	network.alternateLabHopID = newConnection(network.hubPrimaryID, network.labAlternateID, 40, 9.0)

	for _, resourceID := range []string{
		network.nodeID, network.hubPrimaryID, network.hubAlternateID,
		network.labPrimaryID, network.labAlternateID, network.spareLocationID,
		network.primaryHopID, network.primaryFinalID, network.alternateHopID,
		network.alternateFinalID, network.alternateLabHopID,
	} {
		if _, err := service.configureCapacity(ctx, admin, core.Map{
			"resourceId": resourceID, "capacityUnits": 10, "cooledCapacityUnits": 10,
		}); err != nil {
			t.Fatal(err)
		}
	}
	fixture.network = network
	return fixture
}

// planPrimaryJourney raises one collection request over the replannable network
// and asserts it really was planned along the cheap primary route, so a later
// route change can only be the replan's own decision.
func (f replanFixture) planPrimaryJourney(
	t *testing.T,
	service *Service,
	key string,
	overrides core.Map,
) (string, string) {
	t.Helper()
	sampleID, journeyID := requestSampleOfType(
		t, service, f.requester, f.network.sampleTypeID, "amber-container",
		2.0, 8.0, overrides, key,
	)
	stops := journeyStopLocations(t, service, journeyID)
	want := []string{f.network.nodeID, f.network.hubPrimaryID, f.network.labPrimaryID}
	if core.String(stops) != core.String(want) {
		t.Fatalf("request %s was not planned along the primary route: %v", key, stops)
	}
	return sampleID, journeyID
}

func journeyStopLocations(t *testing.T, service *Service, journeyID string) []string {
	t.Helper()
	out := []string{}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		for _, stop := range stopsForJourney(state, journeyID) {
			out = append(out, stop.LocationID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// journeyFacts is what a stored plan says about itself.
type journeyFacts struct {
	status              string
	routeMode           string
	planVersion         int64
	isReplacement       bool
	parentJourneyID     string
	sampleIDs           []string
	stopLocations       []string
	estimatedDeliveryAt string
	capacityUsed        int
}

func readJourneyFacts(t *testing.T, service *Service, journeyID string) journeyFacts {
	t.Helper()
	var out journeyFacts
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		journey := state.Journeys[journeyID]
		if journey == nil {
			t.Fatalf("journey %s is missing", journeyID)
		}
		out = journeyFacts{
			status: journey.Status, routeMode: journey.RouteMode,
			planVersion: journey.PlanVersion, isReplacement: journey.IsReplacement,
			parentJourneyID: journey.ParentJourneyID, sampleIDs: sorted(journey.SampleIDs),
			estimatedDeliveryAt: formatTime(journey.EstimatedDeliveryAt),
			capacityUsed:        journey.CapacityUsed,
		}
		for _, stop := range stopsForJourney(state, journeyID) {
			out.stopLocations = append(out.stopLocations, stop.LocationID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// fireDisruption reports one resource as unavailable through the real operation.
func fireDisruption(
	t *testing.T,
	service *Service,
	actor core.Principal,
	resourceType, resourceID, occurredAt, reason, key string,
) (core.Map, error) {
	t.Helper()
	return service.Handle(context.Background(), "report_disruption", core.Map{
		"resourceType": resourceType, "resourceId": resourceID,
		"occurredAt": occurredAt, "reason": reason, "idempotencyKey": key,
	}, actor)
}

// assertOneReplacement checks the disruption response and the stored plans agree
// that the original journey was replaced by exactly ONE new active plan carrying
// the sample, and returns that replacement's identifier.
func assertOneReplacement(
	t *testing.T,
	service *Service,
	out core.Map,
	originalJourneyID, sampleID string,
) string {
	t.Helper()
	if core.String(out["replanStatus"]) != "replaced" {
		t.Fatalf("replanStatus = %v, want replaced: %v", out["replanStatus"], out)
	}
	replacementIDs := core.Strings(out["replacementPlanIds"])
	if len(replacementIDs) != 1 {
		t.Fatalf("replacementPlanIds = %v, want exactly one replacement plan", replacementIDs)
	}
	if !stringSliceContains(core.Strings(out["affectedSampleIds"]), sampleID) {
		t.Fatalf("affectedSampleIds = %v, want the disrupted sample %s", out["affectedSampleIds"], sampleID)
	}
	replacementID := replacementIDs[0]
	if replacementID == originalJourneyID {
		t.Fatalf("the replacement plan is the original journey %s", originalJourneyID)
	}
	original := readJourneyFacts(t, service, originalJourneyID)
	if original.status != "replaced" {
		t.Fatalf("the disrupted journey status = %q, want replaced", original.status)
	}
	replacement := readJourneyFacts(t, service, replacementID)
	if replacement.status != "active" || !replacement.isReplacement ||
		replacement.parentJourneyID != originalJourneyID {
		t.Fatalf("replacement plan = %+v", replacement)
	}
	if !stringSliceContains(replacement.sampleIDs, sampleID) {
		t.Fatalf("the replacement plan carries %v, not the disrupted sample", replacement.sampleIDs)
	}
	if err := journeyPlanValidity(t, service, replacementID); err != nil {
		t.Fatalf("the replacement plan does not validate: %v", err)
	}
	if got := activeReplacementPlanIDs(t, service, storeAdministrator(t, service), sampleID); len(got) != 1 || got[0] != replacementID {
		t.Fatalf("active replacement plans for the sample = %v, want exactly [%s]", got, replacementID)
	}
	return replacementID
}

// activeReplacementPlanIDs reads the sample's active replacement plans from the
// real lifecycle view, which is the surface an operator sees.
func activeReplacementPlanIDs(
	t *testing.T,
	service *Service,
	reader core.Principal,
	sampleID string,
) []string {
	t.Helper()
	lifecycle, err := service.Handle(
		context.Background(), "read_sample_lifecycle", core.Map{"sampleId": sampleID}, reader,
	)
	if err != nil {
		t.Fatalf("read sample lifecycle: %v", err)
	}
	raw, present := lifecycle["activeReplacementPlanIds"]
	if !present {
		t.Fatalf("the lifecycle view does not report activeReplacementPlanIds: %v", lifecycle)
	}
	return core.Strings(raw)
}

// legFact is one transport assignment as the store holds it.
type legFact struct {
	id          string
	leg         int
	from        string
	to          string
	status      string
	workerID    string
	completedAt string
}

func liveTransportLegs(t *testing.T, service *Service, journeyID, sampleID string) []legFact {
	t.Helper()
	out := []legFact{}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		for _, assignment := range assignmentsForJourney(state, journeyID) {
			if assignment.Kind != "transport" || assignment.SampleID != sampleID ||
				assignment.Status == "cancelled" {
				continue
			}
			fact := legFact{
				id: assignment.ID, leg: assignment.LegSequence,
				from: assignment.FromLocationID, to: assignment.ToLocationID,
				status: assignment.Status, workerID: assignment.WorkerID,
			}
			if assignment.CompletedAt != nil {
				fact.completedAt = formatTime(*assignment.CompletedAt)
			}
			out = append(out, fact)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].leg < out[j].leg })
	return out
}

// journeyWorkerIDs is every worker any live piece of a journey's work is held by.
func journeyWorkerIDs(t *testing.T, service *Service, journeyID string) []string {
	t.Helper()
	out := []string{}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		for _, assignment := range assignmentsForJourney(state, journeyID) {
			if assignment.Status != "cancelled" && assignment.WorkerID != "" {
				out = append(out, assignment.WorkerID)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return sortStringsUnique(out)
}

// firstLegWorker is the worker the first movement of a journey is currently held
// by. Disrupting exactly that worker is what makes a journey worker-affected.
func firstLegWorker(t *testing.T, service *Service, journeyID, sampleID string) string {
	t.Helper()
	legs := liveTransportLegs(t, service, journeyID, sampleID)
	if len(legs) == 0 || legs[0].workerID == "" {
		t.Fatalf("journey %s has no worker on its first movement: %+v", journeyID, legs)
	}
	return legs[0].workerID
}

// storeAdministrator is the administrator principal the integration fixtures
// create. It lets shared assertions read views without every caller threading a
// reader through.
func storeAdministrator(t *testing.T, service *Service) core.Principal {
	t.Helper()
	var admin core.Principal
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		for _, user := range state.Users {
			if stringSliceContains(user.Roles, "administrator") {
				admin = core.Principal{ID: user.ID, Roles: append([]string{}, user.Roles...)}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if admin.ID == "" {
		t.Fatal("no administrator exists in the store")
	}
	return admin
}

// AC-56, worker: a worker becoming unavailable replans the unfinished work of
// every journey that worker was carrying.
func TestAC56WorkerDisruptionReplansUnfinishedWork(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	fixture := buildReplanFixture(t, service, admin, "ac56w")
	sampleID, journeyID := fixture.planPrimaryJourney(t, service, "ac56w-request", nil)
	disrupted := firstLegWorker(t, service, journeyID, sampleID)

	out, err := fireDisruption(
		t, service, fixture.dispatcher, "worker", disrupted,
		"2026-07-30T09:10:00Z", "courier off shift", "ac56w-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption for a worker: %v", err)
	}
	replacementID := assertOneReplacement(t, service, out, journeyID, sampleID)

	// The route itself was never the problem, so the replacement keeps it.
	replacement := readJourneyFacts(t, service, replacementID)
	want := []string{fixture.network.nodeID, fixture.network.hubPrimaryID, fixture.network.labPrimaryID}
	if core.String(replacement.stopLocations) != core.String(want) {
		t.Fatalf("replacement route = %v, want the unchanged primary route %v", replacement.stopLocations, want)
	}
	// The unavailable worker carries none of the replacement's work.
	if stringSliceContains(journeyWorkerIDs(t, service, replacementID), disrupted) {
		t.Fatalf("the replacement plan still offers work to the disrupted worker %s", disrupted)
	}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		if state.Users[disrupted].Available {
			t.Fatalf("the disrupted worker %s is still marked available", disrupted)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-56, node/location: a network location going down replans the journeys that
// route through it, onto a route that avoids it.
func TestAC56NodeDisruptionReplansAroundTheLostLocation(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	fixture := buildReplanFixture(t, service, admin, "ac56n")
	sampleID, journeyID := fixture.planPrimaryJourney(t, service, "ac56n-request", nil)

	// "node" is an accepted alias for a network location.
	out, err := fireDisruption(
		t, service, fixture.dispatcher, "node", fixture.network.hubPrimaryID,
		"2026-07-30T09:10:00Z", "hub flooded", "ac56n-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption for a node: %v", err)
	}
	replacementID := assertOneReplacement(t, service, out, journeyID, sampleID)

	replacement := readJourneyFacts(t, service, replacementID)
	want := []string{fixture.network.nodeID, fixture.network.hubAlternateID, fixture.network.labPrimaryID}
	if core.String(replacement.stopLocations) != core.String(want) {
		t.Fatalf("replacement route = %v, want the alternate hub route %v", replacement.stopLocations, want)
	}
	if stringSliceContains(replacement.stopLocations, fixture.network.hubPrimaryID) {
		t.Fatalf("the replacement still routes through the disrupted hub: %v", replacement.stopLocations)
	}
}

// AC-56, laboratory: a laboratory going down replans the journeys destined for
// it onto another laboratory that accepts the sample.
func TestAC56LaboratoryDisruptionReplansToAnAcceptingLaboratory(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	fixture := buildReplanFixture(t, service, admin, "ac56l")
	sampleID, journeyID := fixture.planPrimaryJourney(t, service, "ac56l-request", nil)

	out, err := fireDisruption(
		t, service, fixture.dispatcher, "laboratory", fixture.network.labPrimaryID,
		"2026-07-30T09:10:00Z", "laboratory power failure", "ac56l-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption for a laboratory: %v", err)
	}
	replacementID := assertOneReplacement(t, service, out, journeyID, sampleID)

	replacement := readJourneyFacts(t, service, replacementID)
	want := []string{fixture.network.nodeID, fixture.network.hubPrimaryID, fixture.network.labAlternateID}
	if core.String(replacement.stopLocations) != core.String(want) {
		t.Fatalf("replacement route = %v, want the alternate laboratory route %v", replacement.stopLocations, want)
	}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		if target := state.Samples[sampleID].TargetLaboratoryID; target != fixture.network.labAlternateID {
			t.Fatalf("sample destination = %q, want the alternate laboratory", target)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-56, connection: a shuttle connection going down replans the unfinished
// movements onto a route that does not use it.
func TestAC56ConnectionDisruptionReplansOntoAnotherRoute(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	fixture := buildReplanFixture(t, service, admin, "ac56c")
	sampleID, journeyID := fixture.planPrimaryJourney(t, service, "ac56c-request", nil)

	out, err := fireDisruption(
		t, service, fixture.dispatcher, "connection", fixture.network.primaryHopID,
		"2026-07-30T09:10:00Z", "shuttle broken down", "ac56c-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption for a connection: %v", err)
	}
	replacementID := assertOneReplacement(t, service, out, journeyID, sampleID)

	replacement := readJourneyFacts(t, service, replacementID)
	want := []string{fixture.network.nodeID, fixture.network.hubAlternateID, fixture.network.labPrimaryID}
	if core.String(replacement.stopLocations) != core.String(want) {
		t.Fatalf("replacement route = %v, want the alternate hop route %v", replacement.stopLocations, want)
	}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		if state.Connections[fixture.network.primaryHopID].Active {
			t.Fatal("the disrupted connection is still active")
		}
		for _, reservation := range state.Reservations {
			if reservation.JourneyID == replacementID &&
				reservation.ResourceID == fixture.network.primaryHopID &&
				reservation.Status == "active" {
				t.Fatal("the replacement plan reserved the disrupted connection")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-56, capacity: a capacity pool becoming unavailable replans the journeys
// holding reservations against it.
func TestAC56CapacityDisruptionReplansAwayFromTheEmptiedPool(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	fixture := buildReplanFixture(t, service, admin, "ac56cap")
	sampleID, journeyID := fixture.planPrimaryJourney(t, service, "ac56cap-request", nil)

	out, err := fireDisruption(
		t, service, fixture.dispatcher, "capacity", fixture.network.hubPrimaryID,
		"2026-07-30T09:10:00Z", "hub storage condemned", "ac56cap-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption for a capacity pool: %v", err)
	}
	replacementID := assertOneReplacement(t, service, out, journeyID, sampleID)

	replacement := readJourneyFacts(t, service, replacementID)
	want := []string{fixture.network.nodeID, fixture.network.hubAlternateID, fixture.network.labPrimaryID}
	if core.String(replacement.stopLocations) != core.String(want) {
		t.Fatalf("replacement route = %v, want the alternate hub route %v", replacement.stopLocations, want)
	}
	// AC-48 rides along: the emptied pool holds nothing, so the replan neither
	// re-reserved it nor leaked the units the old plan held.
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		capacity := state.Capacities[fixture.network.hubPrimaryID]
		if capacity.CapacityUnits != 0 || capacity.ReservedUnits != 0 || capacity.CooledReserved != 0 {
			t.Fatalf("the emptied pool = %+v, want nothing configured and nothing reserved", capacity)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-56, the negative half: a journey that touches none of the disrupted
// resource is not replanned, and its plan is left exactly as it was.
func TestAC56DisruptionOfAnUntouchedResourceRequiresNoReplan(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	fixture := buildReplanFixture(t, service, admin, "ac56none")
	sampleID, journeyID := fixture.planPrimaryJourney(t, service, "ac56none-request", nil)
	before := capturePlanShape(t, service, journeyID)

	for _, probe := range []struct {
		label        string
		resourceType string
		resourceID   string
		key          string
	}{
		{"a location nothing routes through", "location", fixture.network.spareLocationID, "ac56none-location"},
		{"a courier holding no work", "worker", fixture.idleCourier.ID, "ac56none-worker"},
	} {
		out, err := fireDisruption(
			t, service, fixture.dispatcher, probe.resourceType, probe.resourceID,
			"2026-07-30T09:10:00Z", probe.label, probe.key,
		)
		if err != nil {
			t.Fatalf("report_disruption for %s: %v", probe.label, err)
		}
		if core.String(out["replanStatus"]) != "not_required" {
			t.Fatalf("replanStatus for %s = %v, want not_required: %v", probe.label, out["replanStatus"], out)
		}
		if ids := core.Strings(out["replacementPlanIds"]); len(ids) != 0 {
			t.Fatalf("%s produced replacement plans %v", probe.label, ids)
		}
		if ids := core.Strings(out["affectedSampleIds"]); len(ids) != 0 {
			t.Fatalf("%s reported affected samples %v", probe.label, ids)
		}
		assertSamePlanShape(t, "after disrupting "+probe.label, before, capturePlanShape(t, service, journeyID))
		if ids := activeReplacementPlanIDs(t, service, admin, sampleID); len(ids) != 0 {
			t.Fatalf("%s created replacement plans %v", probe.label, ids)
		}
	}

	// Positive control: the same operation on a resource the journey DOES use
	// replans it, so the two not_required answers above are about the resource
	// and not about the operation refusing to do anything.
	out, err := fireDisruption(
		t, service, fixture.dispatcher, "location", fixture.network.hubPrimaryID,
		"2026-07-30T09:10:00Z", "hub flooded", "ac56none-control",
	)
	if err != nil {
		t.Fatalf("control disruption: %v", err)
	}
	assertOneReplacement(t, service, out, journeyID, sampleID)
}

// AC-57, AC-58 (handoff half): a replan does not move custody. The sample is
// taken to a state where a courier really holds it — a two-sided handoff that
// completed — and the worker who holds it is the very resource that is disrupted,
// which is the case a replan is most likely to get wrong.
func TestAC57CurrentHolderIsUnchangedByAReplan(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runJourneyToPendingHandoff(t, service, admin, "ac57")
	dispatcher := createTestUser(t, service, admin, "ac57-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac57-standby", []string{"delivery_partner"}, nil)

	for _, confirmation := range []struct {
		p        core.Principal
		side, at string
	}{
		{fixture.collector, "sender", "2026-07-30T09:07:00Z"},
		{fixture.courier, "receiver", "2026-07-30T09:08:00Z"},
	} {
		if _, err := service.confirmHandoff(ctx, confirmation.p, core.Map{
			"handoffId": fixture.handoffID, "confirmationSide": confirmation.side,
			"confirmedAt": confirmation.at, "idempotencyKey": "ac57-confirm-" + confirmation.side,
		}); err != nil {
			t.Fatal(err)
		}
	}
	custodyBefore := readCustody(t, service, fixture.sampleID, fixture.handoffID)
	if custodyBefore.holderID != fixture.courier.ID || custodyBefore.completedHandoffs != 1 {
		t.Fatalf("custody before the replan = %+v, want the courier holding it", custodyBefore)
	}
	lifecycleBefore, err := service.Handle(
		ctx, "read_sample_lifecycle", core.Map{"sampleId": fixture.sampleID}, admin,
	)
	if err != nil {
		t.Fatal(err)
	}

	out, err := fireDisruption(
		t, service, dispatcher, "worker", fixture.courier.ID,
		"2026-07-30T09:15:00Z", "courier vehicle failure", "ac57-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption: %v", err)
	}
	// A real replan happened, so the holder comparison below is not vacuous.
	assertOneReplacement(t, service, out, fixture.journeyID, fixture.sampleID)

	custodyAfter := readCustody(t, service, fixture.sampleID, fixture.handoffID)
	if custodyAfter.holderID != custodyBefore.holderID {
		t.Fatalf(
			"currentHolderId changed across the replan: %q -> %q",
			custodyBefore.holderID, custodyAfter.holderID,
		)
	}
	// AC-58, handoff half: the confirmed transfer is untouched.
	if custodyAfter.handoffStatus != "confirmed" || !custodyAfter.handoffCompleted ||
		!custodyAfter.senderConfirmed || !custodyAfter.receiverConfirmed ||
		custodyAfter.completedHandoffs != 1 || len(custodyAfter.pendingHandoffIDs) != 0 {
		t.Fatalf("the confirmed handoff was disturbed by the replan: %+v", custodyAfter)
	}
	lifecycleAfter, err := service.Handle(
		ctx, "read_sample_lifecycle", core.Map{"sampleId": fixture.sampleID}, admin,
	)
	if err != nil {
		t.Fatal(err)
	}
	if core.String(lifecycleAfter["currentHolderId"]) != core.String(lifecycleBefore["currentHolderId"]) {
		t.Fatalf(
			"the lifecycle view's holder changed across the replan: %v -> %v",
			lifecycleBefore["currentHolderId"], lifecycleAfter["currentHolderId"],
		)
	}
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return "never"
	}
	return formatTime(*value)
}

// finishedWork is every finished record about one sample: the work that has
// completed, the route stops that have been completed, and the handoffs that
// have been confirmed. Records are keyed by identifier and rendered in full, so a
// probe can assert that every record present before an operation is still there
// afterwards and byte-for-byte the same.
type finishedWork struct {
	assignments map[string]string
	stops       map[string]string
	handoffs    map[string]string
}

func captureFinishedWork(t *testing.T, service *Service, sampleID string) finishedWork {
	t.Helper()
	out := finishedWork{
		assignments: map[string]string{},
		stops:       map[string]string{},
		handoffs:    map[string]string{},
	}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		journeyIDs := map[string]bool{}
		for _, assignment := range state.Assignments {
			if assignment.SampleID != sampleID {
				continue
			}
			journeyIDs[assignment.JourneyID] = true
			if assignment.Status != "completed" && assignment.Status != "delivered" {
				continue
			}
			out.assignments[assignment.ID] = fmt.Sprintf(
				"journey=%s kind=%s leg=%d %s->%s status=%s completedAt=%s worker=%s",
				assignment.JourneyID, assignment.Kind, assignment.LegSequence,
				assignment.FromLocationID, assignment.ToLocationID, assignment.Status,
				formatOptionalTime(assignment.CompletedAt), assignment.WorkerID,
			)
		}
		for _, stop := range state.Stops {
			if !journeyIDs[stop.JourneyID] || stop.CompletedAt == nil {
				continue
			}
			out.stops[stop.ID] = fmt.Sprintf(
				"journey=%s sequence=%d location=%s type=%s completedAt=%s",
				stop.JourneyID, stop.Sequence, stop.LocationID, stop.StopType,
				formatTime(*stop.CompletedAt),
			)
		}
		for _, handoff := range state.Handoffs {
			if handoff.SampleID != sampleID || handoff.Status != "confirmed" {
				continue
			}
			out.handoffs[handoff.ID] = fmt.Sprintf(
				"status=%s %s->%s sender=%s receiver=%s completedAt=%s",
				handoff.Status, handoff.SenderID, handoff.ReceiverID,
				formatOptionalTime(handoff.SenderConfirmedAt),
				formatOptionalTime(handoff.ReceiverConfirmedAt),
				formatOptionalTime(handoff.CompletedAt),
			)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// assertFinishedWorkPreserved checks that every finished record is still present
// and unchanged. New finished records are allowed — work does keep finishing —
// but an existing one may never be rewritten or removed.
func assertFinishedWorkPreserved(t *testing.T, label string, before, after finishedWork) {
	t.Helper()
	compare := func(kind string, before, after map[string]string) {
		t.Helper()
		for id, want := range before {
			got, present := after[id]
			if !present {
				t.Fatalf("%s: completed %s %s disappeared (was %q)", label, kind, id, want)
			}
			if got != want {
				t.Fatalf("%s: completed %s %s was rewritten\nbefore=%s\nafter =%s", label, kind, id, want, got)
			}
		}
	}
	compare("assignment", before.assignments, after.assignments)
	compare("stop", before.stops, after.stops)
	compare("handoff", before.handoffs, after.handoffs)
}

// AC-58: a replan preserves completed assignments, completed stops AND confirmed
// handoffs.
//
// Two replans are needed. Stop completion is recorded by the plan operations
// themselves rather than by progress updates, so the first replan is what turns
// the finished prefix of the route into completed stop records; the second replan
// is the one under test, and it must leave every one of those records alone.
func TestAC58ReplanPreservesCompletedWorkStopsAndConfirmedHandoffs(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runTwoLegJourneyToFirstLegDelivered(t, service, admin, "ac58")
	dispatcher := createTestUser(t, service, admin, "ac58-dispatcher", []string{"dispatcher"}, nil)
	originID := journeyStopLocations(t, service, fixture.journeyID)[0]

	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:25:00Z")
	first, err := fireDisruption(
		t, service, dispatcher, "location", originID,
		"2026-07-30T09:25:00Z", "collection point closed", "ac58-disruption",
	)
	if err != nil {
		t.Fatalf("first replan: %v", err)
	}
	replacementID := assertOneReplacement(t, service, first, fixture.journeyID, fixture.sampleID)

	before := captureFinishedWork(t, service, fixture.sampleID)
	if len(before.assignments) < 2 {
		t.Fatalf("expected the finished collection and the delivered leg: %v", before.assignments)
	}
	if len(before.stops) < 2 {
		t.Fatalf("expected the finished route prefix to be recorded as completed stops: %v", before.stops)
	}
	if len(before.handoffs) != 1 {
		t.Fatalf("expected exactly one confirmed handoff: %v", before.handoffs)
	}

	// The second replan is the one under test.
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:30:00Z")
	rerouted, err := service.Handle(ctx, "reroute_journey", core.Map{
		"journeyId": replacementID, "expectedPlanVersion": 1,
		"reason": "ac58 second replan", "idempotencyKey": "ac58-reroute",
	}, dispatcher)
	if err != nil {
		t.Fatalf("second replan: %v", err)
	}
	if core.String(rerouted["replanStatus"]) != "replaced" {
		t.Fatalf("second replan status = %v, want replaced", rerouted["replanStatus"])
	}

	assertFinishedWorkPreserved(
		t, "after a second replan", before, captureFinishedWork(t, service, fixture.sampleID),
	)

	// The delivered leg is still attached to the journey that delivered it, so it
	// was preserved rather than re-created somewhere else.
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		delivered := state.Assignments[fixture.firstLegID]
		if delivered.JourneyID != fixture.journeyID || delivered.Status != "delivered" {
			t.Fatalf("the delivered leg moved or changed status: %+v", delivered)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-59: only unfinished work is replanned. The finished leg is not rewritten and
// the replacement plan starts where the sample actually is, so the movement that
// already happened is not planned a second time.
func TestAC59ReplanRewritesOnlyTheUnfinishedWork(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runTwoLegJourneyToFirstLegDelivered(t, service, admin, "ac59")
	dispatcher := createTestUser(t, service, admin, "ac59-dispatcher", []string{"dispatcher"}, nil)
	originID := journeyStopLocations(t, service, fixture.journeyID)[0]

	finishedBefore := captureFinishedWork(t, service, fixture.sampleID)
	legsBefore := liveTransportLegs(t, service, fixture.journeyID, fixture.sampleID)
	if len(legsBefore) != 2 || legsBefore[0].status != "delivered" || legsBefore[1].status != "pending" {
		t.Fatalf("expected one delivered and one unstarted leg: %+v", legsBefore)
	}

	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:25:00Z")
	out, err := fireDisruption(
		t, service, dispatcher, "location", originID,
		"2026-07-30T09:25:00Z", "collection point closed", "ac59-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption: %v", err)
	}
	replacementID := assertOneReplacement(t, service, out, fixture.journeyID, fixture.sampleID)

	assertFinishedWorkPreserved(
		t, "after a replan", finishedBefore, captureFinishedWork(t, service, fixture.sampleID),
	)

	// The finished leg on the old plan is untouched, field for field.
	legsAfter := liveTransportLegs(t, service, fixture.journeyID, fixture.sampleID)
	if len(legsAfter) != 1 {
		t.Fatalf("the old plan should retain exactly its finished leg: %+v", legsAfter)
	}
	if legsAfter[0] != legsBefore[0] {
		t.Fatalf("the finished leg was rewritten\nbefore=%+v\nafter =%+v", legsBefore[0], legsAfter[0])
	}

	// The replacement replans the remaining movement only.
	replacementLegs := liveTransportLegs(t, service, replacementID, fixture.sampleID)
	if len(replacementLegs) != 1 {
		t.Fatalf("the replacement plans %d movements, want only the unfinished one: %+v", len(replacementLegs), replacementLegs)
	}
	if replacementLegs[0].from != fixture.hubID || replacementLegs[0].to != fixture.laboratoryID {
		t.Fatalf("the replacement's movement = %+v, want %s -> %s", replacementLegs[0], fixture.hubID, fixture.laboratoryID)
	}
	for _, leg := range replacementLegs {
		if leg.from == originID {
			t.Fatalf("the replacement re-planned the movement that already happened: %+v", leg)
		}
	}
	replacement := readJourneyFacts(t, service, replacementID)
	want := []string{fixture.hubID, fixture.laboratoryID}
	if core.String(replacement.stopLocations) != core.String(want) {
		t.Fatalf("the replacement's stops = %v, want it to start where the sample is (%v)", replacement.stopLocations, want)
	}
	// Collection is finished, so the replacement does not plan it again.
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		for _, assignment := range assignmentsForJourney(state, replacementID) {
			if assignment.Kind == "collection" && assignment.Status != "cancelled" {
				t.Fatalf("the replacement re-planned collection of an already sealed sample: %+v", assignment)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-60: however many times a journey is replanned, a sample has exactly ONE
// active replacement plan. Each event here is a real replan of the plan that is
// currently live, driven both by a reported disruption and by an explicit
// reroute.
func TestAC60ASecondReplanDoesNotCreateASecondActivePlan(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	fixture := buildReplanFixture(t, service, admin, "ac60")
	sampleID, journeyID := fixture.planPrimaryJourney(t, service, "ac60-request", nil)

	activeReplacements := func(label string) []string {
		t.Helper()
		fromView := activeReplacementPlanIDs(t, service, admin, sampleID)
		fromStore := []string{}
		if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
			for _, journey := range state.Journeys {
				if journey.IsReplacement && journey.Status == "active" &&
					stringSliceContains(journey.SampleIDs, sampleID) {
					fromStore = append(fromStore, journey.ID)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if core.String(sorted(fromView)) != core.String(sorted(fromStore)) {
			t.Fatalf("%s: the lifecycle view reports %v but the store holds %v", label, fromView, fromStore)
		}
		if len(fromView) > 1 {
			t.Fatalf("%s: %d active replacement plans for one sample: %v", label, len(fromView), fromView)
		}
		return fromView
	}

	if got := activeReplacements("before any replan"); len(got) != 0 {
		t.Fatalf("a freshly planned journey already has replacement plans: %v", got)
	}

	first, err := fireDisruption(
		t, service, fixture.dispatcher, "worker",
		firstLegWorker(t, service, journeyID, sampleID),
		"2026-07-30T09:10:00Z", "first courier off shift", "ac60-disruption-1",
	)
	if err != nil {
		t.Fatalf("first disruption: %v", err)
	}
	firstReplacement := assertOneReplacement(t, service, first, journeyID, sampleID)
	if got := activeReplacements("after the first replan"); len(got) != 1 || got[0] != firstReplacement {
		t.Fatalf("active replacements after the first replan = %v, want [%s]", got, firstReplacement)
	}

	// A second disruption, this time of the worker carrying the replacement.
	second, err := fireDisruption(
		t, service, fixture.dispatcher, "worker",
		firstLegWorker(t, service, firstReplacement, sampleID),
		"2026-07-30T09:15:00Z", "standby courier off shift", "ac60-disruption-2",
	)
	if err != nil {
		t.Fatalf("second disruption: %v", err)
	}
	secondReplacement := assertOneReplacement(t, service, second, firstReplacement, sampleID)
	if secondReplacement == firstReplacement {
		t.Fatal("the second replan returned the first replacement plan")
	}
	got := activeReplacements("after the second replan")
	if len(got) != 1 || got[0] != secondReplacement {
		t.Fatalf("active replacements after the second replan = %v, want only [%s]", got, secondReplacement)
	}

	// A third replan, driven by an explicit reroute rather than a disruption.
	rerouted, err := service.Handle(context.Background(), "reroute_journey", core.Map{
		"journeyId": secondReplacement, "expectedPlanVersion": 1,
		"reason": "ac60 explicit reroute", "idempotencyKey": "ac60-reroute",
	}, fixture.dispatcher)
	if err != nil {
		t.Fatalf("explicit reroute: %v", err)
	}
	thirdReplacement := core.String(rerouted["replacementPlanId"])
	if thirdReplacement == "" {
		t.Fatalf("reroute did not report a replacement plan: %v", rerouted)
	}
	got = activeReplacements("after an explicit reroute")
	if len(got) != 1 || got[0] != thirdReplacement {
		t.Fatalf("active replacements after the reroute = %v, want only [%s]", got, thirdReplacement)
	}

	// Every superseded plan is inactive, and only the live plan holds capacity.
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		for _, superseded := range []string{journeyID, firstReplacement, secondReplacement} {
			if status := state.Journeys[superseded].Status; status == "active" {
				t.Fatalf("superseded plan %s is still active", superseded)
			}
			for _, reservation := range state.Reservations {
				if reservation.JourneyID == superseded && reservation.Status == "active" {
					t.Fatalf("superseded plan %s still holds capacity: %+v", superseded, reservation)
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-61, worker-eligibility clause: a replacement plan is revalidated rather than
// copied. Two of the three workers on the original plan are ineligible by the
// time the replan runs — one unavailable, one no longer holding the role — and a
// plan copied verbatim would carry both of them and could not be validated.
func TestAC61ReplanRechecksWorkerEligibilityRatherThanCopyingThePlan(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := buildReplanFixture(t, service, admin, "ac61w")
	sampleID, journeyID := fixture.planPrimaryJourney(t, service, "ac61w-request", nil)

	legsBefore := liveTransportLegs(t, service, journeyID, sampleID)
	if len(legsBefore) != 2 {
		t.Fatalf("expected a two-movement plan: %+v", legsBefore)
	}
	firstWorker, secondWorker := legsBefore[0].workerID, legsBefore[1].workerID
	if firstWorker == "" || secondWorker == "" || firstWorker == secondWorker {
		t.Fatalf("expected two different couriers on the plan: %+v", legsBefore)
	}

	// The courier on the second movement stops being a courier at all. Nothing
	// about the route changes, so only a revalidation can notice.
	if _, err := service.Handle(ctx, "update_user_access", core.Map{
		"userId": secondWorker, "roles": []string{"node_handler"},
		"qualifications": []string{}, "active": true,
	}, admin); err != nil {
		t.Fatalf("moving the second courier off the role: %v", err)
	}

	out, err := fireDisruption(
		t, service, fixture.dispatcher, "worker", firstWorker,
		"2026-07-30T09:10:00Z", "first courier off shift", "ac61w-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption: %v", err)
	}
	replacementID := assertOneReplacement(t, service, out, journeyID, sampleID)

	// The route is unchanged, so the only thing the replan had to recheck is who
	// may carry the work.
	replacement := readJourneyFacts(t, service, replacementID)
	want := []string{fixture.network.nodeID, fixture.network.hubPrimaryID, fixture.network.labPrimaryID}
	if core.String(replacement.stopLocations) != core.String(want) {
		t.Fatalf("replacement route = %v, want the unchanged primary route %v", replacement.stopLocations, want)
	}
	// The courier who went off shift carries nothing at all: they are unavailable
	// for every kind of work.
	if workers := journeyWorkerIDs(t, service, replacementID); stringSliceContains(workers, firstWorker) {
		t.Fatalf(
			"the replacement plan still offers work to the unavailable worker %s: %v",
			firstWorker, workers,
		)
	}
	// Neither ineligible courier carries a movement. The second one is a node
	// handler now, so facility work at the hub may legitimately be theirs — it is
	// specifically transport they may no longer be offered.
	for _, leg := range liveTransportLegs(t, service, replacementID, sampleID) {
		if leg.workerID == firstWorker || leg.workerID == secondWorker {
			t.Fatalf("movement %+v is offered to a worker who may not carry it", leg)
		}
	}
	// Every worker on the replacement is eligible, right now, for the work they
	// were given — which is the whole of the eligibility recheck.
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		for _, assignment := range assignmentsForJourney(state, replacementID) {
			if assignment.Status == "cancelled" || assignment.WorkerID == "" {
				continue
			}
			worker := state.Users[assignment.WorkerID]
			role := roleForAssignmentKind(assignment.Kind)
			if worker == nil || !worker.Active || !worker.Available ||
				!stringSliceContains(worker.Roles, role) {
				t.Fatalf(
					"the replacement offered %s work to a worker who cannot do it: %+v",
					assignment.Kind, worker,
				)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-61, compatibility clause: the destination is rechecked. The originally
// planned laboratory stops accepting the sample type before the replan runs, and
// nothing about the route or the workers forces a change, so a plan copied
// verbatim would keep sending the sample to a laboratory that would refuse it.
func TestAC61ReplanRechecksLaboratoryAcceptanceRatherThanCopyingThePlan(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := buildReplanFixture(t, service, admin, "ac61a")
	sampleID, journeyID := fixture.planPrimaryJourney(t, service, "ac61a-request", nil)

	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": fixture.network.labPrimaryID,
		"sampleTypeId": fixture.network.sampleTypeID, "accepts": false,
	}); err != nil {
		t.Fatalf("revoking acceptance at the planned laboratory: %v", err)
	}

	out, err := fireDisruption(
		t, service, fixture.dispatcher, "worker",
		firstLegWorker(t, service, journeyID, sampleID),
		"2026-07-30T09:10:00Z", "courier off shift", "ac61a-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption: %v", err)
	}
	replacementID := assertOneReplacement(t, service, out, journeyID, sampleID)

	replacement := readJourneyFacts(t, service, replacementID)
	want := []string{fixture.network.nodeID, fixture.network.hubPrimaryID, fixture.network.labAlternateID}
	if core.String(replacement.stopLocations) != core.String(want) {
		t.Fatalf(
			"replacement route = %v, want it redirected to the accepting laboratory %v",
			replacement.stopLocations, want,
		)
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if target := state.Samples[sampleID].TargetLaboratoryID; target != fixture.network.labAlternateID {
			t.Fatalf("sample destination = %q, want the laboratory that still accepts it", target)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-61, capacity clause: the capacity every leg needs is rechecked. The pool on
// the second movement of the planned route is emptied, so a plan copied verbatim
// would try to reserve capacity that no longer exists and could not commit; a
// revalidated plan is redirected onto a route whose pools can carry it.
func TestAC61ReplanRechecksRouteCapacityRatherThanCopyingThePlan(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := buildReplanFixture(t, service, admin, "ac61c")
	sampleID, journeyID := fixture.planPrimaryJourney(t, service, "ac61c-request", nil)

	out, err := fireDisruption(
		t, service, fixture.dispatcher, "capacity", fixture.network.primaryFinalID,
		"2026-07-30T09:10:00Z", "shuttle capacity withdrawn", "ac61c-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption: %v", err)
	}
	replacementID := assertOneReplacement(t, service, out, journeyID, sampleID)

	replacement := readJourneyFacts(t, service, replacementID)
	want := []string{fixture.network.nodeID, fixture.network.hubAlternateID, fixture.network.labPrimaryID}
	if core.String(replacement.stopLocations) != core.String(want) {
		t.Fatalf(
			"replacement route = %v, want it redirected onto a route with capacity %v",
			replacement.stopLocations, want,
		)
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		for _, reservation := range state.Reservations {
			if reservation.JourneyID != replacementID || reservation.Status != "active" {
				continue
			}
			if reservation.ResourceID == fixture.network.primaryFinalID {
				t.Fatalf("the replacement reserved the emptied pool: %+v", reservation)
			}
		}
		for _, resourceID := range []string{
			fixture.network.alternateHopID, fixture.network.alternateFinalID,
		} {
			if !hasJourneyReservation(state, replacementID, resourceID, replacement.capacityUsed, false) {
				t.Fatalf("the replacement holds no reservation on %s", resourceID)
			}
		}
		emptied := state.Capacities[fixture.network.primaryFinalID]
		if emptied.ReservedUnits != 0 || emptied.CooledReserved != 0 {
			t.Fatalf("the emptied pool still carries reservations: %+v", emptied)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-62: when no valid plan can meet a sample's deadline, the sample is marked
// exception and the outcome is recorded in the audit trail.
//
// One disruption is reported against two journeys that differ ONLY in their
// delivery deadline: the alternate route is fast enough for one and too slow for
// the other. The journey that can still be replanned is the positive control, so
// the exception can only be about the deadline.
func TestAC62ReplanThatCannotMeetTheDeadlineMarksTheSampleException(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := buildReplanFixture(t, service, admin, "ac62x")
	// The primary route takes 20 minutes and the alternate 40, so a 09:40
	// deadline is reachable on the plan but not on the replan.
	tightSampleID, tightJourneyID := fixture.planPrimaryJourney(
		t, service, "ac62x-tight", core.Map{"deliveryDeadline": "2026-07-30T09:40:00Z"},
	)
	roomySampleID, roomyJourneyID := fixture.planPrimaryJourney(t, service, "ac62x-roomy", nil)
	capacityBefore := readReservedLevels(
		t, service, admin, fixture.network.nodeID, fixture.network.hubPrimaryID,
		fixture.network.labPrimaryID,
	)

	out, err := fireDisruption(
		t, service, fixture.dispatcher, "location", fixture.network.hubPrimaryID,
		"2026-07-30T09:15:00Z", "hub flooded", "ac62x-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption: %v", err)
	}
	if core.String(out["replanStatus"]) != "exception" {
		t.Fatalf("replanStatus = %v, want exception: %v", out["replanStatus"], out)
	}
	for _, sampleID := range []string{tightSampleID, roomySampleID} {
		if !stringSliceContains(core.Strings(out["affectedSampleIds"]), sampleID) {
			t.Fatalf("affectedSampleIds = %v, want both samples", out["affectedSampleIds"])
		}
	}

	// The sample whose deadline can no longer be met is excepted, its plan is
	// cancelled, and it holds no capacity.
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		tight := state.Samples[tightSampleID]
		if tight.Status != "exception" {
			t.Fatalf("the undeliverable sample status = %q, want exception", tight.Status)
		}
		if tight.ActiveJourneyID != "" {
			t.Fatalf("an excepted sample still points at journey %q", tight.ActiveJourneyID)
		}
		if core.String(tight.Exception["reason"]) == "" {
			t.Fatalf("the exception carries no reason: %v", tight.Exception)
		}
		if status := state.Journeys[tightJourneyID].Status; status != "cancelled" {
			t.Fatalf("the undeliverable journey status = %q, want cancelled", status)
		}
		for _, reservation := range state.Reservations {
			if reservation.JourneyID == tightJourneyID && reservation.Status == "active" {
				t.Fatalf("the excepted journey still holds capacity: %+v", reservation)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Positive control: the identical disruption replans the sample whose
	// deadline the alternate route can still meet.
	replacementIDs := core.Strings(out["replacementPlanIds"])
	if len(replacementIDs) != 1 {
		t.Fatalf("replacementPlanIds = %v, want the one journey that could be replanned", replacementIDs)
	}
	if got := activeReplacementPlanIDs(t, service, admin, roomySampleID); len(got) != 1 ||
		got[0] != replacementIDs[0] {
		t.Fatalf("the deliverable sample's active replacement = %v, want %v", got, replacementIDs)
	}
	if status := readJourneyFacts(t, service, roomyJourneyID).status; status != "replaced" {
		t.Fatalf("the deliverable journey status = %q, want replaced", status)
	}
	if err := journeyPlanValidity(t, service, replacementIDs[0]); err != nil {
		t.Fatalf("the replacement plan does not validate: %v", err)
	}

	// AC-62 audit half: the outcome is traceable from the excepted sample.
	lifecycle, err := service.Handle(
		ctx, "read_sample_lifecycle", core.Map{"sampleId": tightSampleID}, admin,
	)
	if err != nil {
		t.Fatal(err)
	}
	sawException := false
	for _, event := range lifecycle["timeline"].([]core.Map) {
		details, _ := event["details"].(core.Map)
		if core.String(details["replanStatus"]) == "exception" ||
			core.String(details["status"]) == "exception" {
			sawException = true
		}
	}
	if !sawException {
		t.Fatalf(
			"the excepted sample's history records no exception outcome: %v",
			lifecycle["timeline"],
		)
	}

	// AC-48 rides along: the excepted plan leaked nothing. The node and the lost
	// hub carry only what the surviving replacement needs.
	capacityAfter := readReservedLevels(
		t, service, admin, fixture.network.nodeID, fixture.network.hubPrimaryID,
		fixture.network.labPrimaryID,
	)
	if capacityAfter[fixture.network.hubPrimaryID].regular != 0 {
		t.Fatalf(
			"the lost hub still carries reservations: %v (was %v)",
			capacityAfter[fixture.network.hubPrimaryID], capacityBefore[fixture.network.hubPrimaryID],
		)
	}
	if capacityAfter[fixture.network.nodeID].regular != 1 {
		t.Fatalf(
			"the origin carries %v, want only the one surviving replacement's unit",
			capacityAfter[fixture.network.nodeID],
		)
	}
}

// AC-62, the expired outcome: a disruption reported after the deadline has
// already passed marks the sample expired rather than excepted, and records it.
func TestAC62ReplanAfterTheDeadlineMarksTheSampleExpired(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := buildReplanFixture(t, service, admin, "ac62e")
	sampleID, journeyID := fixture.planPrimaryJourney(
		t, service, "ac62e-request", core.Map{"deliveryDeadline": "2026-07-30T09:40:00Z"},
	)

	// The route is left intact and only the courier is lost, so the only reason
	// no plan can be built is that the deadline has passed.
	out, err := fireDisruption(
		t, service, fixture.dispatcher, "worker",
		firstLegWorker(t, service, journeyID, sampleID),
		"2026-07-30T09:50:00Z", "courier off shift", "ac62e-disruption",
	)
	if err != nil {
		t.Fatalf("report_disruption: %v", err)
	}
	if core.String(out["replanStatus"]) != "expired" {
		t.Fatalf("replanStatus = %v, want expired: %v", out["replanStatus"], out)
	}
	if ids := core.Strings(out["replacementPlanIds"]); len(ids) != 0 {
		t.Fatalf("an unmeetable deadline produced replacement plans %v", ids)
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		sample := state.Samples[sampleID]
		if sample.Status != "expired" {
			t.Fatalf("sample status = %q, want expired", sample.Status)
		}
		if sample.ActiveJourneyID != "" {
			t.Fatalf("an expired sample still points at journey %q", sample.ActiveJourneyID)
		}
		if status := state.Journeys[journeyID].Status; status != "cancelled" {
			t.Fatalf("journey status = %q, want cancelled", status)
		}
		for _, assignment := range assignmentsForJourney(state, journeyID) {
			if assignment.Status == "pending" || assignment.Status == "accepted" {
				t.Fatalf("work on the expired journey is still live: %+v", assignment)
			}
		}
		for _, reservation := range state.Reservations {
			if reservation.JourneyID == journeyID && reservation.Status == "active" {
				t.Fatalf("the expired journey still holds capacity: %+v", reservation)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !stringSliceContains(auditActionsFor(t, service, admin, "disruption", core.String(out["disruptionId"])), "disruption_reported") {
		t.Fatal("the disruption that expired the sample was not audited")
	}
}

// AC-62, the maintenance timer: a sample whose deadline elapses with work still
// outstanding is marked expired, audited, and stops holding capacity.
func TestAC62ElapsedDeadlineExpiresTheSampleAndReleasesItsCapacity(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	t.Setenv("ASSIGNMENT_OFFER_TTL_SECONDS", "86400")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac62m-requester", []string{"requester"}, nil)
	createTestUser(t, service, admin, "ac62m-collector", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "ac62m-courier", []string{"delivery_partner"}, nil)
	createTestUser(t, service, admin, "ac62m-operator", []string{"laboratory_team"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac62m")

	sampleID, journeyID := requestSampleOfType(
		t, service, requester, network.sampleTypeID, "amber-container", 2.0, 8.0,
		core.Map{"deliveryDeadline": "2026-07-30T10:00:00Z"}, "ac62m-request",
	)
	before := readReservedLevels(t, service, admin, network.nodeID, network.laboratoryID, network.connectionID)
	for resourceID, level := range before {
		if level.regular != 1 {
			t.Fatalf("resource %s starts at %v, want one reserved unit", resourceID, level)
		}
	}

	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T10:30:00Z")
	maintain(t, service)

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		sample := state.Samples[sampleID]
		if sample.Status != "expired" {
			t.Fatalf("sample status = %q, want expired", sample.Status)
		}
		if core.String(sample.Exception["reason"]) != "delivery_deadline_elapsed" {
			t.Fatalf("exception = %v", sample.Exception)
		}
		if status := state.Journeys[journeyID].Status; status != "cancelled" {
			t.Fatalf("journey status = %q, want cancelled", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !stringSliceContains(auditActionsFor(t, service, admin, "sample", sampleID), "sample_expired") {
		t.Fatalf(
			"the expiry was not audited against the sample; actions = %v",
			auditActionsFor(t, service, admin, "sample", sampleID),
		)
	}
	after := readReservedLevels(t, service, admin, network.nodeID, network.laboratoryID, network.connectionID)
	for resourceID, level := range after {
		if level.regular != 0 || level.cooled != 0 {
			t.Fatalf("resource %s still carries %v after the sample expired", resourceID, level)
		}
	}
}

// AC-62, the exception outcome from the maintenance timer: a Pulse sample must
// never wait, so when its offer lapses with no other eligible worker the sample
// is marked exception with the shared "no_qualified_collector_available"
// reason (the same reason a collection assignment that never had anyone
// eligible from the start is flagged with — see
// TestUnassignedIndigoCollectionSearchTimesOutToException below). AC-70 and
// AC-72 require the outcome to be recorded, which is what makes an exception
// visible to anyone but the sample's own record.
func TestAC62PulseSampleWithNoEligibleWorkerIsMarkedExceptionAndAudited(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac62p-requester", []string{"requester"}, nil)
	collector := createTestUser(t, service, admin, "ac62p-collector", []string{"collection_professional"}, []string{"LX-P"})
	courier := createTestUser(t, service, admin, "ac62p-courier", []string{"delivery_partner"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac62p")
	createTestUserWithLocation(t, service, admin, "ac62p-operator", []string{"laboratory_team"}, nil, network.laboratoryID)

	pulse, err := service.createSampleType(ctx, admin, core.Map{
		"name": "Pulse", "containerType": "pulse-container",
		"temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "requiresCooledRoute": false,
		"batchingAllowed": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	pulseTypeID := core.String(pulse["sampleTypeId"])
	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": network.laboratoryID, "sampleTypeId": pulseTypeID, "accepts": true,
	}); err != nil {
		t.Fatal(err)
	}
	sampleID, journeyID := requestSampleOfType(
		t, service, requester, pulseTypeID, "pulse-container", 2.0, 8.0, nil, "ac62p-request",
	)

	// The only workers eligible for this Pulse sample's work go off shift, so
	// when the offers lapse there is nobody to hand the work to.
	for _, workerID := range []string{collector.ID, courier.ID} {
		if _, err := service.Handle(ctx, "set_worker_availability", core.Map{
			"workerId": workerID, "available": false, "capacityUnits": 6,
		}, admin); err != nil {
			t.Fatalf("taking worker %s off shift: %v", workerID, err)
		}
	}

	// Past the offer window: the offers lapse and no eligible worker remains.
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:10:00Z")
	maintain(t, service)

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		sample := state.Samples[sampleID]
		if sample.Status != "exception" {
			t.Fatalf("Pulse sample status = %q, want exception", sample.Status)
		}
		if core.String(sample.Exception["reason"]) != "no_qualified_collector_available" {
			t.Fatalf("exception = %v", sample.Exception)
		}
		if status := state.Journeys[journeyID].Status; status != "cancelled" {
			t.Fatalf("journey status = %q, want cancelled", status)
		}
		for _, reservation := range state.Reservations {
			if reservation.JourneyID == journeyID && reservation.Status == "active" {
				t.Fatalf("the excepted journey still holds capacity: %+v", reservation)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	actions := auditActionsFor(t, service, admin, "sample", sampleID)
	if !stringSliceContains(actions, "sample_exception_recorded") {
		t.Fatalf(
			"a Pulse sample driven to exception left no exception record in the audit trail; actions = %v",
			actions,
		)
	}
	found, err := service.Handle(ctx, "search_audit_trail", core.Map{
		"query": "no_qualified_collector_available",
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if core.Int(found["total"]) == 0 {
		t.Fatal("the Pulse exception reason is not searchable in the audit trail")
	}
}

// createIndigoSampleType is a standalone Indigo sample type (distinct from the
// Amber type buildServiceableNetwork wires up by default) accepted at the
// fixture laboratory, so the unassigned-collector-search tests below can
// exercise the named policy's qualified-collector requirement in isolation.
func createIndigoSampleType(t *testing.T, service *Service, admin core.Principal, laboratoryID string) string {
	t.Helper()
	ctx := context.Background()
	indigo, err := service.createSampleType(ctx, admin, core.Map{
		"name": "Indigo", "containerType": "indigo-container",
		"temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "requiresCooledRoute": false,
		"batchingAllowed": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	typeID := core.String(indigo["sampleTypeId"])
	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": laboratoryID, "sampleTypeId": typeID, "accepts": true,
	}); err != nil {
		t.Fatal(err)
	}
	return typeID
}

// TestUnassignedIndigoCollectionSearchTimesOutToException: an Indigo sample
// requires a qualified collector (LX-I). When nobody holding that
// qualification is ever available, the request is still serviceable (staff
// availability never affects the serviceable region) and plans with an
// unassigned collection assignment. Once the configured search timeout
// elapses with the assignment still never having had a worker, the sample
// must be flagged as an exception distinct from a missed delivery deadline.
func TestUnassignedIndigoCollectionSearchTimesOutToException(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	t.Setenv("UNASSIGNED_SEARCH_TIMEOUT_SECONDS", "60")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "nqc-requester", []string{"requester"}, nil)
	// A real collection professional exists, but holds a different
	// qualification — proving the exhaustion is about the specific
	// qualification Indigo requires, not merely the absence of any collector.
	createTestUser(t, service, admin, "nqc-collector-wrong-qual", []string{"collection_professional"}, []string{"LX-A"})
	network := buildServiceableNetwork(t, service, admin, "nqc")
	indigoTypeID := createIndigoSampleType(t, service, admin, network.laboratoryID)

	sampleID, journeyID := requestSampleOfType(
		t, service, requester, indigoTypeID, "indigo-container", 2.0, 8.0, nil, "nqc-request",
	)

	// Confirm the collection assignment was created but never offered to
	// anyone: no eligible LX-I collector exists at all.
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		found := false
		for _, assignment := range state.Assignments {
			if assignment.SampleID != sampleID || assignment.Kind != "collection" {
				continue
			}
			found = true
			if assignment.WorkerID != "" || len(assignment.Declines) != 0 {
				t.Fatalf("collection assignment unexpectedly had a worker or declines: %+v", assignment)
			}
		}
		if !found {
			t.Fatal("no collection assignment was created for the Indigo sample")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Past the search timeout, but nowhere near the (far later) delivery
	// deadline.
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:02:00Z")
	maintain(t, service)

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		sample := state.Samples[sampleID]
		if sample.Status != "exception" {
			t.Fatalf("sample status = %q, want exception", sample.Status)
		}
		if core.String(sample.Exception["reason"]) != "no_qualified_collector_available" {
			t.Fatalf("exception = %v", sample.Exception)
		}
		if status := state.Journeys[journeyID].Status; status != "cancelled" {
			t.Fatalf("journey status = %q, want cancelled", status)
		}
		for _, reservation := range state.Reservations {
			if reservation.JourneyID == journeyID && reservation.Status == "active" {
				t.Fatalf("the excepted journey still holds capacity: %+v", reservation)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	actions := auditActionsFor(t, service, admin, "sample", sampleID)
	if !stringSliceContains(actions, "sample_exception_recorded") {
		t.Fatalf(
			"the unassigned-collector-search exception left no audit record; actions = %v",
			actions,
		)
	}
	found, err := service.Handle(ctx, "search_audit_trail", core.Map{
		"query": "no_qualified_collector_available",
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if core.Int(found["total"]) == 0 {
		t.Fatal("the no_qualified_collector_available reason is not searchable in the audit trail")
	}
}

// TestIndigoOfferExpiryWithNoOtherEligibleCollectorIsMarkedException proves
// the *other* trigger path to the same shared exception generalizes beyond
// Pulse. TestUnassignedIndigoCollectionSearchTimesOutToException above covers
// the "never offered at all" path; this covers "offered once, then that
// offer expired, then nobody else was eligible" -- the same path AC-62
// exercises for Pulse (see
// TestAC62PulseSampleWithNoEligibleWorkerIsMarkedExceptionAndAudited above).
// An Indigo sample's collection assignment is offered to the single LX-I
// collector at creation time; that collector goes off shift before
// accepting, so once the offer lapses the re-offer search comes up empty and
// the sample must land on "no_qualified_collector_available" just as a Pulse
// sample does.
func TestIndigoOfferExpiryWithNoOtherEligibleCollectorIsMarkedException(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ioe-requester", []string{"requester"}, nil)
	collector := createTestUser(t, service, admin, "ioe-collector", []string{"collection_professional"}, []string{"LX-I"})
	network := buildServiceableNetwork(t, service, admin, "ioe")
	indigoTypeID := createIndigoSampleType(t, service, admin, network.laboratoryID)

	sampleID, journeyID := requestSampleOfType(
		t, service, requester, indigoTypeID, "indigo-container", 2.0, 8.0, nil, "ioe-request",
	)

	// Confirm the collection assignment was offered to the only qualified
	// collector at creation time -- the "offered once" half of this trigger
	// path, as opposed to the never-offered path covered above.
	var collectionAssignmentID string
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		for _, assignment := range state.Assignments {
			if assignment.SampleID == sampleID && assignment.Kind == "collection" {
				collectionAssignmentID = assignment.ID
				if assignment.WorkerID != collector.ID {
					t.Fatalf("collection assignment worker = %q, want the qualified collector %q", assignment.WorkerID, collector.ID)
				}
			}
		}
		if collectionAssignmentID == "" {
			t.Fatal("no collection assignment was created for the Indigo sample")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The only qualified collector goes off shift before accepting the offer,
	// so when the offer lapses there is nobody left to re-offer it to.
	if _, err := service.Handle(ctx, "set_worker_availability", core.Map{
		"workerId": collector.ID, "available": false, "capacityUnits": 6,
	}, admin); err != nil {
		t.Fatalf("taking worker %s off shift: %v", collector.ID, err)
	}

	// Past the (default) offer TTL: the offer lapses and the re-offer search
	// comes up empty.
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:10:00Z")
	maintain(t, service)

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		sample := state.Samples[sampleID]
		if sample.Status != "exception" {
			t.Fatalf("Indigo sample status = %q, want exception", sample.Status)
		}
		if core.String(sample.Exception["reason"]) != "no_qualified_collector_available" {
			t.Fatalf("exception = %v", sample.Exception)
		}
		if status := state.Journeys[journeyID].Status; status != "cancelled" {
			t.Fatalf("journey status = %q, want cancelled", status)
		}
		if status := state.Assignments[collectionAssignmentID].Status; status != "cancelled" {
			t.Fatalf("collection assignment status = %q, want cancelled", status)
		}
		for _, reservation := range state.Reservations {
			if reservation.JourneyID == journeyID && reservation.Status == "active" {
				t.Fatalf("the excepted journey still holds capacity: %+v", reservation)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	actions := auditActionsFor(t, service, admin, "sample", sampleID)
	if !stringSliceContains(actions, "sample_exception_recorded") {
		t.Fatalf(
			"an Indigo sample driven to exception via the offer-expiry path left no exception record in the audit trail; actions = %v",
			actions,
		)
	}
	found, err := service.Handle(ctx, "search_audit_trail", core.Map{
		"query": "no_qualified_collector_available",
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if core.Int(found["total"]) == 0 {
		t.Fatal("the no_qualified_collector_available reason is not searchable in the audit trail")
	}
}

// TestQualifiedCollectorAvailableUnaffectedByUnassignedSearchTimeout: when a
// qualified collector IS available, the collection assignment is offered at
// creation time. The new search timeout only ever applies to an assignment
// that has never had a worker, so it must not touch this assignment even long
// after the timeout window would otherwise have elapsed.
func TestQualifiedCollectorAvailableUnaffectedByUnassignedSearchTimeout(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	t.Setenv("UNASSIGNED_SEARCH_TIMEOUT_SECONDS", "60")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "nqc2-requester", []string{"requester"}, nil)
	collector := createTestUser(t, service, admin, "nqc2-collector", []string{"collection_professional"}, []string{"LX-I"})
	network := buildServiceableNetwork(t, service, admin, "nqc2")
	indigoTypeID := createIndigoSampleType(t, service, admin, network.laboratoryID)

	sampleID, journeyID := requestSampleOfType(
		t, service, requester, indigoTypeID, "indigo-container", 2.0, 8.0, nil, "nqc2-request",
	)

	var collectionAssignmentID string
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		for _, assignment := range state.Assignments {
			if assignment.SampleID == sampleID && assignment.Kind == "collection" {
				collectionAssignmentID = assignment.ID
				if assignment.WorkerID != collector.ID {
					t.Fatalf("collection assignment worker = %q, want the qualified collector %q", assignment.WorkerID, collector.ID)
				}
			}
		}
		if collectionAssignmentID == "" {
			t.Fatal("no collection assignment was created for the Indigo sample")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Advance well past the (short) unassigned-search timeout, but stay inside
	// the still-open offer window (default ASSIGNMENT_OFFER_TTL_SECONDS) so the
	// existing offer is untouched by the offer-expiry path either.
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:02:00Z")
	maintain(t, service)

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		sample := state.Samples[sampleID]
		if sample.Status == "exception" {
			t.Fatalf("an already-offered assignment must not be driven to exception by the search timeout; exception = %v", sample.Exception)
		}
		assignment := state.Assignments[collectionAssignmentID]
		if assignment.WorkerID != collector.ID {
			t.Fatalf("collection assignment worker = %q, want unchanged %q", assignment.WorkerID, collector.ID)
		}
		if status := state.Journeys[journeyID].Status; status != "active" {
			t.Fatalf("journey status = %q, want still active", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUnassignedCollectionSearchTimeoutFiresBeforeDeliveryDeadline: when both
// the unassigned-search timeout and the request's delivery deadline have
// elapsed by the same maintenance pass, the collector-search exception must
// win with its own specific reason — not the generic
// "delivery_deadline_elapsed" reason — because the two are checking different
// things and the collector-search outcome is more specific.
func TestUnassignedCollectionSearchTimeoutFiresBeforeDeliveryDeadline(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	t.Setenv("UNASSIGNED_SEARCH_TIMEOUT_SECONDS", "60")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "nqc3-requester", []string{"requester"}, nil)
	network := buildServiceableNetwork(t, service, admin, "nqc3")
	indigoTypeID := createIndigoSampleType(t, service, admin, network.laboratoryID)

	// A deadline comfortably past the route's ETA (so the request is still
	// serviceable) but well before the moment we advance the clock to, so both
	// the search timeout and the deadline will have elapsed by the same
	// maintenance pass.
	sampleID, journeyID := requestSampleOfType(
		t, service, requester, indigoTypeID, "indigo-container", 2.0, 8.0,
		core.Map{"deliveryDeadline": "2026-07-30T10:00:00Z"}, "nqc3-request",
	)

	// Past both the 60-second search timeout and the 09:00-10:00 delivery
	// deadline.
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T10:05:00Z")
	maintain(t, service)

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		sample := state.Samples[sampleID]
		if sample.Status != "exception" {
			t.Fatalf("sample status = %q, want exception", sample.Status)
		}
		if reason := core.String(sample.Exception["reason"]); reason != "no_qualified_collector_available" {
			t.Fatalf("exception reason = %q, want no_qualified_collector_available (not the generic deadline reason)", reason)
		}
		if status := state.Journeys[journeyID].Status; status != "cancelled" {
			t.Fatalf("journey status = %q, want cancelled", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
