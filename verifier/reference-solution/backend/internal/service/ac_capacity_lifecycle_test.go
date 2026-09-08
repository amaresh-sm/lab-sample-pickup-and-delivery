package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// reservedLevel is the pair of reserved numbers one capacity pool reports.
type reservedLevel struct{ regular, cooled int }

func (r reservedLevel) String() string {
	return fmt.Sprintf("regular=%d cooled=%d", r.regular, r.cooled)
}

// readReservedLevels reads the reserved numbers of several resources from ONE
// read_operations_state call, so a capacity probe measures the pools the way an
// operator does rather than by reaching into the store. Every reading is checked
// against the two invariants that must hold whatever else is going on: a pool is
// never reserved beyond what it was configured with, and the available number an
// operator plans against is always configured minus reserved.
func readReservedLevels(
	t *testing.T,
	service *Service,
	reader core.Principal,
	resourceIDs ...string,
) map[string]reservedLevel {
	t.Helper()
	operations, err := service.Handle(
		context.Background(), "read_operations_state", core.Map{}, reader,
	)
	if err != nil {
		t.Fatalf("read operations state: %v", err)
	}
	out := map[string]reservedLevel{}
	for _, resourceID := range resourceIDs {
		capacity := operationsCapacityFor(operations, resourceID)
		if capacity == nil {
			t.Fatalf("no operation reports capacity for resource %s", resourceID)
		}
		level := reservedLevel{
			regular: core.Int(capacity["reservedUnits"]),
			cooled:  core.Int(capacity["cooledReservedUnits"]),
		}
		configured := core.Int(capacity["capacityUnits"])
		cooledConfigured := core.Int(capacity["cooledCapacityUnits"])
		if level.regular > configured || level.cooled > cooledConfigured {
			t.Fatalf(
				"pool %s is overbooked: %v against %d regular / %d cooled configured units",
				resourceID, level, configured, cooledConfigured,
			)
		}
		if got := core.Int(capacity["availableUnits"]); got != configured-level.regular {
			t.Fatalf(
				"pool %s availableUnits = %d, want %d: %v",
				resourceID, got, configured-level.regular, capacity,
			)
		}
		if got := core.Int(capacity["cooledAvailableUnits"]); got != cooledConfigured-level.cooled {
			t.Fatalf(
				"pool %s cooledAvailableUnits = %d, want %d: %v",
				resourceID, got, cooledConfigured-level.cooled, capacity,
			)
		}
		out[resourceID] = level
	}
	return out
}

// reservationRecord is one journey's durable claim on one resource.
type reservationRecord struct {
	units  int
	cooled bool
}

func (r reservationRecord) String() string {
	return fmt.Sprintf("units=%d cooled=%t", r.units, r.cooled)
}

