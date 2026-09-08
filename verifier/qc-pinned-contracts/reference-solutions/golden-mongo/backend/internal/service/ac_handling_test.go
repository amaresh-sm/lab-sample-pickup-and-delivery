package service

import (
	"context"
	"testing"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// AC-19: the generic container and temperature rules are enforced at collection
// independently of any named sample profile. Sealing with the wrong container or
// an out-of-range observed temperature is refused, and refusing it leaves the
// sample uncollected.
func TestAC19CollectAndSealRefusesNoncompliantContainerOrTemperature(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac19a-requester", []string{"requester"}, nil)
	collector := createTestUser(t, service, admin, "ac19a-collector", []string{"collection_professional"}, []string{"LX-A"})
	createTestUser(t, service, admin, "ac19a-courier", []string{"delivery_partner"}, nil)
	network := buildServiceableNetwork(t, service, admin, "ac19a")

	created, err := service.createCollectionRequest(
		ctx, requester, standardRequest(network, "ac19a-request"),
	)
	if err != nil {
		t.Fatal(err)
	}
	sampleID, journeyID := core.String(created["sampleId"]), core.String(created["journeyId"])
	plan, err := service.readJourneyPlan(ctx, admin, journeyID)
	if err != nil {
		t.Fatal(err)
	}
	collectionID := core.String(plan["collectionAssignmentId"])
	if _, err := service.acceptAssignment(ctx, collector, core.Map{
		"assignmentId": collectionID, "idempotencyKey": "ac19a-accept",
	}); err != nil {
		t.Fatal(err)
	}

	// The Amber profile declares amber-container and 2C..8C.
	for _, attempt := range []struct {
		label       string
		container   string
		temperature float64
	}{
		{"below the range", "amber-container", 1.5},
		{"above the range", "amber-container", 8.5},
		{"far above the range", "amber-container", 25.0},
		{"wrong container", "cooled-box", 5.0},
		{"wrong container and temperature", "cooled-box", 30.0},
	} {
		_, err := service.Handle(ctx, "collect_and_seal_sample", core.Map{
			"sampleId": sampleID, "assignmentId": collectionID,
			"containerType": attempt.container, "observedTemperatureC": attempt.temperature,
			"collectedAt": "2026-07-30T09:05:00Z", "idempotencyKey": "ac19a-" + attempt.label,
		}, collector)
		assertHTTPErrorStatus(t, err, 400)
		assertHTTPErrorCode(t, err, "handling_noncompliant")

		// A refused seal leaves the sample uncollected and unheld.
		if err := service.Store.View(ctx, func(state *mongostore.State) error {
			sample := state.Samples[sampleID]
			if sample.Status != "planned" || sample.SealedAt != nil || sample.CurrentHolderID != "" {
				t.Fatalf("%s: a refused seal changed the sample: %+v", attempt.label, sample)
			}
			if state.Assignments[collectionID].Status != "accepted" {
				t.Fatalf("%s: a refused seal completed the collection assignment", attempt.label)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Both boundary temperatures are compliant, and the range is inclusive, so
	// the refusals above are about non-compliance and not about strictness.
	sealed, err := service.collectAndSeal(ctx, collector, core.Map{
		"sampleId": sampleID, "assignmentId": collectionID,
		"containerType": "amber-container", "observedTemperatureC": 2.0,
		"collectedAt": "2026-07-30T09:05:00Z", "idempotencyKey": "ac19a-compliant",
	})
	if err != nil {
		t.Fatalf("a compliant seal at the lower bound was refused: %v", err)
	}
	if sealed["status"] != "sealed" || !core.Bool(sealed["handlingCompliant"]) {
		t.Fatalf("compliant seal = %v", sealed)
	}
}

// AC-19: the same generic rules are enforced again at every handling checkpoint
// along the route, and a non-compliant reading forces the sample into an
// exception rather than merely being logged.
func TestAC19NoncompliantHandlingCheckpointForcesTheSampleToException(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	fixture := runJourneyToPendingHandoff(t, service, admin, "ac19b")
	for _, confirmation := range []struct {
		p        core.Principal
		side, at string
	}{
		{fixture.collector, "sender", "2026-07-30T09:07:00Z"},
		{fixture.courier, "receiver", "2026-07-30T09:08:00Z"},
	} {
		if _, err := service.confirmHandoff(ctx, confirmation.p, core.Map{
			"handoffId": fixture.handoffID, "confirmationSide": confirmation.side,
			"confirmedAt": confirmation.at, "idempotencyKey": "ac19b-confirm-" + confirmation.side,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// A compliant checkpoint is recorded and changes nothing about the sample.
	compliant, err := service.Handle(ctx, "record_handling_checkpoint", core.Map{
		"sampleId": fixture.sampleID, "assignmentId": fixture.transportID,
		"locationId": fixture.network.nodeID, "containerType": "amber-container",
		"observedTemperatureC": 6.0, "recordedAt": "2026-07-30T09:10:00Z",
		"idempotencyKey": "ac19b-compliant-checkpoint",
	}, fixture.courier)
	if err != nil {
		t.Fatalf("compliant checkpoint: %v", err)
	}
	if !core.Bool(compliant["compliant"]) {
		t.Fatalf("compliant checkpoint = %v", compliant)
	}
	if core.String(compliant["sampleStatus"]) != "sealed" {
		t.Fatalf("a compliant checkpoint moved the sample to %v", compliant["sampleStatus"])
	}

	// A reading outside the sample's temperature range forces an exception.
	breach, err := service.Handle(ctx, "record_handling_checkpoint", core.Map{
		"sampleId": fixture.sampleID, "assignmentId": fixture.transportID,
		"locationId": fixture.network.nodeID, "containerType": "amber-container",
		"observedTemperatureC": 24.0, "recordedAt": "2026-07-30T09:12:00Z",
		"idempotencyKey": "ac19b-noncompliant-checkpoint",
	}, fixture.courier)
	if err != nil {
		t.Fatalf("non-compliant checkpoint was rejected instead of recorded: %v", err)
	}
	if core.Bool(breach["compliant"]) {
		t.Fatalf("an out-of-range reading was recorded as compliant: %v", breach)
	}
	if core.String(breach["sampleStatus"]) != "exception" {
		t.Fatalf("sample status after a non-compliant checkpoint = %v, want exception", breach["sampleStatus"])
	}

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		sample := state.Samples[fixture.sampleID]
		if sample.Status != "exception" {
			t.Fatalf("stored sample status = %q, want exception", sample.Status)
		}
		if core.String(sample.Exception["reason"]) != "handling_noncompliance" ||
			core.String(sample.Exception["source"]) != "handling_checkpoint" {
			t.Fatalf("recorded exception = %v", sample.Exception)
		}
		compliantCount, breachCount := 0, 0
		for _, checkpoint := range state.Checkpoints {
			if checkpoint.SampleID != fixture.sampleID {
				continue
			}
			if checkpoint.Compliant {
				compliantCount++
			} else {
				breachCount++
			}
		}
		if compliantCount != 1 || breachCount != 1 {
			t.Fatalf("stored checkpoints: %d compliant, %d non-compliant", compliantCount, breachCount)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The exception is visible on the sample view with its reason.
	lifecycle, err := service.Handle(
		ctx, "read_sample_lifecycle", core.Map{"sampleId": fixture.sampleID}, admin,
	)
	if err != nil {
		t.Fatal(err)
	}
	if core.String(lifecycle["status"]) != "exception" {
		t.Fatalf("lifecycle status = %v, want exception", lifecycle["status"])
	}
	exception, ok := lifecycle["exception"].(core.Map)
	if !ok || core.String(exception["reason"]) != "handling_noncompliance" {
		t.Fatalf("lifecycle exception = %v", lifecycle["exception"])
	}

	// A sample already in exception is terminal for handling checkpoints.
	_, err = service.Handle(ctx, "record_handling_checkpoint", core.Map{
		"sampleId": fixture.sampleID, "assignmentId": fixture.transportID,
		"locationId": fixture.network.nodeID, "containerType": "amber-container",
		"observedTemperatureC": 5.0, "recordedAt": "2026-07-30T09:14:00Z",
		"idempotencyKey": "ac19b-after-exception",
	}, fixture.courier)
	assertHTTPErrorStatus(t, err, 409)
	assertHTTPErrorCode(t, err, "terminal_sample")

	// The wrong container is refused on the same footing as the wrong
	// temperature: it is a non-compliant reading, not a rejected call.
	second := runJourneyToPendingHandoff(t, service, admin, "ac19c")
	for _, confirmation := range []struct {
		p        core.Principal
		side, at string
	}{
		{second.collector, "sender", "2026-07-30T09:07:00Z"},
		{second.courier, "receiver", "2026-07-30T09:08:00Z"},
	} {
		if _, err := service.confirmHandoff(ctx, confirmation.p, core.Map{
			"handoffId": second.handoffID, "confirmationSide": confirmation.side,
			"confirmedAt": confirmation.at, "idempotencyKey": "ac19c-confirm-" + confirmation.side,
		}); err != nil {
			t.Fatal(err)
		}
	}
	wrongContainer, err := service.Handle(ctx, "record_handling_checkpoint", core.Map{
		"sampleId": second.sampleID, "assignmentId": second.transportID,
		"locationId": second.network.nodeID, "containerType": "cooled-box",
		"observedTemperatureC": 5.0, "recordedAt": "2026-07-30T09:12:00Z",
		"idempotencyKey": "ac19c-wrong-container",
	}, second.courier)
	if err != nil {
		t.Fatalf("wrong-container checkpoint: %v", err)
	}
	if core.Bool(wrongContainer["compliant"]) ||
		core.String(wrongContainer["sampleStatus"]) != "exception" {
		t.Fatalf("wrong-container checkpoint = %v", wrongContainer)
	}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		var stored *mongostore.HandlingCheckpoint
		for _, checkpoint := range state.Checkpoints {
			if checkpoint.SampleID == second.sampleID {
				stored = checkpoint
			}
		}
		if stored == nil || stored.Compliant || stored.ContainerType != "cooled-box" {
			t.Fatalf("stored wrong-container checkpoint = %+v", stored)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
