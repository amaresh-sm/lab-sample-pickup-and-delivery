package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func TestPendingOfferCannotBeAcceptedByAnotherWorker(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	offered := createTestUser(t, service, admin, "offered-collector", []string{"collection_professional"}, nil)
	unrelated := createTestUser(t, service, admin, "unrelated-collector", []string{"collection_professional"}, nil)

	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", SampleTypeID: "type", Status: "planned", ActiveJourneyID: "journey",
		}
		state.SampleTypes["type"] = &mongostore.SampleType{ID: "type"}
		state.Journeys["journey"] = &mongostore.Journey{ID: "journey", Status: "active"}
		state.Assignments["assignment"] = &mongostore.Assignment{
			ID: "assignment", JourneyID: "journey", SampleID: "sample",
			Kind: "collection", WorkerID: offered.ID, Status: "pending",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := service.acceptAssignment(context.Background(), unrelated, core.Map{
		"assignmentId": "assignment", "idempotencyKey": "steal-offer",
	})
	assertHTTPErrorCode(t, err, "offer_not_available")

	accepted, err := service.acceptAssignment(context.Background(), offered, core.Map{
		"assignmentId": "assignment", "idempotencyKey": "accept-own-offer",
	})
	if err != nil {
		t.Fatalf("offered worker acceptance: %v", err)
	}
	if accepted["workerId"] != offered.ID || accepted["status"] != "accepted" {
		t.Fatalf("accepted=%v", accepted)
	}
}