// activeJourneyReservations is every claim a journey currently holds, keyed by
// resource. A resource claimed twice by the same journey is itself a fault: the
// release paths walk reservations once, so a duplicate would be released once and
// leak the rest.
func activeJourneyReservations(
	t *testing.T,
	service *Service,
	journeyID string,
) map[string]reservationRecord {
	t.Helper()
	out := map[string]reservationRecord{}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		for _, reservation := range state.Reservations {
			if reservation.JourneyID != journeyID || reservation.Status != "active" {
				continue
			}
			if existing, duplicate := out[reservation.ResourceID]; duplicate {
				t.Fatalf(
					"journey %s holds two active reservations for resource %s (%v and %v)",
					journeyID, reservation.ResourceID, existing,
					reservationRecord{units: reservation.Units, cooled: reservation.Cooled},
				)
			}
			out[reservation.ResourceID] = reservationRecord{
				units: reservation.Units, cooled: reservation.Cooled,
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func describeReservations(reservations map[string]reservationRecord) string {
	lines := make([]string, 0, len(reservations))
	for resourceID, record := range reservations {
		lines = append(lines, resourceID+"="+record.String())
	}
	sort.Strings(lines)
	return fmt.Sprint(lines)
}

// capacityWorld is a minimal serviceable network plus one principal per role, so
// a capacity probe can plan journeys and drive them to any terminal state.
type capacityWorld struct {
	network    networkFixture
	requester  core.Principal
	dispatcher core.Principal
	collector  core.Principal
	courier    core.Principal
	operator   core.Principal
	// resources is every capacity pool the planned route touches: the collection
	// point, the connection travelled over, and the destination laboratory.
	resources []string
}

func newCapacityWorld(
	t *testing.T,
	service *Service,
	admin core.Principal,
	prefix string,
) capacityWorld {
	t.Helper()
	network := buildServiceableNetwork(t, service, admin, prefix)
	world := capacityWorld{
		network:    network,
		requester:  createTestUser(t, service, admin, prefix+"-requester", []string{"requester"}, nil),
		dispatcher: createTestUser(t, service, admin, prefix+"-dispatcher", []string{"dispatcher"}, nil),
		collector:  createTestUser(t, service, admin, prefix+"-collector", []string{"collection_professional"}, []string{"LX-A"}),
		courier:    createTestUser(t, service, admin, prefix+"-courier", []string{"delivery_partner"}, nil),
		operator:   createTestUserWithLocation(t, service, admin, prefix+"-operator", []string{"laboratory_team"}, nil, network.laboratoryID),
	}
	world.resources = []string{
		world.network.nodeID, world.network.connectionID, world.network.laboratoryID,
	}
	return world
}

// planJourney raises one serviceable collection request and returns its sample
// and journey.
func (w capacityWorld) planJourney(t *testing.T, service *Service, key string) (string, string) {
	t.Helper()
	created, err := service.Handle(
		context.Background(), "create_collection_request",
		standardRequest(w.network, key), w.requester,
	)
	if err != nil {
		t.Fatalf("create collection request %s: %v", key, err)
	}
	if core.String(created["serviceability"]) != "serviceable" {
		t.Fatalf("request %s was not planned: %v", key, created)
	}
	return core.String(created["sampleId"]), core.String(created["journeyId"])
}

// driveToDelivered runs a planned journey through acceptance, sealing, a
// confirmed custody handoff and the whole transport progression, leaving the
// sample delivered at its laboratory and ready for either receipt or a
// laboratory exception.
func (w capacityWorld) driveToDelivered(
	t *testing.T,
	service *Service,
	prefix, sampleID, journeyID string,
) {
	t.Helper()
	ctx := context.Background()
	plan, err := service.readJourneyPlan(ctx, w.dispatcher, journeyID)
	if err != nil {
		t.Fatalf("read journey plan: %v", err)
	}
	collectionID := core.String(plan["collectionAssignmentId"])
	transportID := core.String(plan["firstTransportAssignmentId"])
	if collectionID == "" || transportID == "" {
		t.Fatalf("plan is missing its collection or transport work: %v", plan)
	}
	if _, err := service.acceptAssignment(ctx, w.collector, core.Map{
		"assignmentId": collectionID, "idempotencyKey": prefix + "-accept-collection",
	}); err != nil {
		t.Fatalf("accept collection: %v", err)
	}
	if _, err := service.collectAndSeal(ctx, w.collector, core.Map{
		"sampleId": sampleID, "assignmentId": collectionID,
		"containerType": "amber-container", "observedTemperatureC": 5.0,
		"collectedAt": "2026-07-30T09:05:00Z", "idempotencyKey": prefix + "-seal",
	}); err != nil {
		t.Fatalf("collect and seal: %v", err)
	}
	if _, err := service.acceptAssignment(ctx, w.courier, core.Map{
		"assignmentId": transportID, "idempotencyKey": prefix + "-accept-transport",
	}); err != nil {
		t.Fatalf("accept transport: %v", err)
	}
	handoff, err := service.initiateHandoff(ctx, w.collector, core.Map{
		"sampleId": sampleID, "receiverId": w.courier.ID,
		"fromAssignmentId": collectionID, "toAssignmentId": transportID,
		"initiatedAt": "2026-07-30T09:06:00Z", "idempotencyKey": prefix + "-handoff",
	})
	if err != nil {
		t.Fatalf("initiate handoff: %v", err)
	}
	handoffID := core.String(handoff["handoffId"])
	for _, confirmation := range []struct {
		p    core.Principal
		side string
		at   string
	}{
		{w.collector, "sender", "2026-07-30T09:07:00Z"},
		{w.courier, "receiver", "2026-07-30T09:08:00Z"},
	} {
		if _, err := service.confirmHandoff(ctx, confirmation.p, core.Map{
			"handoffId": handoffID, "confirmationSide": confirmation.side,
			"confirmedAt":    confirmation.at,
			"idempotencyKey": prefix + "-confirm-" + confirmation.side,
		}); err != nil {
			t.Fatalf("confirm handoff (%s): %v", confirmation.side, err)
		}
	}
	for index, update := range []struct{ status, at string }{
		{"at_pickup", "2026-07-30T09:10:00Z"},
		{"picked_up", "2026-07-30T09:11:00Z"},
		{"in_progress", "2026-07-30T09:12:00Z"},
		{"arrived", "2026-07-30T09:35:00Z"},
		{"delivered", "2026-07-30T09:36:00Z"},
	} {
		if _, err := service.updateProgress(ctx, w.courier, core.Map{
			"assignmentId": transportID, "status": update.status, "occurredAt": update.at,
			"idempotencyKey": prefix + "-progress-" + core.String(index),
		}); err != nil {
			t.Fatalf("progress %s: %v", update.status, err)
		}
	}
}

// AC-47: creating a journey reserves capacity — and it reserves it at every kind
// of resource the route uses, which is what makes the reservation meaningful: the
// collection point it starts at, the connection it travels over, and the
// destination laboratory. One reservation record must exist per reserved
// resource, so a release can find and undo every one of them.
func TestAC47CreatingAJourneyReservesCapacityAtEveryRouteResource(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	world := newCapacityWorld(t, service, admin, "ac47")

	before := readReservedLevels(t, service, world.dispatcher, world.resources...)
	for _, resourceID := range world.resources {
		if before[resourceID] != (reservedLevel{}) {
			t.Fatalf(
				"resource %s already held reservations before anything was planned: %v",
				resourceID, before[resourceID],
			)
		}
	}

	_, journeyID := world.planJourney(t, service, "ac47-request")

	after := readReservedLevels(t, service, world.dispatcher, world.resources...)
	for _, resourceID := range world.resources {
		if after[resourceID].regular != before[resourceID].regular+1 {
			t.Fatalf(
				"reserved units did not rise for %s when the journey was created: before %v, after %v",
				resourceID, before[resourceID], after[resourceID],
			)
		}
		// Amber needs no continuous cooling, so planning it must leave the cooled
		// dimension alone: the two dimensions are independent (AC-45).
		if after[resourceID].cooled != 0 {
			t.Fatalf(
				"planning an uncooled sample reserved cooled units at %s: %v",
				resourceID, after[resourceID],
			)
		}
	}

	reservations := activeJourneyReservations(t, service, journeyID)
	if len(reservations) != len(world.resources) {
		t.Fatalf(
			"journey holds %d reservations (%s), want exactly one per route resource %v",
			len(reservations), describeReservations(reservations), world.resources,
		)
	}
	for _, resourceID := range world.resources {
		record, present := reservations[resourceID]
		if !present {
			t.Fatalf(
				"the journey reserved nothing against %s: %s",
				resourceID, describeReservations(reservations),
			)
		}
		if record.units != 1 || record.cooled {
			t.Fatalf("reservation for %s = %v, want one uncooled unit", resourceID, record)
		}
	}
}

// releaseVerdict names which way a release went wrong, so a failure says whether
// capacity leaked or was released twice.
func releaseVerdict(before, after int) string {
	switch {
	case after > before:
		return "reserved units are HIGHER than before planning, so the terminal path leaked capacity"
	case after < before:
		return "reserved units are LOWER than before planning, so the release ran more than once"
	default:
		return "reserved units match"
	}
}

// AC-48: capacity is released when it is no longer required, and released
// exactly once. Reserved units must come back to precisely the level they were
// at before the journey was planned, on every terminal path: laboratory receipt,
// journey cancellation, and a laboratory exception.
//
// Both release paths clamp with maxInt(0, ...) — internal/service/plan_changes.go
// releaseReservations and internal/service/maintenance.go releaseJourneyUnits —
// so a pool that started empty cannot reveal a double release: subtracting two
// units from a pool holding one still reads zero. Every path here therefore runs
// against a pool that ALREADY carries an unrelated journey's reservation, so a
// leak reads high and a double release reads low, and neither can hide behind the
// clamp. The unrelated journey's own claims are checked afterwards too.
func TestAC48ReservedCapacityReturnsToItsPriorLevelOnEveryTerminalPath(t *testing.T) {
	paths := []struct {
		name   string
		finish func(t *testing.T, service *Service, world capacityWorld, sampleID, journeyID string)
	}{
		{
			name: "confirm_laboratory_receipt",
			finish: func(t *testing.T, service *Service, world capacityWorld, sampleID, journeyID string) {
				t.Helper()
				world.driveToDelivered(t, service, "ac48-receipt", sampleID, journeyID)
				if _, err := service.confirmReceipt(context.Background(), world.operator, core.Map{
					"sampleId": sampleID, "laboratoryId": world.network.laboratoryID,
					"receivedAt": "2026-07-30T09:37:00Z", "idempotencyKey": "ac48-receipt",
				}); err != nil {
					t.Fatalf("confirm laboratory receipt: %v", err)
				}
			},
		},
		{
			name: "cancel_journey",
			finish: func(t *testing.T, service *Service, world capacityWorld, sampleID, journeyID string) {
				t.Helper()
				if _, err := service.Handle(context.Background(), "cancel_journey", core.Map{
					"journeyId": journeyID, "expectedPlanVersion": 1,
					"reason": "ac48 cancellation", "idempotencyKey": "ac48-cancel",
				}, world.dispatcher); err != nil {
					t.Fatalf("cancel journey: %v", err)
				}
			},
		},
		{
			name: "record_laboratory_exception",
			finish: func(t *testing.T, service *Service, world capacityWorld, sampleID, journeyID string) {
				t.Helper()
				world.driveToDelivered(t, service, "ac48-exception", sampleID, journeyID)
				if _, err := service.recordLabException(context.Background(), world.operator, core.Map{
					"sampleId": sampleID, "laboratoryId": world.network.laboratoryID,
					"reason": "ac48 excursion", "observedAt": "2026-07-30T09:37:00Z",
					"idempotencyKey": "ac48-exception",
				}); err != nil {
					t.Fatalf("record laboratory exception: %v", err)
				}
			},
		},
	}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
			service, admin := integrationService(t)
			world := newCapacityWorld(t, service, admin, "ac48")

			// An unrelated journey holds one unit in every pool for the whole probe.
			_, backgroundID := world.planJourney(t, service, "ac48-unrelated")
			backgroundClaims := activeJourneyReservations(t, service, backgroundID)
			if len(backgroundClaims) != len(world.resources) {
				t.Fatalf(
					"the unrelated journey did not claim every pool: %s",
					describeReservations(backgroundClaims),
				)
			}

			before := readReservedLevels(t, service, world.dispatcher, world.resources...)
			for _, resourceID := range world.resources {
				if before[resourceID].regular != 1 {
					t.Fatalf(
						"pool %s does not carry the unrelated reservation the clamp check needs: %v",
						resourceID, before[resourceID],
					)
				}
			}

			sampleID, journeyID := world.planJourney(t, service, "ac48-probe")
			planned := readReservedLevels(t, service, world.dispatcher, world.resources...)
			for _, resourceID := range world.resources {
				if planned[resourceID].regular != before[resourceID].regular+1 {
					t.Fatalf(
						"planning the probe journey did not reserve %s: before %v, planned %v",
						resourceID, before[resourceID], planned[resourceID],
					)
				}
			}

			path.finish(t, service, world, sampleID, journeyID)

			after := readReservedLevels(t, service, world.dispatcher, world.resources...)
			for _, resourceID := range world.resources {
				if after[resourceID] != before[resourceID] {
					t.Fatalf(
						"%s: pool %s did not return to its pre-planning level: before %v, after %v — %s",
						path.name, resourceID, before[resourceID], after[resourceID],
						releaseVerdict(before[resourceID].regular, after[resourceID].regular),
					)
				}
			}
			if remaining := activeJourneyReservations(t, service, journeyID); len(remaining) != 0 {
				t.Fatalf(
					"%s left %d active reservation(s) on the finished journey: %s",
					path.name, len(remaining), describeReservations(remaining),
				)
			}
			if got := activeJourneyReservations(t, service, backgroundID); describeReservations(got) !=
				describeReservations(backgroundClaims) {
				t.Fatalf(
					"%s disturbed an unrelated journey's reservations: before %s, after %s",
					path.name, describeReservations(backgroundClaims), describeReservations(got),
				)
			}
		})
	}
}

