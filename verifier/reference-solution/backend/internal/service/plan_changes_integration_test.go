package service

import (
	"context"
	"testing"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func TestMergedBatchCanCompleteEverySampleThroughLaboratoryReceipt(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-26T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	dispatcher := createTestUser(t, service, admin, "batch-dispatcher", []string{"dispatcher"}, nil)
	collector := createTestUser(t, service, admin, "batch-collector", []string{"collection_professional"}, nil)
	courier := createTestUser(t, service, admin, "batch-courier", []string{"delivery_partner"}, nil)
	laboratoryWorker := createTestUserWithLocation(t, service, admin, "batch-laboratory", []string{"laboratory_team"}, nil, "laboratory")
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)

	if err := service.Store.Update(ctx, func(state *mongostore.State) error {
		state.Locations["origin"] = collectionPointLocation("origin", 0, 0)
		state.Locations["laboratory"] = laboratoryLocation("laboratory")
		state.SampleTypes["type"] = &mongostore.SampleType{
			ID: "type", Name: "Amber", ContainerType: "vial",
			TemperatureMinC: 2, TemperatureMaxC: 8, BatchingAllowed: true,
		}
		state.Acceptances["acceptance"] = &mongostore.Acceptance{
			ID: "acceptance", LaboratoryID: "laboratory", SampleTypeID: "type", Accepts: true,
		}
		state.Connections["connection"] = &mongostore.Connection{
			ID: "connection", FromLocationID: "origin", ToLocationID: "laboratory",
			TravelMinutes: 30, CostUnits: 1, Active: true,
		}
		for _, resourceID := range []string{"origin", "connection", "laboratory"} {
			state.Capacities[resourceID] = &mongostore.Capacity{
				ResourceID: resourceID, CapacityUnits: 4, ReservedUnits: 2,
			}
		}
		for index, sampleID := range []string{"sample-1", "sample-2"} {
			journeyID := "journey-" + core.String(index+1)
			requestID := "request-" + core.String(index+1)
			state.Requests[requestID] = &mongostore.CollectionRequest{
				ID: requestID, RequestedAt: now,
				DeliveryDeadline: now.Add(2 * time.Hour),
			}
			state.Samples[sampleID] = &mongostore.Sample{
				ID: sampleID, RequestID: requestID, SampleTypeID: "type",
				ContainerType: "vial", TemperatureMinC: 2, TemperatureMaxC: 8,
				Status: "planned", ActiveJourneyID: journeyID,
				TargetLaboratoryID: "laboratory", Version: 1,
			}
			state.Journeys[journeyID] = &mongostore.Journey{
				ID: journeyID, PrimarySampleID: sampleID, SampleIDs: []string{sampleID},
				PlanVersion: 1, RouteMode: "direct", Status: "active",
				EstimatedDeliveryAt: now.Add(30 * time.Minute),
				TotalCostUnits:      1, CapacityUsed: 1, CapacityAvailable: 4,
				CreatedAt: now, UpdatedAt: now,
			}
			state.Stops[journeyID+"-pickup"] = &mongostore.JourneyStop{
				ID: journeyID + "-pickup", JourneyID: journeyID,
				LocationID: "origin", StopType: "pickup", Sequence: 1, EstimatedAt: now,
			}
			state.Stops[journeyID+"-delivery"] = &mongostore.JourneyStop{
				ID: journeyID + "-delivery", JourneyID: journeyID,
				LocationID: "laboratory", StopType: "delivery", Sequence: 2,
				EstimatedAt: now.Add(30 * time.Minute),
			}
			state.Assignments[journeyID+"-collection"] = newAssignment(
				journeyID+"-collection", journeyID, sampleID, "collection", 0,
				"", "", collector.ID, now,
			)
			state.Assignments[journeyID+"-transport"] = newAssignment(
				journeyID+"-transport", journeyID, sampleID, "transport", 1,
				"origin", "laboratory", courier.ID, now,
			)
			state.Assignments[journeyID+"-laboratory"] = newAssignment(
				journeyID+"-laboratory", journeyID, sampleID, "laboratory", 2,
				"origin", "laboratory", laboratoryWorker.ID, now,
			)
			for _, resourceID := range []string{"origin", "connection", "laboratory"} {
				reservationID := journeyID + "-" + resourceID
				state.Reservations[reservationID] = &mongostore.Reservation{
					ID: reservationID, JourneyID: journeyID, ResourceID: resourceID,
					Units: 1, Status: "active",
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := service.editPlan(ctx, dispatcher, core.Map{
		"journeyId": "journey-1", "expectedPlanVersion": 1,
		"changeType":     "merge_samples",
		"changes":        core.Map{"sampleIds": []string{"sample-1", "sample-2"}},
		"idempotencyKey": "merge-batch",
	}); err != nil {
		t.Fatalf("merge batch: %v", err)
	}

	type sampleAssignments struct {
		collection, transport string
	}
	assignmentIDs := map[string]sampleAssignments{}
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		journey := state.Journeys["journey-1"]
		if err := validateJourneyPlan(state, journey); err != nil {
			t.Fatalf("merged journey invalid: %v", err)
		}
		for _, sampleID := range journey.SampleIDs {
			ids := sampleAssignments{}
			for _, assignment := range assignmentsForSample(state, sampleID) {
				if assignment.JourneyID != journey.ID || assignment.Status == "cancelled" {
					continue
				}
				switch assignment.Kind {
				case "collection":
					ids.collection = assignment.ID
				case "transport":
					ids.transport = assignment.ID
				}
			}
			if ids.collection == "" || ids.transport == "" {
				t.Fatalf("sample %s lacks batch assignments: %+v", sampleID, ids)
			}
			assignmentIDs[sampleID] = ids
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for index, sampleID := range []string{"sample-1", "sample-2"} {
		ids := assignmentIDs[sampleID]
		if _, err := service.acceptAssignment(ctx, collector, core.Map{
			"assignmentId":   ids.collection,
			"idempotencyKey": "accept-collection-" + sampleID,
		}); err != nil {
			t.Fatalf("accept collection %s: %v", sampleID, err)
		}
		collectedAt := now.Add(time.Duration(5+index) * time.Minute)
		if _, err := service.collectAndSeal(ctx, collector, core.Map{
			"sampleId": sampleID, "assignmentId": ids.collection,
			"containerType": "vial", "observedTemperatureC": 5,
			"collectedAt":    collectedAt.Format(time.RFC3339),
			"idempotencyKey": "collect-" + sampleID,
		}); err != nil {
			t.Fatalf("collect %s: %v", sampleID, err)
		}
		if _, err := service.acceptAssignment(ctx, courier, core.Map{
			"assignmentId":   ids.transport,
			"idempotencyKey": "accept-transport-" + sampleID,
		}); err != nil {
			t.Fatalf("accept transport %s: %v", sampleID, err)
		}
		handoff, err := service.initiateHandoff(ctx, collector, core.Map{
			"sampleId": sampleID, "receiverId": courier.ID,
			"fromAssignmentId": ids.collection, "toAssignmentId": ids.transport,
			"initiatedAt":    now.Add(time.Duration(10+index) * time.Minute).Format(time.RFC3339),
			"idempotencyKey": "handoff-" + sampleID,
		})
		if err != nil {
			t.Fatalf("initiate handoff %s: %v", sampleID, err)
		}
		handoffID := core.String(handoff["handoffId"])
		for confirmationIndex, confirmation := range []struct {
			principal core.Principal
			side      string
		}{
			{collector, "sender"},
			{courier, "receiver"},
		} {
			if _, err := service.confirmHandoff(ctx, confirmation.principal, core.Map{
				"handoffId": handoffID, "confirmationSide": confirmation.side,
				"confirmedAt":    now.Add(time.Duration(12+index+confirmationIndex) * time.Minute).Format(time.RFC3339),
				"idempotencyKey": "confirm-" + confirmation.side + "-" + sampleID,
			}); err != nil {
				t.Fatalf("confirm %s handoff %s: %v", confirmation.side, sampleID, err)
			}
		}
		deliveredAt := now.Add(time.Duration(30+index) * time.Minute)
		if _, err := service.updateProgress(ctx, courier, core.Map{
			"assignmentId": ids.transport, "status": "delivered",
			"occurredAt":     deliveredAt.Format(time.RFC3339),
			"idempotencyKey": "deliver-" + sampleID,
		}); err != nil {
			t.Fatalf("deliver %s: %v", sampleID, err)
		}
		if _, err := service.confirmReceipt(ctx, laboratoryWorker, core.Map{
			"sampleId": sampleID, "laboratoryId": "laboratory",
			"receivedAt":     deliveredAt.Add(time.Minute).Format(time.RFC3339),
			"idempotencyKey": "receipt-" + sampleID,
		}); err != nil {
			t.Fatalf("receive %s: %v", sampleID, err)
		}
		if index == 0 {
			if err := service.Store.View(ctx, func(state *mongostore.State) error {
				if state.Journeys["journey-1"].Status != "active" {
					t.Fatal("batch journey completed before every sample was received")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if state.Journeys["journey-1"].Status != "completed" {
			t.Fatalf("batch journey status=%q, want completed", state.Journeys["journey-1"].Status)
		}
		for _, sampleID := range []string{"sample-1", "sample-2"} {
			if state.Samples[sampleID].Status != "received" {
				t.Fatalf("sample %s status=%q, want received", sampleID, state.Samples[sampleID].Status)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
