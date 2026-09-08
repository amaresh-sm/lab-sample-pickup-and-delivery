package service

import (
	"context"
	"sort"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func (s *Service) acceptAssignment(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "collection_professional", "delivery_partner", "node_handler", "laboratory_team"); err != nil {
		return nil, err
	}
	assignmentID, key := core.String(in["assignmentId"]), core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	return s.mutate(ctx, p, "accept_assignment", key, in, func(state *mongostore.State) (txResult, error) {
		assignment := state.Assignments[assignmentID]
		if assignment == nil {
			return txResult{}, notFound("assignment")
		}
		// An assignment may only be accepted by the worker it was offered to. That
		// includes the case where nobody has been offered it yet: work with no
		// offered worker is not available to anyone, and must first be offered
		// through the fair-assignment path before it can be accepted.
		if assignment.Status == "pending" && assignment.WorkerID != p.ID {
			if assignment.WorkerID == "" {
				return txResult{}, core.Fail(
					409,
					"offer_not_offered",
					"assignment has not been offered to a worker yet",
				)
			}
			return txResult{}, core.Fail(409, "offer_not_available", "assignment is offered to a different worker")
		}
		requiredRole := roleForAssignmentKind(assignment.Kind)
		if requiredRole == "" || !p.Has(requiredRole) {
			return txResult{}, core.Fail(403, "ineligible_worker", "worker role is not eligible for this assignment")
		}
		user := state.Users[p.ID]
		if user == nil || !user.Active || !user.Available || activeWorkerAssignments(state, p.ID) >= user.CapacityUnits {
			return txResult{}, core.Fail(409, "worker_unavailable", "worker is inactive, unavailable, or at capacity")
		}
		sample := state.Samples[assignment.SampleID]
		if sample == nil {
			return txResult{}, notFound("sample")
		}
		journey := state.Journeys[assignment.JourneyID]
		if journey == nil || journey.Status != "active" || sample.ActiveJourneyID != assignment.JourneyID {
			return txResult{}, core.Fail(409, "assignment_inactive", "assignment does not belong to the sample's active journey")
		}
		sampleType := state.SampleTypes[sample.SampleTypeID]
		if assignment.Kind == "collection" && sampleType != nil &&
			sampleType.QualifiedCollectorRequired &&
			!userHasActiveCertification(user, sampleType.RequiredQualification) {
			return txResult{}, core.Fail(400, "ineligible_worker", "collector lacks the required qualification")
		}
		if assignment.Status != "pending" {
			if assignment.Status == "accepted" && assignment.WorkerID == p.ID {
				return txResult{body: core.Map{"assignmentId": assignmentID, "workerId": p.ID, "status": "accepted"}}, nil
			}
			return txResult{}, core.Fail(409, "assignment_taken", "assignment is no longer available")
		}
		now := core.Now()
		assignment.WorkerID, assignment.Status = p.ID, "accepted"
		assignment.AcceptedAt, assignment.OfferExpiresAt = &now, nil
		user.LastAssignedAt, user.UpdatedAt = &now, now
		out := core.Map{"assignmentId": assignmentID, "workerId": p.ID, "status": "accepted"}
		audit(state, p, "assignment", assignmentID, "assignment_accepted", out)
		return txResult{body: out, eventID: emit(state, []string{p.ID, "dispatcher"}, "assignment.accepted", "assignment", assignmentID, out)}, nil
	})
}

func (s *Service) declineAssignment(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "collection_professional", "delivery_partner", "node_handler", "laboratory_team"); err != nil {
		return nil, err
	}
	assignmentID, key := core.String(in["assignmentId"]), core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	declinedAt := core.Now()
	if value := core.String(in["declinedAt"]); value != "" {
		parsed, err := core.Time(value)
		if err != nil {
			return nil, err
		}
		declinedAt = parsed
	}
	return s.mutate(ctx, p, "decline_assignment", key, in, func(state *mongostore.State) (txResult, error) {
		assignment := state.Assignments[assignmentID]
		if assignment == nil {
			return txResult{}, notFound("assignment")
		}
		if assignment.Status != "pending" || assignment.WorkerID != p.ID {
			return txResult{}, core.Fail(409, "offer_not_available", "only the currently offered worker may decline")
		}
		assignment.Declines = append(assignment.Declines, mongostore.AssignmentDecline{
			WorkerID: p.ID, Reason: core.String(in["reason"]), At: declinedAt,
		})
		excluded := map[string]bool{}
		for _, decline := range assignment.Declines {
			excluded[decline.WorkerID] = true
		}
		sample := state.Samples[assignment.SampleID]
		sampleType := state.SampleTypes[sample.SampleTypeID]
		role, qualification, qualified := "delivery_partner", "", false
		if assignment.Kind == "collection" {
			role = "collection_professional"
			if sampleType != nil {
				qualification, qualified = sampleType.RequiredQualification, sampleType.QualifiedCollectorRequired
			}
		} else if assignment.Kind == "node" {
			role = "node_handler"
		} else if assignment.Kind == "laboratory" {
			role = "laboratory_team"
		}
		// A "node" or "laboratory" re-offer is scoped to the assignment's own
		// location: only a node_handler or laboratory_team worker assigned to
		// that same location is eligible. Transport (delivery_partner) work is
		// never location-scoped.
		locationID := ""
		if assignment.Kind == "node" || assignment.Kind == "laboratory" {
			locationID = assignment.ToLocationID
		}
		next := fairWorker(state, role, qualification, qualified, excluded, locationID)
		if next == "" {
			// A decline is not a permanent exclusion. Start a fresh offer cycle,
			// excluding only the worker who just declined to avoid an immediate bounce.
			next = fairWorker(state, role, qualification, qualified, map[string]bool{p.ID: true}, locationID)
		}
		assignment.WorkerID, assignment.AcceptedAt = next, nil
		if next != "" {
			assignment.OfferExpiresAt = pointerTime(core.Now().Add(assignmentOfferTTL()))
		} else {
			assignment.OfferExpiresAt = nil
		}
		out := core.Map{
			"assignmentId": assignmentID, "status": "pending",
			"declinedBy": p.ID, "declinedAt": formatTime(declinedAt),
		}
		if next != "" {
			out["reofferedTo"] = next
		}
		audit(state, p, "assignment", assignmentID, "assignment_declined", out)
		audience := []string{p.ID, "dispatcher"}
		if next != "" {
			audience = append(audience, next)
		}
		return txResult{body: out, eventID: emit(state, audience, "assignment.declined", "assignment", assignmentID, out)}, nil
	})
}