// AC-49: a capacity pool is never overbooked. Journey creations are fired
// simultaneously at a network whose pools have room for fewer of them than there
// are requests. Exactly the affordable number may be planned, every refusal must
// name capacity as the reason, and no pool may report more reserved units than it
// was configured with at any point.
func TestAC49ConcurrentJourneyCreationNeverOverbooksAPool(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	world := newCapacityWorld(t, service, admin, "ac49")

	const affordable = 2
	const attempts = 5
	for _, resourceID := range world.resources {
		if _, err := service.configureCapacity(ctx, admin, core.Map{
			"resourceId": resourceID, "capacityUnits": affordable,
			"cooledCapacityUnits": affordable,
		}); err != nil {
			t.Fatalf("configure capacity for %s: %v", resourceID, err)
		}
	}

	type outcome struct {
		journeyID      string
		serviceability string
		reason         string
		err            error
	}
	results := make([]outcome, attempts)
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			created, err := service.Handle(
				ctx, "create_collection_request",
				standardRequest(world.network, fmt.Sprintf("ac49-concurrent-%d", index)),
				world.requester,
			)
			results[index] = outcome{
				journeyID:      core.String(created["journeyId"]),
				serviceability: core.String(created["serviceability"]),
				reason:         core.String(created["serviceabilityReason"]),
				err:            err,
			}
		}(index)
	}
	wait.Wait()

	planned, refused := []string{}, []string{}
	for index, result := range results {
		switch {
		case result.err != nil:
			var apiErr *core.HTTPError
			if !errors.As(result.err, &apiErr) {
				t.Fatalf("attempt %d failed with a non-API error: %v", index, result.err)
			}
			switch apiErr.Code {
			case "capacity_exhausted", "cooled_capacity_exhausted", "concurrent_conflict":
				refused = append(refused, fmt.Sprintf("%d:%s", index, apiErr.Code))
			default:
				t.Fatalf(
					"attempt %d was refused for a reason unrelated to capacity (%s): %v",
					index, apiErr.Code, result.err,
				)
			}
		case result.serviceability == "serviceable":
			planned = append(planned, result.journeyID)
		default:
			if result.reason != reasonInsufficientCapacity {
				t.Fatalf(
					"attempt %d was declared unserviceable for %q, not because the pool was full",
					index, result.reason,
				)
			}
			refused = append(refused, fmt.Sprintf("%d:%s", index, result.reason))
		}
	}
	if len(planned) != affordable {
		t.Fatalf(
			"%d of %d simultaneous journeys were planned against pools of %d units, want exactly %d; planned=%v refused=%v",
			len(planned), attempts, affordable, affordable, planned, refused,
		)
	}

	// readReservedLevels itself refuses any pool reserved beyond its configuration,
	// so this both reports the final level and asserts the pool was never
	// overbooked.
	final := readReservedLevels(t, service, world.dispatcher, world.resources...)
	for _, resourceID := range world.resources {
		if final[resourceID].regular != affordable {
			t.Fatalf(
				"pool %s ended at %v after %d planned journeys, want exactly %d reserved units",
				resourceID, final[resourceID], len(planned), affordable,
			)
		}
	}

	// The pools' reserved numbers must be the sum of the surviving journeys' own
	// claims, so the total is accounted for rather than merely plausible.
	totals := map[string]int{}
	for _, journeyID := range planned {
		for resourceID, record := range activeJourneyReservations(t, service, journeyID) {
			totals[resourceID] += record.units
		}
	}
	for _, resourceID := range world.resources {
		if totals[resourceID] != final[resourceID].regular {
			t.Fatalf(
				"pool %s reports %v reserved but the planned journeys account for %d units",
				resourceID, final[resourceID], totals[resourceID],
			)
		}
	}
}

