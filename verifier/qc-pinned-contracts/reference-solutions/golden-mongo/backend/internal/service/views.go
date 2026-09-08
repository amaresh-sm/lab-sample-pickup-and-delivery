package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func canReadSampleState(state *mongostore.State, p core.Principal, sampleID string) error {
	sample := state.Samples[sampleID]
	if sample == nil {
		return notFound("sample")
	}
	if p.Has("administrator", "dispatcher") || sample.RequesterID == p.ID {
		return nil
	}
	for _, assignment := range state.Assignments {
		if assignment.SampleID == sampleID && assignment.WorkerID == p.ID {
			return nil
		}
	}
	for _, handoff := range state.Handoffs {
		if handoff.SampleID == sampleID && (handoff.SenderID == p.ID || handoff.ReceiverID == p.ID) {
			return nil
		}
	}
	return core.Fail(403, "forbidden", "sample is outside this user's work")
}

func (s *Service) readSampleState(ctx context.Context, p core.Principal, sampleID string) (core.Map, error) {
	var out core.Map
	err := s.Store.View(ctx, func(state *mongostore.State) error {
		if err := canReadSampleState(state, p, sampleID); err != nil {
			return err
		}
		sample, request := state.Samples[sampleID], state.Requests[state.Samples[sampleID].RequestID]
		if request == nil {
			return notFound("collection request")
		}
		activeAssignments := []string{}
		for _, assignment := range state.Assignments {
			if assignment.SampleID == sampleID && assignment.Status != "completed" && assignment.Status != "cancelled" {
				activeAssignments = append(activeAssignments, assignment.ID)
			}
		}
		sort.Strings(activeAssignments)
		out = core.Map{
			"sampleId": sample.ID, "requestId": sample.RequestID, "requesterId": sample.RequesterID,
			"origin":      core.Map{"latitude": request.Latitude, "longitude": request.Longitude},
			"originType":  request.OriginType,
			"requestedAt": formatTime(request.RequestedAt), "deliveryDeadline": formatTime(request.DeliveryDeadline),
			"sampleTypeId": sample.SampleTypeID, "containerType": sample.ContainerType,
			"temperatureMinC": sample.TemperatureMinC, "temperatureMaxC": sample.TemperatureMaxC,
			"incompatibleSampleTypes": append([]string{}, sample.IncompatibleSampleTypes...),
			"serviceability":          request.Serviceability, "activeAssignmentIds": activeAssignments,
			"state":    core.Map{"status": sample.Status},
			"timeline": sampleTimeline(state, sample.ID),
		}
		if sample.LatestLatitude != nil && sample.LatestLongitude != nil {
			out["latestLocation"] = core.Map{
				"latitude": *sample.LatestLatitude, "longitude": *sample.LatestLongitude,
			}
		}
		applySampleLocationFreshness(state, sample.ID, out)
		if sample.ActiveJourneyID != "" {
			out["activeJourneyId"] = sample.ActiveJourneyID
		}
		if sample.TargetLaboratoryID != "" {
			out["targetLaboratoryId"] = sample.TargetLaboratoryID
		}
		return nil
	})
	return out, err
}

// applySampleLocationFreshness reports when a sample's location was last
// recorded and whether that reading is now stale. Every surface that shows a
// sample's location calls this, so a requester or a courier reading their own
// sample sees exactly the verdict the dispatcher's operations view reports, from
// the one shared threshold rule in operational_timers.go.
func applySampleLocationFreshness(state *mongostore.State, sampleID string, out core.Map) {
	if location := latestSampleLocation(state, sampleID); location != nil {
		out["locationRecordedAt"] = formatTime(location.RecordedAt)
		out["locationStale"] = isLocationStale(location.RecordedAt, core.Now())
		return
	}
	// No reading at all is never fresh.
	out["locationStale"] = true
}

func (s *Service) readSampleLifecycle(ctx context.Context, p core.Principal, sampleID string) (core.Map, error) {
	var out core.Map
	err := s.Store.View(ctx, func(state *mongostore.State) error {
		if err := canReadSampleState(state, p, sampleID); err != nil {
			return err
		}
		sample := state.Samples[sampleID]
		out = core.Map{
			"sampleId": sampleID, "status": sample.Status, "sealed": sample.SealedAt != nil,
			"completedLegIds": []string{}, "activeReplacementPlanIds": []string{},
			"timeline": sampleTimeline(state, sampleID),
		}
		if sample.ActiveJourneyID != "" {
			out["activeJourneyId"] = sample.ActiveJourneyID
		}
		if sample.CurrentHolderID != "" {
			out["currentHolderId"] = sample.CurrentHolderID
		}
		if sample.ReceivedLaboratoryID != "" {
			out["receivedLaboratoryId"] = sample.ReceivedLaboratoryID
		}
		if sample.LatestLatitude != nil && sample.LatestLongitude != nil {
			out["latestLocation"] = core.Map{"latitude": *sample.LatestLatitude, "longitude": *sample.LatestLongitude}
		}
		applySampleLocationFreshness(state, sampleID, out)
		if len(sample.Exception) > 0 {
			exception := cloneMap(sample.Exception)
			for _, field := range []string{"occurredAt", "observedAt", "recordedAt"} {
				if occurredAt, ok := exception[field]; ok {
					exception["occurred_at"] = occurredAt
					break
				}
			}
			out["exception"] = exception
		}
		completedLegIDs := []string{}
		for _, assignment := range assignmentsForSample(state, sampleID) {
			if assignment.Kind == "transport" && (assignment.Status == "delivered" || assignment.Status == "completed") {
				completedLegIDs = append(completedLegIDs, assignment.ID)
			}
		}
		out["completedLegIds"] = completedLegIDs
		replacements := []string{}
		for _, journey := range state.Journeys {
			if journey.IsReplacement && journey.Status == "active" && stringSliceContains(journey.SampleIDs, sampleID) {
				replacements = append(replacements, journey.ID)
			}
		}
		sort.Strings(replacements)
		out["activeReplacementPlanIds"] = replacements
		for _, handoff := range state.Handoffs {
			if handoff.SampleID != sampleID || handoff.Status != "pending" {
				continue
			}
			pending := core.Map{
				"handoff_id": handoff.ID, "sender_id": handoff.SenderID, "receiver_id": handoff.ReceiverID,
				"handoffId": handoff.ID, "senderId": handoff.SenderID, "receiverId": handoff.ReceiverID,
				"senderConfirmed":   handoff.SenderConfirmedAt != nil,
				"receiverConfirmed": handoff.ReceiverConfirmedAt != nil, "status": "pending",
			}
			out["pendingHandoffId"], out["pendingHandoff"] = handoff.ID, pending
			break
		}
		return nil
	})
	return out, err
}