func (s *Service) collectAndSeal(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "collection_professional"); err != nil {
		return nil, err
	}
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	collectedAt, err := core.Time(in["collectedAt"])
	if err != nil {
		return nil, err
	}
	sampleID, assignmentID := core.String(in["sampleId"]), core.String(in["assignmentId"])
	return s.mutate(ctx, p, "collect_and_seal_sample", key, in, func(state *mongostore.State) (txResult, error) {
		assignment := state.Assignments[assignmentID]
		if assignment == nil || assignment.SampleID != sampleID {
			return txResult{}, notFound("assignment")
		}
		sample := state.Samples[sampleID]
		if sample == nil {
			return txResult{}, notFound("sample")
		}
		journey := state.Journeys[assignment.JourneyID]
		if journey == nil || journey.Status != "active" || sample.ActiveJourneyID != assignment.JourneyID {
			return txResult{}, core.Fail(409, "assignment_inactive", "collection assignment is not part of the active journey")
		}
		temperature := core.Float(in["observedTemperatureC"])
		if core.String(in["containerType"]) != sample.ContainerType ||
			temperature < sample.TemperatureMinC || temperature > sample.TemperatureMaxC {
			return txResult{}, core.Fail(400, "handling_noncompliant", "container or temperature violates handling rules")
		}
		if assignment.Kind != "collection" || assignment.Status != "accepted" || assignment.WorkerID != p.ID {
			return txResult{}, core.Fail(409, "collection_not_ready", "collector must accept the collection assignment first")
		}
		sampleType := state.SampleTypes[sample.SampleTypeID]
		if sampleType != nil &&
			sampleType.QualifiedCollectorRequired &&
			!userHasActiveCertification(state.Users[p.ID], sampleType.RequiredQualification) {
			return txResult{}, core.Fail(409, "certification_inactive", "collector certification is no longer active")
		}
		if sample.Status != "planned" && sample.Status != "requested" {
			return txResult{}, core.Fail(409, "invalid_sample_state", "sample cannot be collected in its current state")
		}
		request := state.Requests[sample.RequestID]
		if request == nil ||
			collectedAt.Before(request.RequestedAt) ||
			collectedAt.After(request.DeliveryDeadline) ||
			assignment.AcceptedAt != nil && collectedAt.Before(*assignment.AcceptedAt) {
			return txResult{}, core.Fail(409, "invalid_collection_time", "collection time is outside the active request window")
		}
		// The collection window and the delivery deadline are separate
		// constraints. A collection that is late for the window but still ahead
		// of the deadline is rejected here, with its own code.
		earliestCollection, latestCollection := requestCollectionWindow(request)
		if collectedAt.Before(earliestCollection) || collectedAt.After(latestCollection) {
			return txResult{}, core.Fail(
				409,
				"outside_collection_window",
				"collection must take place inside the request's collection window",
			)
		}
		sample.Status, sample.SealedAt, sample.CurrentHolderID = "sealed", pointerTime(collectedAt), p.ID
		sample.Version++
		sample.UpdatedAt = core.Now()
		assignment.Status, assignment.CompletedAt = "completed", pointerTime(collectedAt)
		out := core.Map{
			"sampleId": sampleID, "status": "sealed", "currentHolderId": p.ID,
			"sealedAt": formatTime(collectedAt), "handlingCompliant": true,
		}
		audit(state, p, "sample", sampleID, "sample_collected_and_sealed", out)
		return txResult{body: out, eventID: emit(state, []string{p.ID, "dispatcher"}, "sample.sealed", "sample", sampleID, out)}, nil
	})
}

func (s *Service) initiateHandoff(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "collection_professional", "delivery_partner", "node_handler"); err != nil {
		return nil, err
	}
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	initiatedAt, err := core.Time(in["initiatedAt"])
	if err != nil {
		return nil, err
	}
	sampleID, receiverID := core.String(in["sampleId"]), core.String(in["receiverId"])
	return s.mutate(ctx, p, "initiate_handoff", key, in, func(state *mongostore.State) (txResult, error) {
		sample := state.Samples[sampleID]
		if sample == nil {
			return txResult{}, notFound("sample")
		}
		if sample.SealedAt == nil || sample.CurrentHolderID != p.ID {
			return txResult{}, core.Fail(409, "not_current_holder", "only the current holder of a sealed sample may initiate a handoff")
		}
		receiver := state.Users[receiverID]
		if receiver == nil || !receiver.Active || receiverID == p.ID {
			return txResult{}, core.Fail(400, "invalid_receiver", "receiver must be a different active user")
		}
		fromAssignmentID, toAssignmentID := core.String(in["fromAssignmentId"]), core.String(in["toAssignmentId"])
		toAssignmentID = resolveHandoffTargetAssignmentID(
			state,
			sample,
			fromAssignmentID,
			toAssignmentID,
		)
		if err := prepareTransportReleaseForHandoff(
			state, sample, p, state.Assignments[fromAssignmentID], state.Assignments[toAssignmentID], initiatedAt,
		); err != nil {
			return txResult{}, err
		}
		if err := validateHandoffRoute(
			state, sample, p.ID, receiverID, fromAssignmentID, toAssignmentID,
		); err != nil {
			return txResult{}, err
		}
		for _, handoff := range state.Handoffs {
			if handoff.SampleID == sampleID && handoff.Status == "pending" {
				return txResult{}, core.Fail(409, "pending_handoff_exists", "sample already has a pending handoff")
			}
		}
		handoffID := core.UUID()
		state.Handoffs[handoffID] = &mongostore.Handoff{
			ID: handoffID, SampleID: sampleID,
			FromAssignmentID: fromAssignmentID,
			ToAssignmentID:   toAssignmentID,
			SenderID:         p.ID, ReceiverID: receiverID,
			Status: "pending", InitiatedAt: initiatedAt,
		}
		out := core.Map{
			"handoffId": handoffID, "senderId": p.ID, "receiverId": receiverID,
			"currentHolderId": p.ID, "status": "pending",
		}
		audit(state, p, "handoff", handoffID, "handoff_initiated", core.Map{"sampleId": sampleID})
		return txResult{body: out, eventID: emit(state, []string{p.ID, receiverID, "dispatcher"}, "handoff.initiated", "handoff", handoffID, out)}, nil
	})
}

// resolveHandoffTargetAssignmentID supports clients that identify a completed
// collection assignment as the handoff boundary without separately supplying
// the first transport assignment. The substitution is deliberately limited to
// a self-reference at the collection-to-transport boundary; all normal route,
// worker, lifecycle, and custody validation still runs against the resolved
// first transport leg.
func resolveHandoffTargetAssignmentID(
	state *mongostore.State,
	sample *mongostore.Sample,
	fromAssignmentID, toAssignmentID string,
) string {
	if sample == nil ||
		fromAssignmentID == "" ||
		fromAssignmentID != toAssignmentID {
		return toAssignmentID
	}
	fromAssignment := state.Assignments[fromAssignmentID]
	if fromAssignment == nil ||
		fromAssignment.Kind != "collection" ||
		fromAssignment.SampleID != sample.ID ||
		fromAssignment.JourneyID == "" ||
		fromAssignment.JourneyID != sample.ActiveJourneyID {
		return toAssignmentID
	}
	var target *mongostore.Assignment
	for _, assignment := range state.Assignments {
		if assignment.Kind != "transport" ||
			assignment.Status == "cancelled" ||
			assignment.SampleID != sample.ID ||
			assignment.JourneyID != fromAssignment.JourneyID ||
			assignment.LegSequence != 1 {
			continue
		}
		if target == nil || assignment.ID < target.ID {
			target = assignment
		}
	}
	if target == nil {
		return toAssignmentID
	}
	return target.ID
}

func (s *Service) confirmHandoff(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	confirmedAt, err := core.Time(in["confirmedAt"])
	if err != nil {
		return nil, err
	}
	handoffID, side := core.String(in["handoffId"]), core.String(in["confirmationSide"])
	if side != "sender" && side != "receiver" {
		return nil, core.Fail(400, "invalid_confirmation_side", "confirmationSide must be sender or receiver")
	}
	return s.mutate(ctx, p, "confirm_handoff", key, in, func(state *mongostore.State) (txResult, error) {
		handoff := state.Handoffs[handoffID]
		if handoff == nil {
			return txResult{}, notFound("handoff")
		}
		if side == "sender" && p.ID != handoff.SenderID || side == "receiver" && p.ID != handoff.ReceiverID {
			return txResult{}, core.Fail(403, "forbidden", "only the named sender or receiver may confirm")
		}
		if handoff.Status == "confirmed" {
			return txResult{body: core.Map{
				"handoffId": handoffID, "status": "confirmed",
				"senderConfirmed": true, "receiverConfirmed": true,
				"currentHolderId": handoff.ReceiverID,
			}}, nil
		}
		if handoff.Status != "pending" {
			return txResult{}, core.Fail(409, "handoff_inactive", "handoff is not pending")
		}
		if confirmedAt.Before(handoff.InitiatedAt) {
			return txResult{}, core.Fail(409, "invalid_confirmation_time", "handoff confirmation cannot precede initiation")
		}
		sample := state.Samples[handoff.SampleID]
		if sample == nil {
			return txResult{}, notFound("sample")
		}
		if sample.CurrentHolderID != handoff.SenderID {
			return txResult{}, core.Fail(409, "custody_conflict", "sample holder changed before handoff completion")
		}
		if err := validateHandoffRoute(
			state, sample, handoff.SenderID, handoff.ReceiverID,
			handoff.FromAssignmentID, handoff.ToAssignmentID,
		); err != nil {
			return txResult{}, err
		}
		if side == "sender" && handoff.SenderConfirmedAt == nil {
			handoff.SenderConfirmedAt = pointerTime(confirmedAt)
		}
		if side == "receiver" && handoff.ReceiverConfirmedAt == nil {
			handoff.ReceiverConfirmedAt = pointerTime(confirmedAt)
		}
		status, currentHolder := "pending", handoff.SenderID
		if handoff.SenderConfirmedAt != nil && handoff.ReceiverConfirmedAt != nil {
			completedAt := *handoff.SenderConfirmedAt
			if handoff.ReceiverConfirmedAt.After(completedAt) {
				completedAt = *handoff.ReceiverConfirmedAt
			}
			fromAssignment := state.Assignments[handoff.FromAssignmentID]
			toAssignment := state.Assignments[handoff.ToAssignmentID]
			receiver := state.Users[handoff.ReceiverID]
			if usesDurableNodeBridge(state, fromAssignment, toAssignment, receiver) {
				if err := acceptNodeBridgeAssignment(
					state, p, fromAssignment, toAssignment, handoff.ReceiverID, completedAt,
				); err != nil {
					return txResult{}, err
				}
			} else if err := acceptHandoffReceiverAssignment(
				state, p, handoff.ToAssignmentID, handoff.ReceiverID, completedAt,
			); err != nil {
				return txResult{}, err
			}
			completeNodeBridgeAssignment(
				state, p, fromAssignment, toAssignment, handoff.SenderID, completedAt,
			)
			handoff.Status, handoff.CompletedAt = "confirmed", pointerTime(completedAt)
			sample.CurrentHolderID, sample.UpdatedAt = handoff.ReceiverID, core.Now()
			sample.Version++
			status, currentHolder = "confirmed", handoff.ReceiverID
		}
		out := core.Map{
			"handoffId": handoffID, "status": status,
			"senderConfirmed":   handoff.SenderConfirmedAt != nil,
			"receiverConfirmed": handoff.ReceiverConfirmedAt != nil,
			"currentHolderId":   currentHolder,
		}
		audit(state, p, "handoff", handoffID, "handoff_confirmed_"+side, out)
		return txResult{body: out, eventID: emit(state, []string{handoff.SenderID, handoff.ReceiverID, "dispatcher"}, "handoff."+status, "handoff", handoffID, out)}, nil
	})
}

