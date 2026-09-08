package service

import (
	"context"
	"strings"
	"testing"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// defineBatchableSampleType creates a sample profile that carries no named
// policy of its own and is accepted by the fixture laboratory, so a batch probe
// can vary exactly one generic attribute — container or temperature range — and
// nothing else.
func defineBatchableSampleType(
	t *testing.T,
	service *Service,
	admin core.Principal,
	laboratoryID, name, container string,
	minimum, maximum float64,
) string {
	t.Helper()
	ctx := context.Background()
	created, err := service.createSampleType(ctx, admin, core.Map{
		"name": name, "containerType": container,
		"temperatureMinC": minimum, "temperatureMaxC": maximum,
		"incompatibleSampleTypes": []string{}, "requiresCooledRoute": false,
		"qualifiedCollectorRequired": false, "requiredQualification": "",
		"batchingAllowed": true,
	})
	if err != nil {
		t.Fatalf("create sample type %s: %v", name, err)
	}
	typeID := core.String(created["sampleTypeId"])
	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": laboratoryID, "sampleTypeId": typeID, "accepts": true,
	}); err != nil {
		t.Fatalf("configure acceptance for %s: %v", name, err)
	}
	return typeID
}

// requestSampleOfType raises one serviceable collection request for an arbitrary
// sample profile. overrides replaces individual request fields, so a probe can
// move a collection window or a deadline without restating the whole payload.
func requestSampleOfType(
	t *testing.T,
	service *Service,
	requester core.Principal,
	typeID, container string,
	minimum, maximum float64,
	overrides core.Map,
	key string,
) (string, string) {
	t.Helper()
	request := core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": typeID,
		"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T12:00:00Z",
		"containerType": container, "temperatureMinC": minimum, "temperatureMaxC": maximum,
		"incompatibleSampleTypes": []string{}, "idempotencyKey": key,
	}
	for field, value := range overrides {
		request[field] = value
	}
	created, err := service.Handle(
		context.Background(), "create_collection_request", request, requester,
	)
	if err != nil {
		t.Fatalf("create request %s: %v", key, err)
	}
	if core.String(created["serviceability"]) != "serviceable" {
		t.Fatalf("request %s was not planned: %v", key, created)
	}
	return core.String(created["sampleId"]), core.String(created["journeyId"])
}

// mergeSamplesOnto drives the real batch edit and returns whatever it answered.
func mergeSamplesOnto(
	t *testing.T,
	service *Service,
	dispatcher core.Principal,
	journeyID string,
	planVersion int,
	sampleIDs []string,
	key string,
) (core.Map, error) {
	t.Helper()
	return service.Handle(context.Background(), "edit_active_plan", core.Map{
		"journeyId": journeyID, "expectedPlanVersion": planVersion,
		"changeType":     "merge_samples",
		"changes":        core.Map{"sampleIds": sampleIDs},
		"idempotencyKey": key,
	}, dispatcher)
}

// journeyCapacityAvailable is the route headroom a journey was planned with, the
// number the batch-capacity clause is measured against.
func journeyCapacityAvailable(t *testing.T, service *Service, journeyID string) int {
	t.Helper()
	available := 0
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		journey := state.Journeys[journeyID]
		if journey == nil {
			t.Fatalf("journey %s is missing", journeyID)
		}
		available = journey.CapacityAvailable
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return available
}