func (s *Service) readJourneyPlan(ctx context.Context, p core.Principal, journeyID string) (core.Map, error) {
	if err := require(p, "dispatcher", "administrator", "requester", "collection_professional", "delivery_partner", "node_handler", "laboratory_team"); err != nil {
		return nil, err
	}
	var out core.Map
	err := s.Store.View(ctx, func(state *mongostore.State) error {
		journey := state.Journeys[journeyID]
		if journey == nil {
			return notFound("journey")
		}
		if !p.Has("administrator", "dispatcher") {
			authorized := false
			for _, sampleID := range journey.SampleIDs {
				if sample := state.Samples[sampleID]; sample != nil && sample.RequesterID == p.ID {
					authorized = true
					break
				}
			}
			if !authorized {
				for _, assignment := range state.Assignments {
					if assignment.JourneyID == journeyID && assignment.WorkerID == p.ID {
						authorized = true
						break
					}
				}
			}
			if !authorized {
				for _, handoff := range state.Handoffs {
					if handoff.SenderID != p.ID && handoff.ReceiverID != p.ID {
						continue
					}
					if sample := state.Samples[handoff.SampleID]; sample != nil && sample.ActiveJourneyID == journeyID {
						authorized = true
						break
					}
				}
			}
			if !authorized {
				return core.Fail(403, "forbidden", "journey is outside this user's work")
			}
		}
		out = journeyMap(state, journey)
		// Only the roles that plan the whole journey see the whole journey. A
		// requester sees their own samples; a field worker sees the leg they were
		// assigned and nothing about other legs or other workers.
		if p.Has("administrator", "dispatcher") {
			return nil
		}
		if p.Has("requester") {
			ownedSampleIDs := []string{}
			for _, sampleID := range journey.SampleIDs {
				if sample := state.Samples[sampleID]; sample != nil && sample.RequesterID == p.ID {
					ownedSampleIDs = append(ownedSampleIDs, sampleID)
				}
			}
			if len(ownedSampleIDs) > 0 {
				out = requesterJourneyView(out, ownedSampleIDs)
			}
			return nil
		}
		out = workerJourneyView(state, journey, p.ID)
		return nil
	})
	return out, err
}

// workerJourneyView is the plan as one field worker may see it: the legs and
// facility work assigned to them, the endpoints of those legs, and the
// journey-level facts they need to do the work. Legs belonging to other workers
// and every other worker's identity are absent, not redacted, because the view
// is built from the caller's own assignments rather than filtered out of the
// full plan.
func workerJourneyView(
	state *mongostore.State,
	journey *mongostore.Journey,
	workerID string,
) core.Map {
	out := core.Map{
		"journeyId":           journey.ID,
		"planVersion":         journey.PlanVersion,
		"routeMode":           journey.RouteMode,
		"status":              journey.Status,
		"estimatedDeliveryAt": formatTime(journey.EstimatedDeliveryAt),
		"scope":               "assigned_legs",
	}
	if journey.ParentJourneyID != "" {
		out["parentJourneyId"] = journey.ParentJourneyID
	}
	legs, assignmentIDs, sampleIDs := []core.Map{}, []string{}, []string{}
	collectionIDs, nodeIDs, laboratoryIDs := []string{}, []string{}, []string{}
	endpoints := map[string]bool{}
	for _, assignment := range assignmentsForJourney(state, journey.ID) {
		if assignment.WorkerID != workerID || assignment.Status == "cancelled" {
			continue
		}
		assignmentIDs = append(assignmentIDs, assignment.ID)
		sampleIDs = append(sampleIDs, assignment.SampleID)
		if assignment.FromLocationID != "" {
			endpoints[assignment.FromLocationID] = true
		}
		if assignment.ToLocationID != "" {
			endpoints[assignment.ToLocationID] = true
		}
		switch assignment.Kind {
		case "collection":
			collectionIDs = append(collectionIDs, assignment.ID)
			continue
		case "node":
			nodeIDs = append(nodeIDs, assignment.ID)
			continue
		case "laboratory":
			laboratoryIDs = append(laboratoryIDs, assignment.ID)
			continue
		case "transport":
		default:
			continue
		}
		leg := core.Map{
			"assignment_id": assignment.ID, "assignmentId": assignment.ID,
			"sequence": assignment.LegSequence, "status": assignment.Status,
			"sampleId": assignment.SampleID,
			"workerId": workerID, "worker_id": workerID,
		}
		if assignment.FromLocationID != "" {
			leg["from_location_id"], leg["fromLocationId"] = assignment.FromLocationID, assignment.FromLocationID
		}
		if assignment.ToLocationID != "" {
			leg["to_location_id"], leg["toLocationId"] = assignment.ToLocationID, assignment.ToLocationID
		}
		legs = append(legs, leg)
	}
	out["legs"] = legs
	out["assignmentIds"] = sortStringsUnique(assignmentIDs)
	out["sampleIds"] = sortStringsUnique(sampleIDs)
	out["collectionAssignmentIds"] = collectionIDs
	out["nodeAssignmentIds"], out["laboratoryAssignmentIds"] = nodeIDs, laboratoryIDs
	if len(assignmentIDs) > 0 {
		out["assignedWorkerIds"] = []string{workerID}
	} else {
		out["assignedWorkerIds"] = []string{}
	}
	stops := []core.Map{}
	for _, stop := range stopsForJourney(state, journey.ID) {
		if !endpoints[stop.LocationID] {
			continue
		}
		item := core.Map{
			"stopId":     stop.ID,
			"locationId": stop.LocationID, "stopType": stop.StopType,
			"location_id": stop.LocationID, "stop_type": stop.StopType,
			"sequence": stop.Sequence, "estimatedAt": formatTime(stop.EstimatedAt),
			"estimated_at": formatTime(stop.EstimatedAt),
			"dwellMinutes": stop.DwellMinutes,
		}
		if coordinate := locationCoordinate(state, stop.LocationID); coordinate != nil {
			item["coordinate"] = coordinate
		}
		if stop.CompletedAt != nil {
			item["completedAt"] = formatTime(*stop.CompletedAt)
		}
		stops = append(stops, item)
	}
	out["orderedStops"] = stops
	return out
}