func (s *Service) updateProgress(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "delivery_partner", "node_handler"); err != nil {
		return nil, err
	}
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	occurredAt, err := core.Time(in["occurredAt"])
	if err != nil {
		return nil, err
	}
	assignmentID, wanted := core.String(in["assignmentId"]), core.String(in["status"])
	return s.mutate(ctx, p, "update_assignment_progress", key, in, func(state *mongostore.State) (txResult, error) {
		assignment := state.Assignments[assignmentID]
		if assignment == nil {
			return txResult{}, notFound("assignment")
		}
		sample := state.Samples[assignment.SampleID]
		if sample == nil {
			return txResult{}, notFound("sample")
		}
		journey := state.Journeys[assignment.JourneyID]
		if journey == nil || journey.Status != "active" || sample.ActiveJourneyID != assignment.JourneyID {
			return txResult{}, core.Fail(409, "assignment_inactive", "assignment does not belong to the sample's active journey")
		}
		if assignment.WorkerID != p.ID || assignment.AcceptedAt == nil || assignment.Status == "pending" {
			return txResult{}, core.Fail(409, "assignment_not_ready", "only the accepted assignee may progress this assignment")
		}
		requiredRole := roleForAssignmentKind(assignment.Kind)
		if requiredRole == "" || !p.Has(requiredRole) {
			return txResult{}, core.Fail(403, "ineligible_worker", "worker role is not eligible to progress this assignment")
		}
		if assignment.Kind == "transport" &&
			sample.Status != "sealed" &&
			sample.Status != "in_transit" &&
			sample.Status != "delivered" {
			return txResult{}, core.Fail(409, "collection_required", "transport cannot begin before collection and sealing")
		}
		if occurredAt.Before(*assignment.AcceptedAt) {
			return txResult{}, core.Fail(409, "invalid_progress_time", "assignment progress cannot precede acceptance")
		}
		finalTransport := finalTransportAssignmentForSample(state, assignment.JourneyID, assignment.SampleID)
		directFinalDelivery := assignment.Kind == "transport" &&
			finalTransport != nil &&
			finalTransport.ID == assignment.ID &&
			wanted == "delivered" &&
			sample.CurrentHolderID == p.ID &&
			transportAssignmentCountForSample(state, assignment.JourneyID, assignment.SampleID) == 1
		if !legalAssignmentTransition(assignment.Kind, assignment.Status, wanted, directFinalDelivery) {
			return txResult{}, core.Fail(409, "invalid_transition", "assignment status transition is out of order")
		}
		if assignment.Kind == "transport" &&
			wanted != "at_pickup" &&
			sample.CurrentHolderID != p.ID {
			return txResult{}, core.Fail(409, "custody_required", "confirmed custody is required before beginning this leg")
		}
		if assignment.Kind == "transport" && assignment.LegSequence > 1 && wanted != "at_pickup" {
			prior := precedingTransportAssignment(state, assignment)
			if prior == nil || prior.Status != "delivered" && prior.Status != "completed" {
				return txResult{}, core.Fail(409, "prior_leg_incomplete", "previous transport leg must finish first")
			}
		}
		if assignment.Kind == "transport" && wanted == "delivered" {
			if finalTransport == nil {
				return txResult{}, core.Fail(409, "invalid_route", "journey has no final transport leg")
			}
			if finalTransport.ID == assignment.ID {
				if err := validateDeliveryDeadline(state, sample, occurredAt); err != nil {
					return txResult{}, err
				}
			}
		}
		assignment.Status = wanted
		if wanted == "delivered" || wanted == "completed" {
			assignment.CompletedAt = pointerTime(occurredAt)
		}
		if assignment.Kind == "transport" {
			sample.Status = "in_transit"
			if finalTransport != nil &&
				finalTransport.ID == assignment.ID &&
				(wanted == "delivered" || wanted == "completed") {
				sample.Status = "delivered"
			}
			sample.Version++
			sample.UpdatedAt = core.Now()
		}
		out := core.Map{"assignmentId": assignmentID, "sampleId": assignment.SampleID, "status": wanted}
		audit(state, p, "assignment", assignmentID, "assignment_progress_updated", out)
		return txResult{body: out, eventID: emit(state, []string{p.ID, "dispatcher"}, "assignment.progress", "assignment", assignmentID, out)}, nil
	})
}

func (s *Service) recordHandling(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "collection_professional", "delivery_partner", "node_handler", "laboratory_team"); err != nil {
		return nil, err
	}
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	recordedAt, err := core.Time(in["recordedAt"])
	if err != nil {
		return nil, err
	}
	sampleID := core.String(in["sampleId"])
	requestedAssignmentID := core.String(in["assignmentId"])
	return s.mutate(ctx, p, "record_handling_checkpoint", key, in, func(state *mongostore.State) (txResult, error) {
		sample := state.Samples[sampleID]
		if sample == nil {
			return txResult{}, notFound("sample")
		}
		if handlingCheckpointTerminalSample(sample) {
			return txResult{}, core.Fail(409, "terminal_sample", "handling checkpoints cannot modify a terminal sample")
		}
		journey := state.Journeys[sample.ActiveJourneyID]
		if journey == nil || journey.Status != "active" {
			return txResult{}, core.Fail(409, "journey_inactive", "handling checkpoint requires the sample's active journey")
		}
		assignment, err := resolveHandlingAssignment(state, sampleID, requestedAssignmentID, p.ID)
		if err != nil {
			return txResult{}, err
		}
		assignmentID := assignment.ID
		if recordedAt.Before(assignment.CreatedAt) ||
			assignment.AcceptedAt != nil && recordedAt.Before(*assignment.AcceptedAt) {
			return txResult{}, core.Fail(409, "invalid_checkpoint_time", "handling checkpoint cannot precede the assignment")
		}
		temperature := core.Float(in["observedTemperatureC"])
		compliant := core.String(in["containerType"]) == sample.ContainerType &&
			temperature >= sample.TemperatureMinC && temperature <= sample.TemperatureMaxC
		checkpointID := core.UUID()
		state.Checkpoints[checkpointID] = &mongostore.HandlingCheckpoint{
			ID: checkpointID, SampleID: sampleID, AssignmentID: assignmentID,
			LocationID: core.String(in["locationId"]), ContainerType: core.String(in["containerType"]),
			ObservedTemperatureC: temperature, Compliant: compliant, RecordedAt: recordedAt,
		}
		if !compliant {
			sample.Status = "exception"
			sample.Exception = core.Map{
				"reason": "handling_noncompliance", "occurredAt": formatTime(recordedAt),
				"journeyId": sample.ActiveJourneyID, "source": "handling_checkpoint",
			}
			sample.Version++
			sample.UpdatedAt = core.Now()
		}
		out := core.Map{"checkpointId": checkpointID, "sampleId": sampleID, "compliant": compliant, "sampleStatus": sample.Status}
		audit(state, p, "sample", sampleID, "handling_checkpoint_recorded", out)
		return txResult{body: out, eventID: emit(state, []string{p.ID, "dispatcher"}, "sample.handling_checkpoint", "sample", sampleID, out)}, nil
	})
}