// AC-17: a Frost journey does not merely require a route that is capable of
// cooling — it must actually RESERVE cooled capacity at every leg and at every
// location it passes through. The fixture is deliberately two-legged, so "every
// leg" is plural and a fix that only reserved on the first leg would fail.
//
// A second, uncooled sample type over the same connections is the contrast: its
// journey must move the regular numbers and leave the cooled numbers where Frost
// put them, which proves the cooled reservations above come from Frost's cooling
// requirement rather than from planning anything at all.
func TestAC17FrostReservesCooledCapacityPerLegAndPerLocation(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac17-requester", []string{"requester"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac17-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac17-collector", []string{"collection_professional"}, []string{"LX-F", "LX-B"})
	createTestUser(t, service, admin, "ac17-courier", []string{"delivery_partner"}, nil)
	createTestUser(t, service, admin, "ac17-handler", []string{"node_handler"}, nil)
	createTestUser(t, service, admin, "ac17-operator", []string{"laboratory_team"}, nil)

	location := func(name string, latitude, longitude float64, functions ...string) string {
		t.Helper()
		out, err := service.createLocation(ctx, admin, core.Map{
			"name": name, "active": true,
			"coordinate": core.Map{"latitude": latitude, "longitude": longitude},
			"functions":  functions,
		})
		if err != nil {
			t.Fatalf("create location %s: %v", name, err)
		}
		return core.String(out["locationId"])
	}
	nodeID := location(
		"ac17 node", fixtureOriginLatitude, fixtureOriginLongitude,
		mongostore.FunctionCollectionPoint, mongostore.FunctionTransferPoint,
	)
	hubID := location("ac17 hub", 12.9766, 77.5996, mongostore.FunctionTransferPoint)
	laboratoryID := location("ac17 laboratory", 12.9816, 77.6046, mongostore.FunctionLaboratory)

	// Only the two-legged path exists, so the plan has to use both legs.
	cooledConnection := func(from, to string) string {
		t.Helper()
		out, err := service.createConnection(ctx, admin, core.Map{
			"fromLocationId": from, "toLocationId": to,
			"travelMinutes": 20, "costUnits": 3.0, "cooled": true, "active": true,
		})
		if err != nil {
			t.Fatalf("create connection: %v", err)
		}
		return core.String(out["connectionId"])
	}
	firstLegID := cooledConnection(nodeID, hubID)
	secondLegID := cooledConnection(hubID, laboratoryID)

	sampleType := func(name, container string, minimum, maximum float64) string {
		t.Helper()
		out, err := service.createSampleType(ctx, admin, core.Map{
			"name": name, "containerType": container,
			"temperatureMinC": minimum, "temperatureMaxC": maximum,
			"incompatibleSampleTypes": []string{}, "batchingAllowed": true,
			"qualifiedCollectorRequired": true, "requiredQualification": "LX-B",
		})
		if err != nil {
			t.Fatalf("create sample type %s: %v", name, err)
		}
		typeID := core.String(out["sampleTypeId"])
		if _, err := service.configureAcceptance(ctx, admin, core.Map{
			"laboratoryId": laboratoryID, "sampleTypeId": typeID, "accepts": true,
		}); err != nil {
			t.Fatalf("configure acceptance for %s: %v", name, err)
		}
		return typeID
	}
	// The canonical Frost policy forces continuous cooling; "warm" is an ordinary
	// profile over the very same cooled connections.
	frostTypeID := sampleType("Frost", "cooled_vial", -20.0, -10.0)
	warmTypeID := sampleType("Beta", "beta-vial", 2.0, 8.0)

	locations := []string{nodeID, hubID, laboratoryID}
	legs := []string{firstLegID, secondLegID}
	resources := append(append([]string{}, locations...), legs...)
	for _, resourceID := range resources {
		if _, err := service.configureCapacity(ctx, admin, core.Map{
			"resourceId": resourceID, "capacityUnits": 4, "cooledCapacityUnits": 4,
		}); err != nil {
			t.Fatalf("configure capacity for %s: %v", resourceID, err)
		}
	}

	request := func(typeID, container, key string, minimum, maximum float64) string {
		t.Helper()
		created, err := service.Handle(ctx, "create_collection_request", core.Map{
			"origin": core.Map{
				"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
			},
			"originType": "home", "sampleTypeId": typeID,
			"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T12:00:00Z",
			"containerType": container, "temperatureMinC": minimum, "temperatureMaxC": maximum,
			"incompatibleSampleTypes": []string{}, "idempotencyKey": key,
		}, requester)
		if err != nil {
			t.Fatalf("create request %s: %v", key, err)
		}
		if core.String(created["serviceability"]) != "serviceable" {
			t.Fatalf("request %s was not planned: %v", key, created)
		}
		return core.String(created["journeyId"])
	}

	frostJourneyID := request(frostTypeID, "cooled_vial", "ac17-frost", -20.0, -10.0)

	// Every leg and every location on the route carries a cooled reservation of
	// its own.
	claims := activeJourneyReservations(t, service, frostJourneyID)
	if len(claims) != len(resources) {
		t.Fatalf(
			"the Frost journey holds %d reservations (%s), want one per leg and per location %v",
			len(claims), describeReservations(claims), resources,
		)
	}
	for _, resourceID := range resources {
		record, present := claims[resourceID]
		if !present {
			t.Fatalf(
				"the Frost journey reserved nothing against %s: %s",
				resourceID, describeReservations(claims),
			)
		}
		if !record.cooled || record.units != 1 {
			t.Fatalf(
				"reservation for %s = %v, want one COOLED unit: a Frost journey must hold cooled capacity here",
				resourceID, record,
			)
		}
	}

	frost := readReservedLevels(t, service, dispatcher, resources...)
	for _, resourceID := range resources {
		if frost[resourceID].cooled != 1 {
			kind := "location"
			if resourceID == firstLegID || resourceID == secondLegID {
				kind = "leg"
			}
			t.Fatalf(
				"cooled reserved units at %s %s = %v, want 1: Frost reserved no cooled capacity there",
				kind, resourceID, frost[resourceID],
			)
		}
		if frost[resourceID].regular != 1 {
			t.Fatalf("regular reserved units at %s = %v, want 1", resourceID, frost[resourceID])
		}
	}

	// The contrast: an uncooled profile over the same cooled connections consumes
	// regular capacity only.
	warmJourneyID := request(warmTypeID, "beta-vial", "ac17-warm", 2.0, 8.0)
	for resourceID, record := range activeJourneyReservations(t, service, warmJourneyID) {
		if record.cooled {
			t.Fatalf(
				"an uncooled sample reserved cooled capacity at %s: %v",
				resourceID, record,
			)
		}
	}
	warm := readReservedLevels(t, service, dispatcher, resources...)
	for _, resourceID := range resources {
		if warm[resourceID].regular != 2 || warm[resourceID].cooled != 1 {
			t.Fatalf(
				"after an uncooled journey, %s = %v, want regular=2 cooled=1: the two dimensions are not independent",
				resourceID, warm[resourceID],
			)
		}
	}
}