// AC-42, worker-eligibility clause: a batch or plan change is valid only when the
// worker who would carry the work is eligible for it. Two different ways of being
// ineligible are probed, each against a worker who is otherwise perfectly usable —
// active, available and below their assignment capacity — so the refusal can only
// be about eligibility. AC-44 rides along: each refusal leaves the plan untouched.
func TestAC42WorkOfferedToAnIneligibleWorkerIsRefused(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac42w-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac42w-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac42w-collector", []string{"collection_professional"}, []string{"LX-A"})
	// A real collection professional who simply does not hold Amber's qualification.
	trainee := createTestUser(t, service, admin, "ac42w-trainee", []string{"collection_professional"}, []string{"LX-Z"})
	createTestUser(t, service, admin, "ac42w-courier", []string{"delivery_partner"}, nil)
	handler := createTestUser(t, service, admin, "ac42w-handler", []string{"node_handler"}, nil)
	qualified := createTestUser(t, service, admin, "ac42w-collector-2", []string{"collection_professional"}, []string{"LX-A"})
	network := buildServiceableNetwork(t, service, admin, "ac42w")

	_, journeyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac42w-request",
	)
	plan, err := service.readJourneyPlan(ctx, dispatcher, journeyID)
	if err != nil {
		t.Fatal(err)
	}
	collectionID := core.String(plan["collectionAssignmentId"])
	transportID := core.String(plan["firstTransportAssignmentId"])
	if collectionID == "" || transportID == "" {
		t.Fatalf("plan is missing its collection or transport work: %v", plan)
	}
	before := capturePlanShape(t, service, journeyID)

	for _, probe := range []struct {
		label        string
		assignmentID string
		workerID     string
		key          string
	}{
		{
			label:        "a node handler offered a transport leg",
			assignmentID: transportID,
			workerID:     handler.ID,
			key:          "ac42w-wrong-role",
		},
		{
			label:        "a collection professional without Amber's qualification",
			assignmentID: collectionID,
			workerID:     trainee.ID,
			key:          "ac42w-unqualified",
		},
	} {
		_, err := service.Handle(ctx, "edit_active_plan", core.Map{
			"journeyId": journeyID, "expectedPlanVersion": 1,
			"changeType": "replace_worker",
			"changes": core.Map{
				"assignmentId": probe.assignmentID, "workerId": probe.workerID,
				"reason": "ac42 eligibility probe",
			},
			"idempotencyKey": probe.key,
		}, dispatcher)
		assertHTTPErrorStatus(t, err, 409)
		assertHTTPErrorCode(t, err, "ineligible_worker")
		assertSamePlanShape(
			t, "after "+probe.label, before, capturePlanShape(t, service, journeyID),
		)
	}

	// Control: an eligible collection professional is accepted for the very same
	// assignment, so the two refusals are about the workers and not about the
	// worker-replacement change itself.
	accepted, err := service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": journeyID, "expectedPlanVersion": 1,
		"changeType": "replace_worker",
		"changes": core.Map{
			"assignmentId": collectionID, "workerId": qualified.ID,
			"reason": "ac42 eligibility control",
		},
		"idempotencyKey": "ac42w-eligible",
	}, dispatcher)
	if err != nil {
		t.Fatalf("an eligible replacement collector was refused: %v", err)
	}
	if core.Int(accepted["planVersion"]) != 2 {
		t.Fatalf("planVersion after the accepted swap = %v, want 2", accepted["planVersion"])
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if state.Assignments[collectionID].WorkerID != qualified.ID {
			t.Fatalf("collection work is held by %q", state.Assignments[collectionID].WorkerID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-42, capacity clause: a batch is valid only when the route it will travel has
// capacity for every sample in it. Both pooled journeys are planned on the same
// two-unit network, so the second one is planned with a single unit of headroom
// and cannot absorb a second sample — while the first one, planned when the pools
// were empty, can. That contrast is what makes the refusal about capacity.
func TestAC42BatchBeyondTheRoutesReservedCapacityIsRefused(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac42c-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac42c-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac42c-collector", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "ac42c-courier", []string{"delivery_partner"}, nil)
	createTestUser(t, service, admin, "ac42c-operator", []string{"laboratory_team"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac42c")

	for _, resourceID := range []string{
		network.nodeID, network.laboratoryID, network.connectionID,
	} {
		if _, err := service.configureCapacity(ctx, admin, core.Map{
			"resourceId": resourceID, "capacityUnits": 2, "cooledCapacityUnits": 2,
		}); err != nil {
			t.Fatalf("configure capacity for %s: %v", resourceID, err)
		}
	}

	firstSampleID, firstJourneyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac42c-first",
	)
	secondSampleID, secondJourneyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac42c-second",
	)
	if got := journeyCapacityAvailable(t, service, secondJourneyID); got != 1 {
		t.Fatalf(
			"the second journey was planned with %d units of headroom, so this probe would not be about capacity",
			got,
		)
	}
	if got := journeyCapacityAvailable(t, service, firstJourneyID); got != 2 {
		t.Fatalf("the first journey was planned with %d units of headroom, want 2", got)
	}

	before := capturePlanShape(t, service, secondJourneyID)
	_, err := mergeSamplesOnto(
		t, service, dispatcher, secondJourneyID, 1,
		[]string{secondSampleID, firstSampleID}, "ac42c-merge-samples",
	)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "capacity_exceeded")
	assertSamePlanShape(
		t, "after a batch beyond the route's headroom", before,
		capturePlanShape(t, service, secondJourneyID),
	)

	// The dedicated journey-merge operation enforces the same limit.
	_, err = service.Handle(ctx, "merge_journeys", core.Map{
		"targetJourneyId": secondJourneyID, "expectedPlanVersion": 1,
		"sourceJourneyIds": []string{firstJourneyID},
		"idempotencyKey":   "ac42c-merge-journeys",
	}, dispatcher)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "capacity_exceeded")
	assertSamePlanShape(
		t, "after a journey merge beyond the route's headroom", before,
		capturePlanShape(t, service, secondJourneyID),
	)

	// Control: the identical pair of samples batches onto the journey that does
	// have headroom for two.
	merged, err := mergeSamplesOnto(
		t, service, dispatcher, firstJourneyID, 1,
		[]string{firstSampleID, secondSampleID}, "ac42c-merge-with-headroom",
	)
	if err != nil {
		t.Fatalf("the same batch was refused on a journey with headroom for it: %v", err)
	}
	if core.String(merged["routeMode"]) != "batched" {
		t.Fatalf("the accepted batch = %v", merged)
	}
}

// AC-42, deadline clause: a batch is valid only when every sample in it still
// meets its own delivery deadline. The target journey collects late — its window
// opens two hours after the request — so it arrives at 11:30, while the sample
// offered for the batch must be received by 10:00. Each request is serviceable on
// its own, so only the combination is late.
func TestAC42BatchThatWouldMissASampleDeadlineIsRefused(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	requester := createTestUser(t, service, admin, "ac42d-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac42d-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac42d-collector", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "ac42d-courier", []string{"delivery_partner"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac42d")

	lateSampleID, lateJourneyID := requestSampleOfType(
		t, service, requester, network.sampleTypeID, "amber-container", 2.0, 8.0,
		core.Map{"earliestCollectionAt": "2026-07-30T11:00:00Z"}, "ac42d-late",
	)
	tightSampleID, _ := requestSampleOfType(
		t, service, requester, network.sampleTypeID, "amber-container", 2.0, 8.0,
		core.Map{"deliveryDeadline": "2026-07-30T10:00:00Z"}, "ac42d-tight",
	)
	roomySampleID, _ := requestSampleOfType(
		t, service, requester, network.sampleTypeID, "amber-container", 2.0, 8.0,
		nil, "ac42d-roomy",
	)

	// The target's own arrival estimate really is later than the tight sample's
	// deadline, which is the only thing that separates the two batches below.
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		journey := state.Journeys[lateJourneyID]
		tight := state.Requests[state.Samples[tightSampleID].RequestID]
		if !journey.EstimatedDeliveryAt.After(tight.DeliveryDeadline) {
			t.Fatalf(
				"the target arrives at %s and the tight sample is due by %s, so this probe is not about deadlines",
				formatTime(journey.EstimatedDeliveryAt), formatTime(tight.DeliveryDeadline),
			)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	before := capturePlanShape(t, service, lateJourneyID)
	_, err := mergeSamplesOnto(
		t, service, dispatcher, lateJourneyID, 1,
		[]string{lateSampleID, tightSampleID}, "ac42d-merge-tight",
	)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "deadline_exceeded")
	assertSamePlanShape(
		t, "after a batch that would miss a sample's deadline", before,
		capturePlanShape(t, service, lateJourneyID),
	)

	// Control: the same journey batches a sample whose deadline it does meet.
	if _, err := mergeSamplesOnto(
		t, service, dispatcher, lateJourneyID, 1,
		[]string{lateSampleID, roomySampleID}, "ac42d-merge-roomy",
	); err != nil {
		t.Fatalf("a batch that meets every deadline was refused: %v", err)
	}
}

// AC-42: a batch has to be a batch of this journey, of samples that are still
// travelling. Three ways of failing that are probed, and each leaves the current
// plan exactly as it was.
func TestAC42BatchMembershipIsEnforced(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac42m-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac42m-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac42m-collector", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "ac42m-courier", []string{"delivery_partner"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac42m")

	primarySampleID, primaryJourneyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac42m-primary",
	)
	companionSampleID, _ := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac42m-companion",
	)
	finishedSampleID, finishedJourneyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac42m-finished",
	)

	// A sample whose journey has been cancelled is out of circulation.
	if _, err := service.Handle(ctx, "cancel_journey", core.Map{
		"journeyId": finishedJourneyID, "expectedPlanVersion": 1,
		"reason": "ac42 membership probe", "idempotencyKey": "ac42m-cancel",
	}, dispatcher); err != nil {
		t.Fatalf("cancel journey: %v", err)
	}

	before := capturePlanShape(t, service, primaryJourneyID)
	for _, probe := range []struct {
		label     string
		sampleIDs []string
		key       string
	}{
		{
			label:     "a single sample is not a batch",
			sampleIDs: []string{companionSampleID},
			key:       "ac42m-single",
		},
		{
			label:     "a batch that leaves out the journey's own sample",
			sampleIDs: []string{companionSampleID, finishedSampleID},
			key:       "ac42m-without-primary",
		},
		{
			label:     "a batch containing a cancelled sample",
			sampleIDs: []string{primarySampleID, finishedSampleID},
			key:       "ac42m-terminal",
		},
	} {
		_, err := mergeSamplesOnto(
			t, service, dispatcher, primaryJourneyID, 1, probe.sampleIDs, probe.key,
		)
		assertHTTPErrorStatus(t, err, 409)
		assertHTTPErrorCode(t, err, "invalid_batch")
		assertSamePlanShape(
			t, "after "+probe.label, before, capturePlanShape(t, service, primaryJourneyID),
		)
	}

	// Control: two live samples, one of them the journey's own, do batch.
	if _, err := mergeSamplesOnto(
		t, service, dispatcher, primaryJourneyID, 1,
		[]string{primarySampleID, companionSampleID}, "ac42m-valid",
	); err != nil {
		t.Fatalf("a well-formed batch was refused: %v", err)
	}
}