func handlingCheckpointTerminalSample(sample *mongostore.Sample) bool {
	switch sample.Status {
	case "received", "cancelled", "expired", "exception":
		return true
	default:
		return false
	}
}

// resolveHandlingAssignment distinguishes an explicitly work-scoped checkpoint
// from an unscoped field observation. Explicit assignment IDs require ownership;
// an omitted ID is resolved only to link the observation to the active route leg.
func resolveHandlingAssignment(
	state *mongostore.State,
	sampleID, assignmentID, actorID string,
) (*mongostore.Assignment, error) {
	sample := state.Samples[sampleID]
	if sample == nil {
		return nil, notFound("sample")
	}
	journey := state.Journeys[sample.ActiveJourneyID]
	if journey == nil || journey.Status != "active" {
		return nil, core.Fail(409, "journey_inactive", "sample does not have an active journey")
	}
	explicitAssignment := assignmentID != ""
	if !explicitAssignment {
		if !actorRelatedToSample(state, sampleID, actorID) {
			return nil, core.Fail(403, "forbidden", "checkpoint actor is unrelated to the sample")
		}
		var fallback *mongostore.Assignment
		for _, assignment := range assignmentsForSample(state, sampleID) {
			if assignment.Kind == "transport" &&
				assignment.JourneyID == sample.ActiveJourneyID &&
				assignmentAvailableForHandling(assignment) {
				if fallback == nil {
					fallback = assignment
				}
				if assignment.WorkerID == actorID {
					fallback = assignment
					break
				}
			}
		}
		if fallback != nil {
			assignmentID = fallback.ID
		}
		if assignmentID == "" {
			return nil, notFound("sample assignment")
		}
	}
	assignment := state.Assignments[assignmentID]
	if assignment == nil || assignment.SampleID != sampleID {
		return nil, notFound("assignment")
	}
	if assignment.JourneyID != sample.ActiveJourneyID ||
		!assignmentAvailableForHandling(assignment) {
		return nil, core.Fail(409, "assignment_inactive", "checkpoint assignment is not active for this sample")
	}
	if explicitAssignment && assignment.WorkerID != actorID {
		return nil, core.Fail(403, "forbidden", "checkpoint requires the assigned worker")
	}
	return assignment, nil
}

func assignmentAvailableForHandling(assignment *mongostore.Assignment) bool {
	return assignment.Status != "cancelled" && assignment.Status != "completed"
}

func actorRelatedToSample(state *mongostore.State, sampleID, actorID string) bool {
	sample := state.Samples[sampleID]
	if sample == nil {
		return false
	}
	if sample.CurrentHolderID == actorID {
		return true
	}
	for _, assignment := range state.Assignments {
		if assignment.SampleID == sampleID &&
			assignment.JourneyID == sample.ActiveJourneyID &&
			assignment.WorkerID == actorID &&
			assignmentAvailableForHandling(assignment) {
			return true
		}
	}
	for _, handoff := range state.Handoffs {
		if handoff.SampleID != sampleID ||
			handoff.SenderID != actorID && handoff.ReceiverID != actorID {
			continue
		}
		fromAssignment := state.Assignments[handoff.FromAssignmentID]
		toAssignment := state.Assignments[handoff.ToAssignmentID]
		if fromAssignment != nil && fromAssignment.JourneyID == sample.ActiveJourneyID ||
			toAssignment != nil && toAssignment.JourneyID == sample.ActiveJourneyID {
			return true
		}
	}
	return false
}

func (s *Service) recordLocation(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "delivery_partner", "collection_professional"); err != nil {
		return nil, err
	}
	recordedAt, err := core.Time(in["recordedAt"])
	if err != nil {
		return nil, err
	}
	assignmentID, sequence := core.String(in["assignmentId"]), core.Int(in["sequence"])
	if sequence <= 0 {
		return nil, core.Fail(400, "invalid_sequence", "sequence must be positive")
	}
	coordinate := core.Object(in["coordinate"])
	latitude, longitude := core.Float(coordinate["latitude"]), core.Float(coordinate["longitude"])
	if coordinate == nil || latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		return nil, core.Fail(400, "invalid_coordinate", "invalid worker coordinate")
	}
	return s.mutate(ctx, p, "record_worker_location", "", in, func(state *mongostore.State) (txResult, error) {
		assignment := state.Assignments[assignmentID]
		if assignment == nil {
			return txResult{}, notFound("assignment")
		}
		if assignment.WorkerID != p.ID {
			return txResult{}, core.Fail(403, "forbidden", "location update belongs to another worker")
		}
		locations := state.WorkerLocations[assignmentID]
		if len(locations) > 0 && int64(sequence) <= locations[len(locations)-1].Sequence {
			return txResult{}, core.Fail(409, "stale_location", "location sequence must be strictly increasing")
		}
		state.WorkerLocations[assignmentID] = append(locations, &mongostore.WorkerLocation{
			AssignmentID: assignmentID, Sequence: int64(sequence), WorkerID: p.ID,
			Latitude: latitude, Longitude: longitude, RecordedAt: recordedAt,
		})
		sample := state.Samples[assignment.SampleID]
		if sample != nil {
			sample.LatestLatitude, sample.LatestLongitude = pointerFloat(latitude), pointerFloat(longitude)
			sample.UpdatedAt = core.Now()
		}
		out := core.Map{"assignmentId": assignmentID, "coordinate": coordinate, "recordedAt": formatTime(recordedAt), "sequence": sequence}
		audit(state, p, "assignment", assignmentID, "worker_location_recorded", out)
		return txResult{body: out, eventID: emit(state, []string{p.ID, "dispatcher"}, "worker.location", "assignment", assignmentID, out)}, nil
	})
}