func requesterJourneyView(plan core.Map, ownedSampleIDs []string) core.Map {
	out := core.Map{"sampleIds": sorted(ownedSampleIDs)}
	for _, field := range []string{
		"journeyId", "parentJourneyId", "planVersion", "routeMode", "status",
		"estimatedDeliveryAt", "orderedStops", "totalCostUnits",
	} {
		if value, ok := plan[field]; ok {
			out[field] = value
		}
	}
	legs := []core.Map{}
	if raw, ok := plan["legs"].([]core.Map); ok {
		for _, leg := range raw {
			item := core.Map{}
			for _, field := range []string{
				"sequence", "status", "fromLocationId", "toLocationId",
				"from_location_id", "to_location_id",
			} {
				if value, exists := leg[field]; exists {
					item[field] = value
				}
			}
			legs = append(legs, item)
		}
	}
	out["legs"] = legs
	return out
}

func journeyMap(state *mongostore.State, journey *mongostore.Journey) core.Map {
	out := core.Map{
		"journeyId": journey.ID, "planVersion": journey.PlanVersion,
		"routeMode": journey.RouteMode, "status": journey.Status,
		"estimatedDeliveryAt": formatTime(journey.EstimatedDeliveryAt),
		"totalCostUnits":      journey.TotalCostUnits,
		"capacityUsed":        journey.CapacityUsed, "capacityAvailable": journey.CapacityAvailable,
		"sampleIds": sorted(journey.SampleIDs),
	}
	if journey.ParentJourneyID != "" {
		out["parentJourneyId"] = journey.ParentJourneyID
	}
	stops := []core.Map{}
	for _, stop := range stopsForJourney(state, journey.ID) {
		item := core.Map{
			"stopId":      stop.ID,
			"location_id": stop.LocationID, "stop_type": stop.StopType,
			"estimated_at": formatTime(stop.EstimatedAt),
			"locationId":   stop.LocationID, "stopType": stop.StopType,
			"sequence": stop.Sequence, "estimatedAt": formatTime(stop.EstimatedAt),
			"dwellMinutes": stop.DwellMinutes,
		}
		if coordinate := locationCoordinate(state, stop.LocationID); coordinate != nil {
			item["coordinate"] = coordinate
		}
		if stop.CompletedAt != nil {
			item["completedAt"] = formatTime(*stop.CompletedAt)
		}
		stops = append(stops, item)
	}
	out["orderedStops"] = stops

	legs, workers := []core.Map{}, []string{}
	collectionAssignments, nodeAssignments, laboratoryAssignments := []string{}, []string{}, []string{}
	for _, assignment := range assignmentsForJourney(state, journey.ID) {
		if assignment.Status == "cancelled" {
			continue
		}
		switch assignment.Kind {
		case "collection":
			collectionAssignments = append(collectionAssignments, assignment.ID)
			if assignment.SampleID == journey.PrimarySampleID {
				out["collectionAssignmentId"] = assignment.ID
			}
			if assignment.WorkerID != "" {
				workers = append(workers, assignment.WorkerID)
			}
			continue
		case "node":
			nodeAssignments = append(nodeAssignments, assignment.ID)
			if assignment.WorkerID != "" {
				workers = append(workers, assignment.WorkerID)
			}
			continue
		case "laboratory":
			laboratoryAssignments = append(laboratoryAssignments, assignment.ID)
			if assignment.WorkerID != "" {
				workers = append(workers, assignment.WorkerID)
			}
			continue
		case "transport":
			// Transport assignments are represented as route legs below.
			if assignment.SampleID != journey.PrimarySampleID {
				if assignment.WorkerID != "" {
					workers = append(workers, assignment.WorkerID)
				}
				continue
			}
		default:
			continue
		}
		leg := core.Map{
			"assignment_id": assignment.ID, "assignmentId": assignment.ID,
			"sequence": assignment.LegSequence, "status": assignment.Status,
		}
		if assignment.FromLocationID != "" {
			leg["from_location_id"], leg["fromLocationId"] = assignment.FromLocationID, assignment.FromLocationID
		}
		if assignment.ToLocationID != "" {
			leg["to_location_id"], leg["toLocationId"] = assignment.ToLocationID, assignment.ToLocationID
		}
		if assignment.WorkerID != "" {
			leg["worker_id"], leg["workerId"] = assignment.WorkerID, assignment.WorkerID
			workers = append(workers, assignment.WorkerID)
		}
		legs = append(legs, leg)
		if assignment.LegSequence == 1 {
			out["firstTransportAssignmentId"] = assignment.ID
		}
		out["finalTransportAssignmentId"] = assignment.ID
		if assignment.LegSequence == 2 {
			out["middleTransportAssignmentId"] = assignment.ID
		}
	}
	out["legs"], out["assignedWorkerIds"] = legs, sortStringsUnique(workers)
	out["collectionAssignmentIds"] = collectionAssignments
	out["nodeAssignmentIds"], out["laboratoryAssignmentIds"] = nodeAssignments, laboratoryAssignments
	return out
}

// assignmentWorkKinds maps the four field roles to the assignment kind that
// carries their work.
var assignmentWorkKinds = map[string]string{
	"collection_professional": "collection",
	"delivery_partner":        "transport",
	"node_handler":            "node",
	"laboratory_team":         "laboratory",
}

