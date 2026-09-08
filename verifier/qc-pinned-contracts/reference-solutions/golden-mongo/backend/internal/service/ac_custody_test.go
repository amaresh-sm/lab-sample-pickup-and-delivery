package service

import (
	"context"
	"testing"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// pendingHandoffFixture is a real journey stopped at the moment a custody
// handoff has been initiated and neither side has confirmed it.
type pendingHandoffFixture struct {
	network      networkFixture
	requester    core.Principal
	collector    core.Principal
	courier      core.Principal
	operator     core.Principal
	sampleID     string
	journeyID    string
	collectionID string
	transportID  string
	handoffID    string
}

// runJourneyToPendingHandoff drives a real journey up to an initiated but
// unconfirmed handoff, so a test can observe what one-sided confirmation does.
func runJourneyToPendingHandoff(
	t *testing.T,
	service *Service,
	admin core.Principal,
	prefix string,
) pendingHandoffFixture {
	t.Helper()
	ctx := context.Background()
	fixture := pendingHandoffFixture{
		requester: createTestUser(t, service, admin, prefix+"-requester", []string{"requester"}, nil),
		collector: createTestUser(t, service, admin, prefix+"-collector", []string{"collection_professional"}, []string{"LX-A"}),
		courier:   createTestUser(t, service, admin, prefix+"-courier", []string{"delivery_partner"}, nil),
		operator:  createTestUser(t, service, admin, prefix+"-operator", []string{"laboratory_team"}, nil),
	}
	fixture.network = buildServiceableNetwork(t, service, admin, prefix)

	created, err := service.createCollectionRequest(
		ctx, fixture.requester, standardRequest(fixture.network, prefix+"-request"),
	)
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
	fixture.handoffID = core.String(handoff["handoffId"])
	return fixture
}

// custodySnapshot is what the datastore says about a sample's custody: who holds
// it, and every handoff still awaiting confirmation.
type custodySnapshot struct {
	holderID          string
	pendingHandoffIDs []string
	completedHandoffs int
	senderConfirmed   bool
	receiverConfirmed bool
	handoffStatus     string
	handoffCompleted  bool
}

func readCustody(t *testing.T, service *Service, sampleID, handoffID string) custodySnapshot {
	t.Helper()
	var out custodySnapshot
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		sample := state.Samples[sampleID]
		if sample == nil {
			t.Fatalf("sample %s is missing", sampleID)
		}
		out.holderID = sample.CurrentHolderID
		for _, handoff := range state.Handoffs {
			if handoff.SampleID != sampleID {
				continue
			}
			switch handoff.Status {
			case "pending":
				out.pendingHandoffIDs = append(out.pendingHandoffIDs, handoff.ID)
			case "confirmed":
				out.completedHandoffs++
			}
			if handoff.ID == handoffID {
				out.handoffStatus = handoff.Status
				out.senderConfirmed = handoff.SenderConfirmedAt != nil
				out.receiverConfirmed = handoff.ReceiverConfirmedAt != nil
				out.handoffCompleted = handoff.CompletedAt != nil
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// AC-50, AC-51, AC-52, AC-53: a handoff transfers custody only when BOTH sides
// have confirmed. Confirming one side alone leaves the sender responsible, keeps
// the handoff pending, and keeps the sample single-holder; the holder flips at
// the exact moment the second confirmation lands and not before.
//
// Every other custody test in this package confirms both sides, so a defect that
// transferred custody on the first confirmation would be invisible to all of
// them. This probe deliberately stops after one side.
func TestAC50To53CustodyTransfersOnlyWhenBothSidesConfirm(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runJourneyToPendingHandoff(t, service, admin, "ac50")

	// Before any confirmation the sealing collector holds the sample.
	initial := readCustody(t, service, fixture.sampleID, fixture.handoffID)
	if initial.holderID != fixture.collector.ID {
		t.Fatalf("holder before confirmation = %q, want the collector", initial.holderID)
	}
	if len(initial.pendingHandoffIDs) != 1 || initial.pendingHandoffIDs[0] != fixture.handoffID {
		t.Fatalf("pending handoffs before confirmation = %v", initial.pendingHandoffIDs)
	}

	// The SENDER confirms, and only the sender.
	senderSide, err := service.confirmHandoff(ctx, fixture.collector, core.Map{
		"handoffId": fixture.handoffID, "confirmationSide": "sender",
		"confirmedAt": "2026-07-30T09:07:00Z", "idempotencyKey": "ac50-confirm-sender",
	})
	if err != nil {
		t.Fatalf("sender confirmation: %v", err)
	}

	// AC-51: one-sided confirmation does not complete the handoff.
	if core.String(senderSide["status"]) != "pending" {
		t.Fatalf("handoff status after the sender alone = %v, want pending", senderSide["status"])
	}
	if !core.Bool(senderSide["senderConfirmed"]) || core.Bool(senderSide["receiverConfirmed"]) {
		t.Fatalf("confirmation flags after the sender alone = %v", senderSide)
	}
	// AC-52: the sender remains responsible.
	if core.String(senderSide["currentHolderId"]) != fixture.collector.ID {
		t.Fatalf(
			"currentHolderId after the sender alone = %v, want the sender %s",
			senderSide["currentHolderId"], fixture.collector.ID,
		)
	}

	oneSided := readCustody(t, service, fixture.sampleID, fixture.handoffID)
	if oneSided.holderID != fixture.collector.ID {
		t.Fatalf(
			"stored holder after the sender alone = %q, want the sender %s: custody transferred on one confirmation",
			oneSided.holderID, fixture.collector.ID,
		)
	}
	// AC-50: exactly one holder, exactly one handoff awaiting confirmation.
	if len(oneSided.pendingHandoffIDs) != 1 || oneSided.pendingHandoffIDs[0] != fixture.handoffID {
		t.Fatalf("pending handoffs after the sender alone = %v, want exactly one", oneSided.pendingHandoffIDs)
	}
	if oneSided.completedHandoffs != 0 {
		t.Fatalf("a completed transfer appeared after one confirmation: %+v", oneSided)
	}
	if !oneSided.senderConfirmed || oneSided.receiverConfirmed {
		t.Fatalf("stored confirmation flags after the sender alone = %+v", oneSided)
	}
	if oneSided.handoffStatus != "pending" || oneSided.handoffCompleted {
		t.Fatalf("stored handoff after the sender alone = %+v", oneSided)
	}

	// The read surfaces agree: the sender still holds it and the handoff is
	// still half-confirmed.
	lifecycle, err := service.Handle(
		ctx, "read_sample_lifecycle", core.Map{"sampleId": fixture.sampleID}, admin,
	)
	if err != nil {
		t.Fatal(err)
	}
	if core.String(lifecycle["currentHolderId"]) != fixture.collector.ID {
		t.Fatalf("lifecycle holder after the sender alone = %v", lifecycle["currentHolderId"])
	}
	pending, ok := lifecycle["pendingHandoff"].(core.Map)
	if !ok {
		t.Fatalf("lifecycle reports no pending handoff after one confirmation: %v", lifecycle)
	}
	if !core.Bool(pending["senderConfirmed"]) || core.Bool(pending["receiverConfirmed"]) {
		t.Fatalf("lifecycle pending handoff = %v", pending)
	}
	operations, err := service.Handle(ctx, "read_operations_state", core.Map{}, admin)
	if err != nil {
		t.Fatal(err)
	}
	handoffRows := 0
	for _, row := range operations["handoffs"].([]core.Map) {
		if core.String(row["sampleId"]) != fixture.sampleID {
			continue
		}
		handoffRows++
		if core.String(row["currentHolderId"]) != fixture.collector.ID {
			t.Fatalf("operations view holder after the sender alone = %v", row["currentHolderId"])
		}
		if !core.Bool(row["senderConfirmed"]) || core.Bool(row["receiverConfirmed"]) {
			t.Fatalf("operations view handoff = %v", row)
		}
	}
	if handoffRows != 1 {
		t.Fatalf("operations view lists %d pending handoffs for the sample, want 1", handoffRows)
	}

	// A receiver who does not hold the sample yet cannot begin the leg. This is
	// the operational consequence of AC-52 and would pass silently if custody
	// had already moved.
	_, err = service.Handle(ctx, "update_assignment_progress", core.Map{
		"assignmentId": fixture.transportID, "status": "picked_up",
		"occurredAt": "2026-07-30T09:08:00Z", "idempotencyKey": "ac50-early-pickup",
	}, fixture.courier)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "custody_required")

	// AC-53: the receiver confirms, and the holder flips exactly then.
	receiverSide, err := service.confirmHandoff(ctx, fixture.courier, core.Map{
		"handoffId": fixture.handoffID, "confirmationSide": "receiver",
		"confirmedAt": "2026-07-30T09:09:00Z", "idempotencyKey": "ac50-confirm-receiver",
	})
	if err != nil {
		t.Fatalf("receiver confirmation: %v", err)
	}
	if core.String(receiverSide["status"]) != "confirmed" {
		t.Fatalf("handoff status after both sides = %v, want confirmed", receiverSide["status"])
	}
	if !core.Bool(receiverSide["senderConfirmed"]) || !core.Bool(receiverSide["receiverConfirmed"]) {
		t.Fatalf("confirmation flags after both sides = %v", receiverSide)
	}
	if core.String(receiverSide["currentHolderId"]) != fixture.courier.ID {
		t.Fatalf(
			"currentHolderId after both sides = %v, want the receiver %s",
			receiverSide["currentHolderId"], fixture.courier.ID,
		)
	}

	completed := readCustody(t, service, fixture.sampleID, fixture.handoffID)
	if completed.holderID != fixture.courier.ID {
		t.Fatalf("stored holder after both sides = %q, want the receiver", completed.holderID)
	}
	// AC-50 again: still exactly one holder, and nothing is awaiting
	// confirmation once the transfer is done.
	if len(completed.pendingHandoffIDs) != 0 {
		t.Fatalf("handoffs still pending after completion = %v", completed.pendingHandoffIDs)
	}
	// AC-54's neighbour: exactly one transfer, not two.
	if completed.completedHandoffs != 1 {
		t.Fatalf("completed transfers = %d, want exactly 1", completed.completedHandoffs)
	}
	if !completed.handoffCompleted || completed.handoffStatus != "confirmed" {
		t.Fatalf("stored handoff after both sides = %+v", completed)
	}

	// The receiver may now begin the leg, so the refusal above was about custody
	// and not about something else.
	if _, err := service.Handle(ctx, "update_assignment_progress", core.Map{
		"assignmentId": fixture.transportID, "status": "picked_up",
		"occurredAt": "2026-07-30T09:10:00Z", "idempotencyKey": "ac50-pickup-after-transfer",
	}, fixture.courier); err != nil {
		t.Fatalf("leg progress after a completed handoff: %v", err)
	}
}

// AC-51, AC-52 from the other side: confirming only the RECEIVER must not move
// custody either, so the rule is two-sided rather than "whoever the sender is
// confirms last".
func TestAC51ReceiverOnlyConfirmationDoesNotTransferCustody(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runJourneyToPendingHandoff(t, service, admin, "ac51")

	receiverSide, err := service.confirmHandoff(ctx, fixture.courier, core.Map{
		"handoffId": fixture.handoffID, "confirmationSide": "receiver",
		"confirmedAt": "2026-07-30T09:07:00Z", "idempotencyKey": "ac51-confirm-receiver",
	})
	if err != nil {
		t.Fatalf("receiver confirmation: %v", err)
	}
	if core.String(receiverSide["status"]) != "pending" {
		t.Fatalf("handoff status after the receiver alone = %v, want pending", receiverSide["status"])
	}
	if core.Bool(receiverSide["senderConfirmed"]) || !core.Bool(receiverSide["receiverConfirmed"]) {
		t.Fatalf("confirmation flags after the receiver alone = %v", receiverSide)
	}
	if core.String(receiverSide["currentHolderId"]) != fixture.collector.ID {
		t.Fatalf(
			"currentHolderId after the receiver alone = %v, want the sender %s",
			receiverSide["currentHolderId"], fixture.collector.ID,
		)
	}
	oneSided := readCustody(t, service, fixture.sampleID, fixture.handoffID)
	if oneSided.holderID != fixture.collector.ID {
		t.Fatalf(
			"stored holder after the receiver alone = %q, want the sender: custody transferred on one confirmation",
			oneSided.holderID,
		)
	}
	if len(oneSided.pendingHandoffIDs) != 1 || oneSided.completedHandoffs != 0 {
		t.Fatalf("custody after the receiver alone = %+v", oneSided)
	}

	// Completing it from the sender's side finishes exactly one transfer.
	if _, err := service.confirmHandoff(ctx, fixture.collector, core.Map{
		"handoffId": fixture.handoffID, "confirmationSide": "sender",
		"confirmedAt": "2026-07-30T09:08:00Z", "idempotencyKey": "ac51-confirm-sender",
	}); err != nil {
		t.Fatalf("sender confirmation: %v", err)
	}
	completed := readCustody(t, service, fixture.sampleID, fixture.handoffID)
	if completed.holderID != fixture.courier.ID ||
		len(completed.pendingHandoffIDs) != 0 || completed.completedHandoffs != 1 {
		t.Fatalf("custody after both sides = %+v", completed)
	}
}