func (s *Service) confirmReceipt(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "laboratory_team"); err != nil {
		return nil, err
	}
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	receivedAt, err := core.Time(in["receivedAt"])
	if err != nil {
		return nil, err
	}
	sampleID, laboratoryID := core.String(in["sampleId"]), core.String(in["laboratoryId"])
	return s.mutate(ctx, p, "confirm_laboratory_receipt", key, in, func(state *mongostore.State) (txResult, error) {
		sample := state.Samples[sampleID]
		if sample == nil {
			return txResult{}, notFound("sample")
		}
		if sample.TargetLaboratoryID != laboratoryID || sample.Status != "delivered" {
			return txResult{}, core.Fail(409, "receipt_not_ready", "sample must be delivered to its accepting target laboratory")
		}
		if !servesLaboratory(state, laboratoryID) ||
			!laboratoryAcceptsSample(state, laboratoryID, sample.SampleTypeID) {
			return txResult{}, core.Fail(409, "laboratory_unavailable", "target laboratory is inactive or cannot accept this sample type")
		}
		operator := state.Users[p.ID]
		if operator == nil || !operator.Active || !stringSliceContains(operator.Roles, "laboratory_team") {
			return txResult{}, core.Fail(403, "forbidden", "an active laboratory operator is required")
		}
		journey := state.Journeys[sample.ActiveJourneyID]
		if journey == nil || journey.Status != "active" {
			return txResult{}, core.Fail(409, "receipt_not_ready", "sample journey is not active")
		}
		finalAssignment := finalTransportAssignmentForSample(state, journey.ID, sampleID)
		if finalAssignment == nil ||
			finalAssignment.ToLocationID != laboratoryID ||
			finalAssignment.Status != "delivered" && finalAssignment.Status != "completed" ||
			finalAssignment.CompletedAt == nil {
			return txResult{}, core.Fail(409, "receipt_not_ready", "the final transport leg must be completed at the target laboratory")
		}
		if receivedAt.Before(*finalAssignment.CompletedAt) {
			return txResult{}, core.Fail(409, "invalid_receipt_time", "laboratory receipt cannot precede final delivery")
		}
		if err := validateDeliveryDeadline(state, sample, receivedAt); err != nil {
			return txResult{}, err
		}
		for _, handoff := range state.Handoffs {
			if handoff.SampleID == sampleID && handoff.Status == "pending" {
				return txResult{}, core.Fail(409, "pending_handoff_exists", "laboratory receipt cannot complete while custody transfer is pending")
			}
		}
		if sample.CurrentHolderID != finalAssignment.WorkerID &&
			(sample.CurrentHolderID != p.ID || !confirmedFinalCustodyTransfer(state, sampleID, finalAssignment.ID, p.ID)) {
			return txResult{}, core.Fail(409, "custody_conflict", "final-leg custody is not available for laboratory receipt")
		}
		if !laboratoryOperatorAuthorized(state, sampleID, laboratoryID, p.ID) {
			return txResult{}, core.Fail(403, "wrong_laboratory_operator", "receipt is assigned to a different laboratory operator")
		}
		completeLaboratoryReceiptAssignment(state, p, sampleID, laboratoryID, p.ID, receivedAt)
		sample.Status, sample.ReceivedLaboratoryID, sample.CurrentHolderID = "received", laboratoryID, p.ID
		sample.Version++
		sample.UpdatedAt = core.Now()
		allReceived := true
		for _, journeySampleID := range journey.SampleIDs {
			journeySample := state.Samples[journeySampleID]
			if journeySample == nil || journeySample.Status != "received" {
				allReceived = false
				break
			}
		}
		if allReceived {
			journey.Status, journey.UpdatedAt = "completed", core.Now()
			releaseReservations(state, journey.ID)
		}
		out := core.Map{"sampleId": sampleID, "laboratoryId": laboratoryID, "status": "received", "receivedAt": formatTime(receivedAt)}
		audit(state, p, "sample", sampleID, "laboratory_receipt_confirmed", out)
		return txResult{body: out, eventID: emit(state, []string{"dispatcher"}, "sample.received", "sample", sampleID, out)}, nil
	})
}

func (s *Service) recordLabException(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "laboratory_team"); err != nil {
		return nil, err
	}
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	observedAt, err := core.Time(in["observedAt"])
	if err != nil {
		return nil, err
	}
	sampleID, laboratoryID := core.String(in["sampleId"]), core.String(in["laboratoryId"])
	return s.mutate(ctx, p, "record_laboratory_exception", key, in, func(state *mongostore.State) (txResult, error) {
		sample := state.Samples[sampleID]
		if sample == nil {
			return txResult{}, notFound("sample")
		}
		operator := state.Users[p.ID]
		if operator == nil || !operator.Active || !stringSliceContains(operator.Roles, "laboratory_team") {
			return txResult{}, core.Fail(403, "forbidden", "an active laboratory operator is required")
		}
		// A laboratory that has gone down is the very reason an exception gets
		// recorded, so the location must be a laboratory but need not be active.
		if laboratoryID == "" ||
			sample.TargetLaboratoryID != laboratoryID ||
			!providesLaboratory(state, laboratoryID) {
			return txResult{}, core.Fail(409, "wrong_laboratory", "exception must be recorded for the sample's target laboratory")
		}
		journey := state.Journeys[sample.ActiveJourneyID]
		if journey == nil || journey.Status != "active" {
			return txResult{}, core.Fail(409, "journey_inactive", "laboratory exception requires an active journey")
		}
		if !laboratoryExceptionReady(state, sample, journey, laboratoryID) {
			return txResult{}, core.Fail(409, "laboratory_not_ready", "sample has not reached the target laboratory")
		}
		if handlingNoncomplianceAwaitingLaboratory(sample, journey) {
			checkpointAt, parseErr := time.Parse(
				time.RFC3339, core.String(sample.Exception["occurredAt"]),
			)
			if parseErr != nil || observedAt.Before(checkpointAt) {
				return txResult{}, core.Fail(409, "invalid_exception_time", "laboratory exception cannot precede the handling checkpoint")
			}
		}
		if !laboratoryOperatorAuthorized(state, sampleID, laboratoryID, p.ID) {
			return txResult{}, core.Fail(403, "wrong_laboratory_operator", "exception is assigned to a different laboratory operator")
		}
		request := state.Requests[sample.RequestID]
		if request == nil || observedAt.Before(request.RequestedAt) {
			return txResult{}, core.Fail(409, "invalid_exception_time", "laboratory exception cannot precede the sample request")
		}
		exceptionID := core.UUID()
		exception := core.Map{
			"exceptionId": exceptionID, "reason": core.String(in["reason"]),
			"observedAt": formatTime(observedAt), "laboratoryId": laboratoryID,
		}
		completeLaboratoryReceiptAssignment(state, p, sampleID, laboratoryID, p.ID, observedAt)
		if len(journey.SampleIDs) > 1 {
			if err := removeSampleFromActiveBatch(state, journey, sampleID); err != nil {
				return txResult{}, err
			}
		} else {
			journey.Status, journey.UpdatedAt = "cancelled", core.Now()
			journey.PlanVersion++
			cancelUnfinishedAssignments(state, journey.ID)
			releaseReservations(state, journey.ID)
		}
		sample.Status, sample.Exception, sample.ActiveJourneyID = "exception", exception, ""
		sample.Version++
		sample.UpdatedAt = core.Now()
		out := core.Map{"exceptionId": exceptionID, "sampleId": sampleID, "status": "exception", "reason": exception["reason"]}
		audit(state, p, "sample", sampleID, "laboratory_exception_recorded", exception)
		return txResult{body: out, eventID: emit(state, []string{"dispatcher"}, "sample.exception", "sample", sampleID, out)}, nil
	})
}

func removeSampleFromActiveBatch(
	state *mongostore.State,
	journey *mongostore.Journey,
	sampleID string,
) error {
	remaining := []string{}
	for _, candidateID := range journey.SampleIDs {
		if candidateID != sampleID {
			remaining = append(remaining, candidateID)
		}
	}
	if len(remaining) == 0 {
		return core.Fail(409, "invalid_batch", "batch exception cannot leave an empty active journey")
	}
	for _, reservation := range state.Reservations {
		if reservation.JourneyID != journey.ID || reservation.Status != "active" {
			continue
		}
		if reservation.Units <= 0 {
			return core.Fail(409, "invalid_reservation", "batch reservation cannot release one sample unit")
		}
		reservation.Units--
		if capacity := state.Capacities[reservation.ResourceID]; capacity != nil {
			capacity.ReservedUnits = maxInt(0, capacity.ReservedUnits-1)
			if reservation.Cooled {
				capacity.CooledReserved = maxInt(0, capacity.CooledReserved-1)
			}
			capacity.UpdatedAt = core.Now()
		}
		if reservation.Units == 0 {
			reservation.Status = "released"
		}
	}
	detachSampleFromActiveBatch(state, journey, sampleID, remaining, core.Now())
	return nil
}