// listRoleWork returns the caller's current and past work for the role they are
// acting under, split into pending / inProgress / completed with an ordered
// status history per item.
//
// The role is validated against the SELECTED role, not against the union of held
// roles: a user may only list work for the role they are currently working under,
// and an administrator is not exempt.
func (s *Service) listRoleWork(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if p.RoleSelectionRequired() {
		return nil, core.Fail(
			409,
			"role_not_selected",
			"this account holds more than one role; select the active role before acting",
		)
	}
	role := strings.TrimSpace(core.String(in["role"]))
	if role == "" {
		role = p.ActiveRole()
	}
	if role == "" || !p.Has(role) {
		return nil, core.Fail(403, "forbidden", "work can only be listed for the selected role")
	}
	switch role {
	case "requester":
		return s.listRequesterWork(ctx, p.ID)
	case "dispatcher":
		return s.listDispatcherWork(ctx)
	case "administrator":
		return s.listAdministratorWork(ctx)
	}
	kind := assignmentWorkKinds[role]
	if kind == "" {
		return nil, core.Fail(400, "invalid_role", "role has no work queue")
	}
	out := core.Map{"pending": []core.Map{}, "inProgress": []core.Map{}, "completed": []core.Map{}}
	err := s.Store.View(ctx, func(state *mongostore.State) error {
		assignments := []*mongostore.Assignment{}
		for _, assignment := range state.Assignments {
			if assignment.WorkerID == p.ID && assignment.Kind == kind {
				assignments = append(assignments, assignment)
			}
		}
		sort.Slice(assignments, func(i, j int) bool {
			if !assignments[i].CreatedAt.Equal(assignments[j].CreatedAt) {
				return assignments[i].CreatedAt.Before(assignments[j].CreatedAt)
			}
			return assignments[i].ID < assignments[j].ID
		})
		for _, assignment := range assignments {
			item := core.Map{
				"assignment_id": assignment.ID, "sample_id": assignment.SampleID,
				"assignmentId": assignment.ID, "sampleId": assignment.SampleID,
				"status": assignment.Status, "kind": assignment.Kind,
				"history": entityTimeline(state, "assignment", assignment.ID),
			}
			if assignment.JourneyID != "" {
				item["journeyId"] = assignment.JourneyID
			}
			if assignment.FromLocationID != "" {
				item["fromLocationId"] = assignment.FromLocationID
			}
			if assignment.ToLocationID != "" {
				item["toLocationId"] = assignment.ToLocationID
				if capacity := capacityMap(state.Capacities[assignment.ToLocationID]); capacity != nil {
					item["destinationCapacity"] = capacity
				}
			}
			if assignment.Kind == "node" {
				item["nodeId"] = assignment.ToLocationID
				item["incomingFromLocationId"] = assignment.FromLocationID
				stops := stopsForJourney(state, assignment.JourneyID)
				for index, stop := range stops {
					if stop.LocationID == assignment.ToLocationID && index+1 < len(stops) {
						item["outgoingToLocationId"] = stops[index+1].LocationID
						break
					}
				}
			}
			if assignment.Kind == "laboratory" {
				item["laboratoryId"] = assignment.ToLocationID
				if sample := state.Samples[assignment.SampleID]; sample != nil {
					item["expectedSampleTypeId"] = sample.SampleTypeID
				}
			}
			if assignment.LegSequence > 0 {
				item["sequence"] = assignment.LegSequence
			}
			if assignment.OfferExpiresAt != nil {
				item["offerExpiresAt"] = formatTime(*assignment.OfferExpiresAt)
			}
			bucket := "inProgress"
			if assignment.Status == "pending" {
				bucket = "pending"
			}
			if assignment.Status == "completed" || assignment.Status == "delivered" || assignment.Status == "cancelled" {
				bucket = "completed"
			}
			out[bucket] = append(out[bucket].([]core.Map), item)
		}
		return nil
	})
	return out, err
}

// emptyRoleWork is the shape every role's work view returns, so a caller can
// read the same three buckets whichever role it is acting under.
func emptyRoleWork() core.Map {
	return core.Map{"pending": []core.Map{}, "inProgress": []core.Map{}, "completed": []core.Map{}}
}

// sampleWorkBucket places a sample in the current/past split. A sample not yet
// collected is pending, a sample in motion is in progress, and a sample that has
// reached a terminal state is past work.
func sampleWorkBucket(status string) string {
	switch status {
	case "requested", "planned":
		return "pending"
	case "received", "cancelled", "expired", "exception":
		return "completed"
	default:
		return "inProgress"
	}
}

// sampleWorkItem describes one sample as a unit of work, with the ordered status
// history of everything that happened to it.
func sampleWorkItem(state *mongostore.State, sample *mongostore.Sample) core.Map {
	item := core.Map{
		"sampleId": sample.ID, "sample_id": sample.ID,
		"requestId": sample.RequestID, "status": sample.Status,
		"kind":    "sample",
		"history": sampleTimeline(state, sample.ID),
	}
	if request := state.Requests[sample.RequestID]; request != nil {
		item["serviceability"] = request.Serviceability
		item["requestedAt"] = formatTime(request.RequestedAt)
		item["deliveryDeadline"] = formatTime(request.DeliveryDeadline)
		item["earliestCollectionAt"], item["latestCollectionAt"] = collectionWindowFields(request)
	}
	if sample.ActiveJourneyID != "" {
		item["journeyId"], item["activeJourneyId"] = sample.ActiveJourneyID, sample.ActiveJourneyID
	}
	if sample.TargetLaboratoryID != "" {
		item["laboratoryId"] = sample.TargetLaboratoryID
	}
	if sample.CurrentHolderID != "" {
		item["currentHolderId"] = sample.CurrentHolderID
	}
	applySampleLocationFreshness(state, sample.ID, item)
	return item
}

func collectionWindowFields(request *mongostore.CollectionRequest) (string, string) {
	earliest, latest := requestCollectionWindow(request)
	return formatTime(earliest), formatTime(latest)
}

func sortedSamples(samples []*mongostore.Sample) []*mongostore.Sample {
	sort.Slice(samples, func(i, j int) bool {
		if !samples[i].CreatedAt.Equal(samples[j].CreatedAt) {
			return samples[i].CreatedAt.Before(samples[j].CreatedAt)
		}
		return samples[i].ID < samples[j].ID
	})
	return samples
}

// listRequesterWork is a requester's own work: the requests they raised and the
// samples those requests produced, current and past.
func (s *Service) listRequesterWork(ctx context.Context, requesterID string) (core.Map, error) {
	out := emptyRoleWork()
	err := s.Store.View(ctx, func(state *mongostore.State) error {
		samples := []*mongostore.Sample{}
		for _, sample := range state.Samples {
			if sample.RequesterID == requesterID {
				samples = append(samples, sample)
			}
		}
		for _, sample := range sortedSamples(samples) {
			bucket := sampleWorkBucket(sample.Status)
			out[bucket] = append(out[bucket].([]core.Map), sampleWorkItem(state, sample))
		}
		return nil
	})
	return out, err
}

// listDispatcherWork is the journeys a dispatcher is responsible for. A journey
// nobody has accepted work on yet is pending, a journey under way is in
// progress, and a journey that is no longer active is past work.
func (s *Service) listDispatcherWork(ctx context.Context) (core.Map, error) {
	out := emptyRoleWork()
	err := s.Store.View(ctx, func(state *mongostore.State) error {
		journeys := make([]*mongostore.Journey, 0, len(state.Journeys))
		for _, journey := range state.Journeys {
			journeys = append(journeys, journey)
		}
		sort.Slice(journeys, func(i, j int) bool {
			if !journeys[i].CreatedAt.Equal(journeys[j].CreatedAt) {
				return journeys[i].CreatedAt.Before(journeys[j].CreatedAt)
			}
			return journeys[i].ID < journeys[j].ID
		})
		for _, journey := range journeys {
			assignments := assignmentsForJourney(state, journey.ID)
			started := false
			for _, assignment := range assignments {
				if assignment.Status != "pending" && assignment.Status != "cancelled" {
					started = true
					break
				}
			}
			item := core.Map{
				"journeyId": journey.ID, "journey_id": journey.ID,
				"kind": "journey", "status": journey.Status,
				"routeMode": journey.RouteMode, "planVersion": journey.PlanVersion,
				"sampleIds":           sorted(journey.SampleIDs),
				"estimatedDeliveryAt": formatTime(journey.EstimatedDeliveryAt),
				"assignmentCount":     len(assignments),
				"history":             journeyTimeline(state, journey.ID),
			}
			bucket := "inProgress"
			if journey.Status != "active" {
				bucket = "completed"
			} else if !started {
				bucket = "pending"
			}
			out[bucket] = append(out[bucket].([]core.Map), item)
		}
		return nil
	})
	return out, err
}

