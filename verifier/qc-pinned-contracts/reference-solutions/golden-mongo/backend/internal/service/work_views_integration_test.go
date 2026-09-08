package service

import (
	"context"
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// completedJourneyFixture is one finished journey plus every principal involved,
// so a test can assert what each role sees about current and past work.
type completedJourneyFixture struct {
	network      networkFixture
	requester    core.Principal
	collector    core.Principal
	courier      core.Principal
	operator     core.Principal
	dispatcher   core.Principal
	sampleID     string
	journeyID    string
	collectionID string
	transportID  string
}

func runJourneyToLaboratoryReceipt(
	t *testing.T,
	service *Service,
	admin core.Principal,
	prefix string,
) completedJourneyFixture {
	t.Helper()
	ctx := context.Background()
	network := buildServiceableNetwork(t, service, admin, prefix)
	fixture := completedJourneyFixture{
		network:    network,
		requester:  createTestUser(t, service, admin, prefix+"-requester", []string{"requester"}, nil),
		collector:  createTestUser(t, service, admin, prefix+"-collector", []string{"collection_professional"}, []string{"LX-A"}),
		courier:    createTestUser(t, service, admin, prefix+"-courier", []string{"delivery_partner"}, nil),
		operator:   createTestUserWithLocation(t, service, admin, prefix+"-operator", []string{"laboratory_team"}, nil, network.laboratoryID),
		dispatcher: createTestUser(t, service, admin, prefix+"-dispatcher", []string{"dispatcher"}, nil),
	}

	created, err := service.createCollectionRequest(ctx, fixture.requester, core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": fixture.network.sampleTypeID,
		"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T12:00:00Z",
		"containerType": "amber-container", "temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "idempotencyKey": prefix + "-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.sampleID = core.String(created["sampleId"])
	fixture.journeyID = core.String(created["journeyId"])
	plan, err := service.readJourneyPlan(ctx, admin, fixture.journeyID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.collectionID = core.String(plan["collectionAssignmentId"])
	fixture.transportID = core.String(plan["firstTransportAssignmentId"])

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
		"assignmentId": fixture.transportID, "idempotencyKey": prefix + "-accept-transport",
	}); err != nil {
		t.Fatal(err)
	}
	handoff, err := service.initiateHandoff(ctx, fixture.collector, core.Map{
		"sampleId": fixture.sampleID, "receiverId": fixture.courier.ID,
		"fromAssignmentId": fixture.collectionID, "toAssignmentId": fixture.transportID,
		"initiatedAt": "2026-07-30T09:06:00Z", "idempotencyKey": prefix + "-handoff",
	})
	if err != nil {
		t.Fatal(err)
	}
	handoffID := core.String(handoff["handoffId"])
	for _, confirmation := range []struct {
		p    core.Principal
		side string
		at   string
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
	for index, update := range []struct{ status, at string }{
		{"at_pickup", "2026-07-30T09:10:00Z"},
		{"picked_up", "2026-07-30T09:11:00Z"},
		{"in_progress", "2026-07-30T09:12:00Z"},
		{"arrived", "2026-07-30T09:35:00Z"},
		{"delivered", "2026-07-30T09:36:00Z"},
	} {
		if _, err := service.updateProgress(ctx, fixture.courier, core.Map{
			"assignmentId": fixture.transportID, "status": update.status, "occurredAt": update.at,
			"idempotencyKey": prefix + "-progress-" + core.String(index),
		}); err != nil {
			t.Fatalf("progress %s: %v", update.status, err)
		}
	}
	if _, err := service.confirmReceipt(ctx, fixture.operator, core.Map{
		"sampleId": fixture.sampleID, "laboratoryId": fixture.network.laboratoryID,
		"receivedAt": "2026-07-30T09:37:00Z", "idempotencyKey": prefix + "-receipt",
	}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func roleWorkBuckets(t *testing.T, out core.Map) map[string][]core.Map {
	t.Helper()
	buckets := map[string][]core.Map{}
	for _, name := range []string{"pending", "inProgress", "completed"} {
		value, present := out[name]
		if !present {
			t.Fatalf("work view is missing the %q bucket: %v", name, out)
		}
		items, ok := value.([]core.Map)
		if !ok {
			t.Fatalf("work bucket %q = %#v", name, value)
		}
		buckets[name] = items
	}
	return buckets
}

func assertOrderedHistory(t *testing.T, item core.Map) {
	t.Helper()
	raw, present := item["history"]
	if !present {
		t.Fatalf("work item has no status history: %v", item)
	}
	history, ok := raw.([]core.Map)
	if !ok {
		t.Fatalf("work item history = %#v", raw)
	}
	if len(history) == 0 {
		t.Fatalf("work item history is empty: %v", item)
	}
	previous := time.Time{}
	for _, event := range history {
		at, err := core.Time(event["occurredAt"])
		if err != nil {
			t.Fatalf("history entry has no usable time: %v", event)
		}
		if at.Before(previous) {
			t.Fatalf("history is not ordered oldest to newest: %v", history)
		}
		previous = at
	}
}

// AC-3, AC-4: every one of the seven roles has a current/past work split with an
// ordered per-item status history, and completed work really lands in the
// completed bucket once a journey finishes.
func TestListRoleWorkServesAllSevenRolesWithOrderedHistory(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runJourneyToLaboratoryReceipt(t, service, admin, "sevenroles")

	cases := []struct {
		role      string
		principal core.Principal
	}{
		{"requester", fixture.requester},
		{"collection_professional", fixture.collector},
		{"delivery_partner", fixture.courier},
		{"laboratory_team", fixture.operator},
		{"dispatcher", fixture.dispatcher},
		{"administrator", admin},
	}
	for _, test := range cases {
		out, err := service.Handle(ctx, "list_role_work", core.Map{"role": test.role}, test.principal)
		if err != nil {
			t.Fatalf("list work for %q: %v", test.role, err)
		}
		buckets := roleWorkBuckets(t, out)
		total := 0
		for _, items := range buckets {
			total += len(items)
			for _, item := range items {
				assertOrderedHistory(t, item)
			}
		}
		if total == 0 {
			t.Fatalf("role %q has no work at all after a completed journey", test.role)
		}
		if len(buckets["completed"]) == 0 {
			t.Fatalf("role %q has no completed work after a finished journey", test.role)
		}
	}

	// The seventh role, node_handler, has no work on this direct route but must
	// still answer with the three buckets rather than an error.
	handler := createTestUser(t, service, admin, "sevenroles-handler", []string{"node_handler"}, nil)
	out, err := service.Handle(ctx, "list_role_work", core.Map{"role": "node_handler"}, handler)
	if err != nil {
		t.Fatalf("list work for node_handler: %v", err)
	}
	roleWorkBuckets(t, out)

	// The four field roles keep exactly their previous assignment-shaped items.
	courierWork, err := service.Handle(
		ctx, "list_role_work", core.Map{"role": "delivery_partner"}, fixture.courier,
	)
	if err != nil {
		t.Fatal(err)
	}
	completed := roleWorkBuckets(t, courierWork)["completed"]
	if len(completed) != 1 ||
		core.String(completed[0]["assignmentId"]) != fixture.transportID ||
		core.String(completed[0]["sampleId"]) != fixture.sampleID {
		t.Fatalf("courier completed work changed shape: %v", completed)
	}

	// A requester sees their own request; an administrator sees it too but with
	// the requester named, and neither view is an error for these roles.
	requesterWork, err := service.Handle(
		ctx, "list_role_work", core.Map{"role": "requester"}, fixture.requester,
	)
	if err != nil {
		t.Fatal(err)
	}
	requesterCompleted := roleWorkBuckets(t, requesterWork)["completed"]
	if len(requesterCompleted) != 1 ||
		core.String(requesterCompleted[0]["sampleId"]) != fixture.sampleID {
		t.Fatalf("requester completed work = %v", requesterCompleted)
	}
	administratorWork, err := service.Handle(
		ctx, "list_role_work", core.Map{"role": "administrator"}, admin,
	)
	if err != nil {
		t.Fatal(err)
	}
	administratorCompleted := roleWorkBuckets(t, administratorWork)["completed"]
	if len(administratorCompleted) != 1 ||
		core.String(administratorCompleted[0]["requesterId"]) != fixture.requester.ID {
		t.Fatalf("administrator completed work = %v", administratorCompleted)
	}

	// A dispatcher's completed bucket holds the finished journey.
	dispatcherWork, err := service.Handle(
		ctx, "list_role_work", core.Map{"role": "dispatcher"}, fixture.dispatcher,
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatcherCompleted := roleWorkBuckets(t, dispatcherWork)["completed"]
	if len(dispatcherCompleted) != 1 ||
		core.String(dispatcherCompleted[0]["journeyId"]) != fixture.journeyID {
		t.Fatalf("dispatcher completed work = %v", dispatcherCompleted)
	}
}

// A requester listing another role's work is refused even though the role name
// is a real one.
func TestListRoleWorkRefusesARoleTheCallerIsNotActingUnder(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "scoped-requester", []string{"requester"}, nil)
	for _, role := range []string{"dispatcher", "administrator", "delivery_partner"} {
		_, err := service.Handle(ctx, "list_role_work", core.Map{"role": role}, requester)
		assertHTTPErrorStatus(t, err, 403)
	}
	// Omitting the role defaults to the role the caller is acting under.
	if _, err := service.Handle(ctx, "list_role_work", core.Map{}, requester); err != nil {
		t.Fatalf("defaulted work list refused: %v", err)
	}
}

// AC-68, AC-69: the roles that own the sample view see the last-update time and
// the staleness verdict, and it is the same verdict the dispatcher sees.
func TestSampleLocationFreshnessIsVisibleToTheRolesThatOwnTheSampleView(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	t.Setenv("STALE_LOCATION_THRESHOLD_SECONDS", "300")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runJourneyToLaboratoryReceipt(t, service, admin, "staleness")
	// The journey ran at 09:00; the staleness verdicts below are read at 09:20.
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:20:00Z")

	record := func(recordedAt string, sequence int) {
		t.Helper()
		if err := service.Store.Update(ctx, func(state *mongostore.State) error {
			at, err := core.Time(recordedAt)
			if err != nil {
				return err
			}
			state.WorkerLocations[fixture.transportID] = append(
				state.WorkerLocations[fixture.transportID],
				&mongostore.WorkerLocation{
					AssignmentID: fixture.transportID, Sequence: int64(sequence),
					WorkerID: fixture.courier.ID,
					Latitude: fixtureOriginLatitude, Longitude: fixtureOriginLongitude,
					RecordedAt: at,
				},
			)
			sample := state.Samples[fixture.sampleID]
			sample.LatestLatitude = pointerFloat(fixtureOriginLatitude)
			sample.LatestLongitude = pointerFloat(fixtureOriginLongitude)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	freshness := func(p core.Principal) (string, bool) {
		t.Helper()
		lifecycle, err := service.Handle(ctx, "read_sample_lifecycle", core.Map{"sampleId": fixture.sampleID}, p)
		if err != nil {
			t.Fatal(err)
		}
		state, err := service.Handle(ctx, "read_sample_state", core.Map{"sampleId": fixture.sampleID}, p)
		if err != nil {
			t.Fatal(err)
		}
		if lifecycle["locationRecordedAt"] != state["locationRecordedAt"] ||
			lifecycle["locationStale"] != state["locationStale"] {
			t.Fatalf(
				"lifecycle and state disagree: lifecycle=%v/%v state=%v/%v",
				lifecycle["locationRecordedAt"], lifecycle["locationStale"],
				state["locationRecordedAt"], state["locationStale"],
			)
		}
		return core.String(lifecycle["locationRecordedAt"]), core.Bool(lifecycle["locationStale"])
	}

	operationsFreshness := func() (string, bool) {
		t.Helper()
		operations, err := service.readOperations(ctx, admin, core.Map{})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range operations["samples"].([]core.Map) {
			if core.String(item["sampleId"]) == fixture.sampleID {
				return core.String(item["locationRecordedAt"]), core.Bool(item["locationStale"])
			}
		}
		t.Fatalf("sample %s is missing from the operations view", fixture.sampleID)
		return "", false
	}

	// Nothing recorded yet: never fresh, for every audience.
	if _, stale := freshness(fixture.requester); !stale {
		t.Fatal("a sample with no location reading was reported fresh")
	}

	// A reading inside the threshold is fresh, for the requester and the courier
	// alike, and it matches the dispatcher's operations view exactly.
	record("2026-07-30T09:18:00Z", 1)
	for _, p := range []core.Principal{fixture.requester, fixture.courier, admin} {
		at, stale := freshness(p)
		if stale {
			t.Fatalf("a two-minute-old reading was reported stale to %v", p.Roles)
		}
		if at != "2026-07-30T09:18:00Z" {
			t.Fatalf("last-update time = %q", at)
		}
	}
	operationsAt, operationsStale := operationsFreshness()
	sampleAt, sampleStale := freshness(fixture.requester)
	if operationsAt != sampleAt || operationsStale != sampleStale {
		t.Fatalf(
			"per-sample freshness %q/%v disagrees with operations state %q/%v",
			sampleAt, sampleStale, operationsAt, operationsStale,
		)
	}

	// A reading older than the threshold is stale everywhere.
	if err := service.Store.Update(ctx, func(state *mongostore.State) error {
		for _, reading := range state.WorkerLocations[fixture.transportID] {
			reading.RecordedAt = time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []core.Principal{fixture.requester, fixture.courier, admin} {
		at, stale := freshness(p)
		if !stale {
			t.Fatalf("a twenty-minute-old reading was reported fresh to %v", p.Roles)
		}
		if at != "2026-07-30T09:00:00Z" {
			t.Fatalf("stale last-update time = %q", at)
		}
	}
	operationsAt, operationsStale = operationsFreshness()
	sampleAt, sampleStale = freshness(fixture.requester)
	if operationsAt != sampleAt || operationsStale != sampleStale {
		t.Fatalf(
			"stale per-sample freshness %q/%v disagrees with operations state %q/%v",
			sampleAt, sampleStale, operationsAt, operationsStale,
		)
	}
}

// A relay courier sees only the leg they were assigned. A dispatcher keeps the
// whole plan.
func TestCourierSeesOnlyTheirOwnLegOfAMultiLegPlan(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	first := createTestUser(t, service, admin, "relay-first", []string{"delivery_partner"}, nil)
	second := createTestUser(t, service, admin, "relay-second", []string{"delivery_partner"}, nil)
	dispatcher := createTestUser(t, service, admin, "relay-dispatcher", []string{"dispatcher"}, nil)
	requester := createTestUser(t, service, admin, "relay-requester", []string{"requester"}, nil)

	if err := service.Store.Update(ctx, func(state *mongostore.State) error {
		state.Locations["origin"] = collectionPointLocation("origin", 0, 0)
		state.Locations["hub"] = transferPointLocation("hub")
		state.Locations["lab"] = laboratoryLocation("lab")
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", RequesterID: requester.ID, RequestID: "request",
			Status: "sealed", ActiveJourneyID: "journey",
		}
		state.Requests["request"] = &mongostore.CollectionRequest{
			ID: "request", RequesterID: requester.ID,
			RequestedAt:      core.Now(),
			DeliveryDeadline: core.Now().Add(3 * time.Hour),
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", PrimarySampleID: "sample", SampleIDs: []string{"sample"},
			PlanVersion: 1, RouteMode: "multi_stop", Status: "active",
			EstimatedDeliveryAt: core.Now().Add(time.Hour),
		}
		for index, stop := range []struct {
			id, location, kind string
		}{
			{"stop-1", "origin", "pickup"},
			{"stop-2", "hub", "handoff"},
			{"stop-3", "lab", "delivery"},
		} {
			state.Stops[stop.id] = &mongostore.JourneyStop{
				ID: stop.id, JourneyID: "journey", LocationID: stop.location,
				StopType: stop.kind, Sequence: index + 1, EstimatedAt: core.Now(),
			}
		}
		state.Assignments["leg-1"] = &mongostore.Assignment{
			ID: "leg-1", JourneyID: "journey", SampleID: "sample", Kind: "transport",
			LegSequence: 1, FromLocationID: "origin", ToLocationID: "hub",
			WorkerID: first.ID, Status: "accepted",
		}
		state.Assignments["leg-2"] = &mongostore.Assignment{
			ID: "leg-2", JourneyID: "journey", SampleID: "sample", Kind: "transport",
			LegSequence: 2, FromLocationID: "hub", ToLocationID: "lab",
			WorkerID: second.ID, Status: "pending",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	firstView, err := service.readJourneyPlan(ctx, first, "journey")
	if err != nil {
		t.Fatalf("first courier plan: %v", err)
	}
	legs, ok := firstView["legs"].([]core.Map)
	if !ok || len(legs) != 1 {
		t.Fatalf("first courier legs = %#v", firstView["legs"])
	}
	if core.String(legs[0]["assignmentId"]) != "leg-1" {
		t.Fatalf("first courier saw leg %v", legs[0])
	}
	if core.String(legs[0]["workerId"]) != first.ID {
		t.Fatalf("first courier leg is not attributed to them: %v", legs[0])
	}
	workers := core.Strings(firstView["assignedWorkerIds"])
	if len(workers) != 1 || workers[0] != first.ID {
		t.Fatalf("first courier saw worker identities %v", workers)
	}
	if firstView["totalCostUnits"] != nil || firstView["capacityAvailable"] != nil {
		t.Fatalf("courier view leaked whole-plan economics: %v", firstView)
	}
	stops, ok := firstView["orderedStops"].([]core.Map)
	if !ok || len(stops) != 2 {
		t.Fatalf("first courier stops = %#v", firstView["orderedStops"])
	}
	for _, stop := range stops {
		if core.String(stop["locationId"]) == "lab" {
			t.Fatalf("first courier saw a stop outside their leg: %v", stop)
		}
	}

	secondView, err := service.readJourneyPlan(ctx, second, "journey")
	if err != nil {
		t.Fatalf("second courier plan: %v", err)
	}
	secondLegs := secondView["legs"].([]core.Map)
	if len(secondLegs) != 1 || core.String(secondLegs[0]["assignmentId"]) != "leg-2" {
		t.Fatalf("second courier legs = %v", secondLegs)
	}
	for _, leg := range secondLegs {
		if core.String(leg["workerId"]) == first.ID {
			t.Fatalf("second courier saw the first courier's identity: %v", leg)
		}
	}

	dispatcherView, err := service.readJourneyPlan(ctx, dispatcher, "journey")
	if err != nil {
		t.Fatalf("dispatcher plan: %v", err)
	}
	dispatcherLegs := dispatcherView["legs"].([]core.Map)
	if len(dispatcherLegs) != 2 {
		t.Fatalf("dispatcher legs = %v, want the whole plan", dispatcherLegs)
	}
	dispatcherWorkers := core.Strings(dispatcherView["assignedWorkerIds"])
	if len(dispatcherWorkers) != 2 {
		t.Fatalf("dispatcher assigned workers = %v, want both", dispatcherWorkers)
	}
	if len(dispatcherView["orderedStops"].([]core.Map)) != 3 {
		t.Fatalf("dispatcher stops = %v, want all three", dispatcherView["orderedStops"])
	}
}

// AC-31: an assignment nobody was offered cannot be self-accepted, and the
// normal offer-then-accept flow still works.
func TestUnofferedAssignmentCannotBeSelfAccepted(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	courier := createTestUser(t, service, admin, "eager-courier", []string{"delivery_partner"}, nil)

	if err := service.Store.Update(ctx, func(state *mongostore.State) error {
		state.SampleTypes["type"] = &mongostore.SampleType{ID: "type"}
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", SampleTypeID: "type", Status: "sealed", ActiveJourneyID: "journey",
			SealedAt: pointerTime(core.Now()),
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", PrimarySampleID: "sample", SampleIDs: []string{"sample"},
			Status: "active",
		}
		state.Assignments["unoffered"] = &mongostore.Assignment{
			ID: "unoffered", JourneyID: "journey", SampleID: "sample",
			Kind: "transport", LegSequence: 1, Status: "pending",
			CreatedAt: core.Now(), Declines: []mongostore.AssignmentDecline{},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := service.acceptAssignment(ctx, courier, core.Map{
		"assignmentId": "unoffered", "idempotencyKey": "self-accept-unoffered",
	})
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "offer_not_offered")
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		assignment := state.Assignments["unoffered"]
		if assignment.WorkerID != "" || assignment.Status != "pending" ||
			assignment.AcceptedAt != nil {
			t.Fatalf("a refused self-acceptance changed the assignment: %+v", assignment)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The maintenance pass offers the work through the fair-assignment path, and
	// only then may the offered worker accept it.
	if err := service.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		assignment := state.Assignments["unoffered"]
		if assignment.WorkerID != courier.ID || assignment.OfferExpiresAt == nil {
			t.Fatalf("unoffered work was not offered by the maintenance pass: %+v", assignment)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	accepted, err := service.acceptAssignment(ctx, courier, core.Map{
		"assignmentId": "unoffered", "idempotencyKey": "accept-after-offer",
	})
	if err != nil {
		t.Fatalf("offered worker acceptance: %v", err)
	}
	if accepted["status"] != "accepted" || accepted["workerId"] != courier.ID {
		t.Fatalf("accepted = %v", accepted)
	}
}