// detachSampleFromActiveBatch removes one sample from a live batch and leaves the
// journey a valid, plannable plan.
//
// Everything the departing sample still had on the journey is cancelled: its
// unfinished work, and any handoff of it still awaiting confirmation. Work left
// behind would reference a sample the journey no longer carries, which makes the
// surviving plan fail revalidation — so every plan edit, merge and split of it is
// refused — while still consuming its worker's capacity and still being offered
// out by the maintenance pass.
//
// The route mode is then recomputed from the assignments that remain rather than
// from the sample count alone, so a batch reduced to one sample over one movement
// is reported as direct rather than as a relay that does not exist.
//
// Releasing the capacity the departing sample held stays with the callers,
// because they differ: a laboratory exception on a batch member must refuse a
// batch whose reservation cannot give one unit back, while the deadline timer
// releases whatever it can and carries on.
func detachSampleFromActiveBatch(
	state *mongostore.State,
	journey *mongostore.Journey,
	sampleID string,
	remaining []string,
	now time.Time,
) {
	for _, assignment := range state.Assignments {
		if assignment.JourneyID != journey.ID || assignment.SampleID != sampleID {
			continue
		}
		if assignment.Status != "completed" && assignment.Status != "delivered" &&
			assignment.Status != "cancelled" {
			assignment.Status = "cancelled"
			assignment.CompletedAt = pointerTime(now)
		}
	}
	for _, handoff := range state.Handoffs {
		if handoff.SampleID == sampleID && handoff.Status == "pending" {
			handoff.Status = "cancelled"
		}
	}
	journey.SampleIDs = remaining
	journey.PrimarySampleID = remaining[0]
	journey.CapacityUsed = len(remaining)
	journey.RouteMode = routeModeForJourneyAssignments(state, journey)
	journey.PlanVersion++
	journey.UpdatedAt = now
}

func laboratoryExceptionReady(
	state *mongostore.State,
	sample *mongostore.Sample,
	journey *mongostore.Journey,
	laboratoryID string,
) bool {
	if handlingNoncomplianceAwaitingLaboratory(sample, journey) {
		return true
	}
	if sample.Status != "delivered" {
		return false
	}
	finalAssignment := finalTransportAssignmentForSample(state, journey.ID, sample.ID)
	return finalAssignment != nil &&
		finalAssignment.ToLocationID == laboratoryID &&
		(finalAssignment.Status == "delivered" || finalAssignment.Status == "completed") &&
		finalAssignment.CompletedAt != nil
}

func handlingNoncomplianceAwaitingLaboratory(
	sample *mongostore.Sample,
	journey *mongostore.Journey,
) bool {
	return sample.Status == "exception" &&
		core.String(sample.Exception["reason"]) == "handling_noncompliance" &&
		core.String(sample.Exception["source"]) == "handling_checkpoint" &&
		core.String(sample.Exception["journeyId"]) == journey.ID &&
		sample.ActiveJourneyID == journey.ID
}

func prepareTransportReleaseForHandoff(
	state *mongostore.State,
	sample *mongostore.Sample,
	p core.Principal,
	fromAssignment, toAssignment *mongostore.Assignment,
	initiatedAt time.Time,
) error {
	if fromAssignment == nil || toAssignment == nil || fromAssignment.Kind != "transport" {
		return nil
	}
	if assignmentMayReleaseCustody(fromAssignment) {
		return nil
	}
	if !adjacentAssignments(state, fromAssignment, toAssignment) {
		return nil
	}
	if fromAssignment.WorkerID != p.ID ||
		fromAssignment.AcceptedAt == nil ||
		sample.CurrentHolderID != p.ID {
		return core.Fail(409, "transport_leg_incomplete", "only the accepted current holder may release a transport leg")
	}
	if initiatedAt.Before(*fromAssignment.AcceptedAt) {
		return core.Fail(409, "invalid_handoff_time", "handoff cannot precede transport assignment acceptance")
	}
	switch fromAssignment.Status {
	case "accepted", "at_pickup", "picked_up", "in_progress", "in_transit":
	default:
		return core.Fail(409, "transport_leg_incomplete", "transport leg has not reached the handoff boundary")
	}
	if fromAssignment.LegSequence > 1 {
		prior := precedingTransportAssignment(state, fromAssignment)
		if prior == nil ||
			prior.Status != "delivered" && prior.Status != "completed" ||
			prior.CompletedAt == nil ||
			initiatedAt.Before(*prior.CompletedAt) {
			return core.Fail(409, "prior_leg_incomplete", "previous transport leg must complete before this handoff")
		}
	}
	var boundaryStop *mongostore.JourneyStop
	for _, stop := range state.Stops {
		if stop.JourneyID == fromAssignment.JourneyID &&
			stop.LocationID == fromAssignment.ToLocationID &&
			stop.Sequence == fromAssignment.LegSequence+1 {
			boundaryStop = stop
			break
		}
	}
	if boundaryStop == nil || initiatedAt.Before(boundaryStop.EstimatedAt) {
		return core.Fail(409, "transport_leg_incomplete", "transport leg has not reached the scheduled handoff boundary")
	}
	fromAssignment.Status = "arrived"
	if sample.Status == "sealed" {
		sample.Status = "in_transit"
	}
	sample.Version++
	sample.UpdatedAt = core.Now()
	audit(state, p, "assignment", fromAssignment.ID, "assignment_arrival_inferred", core.Map{
		"locationId": fromAssignment.ToLocationID,
		"arrivedAt":  formatTime(initiatedAt),
	})
	return nil
}

func validateHandoffRoute(
	state *mongostore.State,
	sample *mongostore.Sample,
	senderID, receiverID, fromAssignmentID, toAssignmentID string,
) error {
	if fromAssignmentID == "" || toAssignmentID == "" {
		return core.Fail(400, "invalid_handoff_route", "fromAssignmentId and toAssignmentId are required")
	}
	fromAssignment, toAssignment := state.Assignments[fromAssignmentID], state.Assignments[toAssignmentID]
	if fromAssignment == nil || toAssignment == nil {
		return notFound("handoff assignment")
	}
	if fromAssignment.SampleID != sample.ID ||
		toAssignment.SampleID != sample.ID ||
		fromAssignment.JourneyID == "" ||
		fromAssignment.JourneyID != toAssignment.JourneyID ||
		fromAssignment.JourneyID != sample.ActiveJourneyID {
		return core.Fail(409, "invalid_handoff_route", "handoff assignments must belong to the sample's active journey")
	}
	if !adjacentAssignments(state, fromAssignment, toAssignment) {
		return core.Fail(409, "nonadjacent_handoff", "handoff assignments must be adjacent in route order")
	}

	sender := state.Users[senderID]
	if sender == nil || !sender.Active {
		return core.Fail(403, "invalid_sender", "handoff sender must be an active user")
	}
	senderOwnsWork := fromAssignment.WorkerID == senderID &&
		assignmentMayReleaseCustody(fromAssignment)
	senderIsNodeBridge := sample.CurrentHolderID == senderID &&
		nodeBridgeSenderAuthorized(state, fromAssignment, toAssignment, sender)
	if !senderOwnsWork && !senderIsNodeBridge {
		return core.Fail(403, "sender_assignment_mismatch", "sender must own the releasing assignment or the intermediate node custody")
	}

	receiver := state.Users[receiverID]
	if receiver == nil || !receiver.Active {
		return core.Fail(400, "invalid_receiver", "handoff receiver must be active")
	}
	receiverCanTakeWork := stringSliceContains(receiver.Roles, roleForAssignmentKind(toAssignment.Kind)) &&
		(toAssignment.Status == "pending" &&
			(toAssignment.WorkerID == "" || toAssignment.WorkerID == receiverID) ||
			toAssignment.Status == "accepted" && toAssignment.WorkerID == receiverID)
	receiverIsNodeBridge := nodeBridgeReceiverAuthorized(state, fromAssignment, toAssignment, receiver)
	if !receiverCanTakeWork && !receiverIsNodeBridge {
		return core.Fail(409, "receiver_assignment_mismatch", "receiver must own the next assignment or be the active intermediate node handler")
	}
	if receiverCanTakeWork && toAssignment.Status == "accepted" && toAssignment.AcceptedAt == nil {
		return core.Fail(409, "invalid_assignment_state", "accepted receiver assignment is missing its acceptance record")
	}
	return nil
}