// listAdministratorWork is the administrative view of every sample the network
// is handling, with the same current/past split and per-item history the other
// roles get. An administrator governs the whole network rather than a personal
// queue, so nothing here is filtered by ownership or assignment.
func (s *Service) listAdministratorWork(ctx context.Context) (core.Map, error) {
	out := emptyRoleWork()
	err := s.Store.View(ctx, func(state *mongostore.State) error {
		samples := make([]*mongostore.Sample, 0, len(state.Samples))
		for _, sample := range state.Samples {
			samples = append(samples, sample)
		}
		for _, sample := range sortedSamples(samples) {
			item := sampleWorkItem(state, sample)
			item["requesterId"] = sample.RequesterID
			bucket := sampleWorkBucket(sample.Status)
			out[bucket] = append(out[bucket].([]core.Map), item)
		}
		return nil
	})
	return out, err
}

func (s *Service) readOperations(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "dispatcher", "administrator"); err != nil {
		return nil, err
	}
	out := core.Map{
		"samples": []core.Map{}, "workers": []core.Map{}, "routes": []core.Map{},
		"locations": []core.Map{}, "connections": []core.Map{},
		"handoffs": []core.Map{}, "delays": []core.Map{}, "exceptions": []core.Map{},
	}
	err := s.Store.View(ctx, func(state *mongostore.State) error {
		samples := make([]*mongostore.Sample, 0, len(state.Samples))
		for _, sample := range state.Samples {
			samples = append(samples, sample)
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i].UpdatedAt.After(samples[j].UpdatedAt) })
		if len(samples) > 500 {
			samples = samples[:500]
		}
		for _, sample := range samples {
			item := core.Map{"sample_id": sample.ID, "sampleId": sample.ID, "status": sample.Status}
			if request := state.Requests[sample.RequestID]; request != nil {
				item["requestId"] = request.ID
				item["origin"] = core.Map{"latitude": request.Latitude, "longitude": request.Longitude}
				item["deliveryDeadline"] = formatTime(request.DeliveryDeadline)
			}
			if sample.CurrentHolderID != "" {
				item["currentHolderId"] = sample.CurrentHolderID
			}
			if sample.ActiveJourneyID != "" {
				item["activeJourneyId"] = sample.ActiveJourneyID
			}
			if sample.LatestLatitude != nil && sample.LatestLongitude != nil {
				item["latestLocation"] = core.Map{"latitude": *sample.LatestLatitude, "longitude": *sample.LatestLongitude}
			}
			applySampleLocationFreshness(state, sample.ID, item)
			out["samples"] = append(out["samples"].([]core.Map), item)
		}

		workers := []*mongostore.User{}
		for _, user := range state.Users {
			if stringSliceContains(user.Roles, "collection_professional") || stringSliceContains(user.Roles, "delivery_partner") {
				workers = append(workers, user)
			}
		}
		sort.Slice(workers, func(i, j int) bool { return workers[i].ID < workers[j].ID })
		for _, user := range workers {
			item := core.Map{
				"worker_id": user.ID, "workerId": user.ID, "available": user.Available,
				"capacityUnits": user.CapacityUnits, "assignmentCount": user.AssignmentCount,
				"fullName": user.FullName, "roles": append([]string{}, user.Roles...),
			}
			if user.Available {
				item["status"] = "available"
			} else {
				item["status"] = "unavailable"
			}
			if location := latestWorkerLocation(state, user.ID); location != nil {
				coordinate := core.Map{"latitude": location.Latitude, "longitude": location.Longitude}
				item["latest_location"], item["latestLocation"] = coordinate, coordinate
				item["locationRecordedAt"] = formatTime(location.RecordedAt)
				item["locationStale"] = isLocationStale(location.RecordedAt, core.Now())
			} else {
				item["locationStale"] = true
			}
			out["workers"] = append(out["workers"].([]core.Map), item)
		}

		// One network entity: every site is reported once with the functions it
		// provides and whether each of those functions is currently active.
		locations := make([]*mongostore.Location, 0, len(state.Locations))
		for _, location := range state.Locations {
			locations = append(locations, location)
		}
		sort.Slice(locations, func(i, j int) bool { return locations[i].ID < locations[j].ID })
		for _, location := range locations {
			item := core.Map{
				"id": location.ID, "locationId": location.ID,
				"name": location.Name, "active": location.Active,
				"coordinate":      core.Map{"latitude": location.Latitude, "longitude": location.Longitude},
				"coverageKm":      locationCoverageKM(location),
				"handlingMinutes": location.HandlingMinutes,
				"functions":       functionsView(location.Functions),
			}
			if location.Provides(mongostore.FunctionLaboratory) {
				item["acceptedSampleTypeIds"] = laboratoryAcceptanceIDs(state, location.ID)
			}
			if capacity := capacityMap(state.Capacities[location.ID]); capacity != nil {
				item["capacity"] = capacity
			}
			out["locations"] = append(out["locations"].([]core.Map), item)
		}

		// A connection carries regular and cooled capacity of its own, which
		// planning reserves against, so the same six numbers a location reports have
		// to be readable for a connection too. Without this array the capacity
		// configured against a connection would be write-only.
		connections := make([]*mongostore.Connection, 0, len(state.Connections))
		for _, connection := range state.Connections {
			connections = append(connections, connection)
		}
		sort.Slice(connections, func(i, j int) bool { return connections[i].ID < connections[j].ID })
		for _, connection := range connections {
			item := core.Map{
				"id": connection.ID, "connectionId": connection.ID,
				"fromLocationId": connection.FromLocationID,
				"toLocationId":   connection.ToLocationID,
				"travelMinutes":  connection.TravelMinutes,
				"costUnits":      connection.CostUnits,
				"cooled":         connection.Cooled,
				"active":         connection.Active,
			}
			if capacity := capacityMap(state.Capacities[connection.ID]); capacity != nil {
				item["capacity"] = capacity
			}
			out["connections"] = append(out["connections"].([]core.Map), item)
		}

		filters := core.Object(in["filters"])
		sampleTypeID := core.String(filters["sampleTypeId"])
		if sampleTypeID == "" {
			sampleTypeID = core.String(filters["sample_type_id"])
		}
		counts, ineligible := []int{}, 0
		for _, user := range workers {
			if !user.Active || !user.Available || !stringSliceContains(user.Roles, "collection_professional") {
				continue
			}
			if sampleTypeID != "" {
				sampleType := state.SampleTypes[sampleTypeID]
				if sampleType == nil || sampleType.QualifiedCollectorRequired &&
					!userHasActiveCertification(user, sampleType.RequiredQualification) {
					continue
				}
			}
			count := 0
			for _, assignment := range state.Assignments {
				if assignment.WorkerID == user.ID && (sampleTypeID == "" || assignment.Kind == "collection") {
					count++
				}
			}
			counts = append(counts, count)
		}
		for _, assignment := range state.Assignments {
			if assignment.Kind != "collection" || assignment.WorkerID == "" {
				continue
			}
			sample, user := state.Samples[assignment.SampleID], state.Users[assignment.WorkerID]
			if sample == nil || user == nil {
				continue
			}
			sampleType := state.SampleTypes[sample.SampleTypeID]
			if sampleType != nil && sampleType.QualifiedCollectorRequired &&
				!userHasActiveCertification(user, sampleType.RequiredQualification) {
				ineligible++
			}
		}
		spread := 0
		if len(counts) > 0 {
			minimum, maximum := counts[0], counts[0]
			for _, count := range counts[1:] {
				minimum, maximum = minInt(minimum, count), maxInt(maximum, count)
			}
			spread = maximum - minimum
		}
		out["assignmentCountSpread"], out["ineligibleAssignmentCount"] = spread, ineligible

		for _, journey := range activeJourneys(state) {
			sampleIDs := sorted(journey.SampleIDs)
			route := core.Map{
				"journey_id": journey.ID, "sample_ids": sampleIDs,
				"journeyId": journey.ID, "routeMode": journey.RouteMode,
				"status": journey.Status, "planVersion": journey.PlanVersion, "sampleIds": sampleIDs,
				"orderedStops": []core.Map{}, "coordinates": []core.Map{},
			}
			for _, stop := range stopsForJourney(state, journey.ID) {
				stopItem := core.Map{
					"locationId": stop.LocationID, "stopType": stop.StopType,
					"sequence": stop.Sequence, "estimatedAt": formatTime(stop.EstimatedAt),
				}
				if coordinate := locationCoordinate(state, stop.LocationID); coordinate != nil {
					stopItem["coordinate"] = coordinate
					route["coordinates"] = append(route["coordinates"].([]core.Map), coordinate)
				}
				route["orderedStops"] = append(route["orderedStops"].([]core.Map), stopItem)
			}
			out["routes"] = append(out["routes"].([]core.Map), route)
		}
		for _, handoff := range state.Handoffs {
			if handoff.Status != "pending" {
				continue
			}
			item := core.Map{
				"handoff_id": handoff.ID, "sample_id": handoff.SampleID,
				"handoffId": handoff.ID, "sampleId": handoff.SampleID,
				"senderId": handoff.SenderID, "receiverId": handoff.ReceiverID,
				"senderConfirmed":   handoff.SenderConfirmedAt != nil,
				"receiverConfirmed": handoff.ReceiverConfirmedAt != nil,
				"status":            handoff.Status,
			}
			if sample := state.Samples[handoff.SampleID]; sample != nil && sample.CurrentHolderID != "" {
				item["currentHolderId"] = sample.CurrentHolderID
			}
			out["handoffs"] = append(out["handoffs"].([]core.Map), item)
		}
		exceptions, delays := []core.Map{}, []core.Map{}
		for _, sample := range samples {
			if sample.Status == "exception" || sample.Status == "expired" {
				reason := "exception"
				if value := core.String(sample.Exception["reason"]); value != "" {
					reason = value
				}
				exceptions = append(exceptions, core.Map{"sample_id": sample.ID, "sampleId": sample.ID, "reason": reason})
			}
			request := state.Requests[sample.RequestID]
			if request != nil && request.DeliveryDeadline.Before(core.Now()) &&
				sample.Status != "received" && sample.Status != "exception" && sample.Status != "expired" {
				delays = append(delays, core.Map{
					"sample_id": sample.ID, "delivery_deadline": formatTime(request.DeliveryDeadline),
					"sampleId": sample.ID, "deliveryDeadline": formatTime(request.DeliveryDeadline),
					"reason": "deadline_overdue",
				})
			}
		}
		out["exceptions"], out["delays"] = exceptions, delays
		return nil
	})
	return out, err
}