// AC-42, container and temperature clause. These two generic attributes are what
// AC-19 calls the rules that apply in addition to the named profiles, and neither
// was exercised before. Each probe varies exactly ONE of them and holds
// everything else — destination laboratory, cooling requirement, separation rules,
// capacity, deadlines — identical to the control, so the refusal can only be
// about the attribute under test.
func TestAC42BatchRequiresCompatibleContainersAndTemperatures(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	requester := createTestUser(t, service, admin, "ac42t-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac42t-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac42t-collector", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "ac42t-courier", []string{"delivery_partner"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac42t")

	// Same temperature range as the fixture's Amber profile, different container.
	otherContainerID := defineBatchableSampleType(
		t, service, admin, network.laboratoryID, "Sierra", "sierra-container", 2.0, 8.0,
	)
	// Same container as the fixture's Amber profile, temperature range that cannot
	// overlap it.
	otherTemperatureID := defineBatchableSampleType(
		t, service, admin, network.laboratoryID, "Tango", "amber-container", 20.0, 30.0,
	)

	primarySampleID, primaryJourneyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac42t-primary",
	)
	companionSampleID, _ := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac42t-companion",
	)
	wrongContainerSampleID, _ := requestSampleOfType(
		t, service, requester, otherContainerID, "sierra-container", 2.0, 8.0,
		nil, "ac42t-container",
	)
	wrongTemperatureSampleID, _ := requestSampleOfType(
		t, service, requester, otherTemperatureID, "amber-container", 20.0, 30.0,
		nil, "ac42t-temperature",
	)

	before := capturePlanShape(t, service, primaryJourneyID)
	for _, probe := range []struct {
		label    string
		sampleID string
		key      string
	}{
		{"a sample in a different container", wrongContainerSampleID, "ac42t-merge-container"},
		{"a sample whose temperature range cannot overlap", wrongTemperatureSampleID, "ac42t-merge-temperature"},
	} {
		_, err := mergeSamplesOnto(
			t, service, dispatcher, primaryJourneyID, 1,
			[]string{primarySampleID, probe.sampleID}, probe.key,
		)
		assertHTTPErrorStatus(t, err, 409)
		assertHTTPErrorCode(t, err, "incompatible_batch")
		assertSamePlanShape(
			t, "after batching "+probe.label, before,
			capturePlanShape(t, service, primaryJourneyID),
		)
	}

	// Control: a sample sharing both attributes batches, so the refusals above are
	// about container and temperature and not about batching in general.
	if _, err := mergeSamplesOnto(
		t, service, dispatcher, primaryJourneyID, 1,
		[]string{primarySampleID, companionSampleID}, "ac42t-merge-compatible",
	); err != nil {
		t.Fatalf("a batch sharing container and temperature range was refused: %v", err)
	}
}