func adjacentAssignments(
	state *mongostore.State,
	fromAssignment, toAssignment *mongostore.Assignment,
) bool {
	if fromAssignment.ID == toAssignment.ID {
		return false
	}
	if fromAssignment.Kind == "collection" && toAssignment.Kind == "transport" {
		return fromAssignment.LegSequence == 0 && toAssignment.LegSequence == 1
	}
	if fromAssignment.Kind == "transport" && toAssignment.Kind == "transport" {
		return toAssignment.LegSequence == fromAssignment.LegSequence+1 &&
			fromAssignment.ToLocationID != "" &&
			fromAssignment.ToLocationID == toAssignment.FromLocationID
	}
	if fromAssignment.Kind == "transport" && toAssignment.Kind == "node" {
		return toAssignment.LegSequence == fromAssignment.LegSequence &&
			fromAssignment.ToLocationID != "" &&
			fromAssignment.ToLocationID == toAssignment.ToLocationID
	}
	if fromAssignment.Kind == "node" && toAssignment.Kind == "transport" {
		return toAssignment.LegSequence == fromAssignment.LegSequence+1 &&
			fromAssignment.ToLocationID != "" &&
			fromAssignment.ToLocationID == toAssignment.FromLocationID
	}
	if fromAssignment.Kind == "transport" && toAssignment.Kind == "laboratory" {
		final := finalTransportAssignmentForSample(
			state, fromAssignment.JourneyID, fromAssignment.SampleID,
		)
		return final != nil &&
			final.ID == fromAssignment.ID &&
			toAssignment.LegSequence == fromAssignment.LegSequence+1 &&
			fromAssignment.ToLocationID != "" &&
			(toAssignment.ToLocationID == "" || toAssignment.ToLocationID == fromAssignment.ToLocationID)
	}
	return false
}

func assignmentMayReleaseCustody(assignment *mongostore.Assignment) bool {
	switch assignment.Kind {
	case "collection":
		return assignment.Status == "completed"
	case "transport":
		switch assignment.Status {
		case "arrived", "delivered", "completed":
			return true
		}
	case "node":
		return assignment.Status == "accepted" ||
			assignment.Status == "in_progress" ||
			assignment.Status == "completed"
	}
	return false
}

func intermediateNodeBoundary(
	state *mongostore.State,
	fromAssignment, toAssignment *mongostore.Assignment,
) bool {
	if fromAssignment.Kind != "transport" ||
		toAssignment.Kind != "transport" ||
		fromAssignment.ToLocationID == "" ||
		fromAssignment.ToLocationID != toAssignment.FromLocationID {
		return false
	}
	// The custody bridge only exists where node work is actually created: at an
	// intermediate location currently serving as a transfer point or hub.
	return servesTransferStop(state, fromAssignment.ToLocationID)
}

func matchingNodeBridgeAssignment(
	state *mongostore.State,
	fromAssignment, toAssignment *mongostore.Assignment,
) *mongostore.Assignment {
	if !intermediateNodeBoundary(state, fromAssignment, toAssignment) {
		return nil
	}
	var matched *mongostore.Assignment
	for _, assignment := range state.Assignments {
		if assignment.Kind != "node" ||
			assignment.SampleID != fromAssignment.SampleID ||
			assignment.JourneyID != fromAssignment.JourneyID ||
			assignment.LegSequence != fromAssignment.LegSequence ||
			assignment.ToLocationID != fromAssignment.ToLocationID {
			continue
		}
		if matched == nil || assignment.ID < matched.ID {
			matched = assignment
		}
	}
	return matched
}

func nodeBridgeReceiverAuthorized(
	state *mongostore.State,
	fromAssignment, toAssignment *mongostore.Assignment,
	receiver *mongostore.User,
) bool {
	if !intermediateNodeBoundary(state, fromAssignment, toAssignment) ||
		!stringSliceContains(receiver.Roles, "node_handler") {
		return false
	}
	nodeAssignment := matchingNodeBridgeAssignment(state, fromAssignment, toAssignment)
	if nodeAssignment == nil {
		// Backward compatibility for existing plans created before durable
		// facility assignments were introduced.
		return true
	}
	if nodeAssignment.WorkerID != receiver.ID {
		return false
	}
	switch nodeAssignment.Status {
	case "pending":
		return nodeAssignment.AcceptedAt == nil
	case "accepted", "in_progress":
		return nodeAssignment.AcceptedAt != nil
	default:
		return false
	}
}

func nodeBridgeSenderAuthorized(
	state *mongostore.State,
	fromAssignment, toAssignment *mongostore.Assignment,
	sender *mongostore.User,
) bool {
	if !intermediateNodeBoundary(state, fromAssignment, toAssignment) ||
		!stringSliceContains(sender.Roles, "node_handler") {
		return false
	}
	nodeAssignment := matchingNodeBridgeAssignment(state, fromAssignment, toAssignment)
	if nodeAssignment == nil {
		return true
	}
	return nodeAssignment.WorkerID == sender.ID &&
		nodeAssignment.AcceptedAt != nil &&
		(nodeAssignment.Status == "accepted" || nodeAssignment.Status == "in_progress")
}

func usesDurableNodeBridge(
	state *mongostore.State,
	fromAssignment, toAssignment *mongostore.Assignment,
	receiver *mongostore.User,
) bool {
	if receiver == nil {
		return false
	}
	nodeAssignment := matchingNodeBridgeAssignment(state, fromAssignment, toAssignment)
	return nodeAssignment != nil &&
		nodeAssignment.WorkerID == receiver.ID &&
		stringSliceContains(receiver.Roles, "node_handler")
}

// auditFacilityAssignment records the lifecycle of node and laboratory work that
// advances as a side effect of custody and receipt rather than through an
// explicit accept or progress call. Without it those items of work would have no
// status timeline of their own.
func auditFacilityAssignment(
	state *mongostore.State,
	p core.Principal,
	assignment *mongostore.Assignment,
	action string,
	at time.Time,
) {
	audit(state, p, "assignment", assignment.ID, action, core.Map{
		"assignmentId": assignment.ID, "sampleId": assignment.SampleID,
		"kind": assignment.Kind, "status": assignment.Status,
		"workerId": assignment.WorkerID, "occurredAt": formatTime(at),
	})
}

func acceptNodeBridgeAssignment(
	state *mongostore.State,
	p core.Principal,
	fromAssignment, toAssignment *mongostore.Assignment,
	receiverID string,
	acceptedAt time.Time,
) error {
	assignment := matchingNodeBridgeAssignment(state, fromAssignment, toAssignment)
	if assignment == nil || assignment.WorkerID != receiverID {
		return core.Fail(409, "node_assignment_mismatch", "node custody is assigned to a different handler")
	}
	if assignment.Status == "accepted" {
		if assignment.AcceptedAt == nil {
			return core.Fail(409, "invalid_assignment_state", "accepted node assignment is missing its acceptance record")
		}
		return nil
	}
	if assignment.Status != "pending" || assignment.AcceptedAt != nil {
		return core.Fail(409, "invalid_assignment_state", "node assignment is not available for custody")
	}
	receiver := state.Users[receiverID]
	if receiver == nil ||
		!receiver.Active ||
		!receiver.Available ||
		!stringSliceContains(receiver.Roles, "node_handler") ||
		activeWorkerAssignments(state, receiverID) >= receiver.CapacityUnits {
		return core.Fail(409, "receiver_unavailable", "node handler cannot accept custody")
	}
	assignment.Status, assignment.AcceptedAt, assignment.OfferExpiresAt = "accepted", pointerTime(acceptedAt), nil
	receiver.LastAssignedAt, receiver.UpdatedAt = pointerTime(acceptedAt), core.Now()
	auditFacilityAssignment(state, p, assignment, "assignment_accepted", acceptedAt)
	return nil
}

func completeNodeBridgeAssignment(
	state *mongostore.State,
	p core.Principal,
	fromAssignment, toAssignment *mongostore.Assignment,
	senderID string,
	completedAt time.Time,
) {
	assignment := matchingNodeBridgeAssignment(state, fromAssignment, toAssignment)
	if assignment == nil ||
		assignment.WorkerID != senderID ||
		assignment.AcceptedAt == nil ||
		assignment.Status != "accepted" && assignment.Status != "in_progress" {
		return
	}
	assignment.Status, assignment.CompletedAt = "completed", pointerTime(completedAt)
	auditFacilityAssignment(state, p, assignment, "assignment_completed", completedAt)
}