func locationCoordinate(state *mongostore.State, locationID string) core.Map {
	if location := state.Locations[locationID]; location != nil {
		return core.Map{"latitude": location.Latitude, "longitude": location.Longitude}
	}
	return nil
}

func capacityMap(capacity *mongostore.Capacity) core.Map {
	if capacity == nil {
		return nil
	}
	return core.Map{
		"capacityUnits":        capacity.CapacityUnits,
		"reservedUnits":        capacity.ReservedUnits,
		"availableUnits":       maxInt(0, capacity.CapacityUnits-capacity.ReservedUnits),
		"cooledCapacityUnits":  capacity.CooledUnits,
		"cooledReservedUnits":  capacity.CooledReserved,
		"cooledAvailableUnits": maxInt(0, capacity.CooledUnits-capacity.CooledReserved),
	}
}

func laboratoryAcceptanceIDs(state *mongostore.State, laboratoryID string) []string {
	accepted := []string{}
	for _, acceptance := range state.Acceptances {
		if acceptance.LaboratoryID == laboratoryID && acceptance.Accepts {
			accepted = append(accepted, acceptance.SampleTypeID)
		}
	}
	sort.Strings(accepted)
	return accepted
}

func latestWorkerLocation(state *mongostore.State, workerID string) *mongostore.WorkerLocation {
	var latest *mongostore.WorkerLocation
	for _, locations := range state.WorkerLocations {
		for _, location := range locations {
			if location.WorkerID != workerID {
				continue
			}
			if latest == nil || location.RecordedAt.After(latest.RecordedAt) ||
				location.RecordedAt.Equal(latest.RecordedAt) && location.Sequence > latest.Sequence {
				latest = location
			}
		}
	}
	return latest
}