func TestInitiateHandoffResolvesCollectionBoundarySelfReference(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	collector := createTestUser(
		t,
		service,
		admin,
		"boundary-collector",
		[]string{"collection_professional"},
		nil,
	)
	courier := createTestUser(
		t,
		service,
		admin,
		"boundary-courier",
		[]string{"delivery_partner"},
		nil,
	)
	collectedAt := time.Date(2026, 7, 26, 9, 10, 0, 0, time.UTC)

	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", Status: "sealed", ActiveJourneyID: "journey",
			SealedAt: pointerTime(collectedAt), CurrentHolderID: collector.ID,
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", PrimarySampleID: "sample",
			SampleIDs: []string{"sample"}, Status: "active",
		}
		state.Assignments["collection"] = &mongostore.Assignment{
			ID: "collection", JourneyID: "journey", SampleID: "sample",
			Kind: "collection", LegSequence: 0, WorkerID: collector.ID,
			Status: "completed", CompletedAt: pointerTime(collectedAt),
		}
		state.Assignments["transport"] = &mongostore.Assignment{
			ID: "transport", JourneyID: "journey", SampleID: "sample",
			Kind: "transport", LegSequence: 1, WorkerID: courier.ID,
			Status: "pending",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	result, err := service.initiateHandoff(context.Background(), collector, core.Map{
		"sampleId":         "sample",
		"receiverId":       courier.ID,
		"fromAssignmentId": "collection",
		"toAssignmentId":   "collection",
		"initiatedAt":      "2026-07-26T09:11:00Z",
		"idempotencyKey":   "collection-boundary-self-reference",
	})
	if err != nil {
		t.Fatalf("initiate collection boundary handoff: %v", err)
	}
	handoffID := core.String(result["handoffId"])
	if handoffID == "" {
		t.Fatalf("handoff result=%v", result)
	}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		handoff := state.Handoffs[handoffID]
		if handoff == nil {
			t.Fatal("handoff was not persisted")
		}
		if handoff.FromAssignmentID != "collection" ||
			handoff.ToAssignmentID != "transport" {
			t.Fatalf("resolved handoff route=%+v", handoff)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	confirmations := []struct {
		principal core.Principal
		side      string
		at        string
		key       string
	}{
		{collector, "sender", "2026-07-26T09:12:00Z", "same-sender-confirmation"},
		{courier, "receiver", "2026-07-26T09:12:01Z", "same-receiver-confirmation"},
		{collector, "sender", "2026-07-26T09:12:00Z", "same-sender-confirmation"},
		{courier, "receiver", "2026-07-26T09:12:01Z", "same-receiver-confirmation"},
	}
	var wait sync.WaitGroup
	errors := make(chan error, len(confirmations))
	for _, confirmation := range confirmations {
		confirmation := confirmation
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, confirmErr := service.confirmHandoff(
				context.Background(),
				confirmation.principal,
				core.Map{
					"handoffId":        handoffID,
					"confirmationSide": confirmation.side,
					"confirmedAt":      confirmation.at,
					"idempotencyKey":   confirmation.key,
				},
			)
			errors <- confirmErr
		}()
	}
	wait.Wait()
	close(errors)
	for confirmErr := range errors {
		if confirmErr != nil {
			t.Fatalf("concurrent confirmation: %v", confirmErr)
		}
	}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		if state.Samples["sample"].CurrentHolderID != courier.ID {
			t.Fatalf(
				"current holder=%q, want %q",
				state.Samples["sample"].CurrentHolderID,
				courier.ID,
			)
		}
		handoff := state.Handoffs[handoffID]
		if handoff.Status != "confirmed" ||
			handoff.SenderConfirmedAt == nil ||
			handoff.ReceiverConfirmedAt == nil {
			t.Fatalf("handoff not atomically confirmed: %+v", handoff)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentHolderCannotTakeOverAnotherWorkersAssignment(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	owner := createTestUser(t, service, admin, "leg-owner", []string{"delivery_partner"}, nil)
	holder := createTestUser(t, service, admin, "custody-holder", []string{"delivery_partner"}, nil)
	acceptedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)

	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		state.Requests["request"] = &mongostore.CollectionRequest{
			ID: "request", DeliveryDeadline: acceptedAt.Add(3 * time.Hour),
		}
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", RequestID: "request", Status: "sealed",
			ActiveJourneyID: "journey", CurrentHolderID: holder.ID,
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", PrimarySampleID: "sample", SampleIDs: []string{"sample"},
			Status: "active",
		}
		state.Assignments["assignment"] = &mongostore.Assignment{
			ID: "assignment", JourneyID: "journey", SampleID: "sample",
			Kind: "transport", LegSequence: 1, WorkerID: owner.ID,
			Status: "accepted", AcceptedAt: pointerTime(acceptedAt),
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := service.updateProgress(context.Background(), holder, core.Map{
		"assignmentId": "assignment", "status": "picked_up",
		"occurredAt": "2026-07-26T09:10:00Z", "idempotencyKey": "holder-takeover",
	})
	assertHTTPErrorCode(t, err, "assignment_not_ready")

	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		assignment := state.Assignments["assignment"]
		if assignment.WorkerID != owner.ID || assignment.Status != "accepted" {
			t.Fatalf("assignment mutated after rejected takeover: %+v", assignment)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHandlingCheckpointCannotRegressReceivedSample(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	courier := createTestUser(t, service, admin, "terminal-checkpoint-courier", []string{"delivery_partner"}, nil)
	createdAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", Status: "received", ActiveJourneyID: "journey",
			CurrentHolderID: courier.ID, ContainerType: "cold-box",
			TemperatureMinC: 2, TemperatureMaxC: 8,
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", Status: "active", SampleIDs: []string{"sample"},
		}
		state.Assignments["assignment"] = &mongostore.Assignment{
			ID: "assignment", JourneyID: "journey", SampleID: "sample",
			Kind: "transport", WorkerID: courier.ID, Status: "delivered", CreatedAt: createdAt,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := service.recordHandling(context.Background(), courier, core.Map{
		"sampleId": "sample", "assignmentId": "assignment",
		"containerType": "cold-box", "observedTemperatureC": 20.0,
		"recordedAt": "2026-07-26T10:00:00Z", "idempotencyKey": "terminal-checkpoint",
	})
	assertHTTPErrorCode(t, err, "terminal_sample")
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		if state.Samples["sample"].Status != "received" || len(state.Checkpoints) != 0 {
			t.Fatalf("terminal checkpoint mutated state: sample=%+v checkpoints=%d", state.Samples["sample"], len(state.Checkpoints))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHandlingCheckpointCannotPrecedeAssignment(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	courier := createTestUser(t, service, admin, "backdated-checkpoint-courier", []string{"delivery_partner"}, nil)
	createdAt := time.Date(2026, 7, 26, 9, 30, 0, 0, time.UTC)
	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", Status: "sealed", ActiveJourneyID: "journey",
			CurrentHolderID: courier.ID, ContainerType: "cold-box",
			TemperatureMinC: 2, TemperatureMaxC: 8,
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", Status: "active", SampleIDs: []string{"sample"},
		}
		state.Assignments["assignment"] = &mongostore.Assignment{
			ID: "assignment", JourneyID: "journey", SampleID: "sample",
			Kind: "transport", WorkerID: courier.ID, Status: "pending", CreatedAt: createdAt,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	input := core.Map{
		"sampleId": "sample", "assignmentId": "assignment",
		"containerType": "cold-box", "observedTemperatureC": 5.0,
		"recordedAt": "2026-07-26T09:29:00Z", "idempotencyKey": "before-created",
	}
	_, err := service.recordHandling(context.Background(), courier, input)
	assertHTTPErrorCode(t, err, "invalid_checkpoint_time")

	acceptedAt := time.Date(2026, 7, 26, 9, 40, 0, 0, time.UTC)
	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		assignment := state.Assignments["assignment"]
		assignment.CreatedAt = time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
		assignment.Status, assignment.AcceptedAt = "accepted", &acceptedAt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	input["recordedAt"], input["idempotencyKey"] = "2026-07-26T09:39:00Z", "before-accepted"
	_, err = service.recordHandling(context.Background(), courier, input)
	assertHTTPErrorCode(t, err, "invalid_checkpoint_time")
}

func TestNonFinalTransportDeliveryDoesNotReadySampleForLaboratory(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	courier := createTestUser(t, service, admin, "first-leg-courier", []string{"delivery_partner"}, nil)
	acceptedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)

	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		state.Requests["request"] = &mongostore.CollectionRequest{
			ID: "request", DeliveryDeadline: acceptedAt.Add(3 * time.Hour),
		}
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", RequestID: "request", Status: "sealed",
			ActiveJourneyID: "journey", CurrentHolderID: courier.ID,
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", PrimarySampleID: "sample", SampleIDs: []string{"sample"},
			Status: "active",
		}
		state.Assignments["leg-1"] = &mongostore.Assignment{
			ID: "leg-1", JourneyID: "journey", SampleID: "sample",
			Kind: "transport", LegSequence: 1, WorkerID: courier.ID,
			Status: "accepted", AcceptedAt: pointerTime(acceptedAt), ToLocationID: "node",
		}
		state.Assignments["leg-2"] = &mongostore.Assignment{
			ID: "leg-2", JourneyID: "journey", SampleID: "sample",
			Kind: "transport", LegSequence: 2, Status: "pending",
			FromLocationID: "node", ToLocationID: "lab",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for index, status := range []string{"picked_up", "in_progress", "arrived", "delivered"} {
		if _, err := service.updateProgress(context.Background(), courier, core.Map{
			"assignmentId": "leg-1", "status": status,
			"occurredAt":     time.Date(2026, 7, 26, 9, 10+index, 0, 0, time.UTC).Format(time.RFC3339),
			"idempotencyKey": "leg-1-progress-" + core.String(index),
		}); err != nil {
			t.Fatalf("progress %s: %v", status, err)
		}
	}

	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		if state.Assignments["leg-1"].Status != "delivered" {
			t.Fatalf("first leg status=%q", state.Assignments["leg-1"].Status)
		}
		if state.Samples["sample"].Status != "in_transit" {
			t.Fatalf("sample status=%q, want in_transit", state.Samples["sample"].Status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLaboratoryReceiptRejectsUnrelatedAssignedOperator(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	courier := createTestUser(t, service, admin, "receipt-courier", []string{"delivery_partner"}, nil)
	operatorA := createTestUser(t, service, admin, "lab-operator-a", []string{"laboratory_team"}, nil)
	operatorB := createTestUser(t, service, admin, "lab-operator-b", []string{"laboratory_team"}, nil)
	deliveredAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	acceptedAt := deliveredAt.Add(-time.Hour)

	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		state.Locations["lab"] = laboratoryLocation("lab")
		state.Acceptances["lab|type"] = &mongostore.Acceptance{
			ID: "acceptance", LaboratoryID: "lab", SampleTypeID: "type", Accepts: true,
		}
		state.Requests["request"] = &mongostore.CollectionRequest{
			ID: "request", DeliveryDeadline: deliveredAt.Add(time.Hour),
		}
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", RequestID: "request", SampleTypeID: "type",
			Status: "delivered", ActiveJourneyID: "journey",
			TargetLaboratoryID: "lab", CurrentHolderID: courier.ID,
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", PrimarySampleID: "sample", SampleIDs: []string{"sample"},
			Status: "active",
		}
		state.Assignments["final-leg"] = &mongostore.Assignment{
			ID: "final-leg", JourneyID: "journey", SampleID: "sample",
			Kind: "transport", LegSequence: 1, ToLocationID: "lab",
			WorkerID: courier.ID, Status: "delivered",
			AcceptedAt: pointerTime(acceptedAt), CompletedAt: pointerTime(deliveredAt),
		}
		state.Assignments["lab-work"] = &mongostore.Assignment{
			ID: "lab-work", JourneyID: "journey", SampleID: "sample",
			Kind: "laboratory", ToLocationID: "lab", WorkerID: operatorB.ID,
			Status: "pending",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := service.recordLabException(context.Background(), operatorA, core.Map{
		"sampleId": "sample", "laboratoryId": "lab", "reason": "unauthorized",
		"observedAt": "2026-07-26T10:04:00Z", "idempotencyKey": "wrong-exception-operator",
	})
	assertHTTPErrorCode(t, err, "wrong_laboratory_operator")

	_, err = service.recordLabException(context.Background(), operatorB, core.Map{
		"sampleId": "sample", "laboratoryId": "another-lab", "reason": "wrong-lab",
		"observedAt": "2026-07-26T10:04:00Z", "idempotencyKey": "wrong-exception-lab",
	})
	assertHTTPErrorCode(t, err, "wrong_laboratory")

	_, err = service.confirmReceipt(context.Background(), operatorB, core.Map{
		"sampleId": "sample", "laboratoryId": "lab",
		"receivedAt": "2026-07-26T11:01:00Z", "idempotencyKey": "late-receipt",
	})
	assertHTTPErrorCode(t, err, "delivery_deadline_exceeded")

	_, err = service.confirmReceipt(context.Background(), operatorA, core.Map{
		"sampleId": "sample", "laboratoryId": "lab",
		"receivedAt": "2026-07-26T10:05:00Z", "idempotencyKey": "wrong-operator",
	})
	assertHTTPErrorCode(t, err, "wrong_laboratory_operator")

	receipt, err := service.confirmReceipt(context.Background(), operatorB, core.Map{
		"sampleId": "sample", "laboratoryId": "lab",
		"receivedAt": "2026-07-26T10:05:00Z", "idempotencyKey": "assigned-operator",
	})
	if err != nil {
		t.Fatalf("assigned operator receipt: %v", err)
	}
	if receipt["status"] != "received" {
		t.Fatalf("receipt=%v", receipt)
	}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		assignment := state.Assignments["lab-work"]
		if assignment.Status != "completed" ||
			assignment.AcceptedAt == nil ||
			assignment.CompletedAt == nil {
			t.Fatalf("laboratory assignment not completed by receipt: %+v", assignment)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLaboratoryExceptionFinalizesHandlingNoncompliance(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	operator := createTestUser(t, service, admin, "exception-operator", []string{"laboratory_team"}, nil)
	requestedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)

	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		state.Locations["lab"] = laboratoryLocation("lab")
		state.Requests["request"] = &mongostore.CollectionRequest{
			ID: "request", RequestedAt: requestedAt, DeliveryDeadline: requestedAt.Add(3 * time.Hour),
		}
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", RequestID: "request", Status: "exception",
			ActiveJourneyID: "journey", TargetLaboratoryID: "lab",
			Exception: core.Map{
				"reason": "handling_noncompliance", "source": "handling_checkpoint",
				"journeyId": "journey", "occurredAt": "2026-07-26T09:20:00Z",
			},
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", PrimarySampleID: "sample", SampleIDs: []string{"sample"},
			Status: "active",
		}
		state.Assignments["lab-work"] = &mongostore.Assignment{
			ID: "lab-work", JourneyID: "journey", SampleID: "sample",
			Kind: "laboratory", ToLocationID: "lab", WorkerID: operator.ID, Status: "pending",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := service.recordLabException(context.Background(), operator, core.Map{
		"sampleId": "sample", "laboratoryId": "lab", "reason": "backdated",
		"observedAt": "2026-07-26T09:19:00Z", "idempotencyKey": "backdated-handling-exception",
	})
	assertHTTPErrorCode(t, err, "invalid_exception_time")

	result, err := service.recordLabException(context.Background(), operator, core.Map{
		"sampleId": "sample", "laboratoryId": "lab", "reason": "temperature_excursion",
		"observedAt": "2026-07-26T09:30:00Z", "idempotencyKey": "finalize-handling-exception",
	})
	if err != nil {
		t.Fatalf("finalize handling exception: %v", err)
	}
	if result["status"] != "exception" {
		t.Fatalf("result=%v", result)
	}
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		if state.Journeys["journey"].Status != "cancelled" {
			t.Fatalf("journey status=%q", state.Journeys["journey"].Status)
		}
		if state.Samples["sample"].ActiveJourneyID != "" ||
			core.String(state.Samples["sample"].Exception["reason"]) != "temperature_excursion" {
			t.Fatalf("sample=%+v", state.Samples["sample"])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLaboratoryExceptionRequiresTargetLaboratoryArrival(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	operator := createTestUser(t, service, admin, "not-ready-lab-operator", []string{"laboratory_team"}, nil)
	requestedAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		state.Locations["lab"] = laboratoryLocation("lab")
		state.Requests["request"] = &mongostore.CollectionRequest{
			ID: "request", RequestedAt: requestedAt, DeliveryDeadline: requestedAt.Add(3 * time.Hour),
		}
		state.Samples["sample"] = &mongostore.Sample{
			ID: "sample", RequestID: "request", Status: "planned",
			ActiveJourneyID: "journey", TargetLaboratoryID: "lab",
		}
		state.Journeys["journey"] = &mongostore.Journey{
			ID: "journey", PrimarySampleID: "sample", SampleIDs: []string{"sample"}, Status: "active",
		}
		state.Assignments["transport"] = &mongostore.Assignment{
			ID: "transport", JourneyID: "journey", SampleID: "sample",
			Kind: "transport", LegSequence: 1, ToLocationID: "lab", Status: "pending",
		}
		state.Assignments["lab-work"] = &mongostore.Assignment{
			ID: "lab-work", JourneyID: "journey", SampleID: "sample",
			Kind: "laboratory", ToLocationID: "lab", WorkerID: operator.ID, Status: "pending",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := service.recordLabException(context.Background(), operator, core.Map{
		"sampleId": "sample", "laboratoryId": "lab", "reason": "too-early",
		"observedAt": "2026-07-26T09:30:00Z", "idempotencyKey": "planned-lab-exception",
	})
	assertHTTPErrorCode(t, err, "laboratory_not_ready")

	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		state.Samples["sample"].Status = "in_transit"
		state.Assignments["transport"].Status = "arrived"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.recordLabException(context.Background(), operator, core.Map{
		"sampleId": "sample", "laboratoryId": "lab", "reason": "still-too-early",
		"observedAt": "2026-07-26T09:45:00Z", "idempotencyKey": "transit-lab-exception",
	})
	assertHTTPErrorCode(t, err, "laboratory_not_ready")
}