func acceptHandoffReceiverAssignment(
	state *mongostore.State,
	p core.Principal,
	assignmentID, receiverID string,
	acceptedAt time.Time,
) error {
	assignment := state.Assignments[assignmentID]
	if assignment == nil {
		return notFound("handoff assignment")
	}
	// An intermediate node handler is a custody bridge, not a transport
	// assignee. The following delivery leg remains assigned and cannot be
	// progressed until it is handed to and accepted by that assignee.
	receiver := state.Users[receiverID]
	if receiver == nil || !stringSliceContains(receiver.Roles, roleForAssignmentKind(assignment.Kind)) {
		return nil
	}
	if assignment.Status == "accepted" {
		if assignment.WorkerID != receiverID || assignment.AcceptedAt == nil {
			return core.Fail(409, "invalid_assignment_state", "next assignment is accepted by a different worker")
		}
		return nil
	}
	if assignment.Status != "pending" {
		return core.Fail(409, "invalid_assignment_state", "next assignment is no longer available for handoff")
	}
	if assignment.WorkerID != "" && assignment.WorkerID != receiverID {
		return core.Fail(409, "offer_not_available", "next assignment is offered to a different worker")
	}
	if !receiver.Active ||
		!receiver.Available ||
		activeWorkerAssignments(state, receiverID) >= receiver.CapacityUnits {
		return core.Fail(409, "receiver_unavailable", "handoff receiver cannot accept the next assignment")
	}
	assignment.WorkerID, assignment.Status = receiverID, "accepted"
	assignment.AcceptedAt, assignment.OfferExpiresAt = pointerTime(acceptedAt), nil
	receiver.LastAssignedAt, receiver.UpdatedAt = pointerTime(acceptedAt), core.Now()
	auditFacilityAssignment(state, p, assignment, "assignment_accepted", acceptedAt)
	return nil
}

func roleForAssignmentKind(kind string) string {
	return map[string]string{
		"collection": "collection_professional",
		"transport":  "delivery_partner",
		"node":       "node_handler",
		"laboratory": "laboratory_team",
	}[kind]
}

func legalAssignmentTransition(kind, current, wanted string, directFinalDelivery bool) bool {
	if kind == "transport" {
		allowed := map[string]map[string]bool{
			"accepted": {
				"at_pickup": true, "picked_up": true,
				"in_progress": true, "in_transit": true,
			},
			"at_pickup":   {"picked_up": true},
			"picked_up":   {"in_progress": true, "in_transit": true},
			"in_progress": {"in_transit": true, "arrived": true},
			"in_transit":  {"arrived": true},
			"arrived":     {"delivered": true},
			"delivered":   {"completed": true},
		}
		if directFinalDelivery && current == "accepted" && wanted == "delivered" {
			return true
		}
		return allowed[current][wanted]
	}
	if kind == "node" {
		return current == "accepted" && wanted == "in_progress" ||
			current == "in_progress" && wanted == "completed"
	}
	return false
}

func precedingTransportAssignment(
	state *mongostore.State,
	assignment *mongostore.Assignment,
) *mongostore.Assignment {
	for _, candidate := range state.Assignments {
		if candidate.JourneyID == assignment.JourneyID &&
			candidate.Kind == "transport" &&
			candidate.SampleID == assignment.SampleID &&
			candidate.LegSequence == assignment.LegSequence-1 {
			return candidate
		}
	}
	return nil
}

func finalTransportAssignment(state *mongostore.State, journeyID string) *mongostore.Assignment {
	return finalTransportAssignmentForSample(state, journeyID, "")
}

func finalTransportAssignmentForSample(
	state *mongostore.State,
	journeyID, sampleID string,
) *mongostore.Assignment {
	var final *mongostore.Assignment
	for _, assignment := range state.Assignments {
		if assignment.JourneyID != journeyID || assignment.Kind != "transport" ||
			assignment.Status == "cancelled" ||
			sampleID != "" && assignment.SampleID != sampleID {
			continue
		}
		if final == nil ||
			assignment.LegSequence > final.LegSequence ||
			assignment.LegSequence == final.LegSequence && assignment.ID < final.ID {
			final = assignment
		}
	}
	return final
}

func transportAssignmentCount(state *mongostore.State, journeyID string) int {
	return transportAssignmentCountForSample(state, journeyID, "")
}

func transportAssignmentCountForSample(
	state *mongostore.State,
	journeyID, sampleID string,
) int {
	total := 0
	for _, assignment := range state.Assignments {
		if assignment.JourneyID == journeyID && assignment.Kind == "transport" &&
			assignment.Status != "cancelled" &&
			(sampleID == "" || assignment.SampleID == sampleID) {
			total++
		}
	}
	return total
}

func validateDeliveryDeadline(
	state *mongostore.State,
	sample *mongostore.Sample,
	occurredAt time.Time,
) error {
	request := state.Requests[sample.RequestID]
	if request == nil {
		return core.Fail(409, "invalid_sample_state", "sample request is missing")
	}
	if occurredAt.After(request.DeliveryDeadline) {
		return core.Fail(409, "delivery_deadline_exceeded", "delivery or laboratory receipt is after the sample deadline")
	}
	return nil
}

func laboratoryAcceptsSample(
	state *mongostore.State,
	laboratoryID, sampleTypeID string,
) bool {
	for _, acceptance := range state.Acceptances {
		if acceptance.LaboratoryID == laboratoryID &&
			acceptance.SampleTypeID == sampleTypeID &&
			acceptance.Accepts {
			return true
		}
	}
	return false
}

func confirmedFinalCustodyTransfer(
	state *mongostore.State,
	sampleID, finalAssignmentID, receiverID string,
) bool {
	for _, handoff := range state.Handoffs {
		if handoff.SampleID == sampleID &&
			handoff.FromAssignmentID == finalAssignmentID &&
			handoff.ReceiverID == receiverID &&
			handoff.Status == "confirmed" &&
			handoff.SenderConfirmedAt != nil &&
			handoff.ReceiverConfirmedAt != nil {
			return true
		}
	}
	return false
}

func laboratoryOperatorAuthorized(
	state *mongostore.State,
	sampleID, laboratoryID, operatorID string,
) bool {
	hasLaboratoryAssignment := false
	for _, assignment := range state.Assignments {
		if assignment.SampleID != sampleID ||
			assignment.Kind != "laboratory" ||
			assignment.ToLocationID != "" && assignment.ToLocationID != laboratoryID {
			continue
		}
		hasLaboratoryAssignment = true
		if assignment.WorkerID != operatorID {
			continue
		}
		switch assignment.Status {
		case "pending":
			return assignment.AcceptedAt == nil
		case "accepted", "in_progress", "arrived", "completed":
			if assignment.AcceptedAt == nil {
				continue
			}
			return true
		}
	}
	// Existing journeys do not create a laboratory work assignment. In that
	// legacy shape, receipt itself is the audited custody transfer to an active
	// laboratory-team user. Whenever a lab assignment is present, it is the
	// facility-scoped authorization and unrelated operators are rejected.
	return !hasLaboratoryAssignment
}

func completeLaboratoryReceiptAssignment(
	state *mongostore.State,
	p core.Principal,
	sampleID, laboratoryID, operatorID string,
	completedAt time.Time,
) {
	for _, assignment := range state.Assignments {
		if assignment.SampleID != sampleID ||
			assignment.Kind != "laboratory" ||
			assignment.WorkerID != operatorID ||
			assignment.ToLocationID != "" && assignment.ToLocationID != laboratoryID {
			continue
		}
		if assignment.AcceptedAt == nil {
			assignment.AcceptedAt = pointerTime(completedAt)
		}
		assignment.Status, assignment.CompletedAt, assignment.OfferExpiresAt = "completed", pointerTime(completedAt), nil
		auditFacilityAssignment(state, p, assignment, "assignment_completed", completedAt)
		return
	}
}

func assignmentsForSample(state *mongostore.State, sampleID string) []*mongostore.Assignment {
	out := []*mongostore.Assignment{}
	for _, assignment := range state.Assignments {
		if assignment.SampleID == sampleID {
			out = append(out, assignment)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].LegSequence != out[j].LegSequence {
			return out[i].LegSequence < out[j].LegSequence
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func latestTime(values ...*time.Time) *time.Time {
	var latest *time.Time
	for _, value := range values {
		if value != nil && (latest == nil || value.After(*latest)) {
			copy := *value
			latest = &copy
		}
	}
	return latest
}