func latestSampleLocation(state *mongostore.State, sampleID string) *mongostore.WorkerLocation {
	var latest *mongostore.WorkerLocation
	for assignmentID, locations := range state.WorkerLocations {
		assignment := state.Assignments[assignmentID]
		if assignment == nil || assignment.SampleID != sampleID {
			continue
		}
		for _, location := range locations {
			if latest == nil || location.RecordedAt.After(latest.RecordedAt) ||
				location.RecordedAt.Equal(latest.RecordedAt) && location.Sequence > latest.Sequence {
				latest = location
			}
		}
	}
	return latest
}

func sampleTimeline(state *mongostore.State, sampleID string) []core.Map {
	timeline := []core.Map{}
	for _, event := range state.AuditEvents {
		if !auditEventBelongsToSample(state, event, sampleID) {
			continue
		}
		timeline = append(timeline, auditEventMap(event))
	}
	return timeline
}

// journeyTimeline is the ordered history of one journey: the plan changes made
// to it plus everything that happened to the work and the samples it carries. A
// journey's own audit records alone would omit its legs, so a dispatcher's work
// item would have no timeline.
func journeyTimeline(state *mongostore.State, journeyID string) []core.Map {
	assignments := map[string]bool{}
	for _, assignment := range assignmentsForJourney(state, journeyID) {
		assignments[assignment.ID] = true
	}
	samples := map[string]bool{}
	if journey := state.Journeys[journeyID]; journey != nil {
		for _, sampleID := range journey.SampleIDs {
			samples[sampleID] = true
		}
	}
	timeline := []core.Map{}
	for _, event := range state.AuditEvents {
		switch event.EntityType {
		case "journey":
			if event.EntityID != journeyID {
				continue
			}
		case "assignment":
			if !assignments[event.EntityID] {
				continue
			}
		case "sample":
			if !samples[event.EntityID] {
				continue
			}
		case "handoff":
			handoff := state.Handoffs[event.EntityID]
			if handoff == nil || !samples[handoff.SampleID] {
				continue
			}
		default:
			continue
		}
		timeline = append(timeline, auditEventMap(event))
	}
	return timeline
}

func entityTimeline(state *mongostore.State, entityType, entityID string) []core.Map {
	timeline := []core.Map{}
	for _, event := range state.AuditEvents {
		if event.EntityType == entityType && event.EntityID == entityID {
			timeline = append(timeline, auditEventMap(event))
		}
	}
	return timeline
}

func auditEventBelongsToSample(state *mongostore.State, event *mongostore.AuditEvent, sampleID string) bool {
	switch event.EntityType {
	case "sample":
		return event.EntityID == sampleID
	case "assignment":
		assignment := state.Assignments[event.EntityID]
		return assignment != nil && assignment.SampleID == sampleID
	case "handoff":
		handoff := state.Handoffs[event.EntityID]
		return handoff != nil && handoff.SampleID == sampleID
	case "journey":
		journey := state.Journeys[event.EntityID]
		return journey != nil && stringSliceContains(journey.SampleIDs, sampleID)
	case "disruption":
		disruption := state.Disruptions[event.EntityID]
		return disruption != nil && stringSliceContains(disruption.AffectedSampleIDs, sampleID)
	default:
		return false
	}
}

func auditEventMap(event *mongostore.AuditEvent) core.Map {
	return core.Map{
		"eventId":    event.ID,
		"action":     event.Action,
		"actorId":    event.ActorID,
		"role":       auditEventRole(event),
		"entityType": event.EntityType,
		"entityId":   event.EntityID,
		"success":    event.Success,
		"occurredAt": formatTime(event.OccurredAt),
		"details":    cloneMap(event.Details),
	}
}

// auditEventRole reports the role an action was taken under. Records written
// before the role was captured are attributed to the application's own timers,
// which is the only actor that has no selected role.
func auditEventRole(event *mongostore.AuditEvent) string {
	if event.Role != "" {
		return event.Role
	}
	return mongostore.SystemRole
}

// Audit search ordering. Newest-first is the default so a long-lived instance
// stays traceable: the trail grows continuously — every permission denial and
// every failed sign-in appends to it — and an oldest-first response would spend
// a whole page on the instance's first minutes and never reach recent history.
const (
	auditOrderNewestFirst = "newest_first"
	auditOrderOldestFirst = "oldest_first"
)

// Audit search paging.
//
// auditPageDefault bounds an ordinary search. auditTracePageDefault is the
// larger default used when the caller pins one entity (entityType + entityId),
// because "trace this request or sample across its full history" must return
// the whole chain rather than a page of it. Either way the response reports
// total and nextOffset, so every matching record remains reachable by paging.
const (
	auditPageDefault      = 500
	auditTracePageDefault = 5000
	auditPageMax          = 5000
)

// auditPage is one requested window over the matching audit records.
type auditPage struct {
	limit  int
	offset int
	order  string
}

// parseAuditPage reads the paging and ordering controls. An entity-pinned search
// defaults to the larger page so a full entity trace arrives in one call.
func parseAuditPage(in core.Map, entityTrace bool) (auditPage, error) {
	page := auditPage{limit: auditPageDefault, order: auditOrderNewestFirst}
	if entityTrace {
		page.limit = auditTracePageDefault
	}
	if value, present := in["limit"]; present && value != nil {
		limit := core.Int(value)
		if limit < 1 || limit > auditPageMax {
			return auditPage{}, core.Fail(
				400,
				"invalid_limit",
				"limit must be between 1 and 5000",
			)
		}
		page.limit = limit
	}
	if value, present := in["offset"]; present && value != nil {
		offset := core.Int(value)
		if offset < 0 {
			return auditPage{}, core.Fail(400, "invalid_offset", "offset cannot be negative")
		}
		page.offset = offset
	}
	if value := strings.TrimSpace(core.String(in["order"])); value != "" {
		if value != auditOrderNewestFirst && value != auditOrderOldestFirst {
			return auditPage{}, core.Fail(
				400,
				"invalid_order",
				"order must be newest_first or oldest_first",
			)
		}
		page.order = value
	}
	return page, nil
}

