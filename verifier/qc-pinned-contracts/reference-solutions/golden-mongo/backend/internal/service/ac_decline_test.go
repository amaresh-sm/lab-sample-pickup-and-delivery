package service

import (
	"context"
	"testing"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// declineFacts is everything the store records about one offer after a decline.
type declineFacts struct {
	workerID       string
	status         string
	accepted       bool
	offerExpiresAt string
	declines       []mongostore.AssignmentDecline
}

func readDeclineFacts(t *testing.T, service *Service, assignmentID string) declineFacts {
	t.Helper()
	var out declineFacts
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		assignment := state.Assignments[assignmentID]
		if assignment == nil {
			t.Fatalf("assignment %s is missing", assignmentID)
		}
		out = declineFacts{
			workerID: assignment.WorkerID, status: assignment.Status,
			accepted:       assignment.AcceptedAt != nil,
			offerExpiresAt: formatOptionalTime(assignment.OfferExpiresAt),
			declines:       append([]mongostore.AssignmentDecline{}, assignment.Declines...),
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// workerAvailability is the availability an operator can see for one worker, read
// through the real user view as well as from the store so the two must agree.
func workerAvailability(
	t *testing.T,
	service *Service,
	reader core.Principal,
	workerID string,
) (bool, bool) {
	t.Helper()
	view, err := service.Handle(
		context.Background(), "read_user", core.Map{"userId": workerID}, reader,
	)
	if err != nil {
		t.Fatalf("read user %s: %v", workerID, err)
	}
	available, active := false, false
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		worker := state.Users[workerID]
		if worker == nil {
			t.Fatalf("worker %s is missing", workerID)
		}
		available, active = worker.Available, worker.Active
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, present := view["available"]; present {
		if core.Bool(view["available"]) != available {
			t.Fatalf("read_user reports available=%v but the store holds %v", view["available"], available)
		}
	}
	if core.Bool(view["active"]) != active {
		t.Fatalf("read_user reports active=%v but the store holds %v", view["active"], active)
	}
	return available, active
}

// AC-32: declining work is a normal answer, not a withdrawal from service. The
// decline is recorded on the offer, the worker's availability is untouched, and
// the work moves on to a DIFFERENT eligible worker — who can then take it.
//
// This drives the real decline_assignment operation end to end: nothing here
// reaches into the store to move an offer along.
func TestAC32DeclineIsRecordedReofferedAndLeavesTheWorkerAvailable(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac32-requester", []string{"requester"}, nil)
	first := createTestUser(t, service, admin, "ac32-collector-1", []string{"collection_professional"}, []string{"LX-A"})
	second := createTestUser(t, service, admin, "ac32-collector-2", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "ac32-courier", []string{"delivery_partner"}, nil)
	createTestUser(t, service, admin, "ac32-operator", []string{"laboratory_team"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac32")

	sampleID, journeyID := requestSampleOfType(
		t, service, requester, network.sampleTypeID, "amber-container",
		2.0, 8.0, nil, "ac32-request",
	)
	plan, err := service.readJourneyPlan(ctx, admin, journeyID)
	if err != nil {
		t.Fatal(err)
	}
	collectionID := core.String(plan["collectionAssignmentId"])

	offered := readDeclineFacts(t, service, collectionID)
	if offered.workerID != first.ID || offered.status != "pending" {
		t.Fatalf("the collection offer went to %q with status %q, want the first collector", offered.workerID, offered.status)
	}
	if len(offered.declines) != 0 {
		t.Fatalf("a fresh offer already carries declines: %+v", offered.declines)
	}
	firstAvailableBefore, firstActiveBefore := workerAvailability(t, service, admin, first.ID)
	if !firstAvailableBefore || !firstActiveBefore {
		t.Fatal("the collector was not available before declining")
	}

	// Only the worker holding the offer may decline it.
	_, err = service.Handle(ctx, "decline_assignment", core.Map{
		"assignmentId": collectionID, "reason": "not mine to refuse",
		"declinedAt": "2026-07-30T09:01:00Z", "idempotencyKey": "ac32-decline-wrong-worker",
	}, second)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "offer_not_available")
	if unchanged := readDeclineFacts(t, service, collectionID); unchanged.workerID != first.ID ||
		len(unchanged.declines) != 0 {
		t.Fatalf("a refused decline changed the offer: %+v", unchanged)
	}

	// The positive control for the refusal above: the same operation from the
	// worker who DOES hold the offer succeeds.
	declined, err := service.Handle(ctx, "decline_assignment", core.Map{
		"assignmentId": collectionID, "reason": "already on another collection",
		"declinedAt": "2026-07-30T09:02:00Z", "idempotencyKey": "ac32-decline",
	}, first)
	if err != nil {
		t.Fatalf("the offered collector could not decline: %v", err)
	}
	if core.String(declined["declinedBy"]) != first.ID {
		t.Fatalf("decline response = %v, want it attributed to the declining collector", declined)
	}
	if core.String(declined["status"]) != "pending" {
		t.Fatalf("the declined work is %v, want it still pending for somebody else", declined["status"])
	}
	// The work moves to a DIFFERENT eligible worker.
	if core.String(declined["reofferedTo"]) != second.ID {
		t.Fatalf(
			"reofferedTo = %v, want the other eligible collector %s",
			declined["reofferedTo"], second.ID,
		)
	}

	// The decline is recorded on the offer, with who declined and why.
	after := readDeclineFacts(t, service, collectionID)
	if len(after.declines) != 1 {
		t.Fatalf("declines recorded on the offer = %+v, want exactly one", after.declines)
	}
	if after.declines[0].WorkerID != first.ID {
		t.Fatalf("the decline is attributed to %q, want %s", after.declines[0].WorkerID, first.ID)
	}
	if after.declines[0].Reason != "already on another collection" {
		t.Fatalf("the decline reason was not recorded: %+v", after.declines[0])
	}
	if formatTime(after.declines[0].At) != "2026-07-30T09:02:00Z" {
		t.Fatalf("the decline time was not recorded: %+v", after.declines[0])
	}
	if after.workerID != second.ID || after.status != "pending" || after.accepted {
		t.Fatalf("the offer after the decline = %+v", after)
	}
	if after.offerExpiresAt == "never" {
		t.Fatalf("the re-offered work has no offer window: %+v", after)
	}
	// The decline is in the audit trail.
	if !stringSliceContains(auditActionsFor(t, service, admin, "assignment", collectionID), "assignment_declined") {
		t.Fatalf(
			"the decline is not in the audit trail; actions = %v",
			auditActionsFor(t, service, admin, "assignment", collectionID),
		)
	}

	// AC-32's core clause: declining did NOT make the worker unavailable.
	firstAvailableAfter, firstActiveAfter := workerAvailability(t, service, admin, first.ID)
	if firstAvailableAfter != firstAvailableBefore || firstActiveAfter != firstActiveBefore {
		t.Fatalf(
			"declining changed the worker's standing: available %v -> %v, active %v -> %v",
			firstAvailableBefore, firstAvailableAfter, firstActiveBefore, firstActiveAfter,
		)
	}

	// The worker who declined can no longer accept the work they turned down, and
	// the worker it moved to can.
	_, err = service.Handle(ctx, "accept_assignment", core.Map{
		"assignmentId": collectionID, "idempotencyKey": "ac32-accept-decliner",
	}, first)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "offer_not_available")
	accepted, err := service.Handle(ctx, "accept_assignment", core.Map{
		"assignmentId": collectionID, "idempotencyKey": "ac32-accept-reoffered",
	}, second)
	if err != nil {
		t.Fatalf("the re-offered collector could not accept the work: %v", err)
	}
	if core.String(accepted["workerId"]) != second.ID || core.String(accepted["status"]) != "accepted" {
		t.Fatalf("acceptance by the re-offered collector = %v", accepted)
	}

	// AC-32's other half, and AC-33's floor: a decline is not an exclusion. The
	// collector who declined is offered the next comparable piece of work.
	nextSampleID, nextJourneyID := requestSampleOfType(
		t, service, requester, network.sampleTypeID, "amber-container",
		2.0, 8.0, nil, "ac32-second-request",
	)
	if nextSampleID == sampleID {
		t.Fatal("the second request reused the first sample")
	}
	nextPlan, err := service.readJourneyPlan(ctx, admin, nextJourneyID)
	if err != nil {
		t.Fatal(err)
	}
	nextOffer := readDeclineFacts(t, service, core.String(nextPlan["collectionAssignmentId"]))
	if nextOffer.workerID != first.ID {
		t.Fatalf(
			"the next collection was offered to %q, want the collector who declined earlier (%s): a decline must not exclude a worker",
			nextOffer.workerID, first.ID,
		)
	}
}