// AC-42, stop-and-mode consistency: a journey carrying more than one sample must
// be marked as a batch, or its route mode misdescribes the work being planned.
//
// Every public operation that changes a journey's sample set recomputes the route
// mode from the sample count — edit_active_plan merge_samples and split_samples,
// merge_journeys, split_journey, rebuildEditedJourneyRoute, replaceJourneyRoute,
// removeSampleFromActiveBatch and expireSample all do — so no request can leave a
// multi-sample journey unmarked, and the first half of this probe asserts that
// normalisation really happens. The invariant is therefore checked against a real
// batched plan whose route mode alone is then corrupted in the store: the plan
// validator rejects it, and so does a real edit_active_plan over it, which is what
// keeps the guard from rotting away silently.
func TestAC42MultiSampleJourneyMustBeMarkedAsBatched(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac42r-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac42r-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac42r-collector", []string{"collection_professional"}, []string{"LX-A"})
	courier := createTestUser(t, service, admin, "ac42r-courier", []string{"delivery_partner"}, nil)
	replacement := createTestUser(t, service, admin, "ac42r-courier-2", []string{"delivery_partner"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac42r")

	primarySampleID, journeyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac42r-primary",
	)
	companionSampleID, _ := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac42r-companion",
	)
	batched, err := mergeSamplesOnto(
		t, service, dispatcher, journeyID, 1,
		[]string{primarySampleID, companionSampleID}, "ac42r-merge",
	)
	if err != nil {
		t.Fatalf("build a batched journey: %v", err)
	}
	// A real batch really is marked as one; nothing has to be corrupted for that.
	if core.String(batched["routeMode"]) != "batched" {
		t.Fatalf("a two-sample journey reports routeMode %v", batched["routeMode"])
	}
	planVersion := core.Int(batched["planVersion"])

	plan, err := service.readJourneyPlan(ctx, dispatcher, journeyID)
	if err != nil {
		t.Fatal(err)
	}
	transportID := core.String(plan["firstTransportAssignmentId"])
	if transportID == "" {
		t.Fatalf("batched plan has no transport leg: %v", plan)
	}
	// The replacement worker has to differ from whoever holds the leg now.
	swapTo := replacement
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if state.Assignments[transportID].WorkerID == replacement.ID {
			swapTo = courier
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Misreport nothing but the route mode.
	if err := service.Store.Update(ctx, func(state *mongostore.State) error {
		state.Journeys[journeyID].RouteMode = "direct"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var validation error
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		validation = validateJourneyPlan(state, state.Journeys[journeyID])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertHTTPErrorStatus(t, validation, 409)
	assertHTTPErrorCode(t, validation, "invalid_route_mode")

	// And the guard is enforced on the command path, not only in isolation: a plan
	// edit over the misreported journey is refused by the same rule.
	before := capturePlanShape(t, service, journeyID)
	_, err = service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": journeyID, "expectedPlanVersion": planVersion,
		"changeType": "replace_worker",
		"changes": core.Map{
			"assignmentId": transportID, "workerId": swapTo.ID,
			"reason": "ac42 route-mode probe",
		},
		"idempotencyKey": "ac42r-edit-misreported",
	}, dispatcher)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "invalid_route_mode")
	assertSamePlanShape(
		t, "after an edit over a misreported route mode", before,
		capturePlanShape(t, service, journeyID),
	)

	// Restore only the route mode and the identical edit is accepted, so the
	// refusal above was about the route mode and nothing else.
	if err := service.Store.Update(ctx, func(state *mongostore.State) error {
		state.Journeys[journeyID].RouteMode = "batched"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": journeyID, "expectedPlanVersion": planVersion,
		"changeType": "replace_worker",
		"changes": core.Map{
			"assignmentId": transportID, "workerId": swapTo.ID,
			"reason": "ac42 route-mode control",
		},
		"idempotencyKey": "ac42r-edit-restored",
	}, dispatcher); err != nil {
		t.Fatalf("the identical edit was refused once the route mode was correct: %v", err)
	}
}