// auditEventMatcher builds the filter predicate for one audit search.
func auditEventMatcher(in core.Map, from, to *time.Time) func(*mongostore.AuditEvent) bool {
	query := strings.ToLower(core.String(in["query"]))
	actorID := core.String(in["actorId"])
	entityType := core.String(in["entityType"])
	entityID := core.String(in["entityId"])
	action := core.String(in["action"])
	role := core.String(in["role"])
	successValue, successFiltered := in["success"]
	successWanted := successFiltered && successValue != nil && core.Bool(successValue)
	successFiltered = successFiltered && successValue != nil
	return func(event *mongostore.AuditEvent) bool {
		if actorID != "" && event.ActorID != actorID {
			return false
		}
		if entityType != "" && event.EntityType != entityType {
			return false
		}
		if entityID != "" && event.EntityID != entityID {
			return false
		}
		if action != "" && event.Action != action {
			return false
		}
		if role != "" && auditEventRole(event) != role {
			return false
		}
		if successFiltered && event.Success != successWanted {
			return false
		}
		if from != nil && event.OccurredAt.Before(*from) ||
			to != nil && event.OccurredAt.After(*to) {
			return false
		}
		if query != "" {
			raw, _ := json.Marshal(event.Details)
			if !strings.Contains(strings.ToLower(event.Action), query) &&
				!strings.Contains(strings.ToLower(string(raw)), query) {
				return false
			}
		}
		return true
	}
}

// auditSearchEntry renders one searchable audit record. Every record carries the
// seven accountability facts, and actorId is always present: a timer-driven
// record is attributed to the application itself rather than to nobody.
func auditSearchEntry(event *mongostore.AuditEvent) core.Map {
	auditID := core.String(event.ID)
	role := auditEventRole(event)
	occurredAt := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	actorID := event.ActorID
	if actorID == "" {
		actorID = mongostore.SystemActorID
	}
	return core.Map{
		"audit_id": auditID, "entity_type": event.EntityType, "entity_id": event.EntityID,
		"occurred_at": occurredAt,
		"auditId":     auditID, "entityType": event.EntityType, "entityId": event.EntityID,
		"action": event.Action, "success": event.Success, "details": cloneMap(event.Details),
		"role": role, "actorRole": role,
		"occurredAt": occurredAt,
		"actor_id":   actorID, "actorId": actorID,
	}
}

// searchAudit answers an audit query over the whole immutable trail.
//
// Matching records are counted in full and then windowed, so total reports how
// much history matched rather than how much fitted in one response. Results are
// newest-first by default and every record stays reachable through offset, which
// is what keeps recent history readable on an instance whose trail has grown far
// past one page.
func (s *Service) searchAudit(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator", "dispatcher"); err != nil {
		return nil, err
	}
	var from, to *time.Time
	if value := core.String(in["fromTime"]); value != "" {
		parsed, err := core.Time(value)
		if err != nil {
			return nil, err
		}
		from = &parsed
	}
	if value := core.String(in["toTime"]); value != "" {
		parsed, err := core.Time(value)
		if err != nil {
			return nil, err
		}
		to = &parsed
	}
	entityTrace := core.String(in["entityType"]) != "" && core.String(in["entityId"]) != ""
	page, err := parseAuditPage(in, entityTrace)
	if err != nil {
		return nil, err
	}
	out := core.Map{}
	viewErr := s.Store.View(ctx, func(state *mongostore.State) error {
		matches := auditEventMatcher(in, from, to)
		matched := make([]*mongostore.AuditEvent, 0, len(state.AuditEvents))
		for _, event := range state.AuditEvents {
			if matches(event) {
				matched = append(matched, event)
			}
		}
		total := len(matched)
		if page.order == auditOrderNewestFirst {
			for left, right := 0, total-1; left < right; left, right = left+1, right-1 {
				matched[left], matched[right] = matched[right], matched[left]
			}
		}
		start := minInt(page.offset, total)
		end := minInt(start+page.limit, total)
		entries := make([]core.Map, 0, end-start)
		for _, event := range matched[start:end] {
			entries = append(entries, auditSearchEntry(event))
		}
		out = core.Map{
			"entries": entries, "total": total, "returned": len(entries),
			"offset": page.offset, "limit": page.limit, "order": page.order,
			"hasMore": end < total,
		}
		if end < total {
			out["nextOffset"] = end
		}
		return nil
	})
	return out, viewErr
}

func requesterSampleSummaries(state *mongostore.State, requesterID string) []core.Map {
	samples := make([]*mongostore.Sample, 0)
	for _, sample := range state.Samples {
		if sample.RequesterID == requesterID {
			samples = append(samples, sample)
		}
	}
	sort.Slice(samples, func(i, j int) bool {
		if !samples[i].UpdatedAt.Equal(samples[j].UpdatedAt) {
			return samples[i].UpdatedAt.After(samples[j].UpdatedAt)
		}
		return samples[i].ID < samples[j].ID
	})
	if len(samples) > 500 {
		samples = samples[:500]
	}
	out := make([]core.Map, 0, len(samples))
	for _, sample := range samples {
		request := state.Requests[sample.RequestID]
		if request == nil {
			continue
		}
		item := core.Map{
			"sampleId": sample.ID, "requestId": sample.RequestID,
			"status": sample.Status, "serviceability": request.Serviceability,
			"requestedAt":      formatTime(request.RequestedAt),
			"deliveryDeadline": formatTime(request.DeliveryDeadline),
			"origin":           core.Map{"latitude": request.Latitude, "longitude": request.Longitude},
		}
		if sample.ActiveJourneyID != "" {
			item["activeJourneyId"] = sample.ActiveJourneyID
		}
		if sample.TargetLaboratoryID != "" {
			item["targetLaboratoryId"] = sample.TargetLaboratoryID
		}
		if sample.CurrentHolderID != "" {
			item["currentHolderId"] = sample.CurrentHolderID
		}
		if sample.LatestLatitude != nil && sample.LatestLongitude != nil {
			item["latestLocation"] = core.Map{
				"latitude":  *sample.LatestLatitude,
				"longitude": *sample.LatestLongitude,
			}
		}
		out = append(out, item)
	}
	return out
}

func (s *Service) Bootstrap(ctx context.Context, p core.Principal) (core.Map, error) {
	var out core.Map
	err := s.Store.View(ctx, func(state *mongostore.State) error {
		user := state.Users[p.ID]
		if user == nil {
			return notFound("user")
		}
		out = core.Map{
			"user": core.Map{
				"userId": p.ID, "username": user.Username,
				"fullName": user.FullName, "roles": append([]string{}, p.Roles...),
				// The client drives navigation and permissions off the role the
				// server says is active, never off a locally remembered choice.
				"activeRole":            p.ActiveRole(),
				"roleSelectionRequired": p.RoleSelectionRequired(),
			},
			"serverTime": core.Now().Format(time.RFC3339Nano),
		}
		if p.Has("requester") {
			out["requesterSamples"] = requesterSampleSummaries(state, p.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if p.Has("dispatcher", "administrator") {
		operations, operationsErr := s.readOperations(ctx, p, core.Map{})
		if operationsErr == nil {
			out["operations"] = operations
		}
	}
	return out, nil
}
