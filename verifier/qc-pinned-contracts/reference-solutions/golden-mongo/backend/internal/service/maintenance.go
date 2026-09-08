package service

import (
	"context"
	"sort"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// Maintain advances configurable timers without keeping authoritative state in
// process memory. Each pass is an atomic, restart-safe MongoDB command.
func (s *Service) Maintain(ctx context.Context) error {
	return s.Store.Update(ctx, func(state *mongostore.State) error {
		now := core.Now()
		offerUnofferedAssignments(state, now)
		expireUnassignedQualifiedCollectionSearches(state, now)
		for _, assignment := range state.Assignments {
			if assignment.Status != "pending" || assignment.WorkerID == "" ||
				assignment.OfferExpiresAt == nil || assignment.OfferExpiresAt.After(now) {
				continue
			}
			previousWorker := assignment.WorkerID
			assignment.Declines = append(assignment.Declines, mongostore.AssignmentDecline{
				WorkerID: previousWorker, Reason: "offer_expired", At: now,
			})
			role, qualification, required := assignmentEligibility(state, assignment)
			next := fairWorker(state, role, qualification, required, map[string]bool{previousWorker: true}, assignmentLocationScope(assignment))
			assignment.WorkerID, assignment.AcceptedAt = next, nil
			if next == "" {
				assignment.OfferExpiresAt = nil
			} else {
				assignment.OfferExpiresAt = pointerTime(now.Add(assignmentOfferTTL()))
			}
			payload := core.Map{
				"assignmentId": assignment.ID, "expiredWorkerId": previousWorker,
				"status": "pending",
			}
			if next != "" {
				payload["reofferedTo"] = next
			}
			state.AddAudit(
				mongostore.SystemActorID, mongostore.SystemRole,
				"assignment", assignment.ID, "assignment_offer_expired", true, payload,
			)
			audience := []string{previousWorker, "dispatcher"}
			if next != "" {
				audience = append(audience, next)
			}
			emit(state, audience, "assignment.offer_expired", "assignment", assignment.ID, payload)

			// `required` (from assignmentEligibility above) is true for any
			// collection assignment whose sample type demands a qualified
			// collector -- all four named types, not just Pulse. Once the
			// re-offer search comes up empty for one of them, nobody is left
			// to ever accept this assignment, so it must resolve to the same
			// distinguishable exception Pulse has always gotten here, not
			// silently wait out the (unrelated) delivery deadline instead.
			if next == "" && required {
				recordNoQualifiedCollectorException(state, state.Samples[assignment.SampleID], now)
			}
		}
		for _, sample := range state.Samples {
			request := state.Requests[sample.RequestID]
			if request == nil || request.DeliveryDeadline.After(now) || terminalSampleStatus(sample.Status) {
				continue
			}
			expireSample(state, sample, now, "delivery_deadline_elapsed")
		}
		// Republish the serviceability calculation whenever the published version
		// no longer describes the current network revision. The new version is
		// built whole and then swapped in, so callers keep being answered from
		// the previous complete version until the swap happens.
		ensureServiceabilityPublished(state)
		return nil
	})
}

// offerUnofferedAssignments offers pending work that no eligible worker was
// available for when the plan was built. Nobody may accept an assignment that
// was never offered to them, so without this pass such work would stall: it
// travels through the same fair-assignment path as every other offer, and the
// exclusion of workers who already declined decays, so a decline can never
// permanently keep a worker from being offered the work again.
func offerUnofferedAssignments(state *mongostore.State, now time.Time) {
	assignments := make([]*mongostore.Assignment, 0, len(state.Assignments))
	for _, assignment := range state.Assignments {
		if assignment.Status != "pending" || assignment.WorkerID != "" {
			continue
		}
		journey := state.Journeys[assignment.JourneyID]
		if journey == nil || journey.Status != "active" {
			continue
		}
		assignments = append(assignments, assignment)
	}
	sort.Slice(assignments, func(i, j int) bool {
		if !assignments[i].CreatedAt.Equal(assignments[j].CreatedAt) {
			return assignments[i].CreatedAt.Before(assignments[j].CreatedAt)
		}
		return assignments[i].ID < assignments[j].ID
	})
	for _, assignment := range assignments {
		role, qualification, required := assignmentEligibility(state, assignment)
		if role == "" {
			continue
		}
		next := fairWorker(state, role, qualification, required, recentlyDeclinedWorkers(assignment, now), assignmentLocationScope(assignment))
		if next == "" {
			continue
		}
		assignment.WorkerID = next
		assignment.AcceptedAt = nil
		assignment.OfferExpiresAt = pointerTime(now.Add(assignmentOfferTTL()))
		payload := core.Map{
			"assignmentId": assignment.ID, "offeredTo": next, "status": "pending",
		}
		state.AddAudit(
			mongostore.SystemActorID, mongostore.SystemRole,
			"assignment", assignment.ID, "assignment_offered", true, payload,
		)
		emit(state, []string{next, "dispatcher"}, "assignment.offered", "assignment", assignment.ID, payload)
	}
}

// expireUnassignedQualifiedCollectionSearches flags a sample as an exception
// when its collection assignment has required a qualified collector since
// the assignment was created and no eligible worker has EVER been available
// to offer it to, for longer than the configured search timeout.
//
// This is deliberately distinct from two neighbouring mechanisms:
//   - offerUnofferedAssignments (above), which keeps retrying fairWorker every
//     sweep and only succeeds once someone becomes eligible;
//   - the delivery-deadline expiry below, which is about the request's overall
//     deadline, not about how long this specific assignment has gone unassigned.
//
// It therefore fires independently of, and may fire well before, the delivery
// deadline. An assignment that was offered to anyone at least once (it carries
// a decline, even a synthetic "offer_expired" one) took the existing
// decline/re-offer/offer-expiry path instead and is left untouched here — this
// only covers assignments that have never once had a worker since creation.
func expireUnassignedQualifiedCollectionSearches(state *mongostore.State, now time.Time) {
	for _, assignment := range state.Assignments {
		if assignment.Status != "pending" || assignment.WorkerID != "" || len(assignment.Declines) != 0 {
			continue
		}
		journey := state.Journeys[assignment.JourneyID]
		if journey == nil || journey.Status != "active" {
			continue
		}
		_, _, required := assignmentEligibility(state, assignment)
		if !required {
			continue
		}
		if now.Before(assignment.CreatedAt.Add(unassignedSearchTimeout())) {
			continue
		}
		recordNoQualifiedCollectorException(state, state.Samples[assignment.SampleID], now)
	}
}

// recentlyDeclinedWorkers lists the workers whose decline of this assignment is
// still inside the decay window. Older declines are ignored so no worker is
// permanently excluded from work they once turned down.
func recentlyDeclinedWorkers(assignment *mongostore.Assignment, now time.Time) map[string]bool {
	excluded := map[string]bool{}
	for _, decline := range assignment.Declines {
		if declinePenaltyActive(decline.At, now) {
			excluded[decline.WorkerID] = true
		}
	}
	return excluded
}

func assignmentEligibility(state *mongostore.State, assignment *mongostore.Assignment) (string, string, bool) {
	role := map[string]string{
		"collection": "collection_professional",
		"transport":  "delivery_partner",
		"node":       "node_handler",
		"laboratory": "laboratory_team",
	}[assignment.Kind]
	if assignment.Kind != "collection" {
		return role, "", false
	}
	sample := state.Samples[assignment.SampleID]
	if sample == nil {
		return role, "", false
	}
	sampleType := state.SampleTypes[sample.SampleTypeID]
	if sampleType == nil {
		return role, "", false
	}
	return role, sampleType.RequiredQualification, sampleType.QualifiedCollectorRequired
}

// assignmentLocationScope returns the location a "node" or "laboratory"
// assignment is scoped to (its ToLocationID), so an automatic re-offer during
// maintenance only considers a worker assigned to that same node or
// laboratory. This mirrors the derivation used for the manual re-offer path
// in declineAssignment (see execution.go). Other assignment kinds
// (transport, collection) were never meant to be location-scoped, so this
// returns "" for them, preserving their existing unscoped behavior.
func assignmentLocationScope(assignment *mongostore.Assignment) string {
	if assignment.Kind == "node" || assignment.Kind == "laboratory" {
		return assignment.ToLocationID
	}
	return ""
}

// noQualifiedCollectorReason is the single exception reason for "a collection
// assignment required a qualified collector and none was ever available
// within a reasonable time". It is shared by both paths that can discover
// this exhaustion, for any of the four named types, not just Pulse:
//   - the `next == "" && required` check in Maintain's offer-expiry loop
//     above, triggered when an offer that WAS made to someone lapses with
//     nobody left to re-offer to;
//   - expireUnassignedQualifiedCollectionSearches, the generic search timeout
//     triggered when nobody was EVER offered the assignment in the first
//     place.
//
// Both are conceptually the same outcome — "we searched for a qualified
// collector and none was ever available" — reached via different triggers, so
// they are recorded with the same searchable reason.
const noQualifiedCollectorReason = "no_qualified_collector_available"

// recordNoQualifiedCollectorException marks a sample as an exception because
// no qualified collector was ever available for its collection assignment
// within a reasonable time. It is the shared body behind both trigger paths
// documented on noQualifiedCollectorReason above.
func recordNoQualifiedCollectorException(state *mongostore.State, sample *mongostore.Sample, now time.Time) {
	if sample == nil || terminalSampleStatus(sample.Status) {
		return
	}
	sample.Status = "exception"
	sample.Exception = core.Map{"reason": noQualifiedCollectorReason, "occurredAt": formatTime(now)}
	if journey := state.Journeys[sample.ActiveJourneyID]; journey != nil {
		journey.Status, journey.UpdatedAt = "cancelled", now
		cancelUnfinishedAssignments(state, journey.ID)
		releaseReservations(state, journey.ID)
	}
	sample.ActiveJourneyID = ""
	sample.Version++
	sample.UpdatedAt = now
	// A timer-driven exception is still an exception: it is recorded and
	// published exactly as the deadline expiry beside it is, so the outcome is
	// searchable in the audit trail and reaches the requester and the dispatcher
	// rather than only appearing on the sample record.
	payload := core.Map{"sampleId": sample.ID, "status": "exception", "reason": noQualifiedCollectorReason}
	state.AddAudit(
		mongostore.SystemActorID, mongostore.SystemRole,
		"sample", sample.ID, "sample_exception_recorded", true, payload,
	)
	emit(state, []string{sample.RequesterID, "dispatcher"}, "sample.exception", "sample", sample.ID, payload)
}

func expireSample(state *mongostore.State, sample *mongostore.Sample, now time.Time, reason string) {
	journey := state.Journeys[sample.ActiveJourneyID]
	if journey != nil && journey.Status == "active" {
		if len(journey.SampleIDs) <= 1 {
			journey.Status, journey.UpdatedAt = "cancelled", now
			cancelUnfinishedAssignments(state, journey.ID)
			releaseReservations(state, journey.ID)
		} else {
			// One member of a live batch missed its deadline. The surviving journey
			// keeps running, so it must be left in a state it can still be replanned
			// from: the departing sample's work is cancelled and the route mode is
			// recomputed from the work that remains, exactly as a laboratory
			// exception on a batch member does.
			remaining := []string{}
			for _, sampleID := range journey.SampleIDs {
				if sampleID != sample.ID {
					remaining = append(remaining, sampleID)
				}
			}
			releaseJourneyUnits(state, journey.ID, 1)
			detachSampleFromActiveBatch(state, journey, sample.ID, remaining, now)
		}
	}
	sample.Status, sample.ActiveJourneyID = "expired", ""
	sample.Exception = core.Map{"reason": reason, "occurredAt": formatTime(now)}
	sample.Version++
	sample.UpdatedAt = now
	payload := core.Map{"sampleId": sample.ID, "status": "expired", "reason": reason}
	state.AddAudit(
		mongostore.SystemActorID, mongostore.SystemRole,
		"sample", sample.ID, "sample_expired", true, payload,
	)
	emit(state, []string{sample.RequesterID, "dispatcher"}, "sample.expired", "sample", sample.ID, payload)
}

func releaseJourneyUnits(state *mongostore.State, journeyID string, units int) {
	for _, reservation := range state.Reservations {
		if reservation.JourneyID != journeyID || reservation.Status != "active" || units <= 0 {
			continue
		}
		released := minInt(units, reservation.Units)
		reservation.Units -= released
		if capacity := state.Capacities[reservation.ResourceID]; capacity != nil {
			capacity.ReservedUnits = maxInt(0, capacity.ReservedUnits-released)
			if reservation.Cooled {
				capacity.CooledReserved = maxInt(0, capacity.CooledReserved-released)
			}
			capacity.UpdatedAt = core.Now()
		}
		if reservation.Units == 0 {
			reservation.Status = "released"
		}
	}
}