// AC-43: a plan change is genuinely GATED by revalidation of the whole affected
// plan, not merely by the checks belonging to the change itself.
//
// The change asked for here is impeccable on its own terms: a pending transport
// leg is handed to a second delivery partner who is active, available, holds the
// right role and is below their assignment capacity, and the leg's own worker
// check passes. What makes it fail is a DIFFERENT leg — the collection work,
// which the edit does not touch — whose collector lost Amber's qualification in
// the meantime. Only a revalidation that walks the entire plan can see that, so
// the refusal proves the gate exists; restoring the qualification and replaying
// the identical change proves the gate is what refused it.
func TestAC43PlanEditIsGatedByWholePlanRevalidation(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac43-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac43-dispatcher", []string{"dispatcher"}, nil)
	collector := createTestUser(t, service, admin, "ac43-collector", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "ac43-courier", []string{"delivery_partner"}, nil)
	secondCourier := createTestUser(t, service, admin, "ac43-courier-2", []string{"delivery_partner"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac43")

	_, journeyID := createRequestFor(
		t, service, requester, network.sampleTypeID, "amber-container", "ac43-request",
	)
	plan, err := service.readJourneyPlan(ctx, dispatcher, journeyID)
	if err != nil {
		t.Fatal(err)
	}
	collectionID := core.String(plan["collectionAssignmentId"])
	transportID := core.String(plan["firstTransportAssignmentId"])
	if collectionID == "" || transportID == "" {
		t.Fatalf("plan is missing its collection or transport work: %v", plan)
	}
	var originalCourierID string
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if state.Assignments[collectionID].WorkerID != collector.ID {
			t.Fatalf(
				"collection work went to %q, not the qualified collector this probe needs",
				state.Assignments[collectionID].WorkerID,
			)
		}
		originalCourierID = state.Assignments[transportID].WorkerID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if originalCourierID == secondCourier.ID {
		t.Fatal("the transport leg is already held by the replacement worker")
	}

	setCollectorQualification := func(active bool) {
		t.Helper()
		if _, err := service.updateUserAccess(ctx, admin, core.Map{
			"userId": collector.ID, "roles": []string{"collection_professional"},
			"qualifications": []string{"LX-A"}, "active": true,
			"certifications": []any{core.Map{"code": "LX-A", "active": active}},
		}); err != nil {
			t.Fatalf("set collector qualification active=%t: %v", active, err)
		}
	}

	swap := func(key string) (core.Map, error) {
		return service.Handle(ctx, "edit_active_plan", core.Map{
			"journeyId": journeyID, "expectedPlanVersion": 1,
			"changeType": "replace_worker",
			"changes": core.Map{
				"assignmentId": transportID, "workerId": secondCourier.ID,
				"reason": "ac43 revalidation gate",
			},
			"idempotencyKey": key,
		}, dispatcher)
	}

	// The plan becomes invalid somewhere the edit does not touch.
	setCollectorQualification(false)
	before := capturePlanShape(t, service, journeyID)

	_, err = swap("ac43-gated")
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "ineligible_worker")
	// The refusal has to name the collector's qualification. If it named the role
	// of the swapped worker instead, the change would have been stopped by its own
	// check rather than by revalidation of the rest of the plan.
	if message := err.Error(); !strings.Contains(message, "not qualified") {
		t.Fatalf(
			"the edit was refused with %q; AC-43 needs it refused by the untouched collection leg's revalidation",
			message,
		)
	}
	assertSamePlanShape(
		t, "after an edit refused by revalidation", before,
		capturePlanShape(t, service, journeyID),
	)
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if got := state.Assignments[transportID].WorkerID; got != originalCourierID {
			t.Fatalf("the refused edit still moved the transport leg to %q", got)
		}
		if state.Assignments[transportID].AcceptedAt != nil {
			t.Fatal("the refused edit re-offered the transport leg")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Remove the reason revalidation failed, change nothing else, replay the
	// identical edit.
	setCollectorQualification(true)
	accepted, err := swap("ac43-accepted")
	if err != nil {
		t.Fatalf(
			"the identical edit was still refused once the plan revalidated, so the refusal was not the gate: %v",
			err,
		)
	}
	if core.Int(accepted["planVersion"]) != 2 {
		t.Fatalf("planVersion after the accepted edit = %v, want 2", accepted["planVersion"])
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if got := state.Assignments[transportID].WorkerID; got != secondCourier.ID {
			t.Fatalf("the accepted edit left the transport leg with %q", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
