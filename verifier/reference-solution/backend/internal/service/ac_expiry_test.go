package service

import (
	"context"
	"sort"
	"testing"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// batchExpiryFixture is ONE active batched journey carrying two Amber samples
// whose delivery deadlines differ, so exactly one member of the batch can miss
// its deadline while the other is still comfortably deliverable. Everything is
// built through the real operations: two collection requests and the real batch
// edit that consolidates them.
type batchExpiryFixture struct {
	network           networkFixture
	requester         core.Principal
	dispatcher        core.Principal
	collector         core.Principal
	courier           core.Principal
	operator          core.Principal
	journeyID         string
	survivingSampleID string
	expiringSampleID  string
}

// runBatchedJourneyWithStaggeredDeadlines plans two Amber samples and batches
// them onto one journey. The surviving sample must be delivered by 12:00 and the
// expiring one by 10:00, so a maintenance pass run after 10:00 and before 12:00
// expires exactly one member of a live batch.
func runBatchedJourneyWithStaggeredDeadlines(
	t *testing.T,
	service *Service,
	admin core.Principal,
	prefix string,
) batchExpiryFixture {
	t.Helper()
	fixture := batchExpiryFixture{
		requester:  createTestUser(t, service, admin, prefix+"-requester", []string{"requester"}, nil),
		dispatcher: createTestUser(t, service, admin, prefix+"-dispatcher", []string{"dispatcher"}, nil),
		collector:  createTestUser(t, service, admin, prefix+"-collector", []string{"collection_professional"}, []string{"LX-A"}),
		courier:    createTestUser(t, service, admin, prefix+"-courier", []string{"delivery_partner"}, nil),
		operator:   createTestUser(t, service, admin, prefix+"-operator", []string{"laboratory_team"}, nil),
	}
	fixture.network = buildServiceableNetwork(t, service, admin, prefix)

	survivingSampleID, journeyID := requestSampleOfType(
		t, service, fixture.requester, fixture.network.sampleTypeID, "amber-container",
		2.0, 8.0, nil, prefix+"-surviving",
	)
	expiringSampleID, _ := requestSampleOfType(
		t, service, fixture.requester, fixture.network.sampleTypeID, "amber-container",
		2.0, 8.0, core.Map{"deliveryDeadline": "2026-07-30T10:00:00Z"}, prefix+"-expiring",
	)
	fixture.journeyID = journeyID
	fixture.survivingSampleID = survivingSampleID
	fixture.expiringSampleID = expiringSampleID

	merged, err := mergeSamplesOnto(
		t, service, fixture.dispatcher, journeyID, 1,
		[]string{survivingSampleID, expiringSampleID}, prefix+"-merge",
	)
	if err != nil {
		t.Fatalf("batching the two samples: %v", err)
	}
	if core.String(merged["routeMode"]) != "batched" || core.Int(merged["planVersion"]) != 2 {
		t.Fatalf("batched plan = %v", merged)
	}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		journey := state.Journeys[journeyID]
		if len(journey.SampleIDs) != 2 || journey.CapacityUsed != 2 {
			t.Fatalf("batched journey carries %v with capacityUsed=%d", journey.SampleIDs, journey.CapacityUsed)
		}
		if err := validateJourneyPlan(state, journey); err != nil {
			t.Fatalf("the batched journey is invalid before anything expires: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// journeyPlanValidity runs the application's own plan validator over a stored
// journey. A journey that cannot be validated cannot be edited, merged or split,
// so this is the state every revalidating operation depends on.
func journeyPlanValidity(t *testing.T, service *Service, journeyID string) error {
	t.Helper()
	var out error
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		journey := state.Journeys[journeyID]
		if journey == nil {
			t.Fatalf("journey %s is missing", journeyID)
		}
		out = validateJourneyPlan(state, journey)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// sampleAssignmentStates describes every assignment a journey holds for one
// sample, as "kind:status" keyed by assignment id.
func sampleAssignmentStates(
	t *testing.T,
	service *Service,
	journeyID, sampleID string,
) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		for _, assignment := range state.Assignments {
			if assignment.JourneyID == journeyID && assignment.SampleID == sampleID {
				out[assignment.ID] = assignment.Kind + ":" + assignment.Status
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// transportAssignmentFor is the transport assignment a journey holds for one
// sample, with the worker it is currently offered to.
func transportAssignmentFor(
	t *testing.T,
	service *Service,
	journeyID, sampleID string,
) (string, string) {
	t.Helper()
	assignmentID, workerID := "", ""
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		for _, assignment := range assignmentsForJourney(state, journeyID) {
			if assignment.SampleID == sampleID && assignment.Kind == "transport" &&
				assignment.Status != "cancelled" {
				assignmentID, workerID = assignment.ID, assignment.WorkerID
				return nil
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if assignmentID == "" {
		t.Fatalf("journey %s holds no live transport assignment for sample %s", journeyID, sampleID)
	}
	return assignmentID, workerID
}

func workerActiveAssignmentCount(t *testing.T, service *Service, workerID string) int {
	t.Helper()
	total := 0
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		total = activeWorkerAssignments(state, workerID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return total
}

// auditEntriesFor is every audit record about one entity, read through the real
// audit search rather than out of the store, keyed by audit id so a probe can
// tell records written before a moment from records written after it.
func auditEntriesFor(
	t *testing.T,
	service *Service,
	reader core.Principal,
	entityType, entityID string,
) map[string]string {
	t.Helper()
	out, err := service.Handle(context.Background(), "search_audit_trail", core.Map{
		"entityType": entityType, "entityId": entityID,
	}, reader)
	if err != nil {
		t.Fatalf("audit search for %s %s: %v", entityType, entityID, err)
	}
	entries := map[string]string{}
	for _, entry := range out["entries"].([]core.Map) {
		entries[core.String(entry["auditId"])] = core.String(entry["action"])
	}
	return entries
}

// auditActionsFor is every audit action recorded against one entity.
func auditActionsFor(
	t *testing.T,
	service *Service,
	reader core.Principal,
	entityType, entityID string,
) []string {
	t.Helper()
	actions := []string{}
	for _, action := range auditEntriesFor(t, service, reader, entityType, entityID) {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	return actions
}

func maintain(t *testing.T, service *Service) {
	t.Helper()
	if err := service.Maintain(context.Background()); err != nil {
		t.Fatalf("maintenance pass: %v", err)
	}
}

// AC-62, and the orphan-assignment defect in expireSample: when ONE member of a
// live batch misses its deadline, the sample is marked expired with an audit
// record, and every piece of work the expired sample still had on the surviving
// journey is cancelled. An expiry that removed the sample from the journey but
// left its assignments behind would leave those assignments pointing at a sample
// the journey no longer carries.
func TestAC62ExpiredBatchMemberCancelsTheWorkItLeavesBehind(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	// A long offer window keeps the maintenance pass from re-offering work for
	// unrelated reasons, so the only thing this probe observes is the expiry.
	t.Setenv("ASSIGNMENT_OFFER_TTL_SECONDS", "86400")
	service, admin := integrationService(t)
	fixture := runBatchedJourneyWithStaggeredDeadlines(t, service, admin, "ac62exp")

	before := sampleAssignmentStates(t, service, fixture.journeyID, fixture.expiringSampleID)
	if len(before) == 0 {
		t.Fatal("the expiring sample has no work on the batched journey to begin with")
	}
	for id, state := range before {
		if state == "collection:cancelled" || state == "transport:cancelled" {
			t.Fatalf("assignment %s was already cancelled before the expiry: %s", id, state)
		}
	}

	// 10:30 is past the expiring sample's 10:00 deadline and well inside the
	// surviving sample's 12:00 deadline.
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T10:30:00Z")
	maintain(t, service)

	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		expired := state.Samples[fixture.expiringSampleID]
		if expired.Status != "expired" {
			t.Fatalf("expiring sample status = %q, want expired", expired.Status)
		}
		if expired.ActiveJourneyID != "" {
			t.Fatalf("an expired sample still points at journey %q", expired.ActiveJourneyID)
		}
		surviving := state.Samples[fixture.survivingSampleID]
		if surviving.Status == "expired" || surviving.ActiveJourneyID != fixture.journeyID {
			t.Fatalf("the surviving sample was disturbed: status=%q journey=%q", surviving.Status, surviving.ActiveJourneyID)
		}
		journey := state.Journeys[fixture.journeyID]
		if journey.Status != "active" {
			t.Fatalf("the surviving journey status = %q, want active", journey.Status)
		}
		if len(journey.SampleIDs) != 1 || journey.SampleIDs[0] != fixture.survivingSampleID {
			t.Fatalf("the surviving journey carries %v", journey.SampleIDs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// AC-62: the expiry is recorded.
	actions := auditActionsFor(t, service, admin, "sample", fixture.expiringSampleID)
	if !stringSliceContains(actions, "sample_expired") {
		t.Fatalf("no sample_expired audit record for the expired sample; actions = %v", actions)
	}

	// The defect: every assignment the expired sample held on the surviving
	// journey must be cancelled, not left pointing at a sample the journey no
	// longer carries.
	after := sampleAssignmentStates(t, service, fixture.journeyID, fixture.expiringSampleID)
	for id, state := range after {
		if state != "collection:cancelled" && state != "transport:cancelled" &&
			state != "laboratory:cancelled" && state != "node:cancelled" {
			t.Fatalf(
				"the expired sample left assignment %s on the surviving journey as %q: orphan work still references a sample outside the journey",
				id, state,
			)
		}
	}

	// The consequence that makes the orphans matter: the surviving journey must
	// still be a valid plan.
	if err := journeyPlanValidity(t, service, fixture.journeyID); err != nil {
		t.Fatalf("the surviving journey no longer validates after a batch member expired: %v", err)
	}
}

// AC-27 route classification after a batch member expires: a journey left
// carrying one sample over one movement is a direct journey. Reporting it as
// multi_stop would describe a relay that does not exist.
func TestAC27SurvivingSingleSampleJourneyIsDirectAfterABatchMemberExpires(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	t.Setenv("ASSIGNMENT_OFFER_TTL_SECONDS", "86400")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runBatchedJourneyWithStaggeredDeadlines(t, service, admin, "ac27exp")

	// The batch really is a single-movement route, so "multi_stop" could only be
	// a mislabelling rather than a description of a longer route.
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if stops := stopsForJourney(state, fixture.journeyID); len(stops) != 2 {
			t.Fatalf("the batched journey has %d stops, want a single movement", len(stops))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T10:30:00Z")
	maintain(t, service)

	plan, err := service.readJourneyPlan(ctx, fixture.dispatcher, fixture.journeyID)
	if err != nil {
		t.Fatal(err)
	}
	if core.String(plan["routeMode"]) != "direct" {
		t.Fatalf(
			"routeMode after a batch member expired = %v, want direct for one sample over one movement",
			plan["routeMode"],
		)
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if mode := state.Journeys[fixture.journeyID].RouteMode; mode != "direct" {
			t.Fatalf("stored routeMode = %q, want direct", mode)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-43, AC-44: a journey that survives the expiry of one batch member is still
// a plannable journey. Both revalidating plan-change operations must accept it —
// the batch edit and the dedicated journey merge — because both revalidate the
// whole candidate plan before committing.
func TestAC43SurvivingBatchJourneyStaysPlannableAfterAMemberExpires(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	t.Setenv("ASSIGNMENT_OFFER_TTL_SECONDS", "86400")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runBatchedJourneyWithStaggeredDeadlines(t, service, admin, "ac43exp")

	// Two more comfortable Amber samples, one for each revalidating operation.
	editSampleID, _ := requestSampleOfType(
		t, service, fixture.requester, fixture.network.sampleTypeID, "amber-container",
		2.0, 8.0, nil, "ac43exp-edit-sample",
	)
	mergeSampleID, mergeJourneyID := requestSampleOfType(
		t, service, fixture.requester, fixture.network.sampleTypeID, "amber-container",
		2.0, 8.0, nil, "ac43exp-merge-journey",
	)

	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T10:30:00Z")
	maintain(t, service)

	// The expiry bumped the plan version once, from the batch edit's 2 to 3.
	planVersion := 0
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		planVersion = int(state.Journeys[fixture.journeyID].PlanVersion)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if planVersion != 3 {
		t.Fatalf("plan version after the expiry = %d, want 3", planVersion)
	}

	edited, err := mergeSamplesOnto(
		t, service, fixture.dispatcher, fixture.journeyID, planVersion,
		[]string{fixture.survivingSampleID, editSampleID}, "ac43exp-edit",
	)
	if err != nil {
		t.Fatalf("edit_active_plan on the surviving journey was refused: %v", err)
	}
	if core.String(edited["routeMode"]) != "batched" {
		t.Fatalf("edited plan = %v", edited)
	}

	mergedIn, err := service.Handle(ctx, "merge_journeys", core.Map{
		"targetJourneyId": fixture.journeyID, "expectedPlanVersion": planVersion + 1,
		"sourceJourneyIds": []string{mergeJourneyID},
		"idempotencyKey":   "ac43exp-merge",
	}, fixture.dispatcher)
	if err != nil {
		t.Fatalf("merge_journeys onto the surviving journey was refused: %v", err)
	}
	if core.String(mergedIn["routeMode"]) != "batched" {
		t.Fatalf("merged plan = %v", mergedIn)
	}

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		journey := state.Journeys[fixture.journeyID]
		if len(journey.SampleIDs) != 3 {
			t.Fatalf("the surviving journey carries %v, want three samples", journey.SampleIDs)
		}
		for _, sampleID := range []string{fixture.survivingSampleID, editSampleID, mergeSampleID} {
			if !stringSliceContains(journey.SampleIDs, sampleID) {
				t.Fatalf("sample %s is missing from %v", sampleID, journey.SampleIDs)
			}
		}
		if stringSliceContains(journey.SampleIDs, fixture.expiringSampleID) {
			t.Fatalf("the expired sample came back onto the journey: %v", journey.SampleIDs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-48: an expired batch member stops consuming the worker capacity its work
// held. The courier is deliberately saturated at exactly one assignment, so the
// refusal below can only be about capacity, and the same acceptance succeeding
// after the expiry is the positive control.
func TestAC48ExpiredBatchMemberFreesTheWorkerCapacityItHeld(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	t.Setenv("ASSIGNMENT_OFFER_TTL_SECONDS", "86400")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runBatchedJourneyWithStaggeredDeadlines(t, service, admin, "ac48exp")

	expiringLegID, expiringWorkerID := transportAssignmentFor(
		t, service, fixture.journeyID, fixture.expiringSampleID,
	)
	survivingLegID, survivingWorkerID := transportAssignmentFor(
		t, service, fixture.journeyID, fixture.survivingSampleID,
	)
	if expiringWorkerID != fixture.courier.ID || survivingWorkerID != fixture.courier.ID {
		t.Fatalf(
			"both legs must be offered to the one courier for this probe: %q and %q",
			expiringWorkerID, survivingWorkerID,
		)
	}

	if _, err := service.Handle(ctx, "accept_assignment", core.Map{
		"assignmentId": expiringLegID, "idempotencyKey": "ac48exp-accept-expiring",
	}, fixture.courier); err != nil {
		t.Fatalf("accepting the expiring sample's leg: %v", err)
	}
	if got := workerActiveAssignmentCount(t, service, fixture.courier.ID); got != 1 {
		t.Fatalf("courier active assignments after accepting one leg = %d, want 1", got)
	}
	// Saturate the courier at exactly the one assignment they hold.
	if _, err := service.Handle(ctx, "set_worker_availability", core.Map{
		"workerId": fixture.courier.ID, "available": true, "capacityUnits": 1,
	}, admin); err != nil {
		t.Fatalf("saturating the courier: %v", err)
	}

	// At capacity, the courier cannot take the surviving sample's leg.
	_, err := service.Handle(ctx, "accept_assignment", core.Map{
		"assignmentId": survivingLegID, "idempotencyKey": "ac48exp-accept-surviving-blocked",
	}, fixture.courier)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "worker_unavailable")

	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T10:30:00Z")
	maintain(t, service)

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if status := state.Assignments[expiringLegID].Status; status != "cancelled" {
			t.Fatalf(
				"the expired sample's accepted leg is %q, want cancelled: it still consumes the courier's capacity",
				status,
			)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := workerActiveAssignmentCount(t, service, fixture.courier.ID); got != 0 {
		t.Fatalf("courier active assignments after the expiry = %d, want 0", got)
	}

	// Positive control: the single blocking condition — the orphan holding the
	// courier's only capacity unit — is gone, and the same acceptance succeeds.
	accepted, err := service.Handle(ctx, "accept_assignment", core.Map{
		"assignmentId": survivingLegID, "idempotencyKey": "ac48exp-accept-surviving-allowed",
	}, fixture.courier)
	if err != nil {
		t.Fatalf("the courier still cannot take work after the expired sample released its capacity: %v", err)
	}
	if core.String(accepted["status"]) != "accepted" {
		t.Fatalf("acceptance after the expiry = %v", accepted)
	}
}

// The third consequence of an orphaned assignment: the maintenance pass keeps
// offering it. Work belonging to an expired sample must never be offered to
// anybody again.
func TestExpiredBatchMemberWorkIsNeverOfferedAgain(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runBatchedJourneyWithStaggeredDeadlines(t, service, admin, "acoffer")
	orphanIDs := []string{}
	for id := range sampleAssignmentStates(t, service, fixture.journeyID, fixture.expiringSampleID) {
		orphanIDs = append(orphanIDs, id)
	}
	sort.Strings(orphanIDs)

	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T10:30:00Z")
	maintain(t, service)
	// Everything recorded up to and including the expiring pass is history; only
	// what a LATER pass records can be an offer of work that no longer exists.
	settled := map[string]map[string]string{}
	for _, id := range orphanIDs {
		settled[id] = auditEntriesFor(t, service, admin, "assignment", id)
	}

	// A later pass, beyond both the offer window and the decline-penalty window,
	// is exactly when a still-pending orphan would be offered out again.
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T11:45:00Z")
	maintain(t, service)

	for _, id := range orphanIDs {
		for auditID, action := range auditEntriesFor(t, service, admin, "assignment", id) {
			if _, known := settled[id][auditID]; known {
				continue
			}
			if action == "assignment_offered" || action == "assignment_offer_expired" {
				t.Fatalf(
					"assignment %s of an expired sample was offered again by a later maintenance pass (%s)",
					id, action,
				)
			}
		}
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		for _, id := range orphanIDs {
			assignment := state.Assignments[id]
			if assignment.Status == "pending" {
				t.Fatalf("assignment %s of an expired sample is still pending work", id)
			}
			if assignment.WorkerID != "" && assignment.Status != "cancelled" {
				t.Fatalf("assignment %s of an expired sample is still held by %s", id, assignment.WorkerID)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
