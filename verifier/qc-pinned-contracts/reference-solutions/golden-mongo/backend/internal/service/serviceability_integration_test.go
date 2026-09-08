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

// networkFixture identifies the entities of a minimal serviceable network built
// through the real administration operations.
type networkFixture struct {
	nodeID       string
	laboratoryID string
	sampleTypeID string
	connectionID string
}

const (
	fixtureOriginLatitude  = 12.9716
	fixtureOriginLongitude = 77.5946
)

func buildServiceableNetwork(
	t *testing.T,
	service *Service,
	admin core.Principal,
	prefix string,
) networkFixture {
	t.Helper()
	ctx := context.Background()
	node, err := service.createLocation(ctx, admin, core.Map{
		"name": prefix + " node", "active": true,
		"coordinate": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"functions": []string{
			mongostore.FunctionCollectionPoint,
			mongostore.FunctionTransferPoint,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	laboratory, err := service.createLocation(ctx, admin, core.Map{
		"name": prefix + " laboratory", "active": true,
		"coordinate": core.Map{"latitude": 12.9816, "longitude": 77.6046},
		"functions":  []string{mongostore.FunctionLaboratory},
	})
	if err != nil {
		t.Fatal(err)
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
	fixture := networkFixture{
		nodeID:       core.String(node["locationId"]),
		laboratoryID: core.String(laboratory["locationId"]),
		sampleTypeID: core.String(sampleType["sampleTypeId"]),
	}
	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": fixture.laboratoryID,
		"sampleTypeId": fixture.sampleTypeID, "accepts": true,
	}); err != nil {
		t.Fatal(err)
	}
	connection, err := service.createConnection(ctx, admin, core.Map{
		"fromLocationId": fixture.nodeID, "toLocationId": fixture.laboratoryID,
		"travelMinutes": 30, "costUnits": 5.0, "active": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.connectionID = core.String(connection["connectionId"])
	for _, resourceID := range []string{
		fixture.nodeID, fixture.laboratoryID, fixture.connectionID,
	} {
		if _, err := service.configureCapacity(ctx, admin, core.Map{
			"resourceId": resourceID, "capacityUnits": 10, "cooledCapacityUnits": 10,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

// stateFingerprint is every durable counter a dry run could plausibly disturb.
// It is a comparable struct so a single equality check covers all of them.
type stateFingerprint struct {
	version         int64
	networkRevision int64
	requests        int
	samples         int
	journeys        int
	stops           int
	assignments     int
	reservations    int
	handoffs        int
	checkpoints     int
	idempotency     int
	auditEvents     int
	outboxEvents    int
	nextAuditID     int64
	nextOutboxID    int64
	capacities      string
	workerFairness  string
	serviceability  string
	draft           string
}

func describeCapacities(state *mongostore.State) string {
	lines := make([]string, 0, len(state.Capacities))
	for _, capacity := range state.Capacities {
		lines = append(lines, fmt.Sprintf(
			"%s=%d/%d/%d/%d",
			capacity.ResourceID, capacity.CapacityUnits, capacity.ReservedUnits,
			capacity.CooledUnits, capacity.CooledReserved,
		))
	}
	sort.Strings(lines)
	return fmt.Sprint(lines)
}

func describeWorkerFairness(state *mongostore.State) string {
	lines := make([]string, 0, len(state.Users))
	for _, user := range state.Users {
		offered, assigned := "never", "never"
		if user.LastOfferedAt != nil {
			offered = formatTime(*user.LastOfferedAt)
		}
		if user.LastAssignedAt != nil {
			assigned = formatTime(*user.LastAssignedAt)
		}
		lines = append(lines, fmt.Sprintf(
			"%s=%d/%s/%s", user.ID, user.AssignmentCount, offered, assigned,
		))
	}
	sort.Strings(lines)
	return fmt.Sprint(lines)
}

func describeCalculation(calculation *mongostore.ServiceabilityCalculation) string {
	if calculation == nil {
		return "none"
	}
	regions := make([]string, 0, len(calculation.Regions))
	for _, region := range calculation.Regions {
		regions = append(regions, fmt.Sprintf("%s@%v", region.LocationID, region.CoverageKM))
	}
	sort.Strings(regions)
	return fmt.Sprintf(
		"v%d/%s/r%d/%s/%v",
		calculation.Version, calculation.Status, calculation.NetworkRevision,
		formatTime(calculation.ComputedAt), regions,
	)
}

func captureStateFingerprint(t *testing.T, service *Service) stateFingerprint {
	t.Helper()
	var out stateFingerprint
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		out = stateFingerprint{
			version:         state.Version,
			networkRevision: state.NetworkRevision,
			requests:        len(state.Requests),
			samples:         len(state.Samples),
			journeys:        len(state.Journeys),
			stops:           len(state.Stops),
			assignments:     len(state.Assignments),
			reservations:    len(state.Reservations),
			handoffs:        len(state.Handoffs),
			checkpoints:     len(state.Checkpoints),
			idempotency:     len(state.Idempotency),
			auditEvents:     len(state.AuditEvents),
			outboxEvents:    len(state.OutboxEvents),
			nextAuditID:     state.NextAuditID,
			nextOutboxID:    state.NextOutboxID,
			capacities:      describeCapacities(state),
			workerFairness:  describeWorkerFairness(state),
			serviceability:  describeCalculation(state.Serviceability),
			draft:           describeCalculation(state.ServiceabilityDraft),
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func publishedCalculation(
	t *testing.T,
	service *Service,
) mongostore.ServiceabilityCalculation {
	t.Helper()
	var out mongostore.ServiceabilityCalculation
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		if state.Serviceability == nil {
			t.Fatal("no serviceability calculation has been published")
		}
		if state.ServiceabilityDraft != nil {
			t.Fatalf("a draft calculation was left behind: %+v", state.ServiceabilityDraft)
		}
		out = *state.Serviceability
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func networkRevisionOf(t *testing.T, service *Service) int64 {
	t.Helper()
	var revision int64
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		revision = state.NetworkRevision
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return revision
}

// Recording a laboratory exception is exactly what happens when a laboratory has
// gone down, so a deactivated laboratory must still accept an exception record —
// while planning to it, and normal receipt at it, must still be refused.
func TestLaboratoryExceptionCanBeRecordedAgainstADeactivatedLaboratory(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	operator := createTestUser(t, service, admin, "downed-lab-operator", []string{"laboratory_team"}, nil)
	courier := createTestUser(t, service, admin, "downed-lab-courier", []string{"delivery_partner"}, nil)
	requestedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	deliveredAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)

	if err := service.Store.Update(ctx, func(state *mongostore.State) error {
		laboratory := laboratoryLocation("lab")
		// The laboratory has been deactivated: this is the disruption that makes
		// an exception necessary in the first place.
		laboratory.Active = false
		state.Locations["lab"] = laboratory
		state.Acceptances["lab|type"] = &mongostore.Acceptance{
			ID: "acceptance", LaboratoryID: "lab", SampleTypeID: "type", Accepts: true,
		}
		state.Requests["request"] = &mongostore.CollectionRequest{
			ID: "request", RequestedAt: requestedAt,
			DeliveryDeadline: requestedAt.Add(4 * time.Hour),
		}
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", RequestID: "request", SampleTypeID: "type",
			Status: "delivered", ActiveJourneyID: "journey",
			TargetLaboratoryID: "lab", CurrentHolderID: courier.ID,
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", PrimarySampleID: "sample",
			SampleIDs: []string{"sample"}, Status: "active",
		}
		state.Assignments["final-leg"] = &mongostore.Assignment{
			ID: "final-leg", JourneyID: "journey", SampleID: "sample",
			Kind: "transport", LegSequence: 1, ToLocationID: "lab",
			WorkerID: courier.ID, Status: "delivered",
			AcceptedAt: pointerTime(requestedAt), CompletedAt: pointerTime(deliveredAt),
		}
		state.Assignments["lab-work"] = &mongostore.Assignment{
			ID: "lab-work", JourneyID: "journey", SampleID: "sample",
			Kind: "laboratory", ToLocationID: "lab", WorkerID: operator.ID,
			Status: "pending",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A deactivated laboratory still cannot take normal receipt of a sample.
	_, err := service.confirmReceipt(ctx, operator, core.Map{
		"sampleId": "sample", "laboratoryId": "lab",
		"receivedAt": "2026-07-26T10:05:00Z", "idempotencyKey": "receipt-at-downed-lab",
	})
	assertHTTPErrorCode(t, err, "laboratory_unavailable")

	result, err := service.recordLabException(ctx, operator, core.Map{
		"sampleId": "sample", "laboratoryId": "lab", "reason": "laboratory_offline",
		"observedAt": "2026-07-26T10:05:00Z", "idempotencyKey": "exception-at-downed-lab",
	})
	if err != nil {
		t.Fatalf("exception against a deactivated laboratory was refused: %v", err)
	}
	if result["status"] != "exception" || result["reason"] != "laboratory_offline" {
		t.Fatalf("exception result = %v", result)
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		sample := state.Samples["sample"]
		if sample.Status != "exception" || sample.ActiveJourneyID != "" {
			t.Fatalf("sample after exception = %+v", sample)
		}
		if core.String(sample.Exception["reason"]) != "laboratory_offline" {
			t.Fatalf("recorded exception = %v", sample.Exception)
		}
		if state.Journeys["journey"].Status != "cancelled" {
			t.Fatalf("journey status = %q", state.Journeys["journey"].Status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The dry run must answer the serviceability question without creating a
// request, sample, journey, assignment, stop or capacity reservation, without
// touching worker fairness counters, and without consuming an idempotency key.
func TestCheckServiceabilityIsAPureReadAndMutatesNoState(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "dry-run-requester", []string{"requester"}, nil)
	createTestUser(t, service, admin, "dry-run-collector", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "dry-run-courier", []string{"delivery_partner"}, nil)
	createTestUser(t, service, admin, "dry-run-operator", []string{"laboratory_team"}, nil)
	network := buildServiceableNetwork(t, service, admin, "dry-run")
	// Publish the calculation durably so the dry run answers from a published
	// complete version rather than an in-memory projection.
	if err := service.Maintain(ctx); err != nil {
		t.Fatal(err)
	}

	payload := core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": network.sampleTypeID,
		"requestedAt":          "2026-07-30T09:00:00Z",
		"deliveryDeadline":     "2026-07-30T12:00:00Z",
		"earliestCollectionAt": "2026-07-30T09:00:00Z",
		"latestCollectionAt":   "2026-07-30T10:00:00Z",
		"idempotencyKey":       "a-dry-run-must-not-consume-this-key",
	}

	before := captureStateFingerprint(t, service)
	verdict, err := service.checkServiceability(ctx, requester, payload)
	if err != nil {
		t.Fatalf("requester dry run: %v", err)
	}
	if !core.Bool(verdict["serviceable"]) ||
		core.String(verdict["reason"]) != serviceableReason {
		t.Fatalf("dry-run verdict = %v", verdict)
	}
	if core.String(verdict["targetLaboratoryId"]) != network.laboratoryID {
		t.Fatalf("dry-run target laboratory = %v", verdict["targetLaboratoryId"])
	}
	if core.String(verdict["estimatedArrivalAt"]) != "2026-07-30T09:30:00Z" {
		t.Fatalf("dry-run estimated arrival = %v", verdict["estimatedArrivalAt"])
	}
	published := publishedCalculation(t, service)
	if int64(core.Float(verdict["serviceabilityVersion"])) != published.Version {
		t.Fatalf(
			"dry-run serviceabilityVersion = %v, want the published %d",
			verdict["serviceabilityVersion"], published.Version,
		)
	}
	// Repeat with the same idempotency key: a pure read never consumes one.
	if _, err := service.checkServiceability(ctx, requester, payload); err != nil {
		t.Fatalf("repeated dry run: %v", err)
	}
	after := captureStateFingerprint(t, service)
	if before != after {
		t.Fatalf("the dry run mutated state:\nbefore=%+v\nafter =%+v", before, after)
	}

	// The real request must reach the same verdict, because both go through the
	// same decision.
	created, err := service.createCollectionRequest(ctx, requester, core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": network.sampleTypeID,
		"requestedAt":          "2026-07-30T09:00:00Z",
		"deliveryDeadline":     "2026-07-30T12:00:00Z",
		"earliestCollectionAt": "2026-07-30T09:00:00Z",
		"latestCollectionAt":   "2026-07-30T10:00:00Z",
		"containerType":        "amber-container",
		"temperatureMinC":      2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{},
		"idempotencyKey":          "dry-run-then-real-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created["serviceability"] != "serviceable" ||
		core.String(created["targetLaboratoryId"]) != network.laboratoryID {
		t.Fatalf("real request disagreed with the dry run: %v", created)
	}
}

// Every worker being unavailable must not change the dry-run verdict.
func TestCheckServiceabilityIgnoresWorkerAvailability(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "availability-requester", []string{"requester"}, nil)
	collector := createTestUser(t, service, admin, "availability-collector", []string{"collection_professional"}, []string{"LX-A"})
	courier := createTestUser(t, service, admin, "availability-courier", []string{"delivery_partner"}, nil)
	network := buildServiceableNetwork(t, service, admin, "availability")

	payload := core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": network.sampleTypeID,
		"requestedAt":      "2026-07-30T09:00:00Z",
		"deliveryDeadline": "2026-07-30T12:00:00Z",
	}
	staffed, err := service.checkServiceability(ctx, requester, payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, worker := range []core.Principal{collector, courier} {
		if _, err := service.setWorkerAvailability(ctx, admin, core.Map{
			"workerId": worker.ID, "available": false, "capacityUnits": 0,
		}); err != nil {
			t.Fatal(err)
		}
	}
	unstaffed, err := service.checkServiceability(ctx, requester, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !core.Bool(unstaffed["serviceable"]) {
		t.Fatalf("verdict with no available worker = %v, want serviceable", unstaffed)
	}
	for _, field := range []string{"serviceable", "reason", "targetLaboratoryId", "estimatedArrivalAt"} {
		if core.String(staffed[field]) != core.String(unstaffed[field]) {
			t.Fatalf(
				"%s changed with worker availability: %v -> %v",
				field, staffed[field], unstaffed[field],
			)
		}
	}
}

// The collection window and the delivery deadline are separate constraints: a
// collection after latestCollectionAt but still before deliveryDeadline is
// rejected, and with its own error code.
func TestCollectionWindowIsEnforcedSeparatelyFromTheDeliveryDeadline(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	collector := createTestUser(
		t, service, admin, "window-collector",
		[]string{"collection_professional"}, []string{"LX-A"},
	)
	requestedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)

	if err := service.Store.Update(ctx, func(state *mongostore.State) error {
		state.Requests["request"] = &mongostore.CollectionRequest{
			ID: "request", RequestedAt: requestedAt,
			EarliestCollectionAt: requestedAt.Add(30 * time.Minute),
			LatestCollectionAt:   requestedAt.Add(time.Hour),
			DeliveryDeadline:     requestedAt.Add(3 * time.Hour),
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", PrimarySampleID: "late",
			SampleIDs: []string{"late", "early", "inside"}, Status: "active",
		}
		for _, sampleID := range []string{"late", "early", "inside"} {
			state.Samples[sampleID] = &mongostore.Sample{
				ID: sampleID, RequestID: "request", Status: "planned",
				ActiveJourneyID: "journey", ContainerType: "amber-container",
				TemperatureMinC: 2, TemperatureMaxC: 8,
			}
			state.Assignments[sampleID+"-collection"] = &mongostore.Assignment{
				ID: sampleID + "-collection", JourneyID: "journey", SampleID: sampleID,
				Kind: "collection", LegSequence: 0, WorkerID: collector.ID,
				Status: "accepted", AcceptedAt: pointerTime(requestedAt),
				CreatedAt: requestedAt,
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	seal := func(sampleID, collectedAt, key string) error {
		_, err := service.collectAndSeal(ctx, collector, core.Map{
			"sampleId": sampleID, "assignmentId": sampleID + "-collection",
			"containerType": "amber-container", "observedTemperatureC": 5.0,
			"collectedAt": collectedAt, "idempotencyKey": key,
		})
		return err
	}

	// After the delivery deadline: still the deadline error, so the two
	// constraints stay separable.
	assertHTTPErrorCode(
		t,
		seal("late", "2026-07-26T12:30:00Z", "after-deadline"),
		"invalid_collection_time",
	)
	// AC-10: after latestCollectionAt (10:00) but well before the deadline
	// (12:00) — rejected for the window, not the deadline.
	assertHTTPErrorCode(
		t,
		seal("late", "2026-07-26T10:30:00Z", "after-window-before-deadline"),
		"outside_collection_window",
	)
	// Before the window opens (09:30) but after the request was made.
	assertHTTPErrorCode(
		t,
		seal("early", "2026-07-26T09:15:00Z", "before-window-opens"),
		"outside_collection_window",
	)
	// AC-11: inside the window is accepted.
	if err := seal("inside", "2026-07-26T09:45:00Z", "inside-window"); err != nil {
		t.Fatalf("collection inside the window was refused: %v", err)
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if state.Samples["inside"].Status != "sealed" {
			t.Fatalf("in-window sample = %+v", state.Samples["inside"])
		}
		for _, sampleID := range []string{"late", "early"} {
			if state.Samples[sampleID].Status != "planned" ||
				state.Samples[sampleID].SealedAt != nil {
				t.Fatalf("rejected collection mutated %s: %+v", sampleID, state.Samples[sampleID])
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The request records and echoes the window, defaults it when absent, and
// refuses a window that cannot be satisfied.
func TestCreateCollectionRequestRecordsAndEchoesTheCollectionWindow(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "window-requester", []string{"requester"}, nil)
	createTestUser(t, service, admin, "window-request-collector", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "window-request-courier", []string{"delivery_partner"}, nil)
	createTestUser(t, service, admin, "window-request-operator", []string{"laboratory_team"}, nil)
	network := buildServiceableNetwork(t, service, admin, "window-request")

	base := func() core.Map {
		return core.Map{
			"origin": core.Map{
				"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
			},
			"originType": "home", "sampleTypeId": network.sampleTypeID,
			"requestedAt":     "2026-07-30T09:00:00Z",
			"containerType":   "amber-container",
			"temperatureMinC": 2.0, "temperatureMaxC": 8.0,
			"incompatibleSampleTypes": []string{},
			"deliveryDeadline":        "2026-07-30T12:00:00Z",
		}
	}

	withWindow := base()
	withWindow["earliestCollectionAt"] = "2026-07-30T09:30:00Z"
	withWindow["latestCollectionAt"] = "2026-07-30T10:00:00Z"
	withWindow["idempotencyKey"] = "request-with-window"
	created, err := service.createCollectionRequest(ctx, requester, withWindow)
	if err != nil {
		t.Fatal(err)
	}
	if created["earliestCollectionAt"] != "2026-07-30T09:30:00Z" ||
		created["latestCollectionAt"] != "2026-07-30T10:00:00Z" {
		t.Fatalf("window was not echoed: %v", created)
	}
	if created["serviceability"] != "serviceable" {
		t.Fatalf("in-window request = %v", created)
	}

	defaults := base()
	defaults["idempotencyKey"] = "request-without-window"
	fallback, err := service.createCollectionRequest(ctx, requester, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if fallback["earliestCollectionAt"] != "2026-07-30T09:00:00Z" ||
		fallback["latestCollectionAt"] != "2026-07-30T12:00:00Z" {
		t.Fatalf("absent window did not default to [requestedAt, deadline]: %v", fallback)
	}

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		request := state.Requests[core.String(created["requestId"])]
		if formatTime(request.EarliestCollectionAt) != "2026-07-30T09:30:00Z" ||
			formatTime(request.LatestCollectionAt) != "2026-07-30T10:00:00Z" {
			t.Fatalf("stored window = %+v", request)
		}
		// The journey's collection step is not planned before the window opens.
		stops := stopsForJourney(state, core.String(created["journeyId"]))
		if len(stops) == 0 || stops[0].StopType != "pickup" {
			t.Fatalf("planned stops = %+v", stops)
		}
		if stops[0].EstimatedAt.Before(request.EarliestCollectionAt) {
			t.Fatalf(
				"pickup planned at %s, before the collection window opened at %s",
				formatTime(stops[0].EstimatedAt), formatTime(request.EarliestCollectionAt),
			)
		}
		if !stops[0].EstimatedAt.Equal(request.EarliestCollectionAt) {
			t.Fatalf(
				"pickup planned at %s, want the window opening %s",
				formatTime(stops[0].EstimatedAt), formatTime(request.EarliestCollectionAt),
			)
		}
		journey := state.Journeys[core.String(created["journeyId"])]
		if journey.EstimatedDeliveryAt.After(request.DeliveryDeadline) {
			t.Fatalf(
				"window-shifted plan misses the deadline: eta=%s deadline=%s",
				formatTime(journey.EstimatedDeliveryAt), formatTime(request.DeliveryDeadline),
			)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	invalid := base()
	invalid["earliestCollectionAt"] = "2026-07-30T09:30:00Z"
	invalid["latestCollectionAt"] = "2026-07-30T12:30:00Z"
	invalid["idempotencyKey"] = "request-with-window-past-deadline"
	_, err = service.createCollectionRequest(ctx, requester, invalid)
	assertHTTPErrorStatus(t, err, 400)
	assertHTTPErrorCode(t, err, "invalid_collection_window")

	// AC-12: a valid window that opens too late for the route to beat the
	// deadline makes the request unserviceable, with a stated reason.
	unreachable := base()
	unreachable["deliveryDeadline"] = "2026-07-30T10:00:00Z"
	unreachable["earliestCollectionAt"] = "2026-07-30T09:45:00Z"
	unreachable["latestCollectionAt"] = "2026-07-30T09:55:00Z"
	unreachable["idempotencyKey"] = "request-with-unreachable-window"
	late, err := service.createCollectionRequest(ctx, requester, unreachable)
	if err != nil {
		t.Fatal(err)
	}
	if late["serviceability"] != "unserviceable" ||
		late["serviceabilityReason"] != reasonDeadlineUnreachable {
		t.Fatalf("late-window request = %v", late)
	}
}

// Every structural and policy change bumps the network revision, and the
// maintenance pass republishes the calculation one whole version at a time.
func TestStructuralChangesBumpNetworkRevisionAndRepublishServiceability(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	dispatcher := createTestUser(t, service, admin, "revision-dispatcher", []string{"dispatcher"}, nil)
	requester := createTestUser(t, service, admin, "revision-requester", []string{"requester"}, nil)

	revision := networkRevisionOf(t, service)
	assertBumped := func(label string) {
		t.Helper()
		next := networkRevisionOf(t, service)
		if next <= revision {
			t.Fatalf("%s did not bump the network revision: %d -> %d", label, revision, next)
		}
		revision = next
	}

	node, err := service.createLocation(ctx, admin, core.Map{
		"name": "Revision node", "active": true,
		"coordinate": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"functions": []string{
			mongostore.FunctionCollectionPoint, mongostore.FunctionTransferPoint,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertBumped("create_location")
	nodeID := core.String(node["locationId"])

	laboratory, err := service.createLocation(ctx, admin, core.Map{
		"name": "Revision laboratory", "active": true,
		"coordinate": core.Map{"latitude": 12.9816, "longitude": 77.6046},
		"functions":  []string{mongostore.FunctionLaboratory},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertBumped("create_location for the laboratory")
	laboratoryID := core.String(laboratory["locationId"])

	if _, err := service.updateLocation(ctx, admin, core.Map{
		"locationId": nodeID, "name": "Revision node renamed",
		"coordinate": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
	}); err != nil {
		t.Fatal(err)
	}
	assertBumped("update_location")

	if _, err := service.setActive(ctx, admin, "location", nodeID, true); err != nil {
		t.Fatal(err)
	}
	assertBumped("set_location_status")

	if _, err := service.setLocationFunctionStatus(ctx, admin, core.Map{
		"locationId": nodeID, "function": mongostore.FunctionTransferPoint, "active": false,
	}); err != nil {
		t.Fatal(err)
	}
	assertBumped("set_location_function_status")

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
	if _, err := service.configureAcceptance(ctx, admin, core.Map{
		"laboratoryId": laboratoryID,
		"sampleTypeId": core.String(sampleType["sampleTypeId"]), "accepts": true,
	}); err != nil {
		t.Fatal(err)
	}
	assertBumped("configure_laboratory_acceptance")

	connection, err := service.createConnection(ctx, admin, core.Map{
		"fromLocationId": nodeID, "toLocationId": laboratoryID,
		"travelMinutes": 30, "costUnits": 5.0, "active": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertBumped("create_connection")

	if _, err := service.setActive(
		ctx, admin, "connection", core.String(connection["connectionId"]), true,
	); err != nil {
		t.Fatal(err)
	}
	assertBumped("set_connection_status")

	if _, err := service.configureCapacity(ctx, admin, core.Map{
		"resourceId": nodeID, "capacityUnits": 10, "cooledCapacityUnits": 4,
	}); err != nil {
		t.Fatal(err)
	}
	assertBumped("configure_capacity")

	if err := service.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	first := publishedCalculation(t, service)
	if first.Version != 1 || first.Status != mongostore.ServiceabilityComplete {
		t.Fatalf("first published calculation = %+v", first)
	}
	if first.NetworkRevision != revision {
		t.Fatalf(
			"published revision = %d, current network revision = %d",
			first.NetworkRevision, revision,
		)
	}
	if len(first.Regions) != 1 || first.Regions[0].LocationID != nodeID {
		t.Fatalf("published regions = %+v, want the single collection point", first.Regions)
	}

	// No structural change: the maintenance pass must not republish, and must
	// not write at all.
	stable := captureStateFingerprint(t, service)
	if err := service.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if again := captureStateFingerprint(t, service); again != stable {
		t.Fatalf("an idle maintenance pass republished:\nbefore=%+v\nafter =%+v", stable, again)
	}

	// One more structural change publishes exactly one new version.
	if _, err := service.setActive(ctx, admin, "location", laboratoryID, true); err != nil {
		t.Fatal(err)
	}
	if err := service.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	second := publishedCalculation(t, service)
	if second.Version != first.Version+1 {
		t.Fatalf("republished version = %d, want %d", second.Version, first.Version+1)
	}
	if second.Status != mongostore.ServiceabilityComplete {
		t.Fatalf("republished status = %q", second.Status)
	}

	// Administrators and dispatchers can read the published snapshot; a
	// requester cannot.
	for _, reader := range []core.Principal{admin, dispatcher} {
		snapshot, err := service.readServiceabilitySnapshot(ctx, reader)
		if err != nil {
			t.Fatalf("snapshot read: %v", err)
		}
		if int64(core.Float(snapshot["version"])) != second.Version ||
			snapshot["status"] != mongostore.ServiceabilityComplete ||
			core.Int(snapshot["activeCollectionPointCount"]) != 1 {
			t.Fatalf("snapshot = %v", snapshot)
		}
		regions := snapshot["regions"].([]core.Map)
		if len(regions) != 1 ||
			core.String(regions[0]["locationId"]) != nodeID ||
			core.Float(regions[0]["coverageKm"]) != 5 {
			t.Fatalf("snapshot regions = %v", regions)
		}
	}
	_, err = service.readServiceabilitySnapshot(ctx, requester)
	assertHTTPErrorStatus(t, err, 403)
}
