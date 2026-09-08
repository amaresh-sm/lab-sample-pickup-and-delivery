package service

import (
	"context"
	"testing"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// twoLegFixture is a real two-leg journey whose first leg has been delivered
// and whose second leg has not started, so route progress is genuinely partial.
type twoLegFixture struct {
	requester    core.Principal
	collector    core.Principal
	courier      core.Principal
	operator     core.Principal
	sampleID     string
	journeyID    string
	sampleTypeID string
	laboratoryID string
	hubID        string
	collectionID string
	firstLegID   string
	secondLegID  string
	recordedAt   string
	latitude     float64
	longitude    float64
}

// runTwoLegJourneyToFirstLegDelivered builds a node -> hub -> laboratory network
// with no direct connection, so planning must produce two transport legs, then
// drives the first leg to delivered and records one worker location on it.
func runTwoLegJourneyToFirstLegDelivered(
	t *testing.T,
	service *Service,
	admin core.Principal,
	prefix string,
) twoLegFixture {
	t.Helper()
	ctx := context.Background()
	fixture := twoLegFixture{
		requester: createTestUser(t, service, admin, prefix+"-requester", []string{"requester"}, nil),
		collector: createTestUser(t, service, admin, prefix+"-collector", []string{"collection_professional"}, []string{"LX-A"}),
		courier:   createTestUser(t, service, admin, prefix+"-courier", []string{"delivery_partner"}, nil),
		operator:  createTestUser(t, service, admin, prefix+"-operator", []string{"laboratory_team"}, nil),
	}
	createTestUser(t, service, admin, prefix+"-handler", []string{"node_handler"}, nil)

	createLocation := func(name string, latitude, longitude float64, functions ...string) string {
		t.Helper()
		out, err := service.createLocation(ctx, admin, core.Map{
			"name": name, "active": true,
			"coordinate": core.Map{"latitude": latitude, "longitude": longitude},
			"functions":  functions,
		})
		if err != nil {
			t.Fatal(err)
		}
		return core.String(out["locationId"])
	}
	nodeID := createLocation(
		prefix+" node", fixtureOriginLatitude, fixtureOriginLongitude,
		mongostore.FunctionCollectionPoint, mongostore.FunctionTransferPoint,
	)
	fixture.hubID = createLocation(prefix+" hub", 12.9766, 77.5996, mongostore.FunctionTransferPoint)
	fixture.laboratoryID = createLocation(prefix+" laboratory", 12.9816, 77.6046, mongostore.FunctionLaboratory)

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
	fixture.sampleTypeID = core.String(sampleType["sampleTypeId"])
	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": fixture.laboratoryID,
		"sampleTypeId": fixture.sampleTypeID, "accepts": true,
	}); err != nil {
		t.Fatal(err)
	}

	resourceIDs := []string{nodeID, fixture.hubID, fixture.laboratoryID}
	for _, hop := range []struct{ from, to string }{
		{nodeID, fixture.hubID},
		{fixture.hubID, fixture.laboratoryID},
	} {
		connection, err := service.createConnection(ctx, admin, core.Map{
			"fromLocationId": hop.from, "toLocationId": hop.to,
			"travelMinutes": 15, "costUnits": 2.0, "active": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		resourceIDs = append(resourceIDs, core.String(connection["connectionId"]))
	}
	for _, resourceID := range resourceIDs {
		if _, err := service.configureCapacity(ctx, admin, core.Map{
			"resourceId": resourceID, "capacityUnits": 10, "cooledCapacityUnits": 10,
		}); err != nil {
			t.Fatal(err)
		}
	}

	created, err := service.createCollectionRequest(ctx, fixture.requester, core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": fixture.sampleTypeID,
		"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T12:00:00Z",
		"containerType": "amber-container", "temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "idempotencyKey": prefix + "-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created["serviceability"] != "serviceable" {
		t.Fatalf("two-leg request = %v", created)
	}
	fixture.sampleID = core.String(created["sampleId"])
	fixture.journeyID = core.String(created["journeyId"])

	plan, err := service.readJourneyPlan(ctx, admin, fixture.journeyID)
	if err != nil {
		t.Fatal(err)
	}
	if core.String(plan["routeMode"]) != "multi_stop" {
		t.Fatalf("routeMode = %v, want multi_stop so the journey really has two legs", plan["routeMode"])
	}
	fixture.collectionID = core.String(plan["collectionAssignmentId"])
	fixture.firstLegID = core.String(plan["firstTransportAssignmentId"])
	fixture.secondLegID = core.String(plan["finalTransportAssignmentId"])
	if fixture.firstLegID == "" || fixture.secondLegID == "" ||
		fixture.firstLegID == fixture.secondLegID {
		t.Fatalf("expected two distinct transport legs, got %q and %q", fixture.firstLegID, fixture.secondLegID)
	}

	if _, err := service.acceptAssignment(ctx, fixture.collector, core.Map{
		"assignmentId": fixture.collectionID, "idempotencyKey": prefix + "-accept-collection",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.collectAndSeal(ctx, fixture.collector, core.Map{
		"sampleId": fixture.sampleID, "assignmentId": fixture.collectionID,
		"containerType": "amber-container", "observedTemperatureC": 5.0,
		"collectedAt": "2026-07-30T09:05:00Z", "idempotencyKey": prefix + "-seal",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.acceptAssignment(ctx, fixture.courier, core.Map{
		"assignmentId": fixture.firstLegID, "idempotencyKey": prefix + "-accept-leg-1",
	}); err != nil {
		t.Fatal(err)
	}
	handoff, err := service.initiateHandoff(ctx, fixture.collector, core.Map{
		"sampleId": fixture.sampleID, "receiverId": fixture.courier.ID,
		"fromAssignmentId": fixture.collectionID, "toAssignmentId": fixture.firstLegID,
		"initiatedAt": "2026-07-30T09:06:00Z", "idempotencyKey": prefix + "-handoff",
	})
	if err != nil {
		t.Fatal(err)
	}
	handoffID := core.String(handoff["handoffId"])
	for _, confirmation := range []struct {
		p        core.Principal
		side, at string
	}{
		{fixture.collector, "sender", "2026-07-30T09:07:00Z"},
		{fixture.courier, "receiver", "2026-07-30T09:08:00Z"},
	} {
		if _, err := service.confirmHandoff(ctx, confirmation.p, core.Map{
			"handoffId": handoffID, "confirmationSide": confirmation.side,
			"confirmedAt": confirmation.at, "idempotencyKey": prefix + "-confirm-" + confirmation.side,
		}); err != nil {
			t.Fatal(err)
		}
	}

	fixture.latitude, fixture.longitude = 12.9741, 77.5971
	fixture.recordedAt = "2026-07-30T09:09:00Z"
	if _, err := service.Handle(ctx, "record_worker_location", core.Map{
		"assignmentId": fixture.firstLegID, "sequence": 1,
		"coordinate": core.Map{"latitude": fixture.latitude, "longitude": fixture.longitude},
		"recordedAt": fixture.recordedAt,
	}, fixture.courier); err != nil {
		t.Fatalf("record worker location: %v", err)
	}

	for index, update := range []struct{ status, at string }{
		{"at_pickup", "2026-07-30T09:10:00Z"},
		{"picked_up", "2026-07-30T09:11:00Z"},
		{"in_progress", "2026-07-30T09:12:00Z"},
		{"arrived", "2026-07-30T09:20:00Z"},
		{"delivered", "2026-07-30T09:21:00Z"},
	} {
		if _, err := service.updateProgress(ctx, fixture.courier, core.Map{
			"assignmentId": fixture.firstLegID, "status": update.status, "occurredAt": update.at,
			"idempotencyKey": prefix + "-leg-1-" + core.String(index),
		}); err != nil {
			t.Fatalf("first-leg progress %s: %v", update.status, err)
		}
	}
	return fixture
}

// AC-65: the sample views report the sample's current or last known location.
func TestAC65LatestKnownLocationIsReportedOnTheSampleViews(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	t.Setenv("STALE_LOCATION_THRESHOLD_SECONDS", "3600")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runTwoLegJourneyToFirstLegDelivered(t, service, admin, "ac65")

	assertLatestLocation := func(label string, view core.Map) {
		t.Helper()
		raw, present := view["latestLocation"]
		if !present {
			t.Fatalf("%s does not report latestLocation at all: %v", label, view)
		}
		location, ok := raw.(core.Map)
		if !ok {
			t.Fatalf("%s latestLocation = %#v", label, raw)
		}
		if core.Float(location["latitude"]) != fixture.latitude ||
			core.Float(location["longitude"]) != fixture.longitude {
			t.Fatalf(
				"%s latestLocation = %v, want %v/%v",
				label, location, fixture.latitude, fixture.longitude,
			)
		}
		// The last known location is only meaningful with the time it was known.
		if core.String(view["locationRecordedAt"]) != fixture.recordedAt {
			t.Fatalf("%s locationRecordedAt = %v, want %q", label, view["locationRecordedAt"], fixture.recordedAt)
		}
	}

	// Every audience that owns the sample view sees it, not only dispatchers.
	for _, audience := range []struct {
		label string
		p     core.Principal
	}{
		{"requester", fixture.requester},
		{"courier", fixture.courier},
		{"administrator", admin},
	} {
		state, err := service.Handle(ctx, "read_sample_state", core.Map{"sampleId": fixture.sampleID}, audience.p)
		if err != nil {
			t.Fatal(err)
		}
		assertLatestLocation(audience.label+" read_sample_state", state)
		lifecycle, err := service.Handle(ctx, "read_sample_lifecycle", core.Map{"sampleId": fixture.sampleID}, audience.p)
		if err != nil {
			t.Fatal(err)
		}
		assertLatestLocation(audience.label+" read_sample_lifecycle", lifecycle)
	}

	// The dispatcher console reports the same last known position.
	operations, err := service.Handle(ctx, "read_operations_state", core.Map{}, admin)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range operations["samples"].([]core.Map) {
		if core.String(item["sampleId"]) != fixture.sampleID {
			continue
		}
		found = true
		assertLatestLocation("operations state", item)
	}
	if !found {
		t.Fatalf("sample %s is missing from the operations view", fixture.sampleID)
	}
}

// AC-67: route progress is reported per sample. A journey whose first leg is
// delivered and whose second leg has not started reports exactly the finished
// leg, so the field tracks progress rather than merely listing the route.
func TestAC67CompletedLegIdsTrackPartialRouteProgress(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runTwoLegJourneyToFirstLegDelivered(t, service, admin, "ac67")

	completedLegIDs := func() []string {
		t.Helper()
		lifecycle, err := service.Handle(
			ctx, "read_sample_lifecycle", core.Map{"sampleId": fixture.sampleID}, admin,
		)
		if err != nil {
			t.Fatal(err)
		}
		raw, present := lifecycle["completedLegIds"]
		if !present {
			t.Fatalf("the lifecycle view does not report completedLegIds: %v", lifecycle)
		}
		return core.Strings(raw)
	}

	partial := completedLegIDs()
	if len(partial) != 1 || partial[0] != fixture.firstLegID {
		t.Fatalf(
			"completedLegIds after one delivered leg = %v, want exactly [%s]",
			partial, fixture.firstLegID,
		)
	}
	if stringSliceContains(partial, fixture.secondLegID) {
		t.Fatalf("an unstarted leg is reported as completed: %v", partial)
	}
	// Sanity: the second leg really has not started.
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if status := state.Assignments[fixture.secondLegID].Status; status != "pending" {
			t.Fatalf("second leg status = %q, want pending", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Finishing the second leg must extend progress, not replace it.
	if err := service.Store.Update(ctx, func(state *mongostore.State) error {
		second := state.Assignments[fixture.secondLegID]
		second.Status = "delivered"
		second.CompletedAt = pointerTime(core.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	complete := completedLegIDs()
	if len(complete) != 2 ||
		!stringSliceContains(complete, fixture.firstLegID) ||
		!stringSliceContains(complete, fixture.secondLegID) {
		t.Fatalf("completedLegIds after both legs = %v, want both legs", complete)
	}
}

// AC-55: every custody change is visible in the sample lifecycle, not only in
// the audit trail. The audit half is already covered; this is the lifecycle
// half.
func TestAC55CustodyChangesAppearInTheSampleLifecycleTimeline(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runJourneyToLaboratoryReceipt(t, service, admin, "ac55")

	lifecycle, err := service.Handle(
		ctx, "read_sample_lifecycle", core.Map{"sampleId": fixture.sampleID}, admin,
	)
	if err != nil {
		t.Fatal(err)
	}
	timeline, ok := lifecycle["timeline"].([]core.Map)
	if !ok {
		t.Fatalf("lifecycle timeline = %#v", lifecycle["timeline"])
	}

	// Every custody change of the journey: custody begins at sealing, transfers
	// through a two-sided handoff, and ends at the laboratory.
	wanted := []string{
		"sample_collected_and_sealed",
		"handoff_initiated",
		"handoff_confirmed_sender",
		"handoff_confirmed_receiver",
		"laboratory_receipt_confirmed",
	}
	positions := map[string]int{}
	for index, event := range timeline {
		action := core.String(event["action"])
		if _, seen := positions[action]; !seen {
			positions[action] = index
		}
	}
	for _, action := range wanted {
		if _, present := positions[action]; !present {
			t.Fatalf(
				"custody change %q is absent from the sample lifecycle timeline; actions present: %v",
				action, positions,
			)
		}
	}
	for index := 1; index < len(wanted); index++ {
		if positions[wanted[index-1]] >= positions[wanted[index]] {
			t.Fatalf(
				"custody changes are out of order in the lifecycle timeline: %q at %d, %q at %d",
				wanted[index-1], positions[wanted[index-1]],
				wanted[index], positions[wanted[index]],
			)
		}
	}

	// The timeline shows the change, not merely that something happened: the
	// receiving side's record names the new holder, and the timeline is ordered.
	previous := ""
	sawNewHolder := false
	for _, event := range timeline {
		occurredAt := core.String(event["occurredAt"])
		if previous != "" && occurredAt < previous {
			t.Fatalf("lifecycle timeline is not ordered oldest to newest: %v", timeline)
		}
		previous = occurredAt
		if core.String(event["action"]) != "handoff_confirmed_receiver" {
			continue
		}
		details, ok := event["details"].(core.Map)
		if !ok {
			t.Fatalf("handoff confirmation has no details: %v", event)
		}
		if core.String(details["currentHolderId"]) != fixture.courier.ID {
			t.Fatalf("completed handoff records holder %v, want the courier", details["currentHolderId"])
		}
		sawNewHolder = true
	}
	if !sawNewHolder {
		t.Fatal("no completed custody transfer recorded the new holder in the lifecycle timeline")
	}

	// The lifecycle's own holder field agrees with the timeline's last change.
	if core.String(lifecycle["currentHolderId"]) != fixture.operator.ID {
		t.Fatalf("lifecycle currentHolderId = %v, want the laboratory operator", lifecycle["currentHolderId"])
	}
}
