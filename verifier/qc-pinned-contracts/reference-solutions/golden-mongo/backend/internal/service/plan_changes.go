package service

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func (s *Service) editPlan(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "dispatcher", "administrator"); err != nil {
		return nil, err
	}
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	journeyID := core.String(in["journeyId"])
	return s.mutate(ctx, p, "edit_active_plan", key, in, func(state *mongostore.State) (txResult, error) {
		journey, err := activeExpectedJourney(state, journeyID, core.Int(in["expectedPlanVersion"]))
		if err != nil {
			return txResult{}, err
		}
		changes := core.Object(in["changes"])
		// A dispatcher swapping the worker on planned work is a manual override,
		// not an ordinary plan edit. It is audited under its own action name so the
		// "overrides" audit category is distinguishable from route and batch edits.
		action := "active_plan_edited"
		var overrideDetails core.Map
		switch core.String(in["changeType"]) {
		case "add_stop":
			markCompletedStopsAndCurrentOrigin(state, journey)
			if err := addPlanStop(state, journey, changes); err != nil {
				return txResult{}, err
			}
			if err := rebuildEditedJourneyRoute(state, journey); err != nil {
				return txResult{}, err
			}
		case "remove_stop":
			markCompletedStopsAndCurrentOrigin(state, journey)
			if err := removePlanStop(state, journey, changes); err != nil {
				return txResult{}, err
			}
			if err := rebuildEditedJourneyRoute(state, journey); err != nil {
				return txResult{}, err
			}
		case "reorder_stops":
			markCompletedStopsAndCurrentOrigin(state, journey)
			if err := reorderPlanStops(state, journey, changes); err != nil {
				return txResult{}, err
			}
			if err := rebuildEditedJourneyRoute(state, journey); err != nil {
				return txResult{}, err
			}
		case "replace_worker", "reassign_worker":
			previousWorkerID, err := replacePlanWorker(state, journey, changes)
			if err != nil {
				return txResult{}, err
			}
			action = "override"
			overrideDetails = core.Map{
				"changeType": core.String(in["changeType"]),
				"reason":     core.String(changes["reason"]),
			}
			if previousWorkerID != "" {
				overrideDetails["previousWorkerId"] = previousWorkerID
			}
			if assignmentID := planWorkerAssignmentID(changes); assignmentID != "" {
				overrideDetails["assignmentId"] = assignmentID
				if assignment := state.Assignments[assignmentID]; assignment != nil {
					overrideDetails["workerId"] = assignment.WorkerID
				}
			}
		case "merge_samples":
			raw := changes["sampleIds"]
			if raw == nil {
				raw = changes["sample_ids"]
			}
			sampleIDs := uniqueStrings(core.Strings(raw))
			if len(sampleIDs) < 2 || !stringSliceContains(sampleIDs, journey.PrimarySampleID) {
				return txResult{}, core.Fail(409, "invalid_batch", "batch must contain the primary sample and at least one other sample")
			}
			if len(sampleIDs) > journey.CapacityAvailable {
				return txResult{}, core.Fail(409, "capacity_exceeded", "batch exceeds reserved route capacity")
			}
			if err := mergeSamples(state, journey, sampleIDs); err != nil {
				return txResult{}, err
			}
			journey.RouteMode, journey.CapacityUsed = "batched", len(sampleIDs)
			if err := rebuildEditedJourneyRoute(state, journey); err != nil {
				return txResult{}, err
			}
		case "split_samples", "split_batch":
			sampleIDs := uniqueStrings(core.Strings(changes["sampleIds"]))
			if len(sampleIDs) == 0 {
				sampleIDs = uniqueStrings(core.Strings(changes["sample_ids"]))
			}
			split, err := splitJourneyState(state, journey, sampleIDs)
			if err != nil {
				return txResult{}, err
			}
			if err := validateJourneyPlan(state, journey); err != nil {
				return txResult{}, err
			}
			if err := validateJourneyPlan(state, split); err != nil {
				return txResult{}, err
			}
			journey.PlanVersion++
			journey.UpdatedAt = core.Now()
			out := journeyMap(state, journey)
			out["splitJourney"] = journeyMap(state, split)
			out["valid"] = true
			audit(state, p, "journey", journeyID, "active_plan_edited", core.Map{"changeType": in["changeType"], "planVersion": journey.PlanVersion})
			return txResult{body: out, eventID: emit(state, []string{"dispatcher"}, "journey.edited", "journey", journeyID, out)}, nil
		case "cancel_work":
			cancelJourneyState(state, journey, core.String(changes["reason"]))
		case "reroute", "replace_route":
			replacement, status, err := replaceJourneyRoute(state, journey, core.Now(), core.String(changes["reason"]))
			if err != nil {
				return txResult{}, err
			}
			// Every other branch of this operation answers with the whole journey
			// plan, and one plan shape is what the operation documents. A reroute is
			// no exception: the caller gets the edited plan itself — now "replaced",
			// or "cancelled" when nothing could be replanned — alongside the replan
			// outcome and the replacement plan when one was built. Returning only
			// {journeyId,status,valid,replacementJourney} left the caller without the
			// plan version it needs for its next optimistic edit.
			out := journeyMap(state, journey)
			out["valid"] = replacement != nil
			out["replanStatus"] = status
			if replacement != nil {
				out["replacementJourney"] = journeyMap(state, replacement)
			}
			audit(state, p, "journey", journeyID, "active_plan_edited", core.Map{"changeType": in["changeType"], "status": status})
			return txResult{body: out, eventID: emit(state, []string{"dispatcher"}, "journey.edited", "journey", journeyID, out)}, nil
		default:
			return txResult{}, core.Fail(400, "invalid_change_type", "unsupported plan change")
		}
		if journey.Status == "active" {
			if err := validateJourneyPlan(state, journey); err != nil {
				return txResult{}, err
			}
		}
		journey.PlanVersion++
		journey.UpdatedAt = core.Now()
		out := journeyMap(state, journey)
		out["valid"] = journey.Status == "active"
		details := core.Map{"changeType": in["changeType"], "planVersion": journey.PlanVersion}
		for key, value := range overrideDetails {
			details[key] = value
		}
		audit(state, p, "journey", journeyID, action, details)
		return txResult{body: out, eventID: emit(state, []string{"dispatcher"}, "journey.edited", "journey", journeyID, out)}, nil
	})
}

// planWorkerAssignmentID reads the assignment a worker override targets, in
// either accepted spelling.
func planWorkerAssignmentID(changes core.Map) string {
	if value := core.String(changes["assignmentId"]); value != "" {
		return value
	}
	return core.String(changes["assignment_id"])
}

// replacePlanWorker performs a dispatcher's manual worker override and returns
// the worker who previously held the offer, so the override can be audited with
// the change it actually made.
func replacePlanWorker(
	state *mongostore.State,
	journey *mongostore.Journey,
	changes core.Map,
) (string, error) {
	assignmentID := planWorkerAssignmentID(changes)
	workerID := core.String(changes["workerId"])
	if workerID == "" {
		workerID = core.String(changes["worker_id"])
	}
	assignment := state.Assignments[assignmentID]
	if assignment == nil || assignment.JourneyID != journey.ID {
		return "", notFound("assignment")
	}
	if assignment.Status != "pending" {
		return "", core.Fail(409, "work_in_progress", "only pending work may be reassigned without a custody transfer")
	}
	if workerID == "" || workerID == assignment.WorkerID {
		return "", core.Fail(400, "invalid_worker", "a different replacement worker is required")
	}
	worker := state.Users[workerID]
	if worker == nil || !worker.Active || !worker.Available ||
		activeWorkerAssignments(state, workerID) >= worker.CapacityUnits {
		return "", core.Fail(409, "worker_unavailable", "replacement worker is inactive, unavailable, or at capacity")
	}
	previousWorkerID := assignment.WorkerID
	assignment.WorkerID = workerID
	if err := validatePlanWorker(state, journey, assignment); err != nil {
		assignment.WorkerID = previousWorkerID
		return "", err
	}
	now := core.Now()
	assignment.AcceptedAt = nil
	assignment.OfferExpiresAt = pointerTime(now.Add(assignmentOfferTTL()))
	worker.AssignmentCount++
	worker.LastOfferedAt, worker.UpdatedAt = &now, now
	return previousWorkerID, nil
}

func activeExpectedJourney(state *mongostore.State, journeyID string, expected int) (*mongostore.Journey, error) {
	if expected <= 0 {
		return nil, core.Fail(400, "invalid_plan_version", "expectedPlanVersion must be a positive version")
	}
	journey := state.Journeys[journeyID]
	if journey == nil {
		return nil, notFound("journey")
	}
	if journey.Status != "active" {
		return nil, core.Fail(409, "inactive_plan", "only an active plan can be edited")
	}
	if int(journey.PlanVersion) != expected {
		return nil, core.Fail(409, "stale_plan", "plan version changed")
	}
	return journey, nil
}

func addPlanStop(state *mongostore.State, journey *mongostore.Journey, changes core.Map) error {
	locationID := core.String(changes["locationId"])
	if locationID == "" {
		locationID = core.String(changes["location_id"])
	}
	// An inserted stop is always intermediate, so the location must be able to
	// act as a transfer point or hub right now.
	if !servesTransferStop(state, locationID) {
		return core.Fail(409, "invalid_stop", "new stop must be an active transfer point or hub")
	}
	dwell := core.Int(changes["dwellMinutes"])
	if _, ok := changes["dwellMinutes"]; !ok {
		dwell = core.Int(changes["dwell_minutes"])
	}
	if dwell < 0 {
		return core.Fail(400, "invalid_dwell", "dwell time cannot be negative")
	}
	newETA := journey.EstimatedDeliveryAt.Add(time.Duration(dwell) * time.Minute)
	for _, sampleID := range journey.SampleIDs {
		sample := state.Samples[sampleID]
		if sample == nil {
			return notFound("sample")
		}
		request := state.Requests[sample.RequestID]
		if request == nil || newETA.After(request.DeliveryDeadline) {
			return core.Fail(409, "deadline_exceeded", "stop would make a sample late")
		}
	}
	stops := stopsForJourney(state, journey.ID)
	if len(stops) < 2 {
		return core.Fail(409, "invalid_plan", "journey must retain pickup and delivery stops")
	}
	position := core.Int(changes["position"])
	if position < 1 {
		position = 1
	}
	if position >= len(stops) {
		position = len(stops) - 1
	}
	for _, stop := range stops {
		if stop.CompletedAt != nil && stop.Sequence > position {
			return core.Fail(409, "completed_work_immutable", "a stop cannot be inserted before completed work")
		}
	}
	insertedETA := stops[position].EstimatedAt
	for _, stop := range stops {
		if stop.Sequence > position {
			stop.Sequence++
			stop.EstimatedAt = stop.EstimatedAt.Add(time.Duration(dwell) * time.Minute)
		}
	}
	stopID := core.UUID()
	state.Stops[stopID] = &mongostore.JourneyStop{
		ID: stopID, JourneyID: journey.ID, LocationID: locationID,
		StopType: "handoff", Sequence: position + 1,
		EstimatedAt: insertedETA, DwellMinutes: dwell,
	}
	journey.EstimatedDeliveryAt = newETA
	for _, reservation := range state.Reservations {
		if reservation.JourneyID == journey.ID && reservation.ResourceID == locationID && reservation.Status == "active" {
			return nil
		}
	}
	return reserveJourneyResource(state, journey.ID, locationID, journeyRequiresCooling(state, journey), journey.CapacityUsed)
}

func removePlanStop(state *mongostore.State, journey *mongostore.Journey, changes core.Map) error {
	stopID := core.String(changes["stopId"])
	if stopID == "" {
		stopID = core.String(changes["stop_id"])
	}
	locationID := core.String(changes["locationId"])
	if locationID == "" {
		locationID = core.String(changes["location_id"])
	}
	sequence := core.Int(changes["sequence"])
	stops := stopsForJourney(state, journey.ID)
	matches := []*mongostore.JourneyStop{}
	for _, stop := range stops {
		if stopID != "" && stop.ID != stopID {
			continue
		}
		if locationID != "" && stop.LocationID != locationID {
			continue
		}
		if sequence > 0 && stop.Sequence != sequence {
			continue
		}
		matches = append(matches, stop)
	}
	if len(matches) == 0 {
		return notFound("stop")
	}
	if len(matches) > 1 {
		return core.Fail(409, "ambiguous_stop", "repeated locations require a stop sequence")
	}
	stop := matches[0]
	if stop.Sequence == 1 || stop.Sequence == len(stops) ||
		stop.StopType == "pickup" || stop.StopType == "delivery" {
		return core.Fail(409, "invalid_stop_order", "pickup and delivery stops cannot be removed")
	}
	if stop.CompletedAt != nil {
		return core.Fail(409, "completed_work_immutable", "completed stops cannot be removed")
	}
	delete(state.Stops, stop.ID)
	for _, candidate := range stops {
		if candidate.Sequence > stop.Sequence {
			candidate.Sequence--
		}
	}
	return nil
}

func reorderPlanStops(state *mongostore.State, journey *mongostore.Journey, changes core.Map) error {
	order := core.Strings(changes["locationIds"])
	if len(order) == 0 {
		order = core.Strings(changes["location_ids"])
	}
	if len(order) == 0 {
		order = core.Strings(changes["orderedStopIds"])
	}
	if len(order) == 0 {
		order = core.Strings(changes["ordered_stop_ids"])
	}
	if len(order) == 0 {
		order = core.Strings(changes["order"])
	}
	stops := stopsForJourney(state, journey.ID)
	if len(order) != len(stops) {
		return core.Fail(409, "invalid_stop_order", "reorder must include every current stop exactly once")
	}
	byLocation := map[string]*mongostore.JourneyStop{}
	for _, stop := range stops {
		if _, duplicate := byLocation[stop.LocationID]; duplicate {
			return core.Fail(409, "ambiguous_stop_order", "plans with repeated locations require stop IDs")
		}
		byLocation[stop.LocationID] = stop
	}
	seen := map[string]bool{}
	for index, locationID := range order {
		stop := byLocation[locationID]
		if stop == nil || seen[locationID] {
			return core.Fail(409, "invalid_stop_order", "reorder includes an unknown or duplicate location")
		}
		seen[locationID] = true
		if index == 0 && stop.StopType != "pickup" || index == len(order)-1 && stop.StopType != "delivery" {
			return core.Fail(409, "invalid_stop_order", "pickup must remain first and delivery last")
		}
		if stop.CompletedAt != nil && stop.Sequence != index+1 {
			return core.Fail(409, "completed_work_immutable", "completed stops cannot be reordered")
		}
		stop.Sequence = index + 1
	}
	return nil
}

func rebuildEditedJourneyRoute(state *mongostore.State, journey *mongostore.Journey) error {
	for _, handoff := range state.Handoffs {
		if handoff.Status == "pending" && stringSliceContains(journey.SampleIDs, handoff.SampleID) {
			return core.Fail(409, "work_in_progress", "route edits cannot invalidate a pending custody handoff")
		}
	}
	for id, assignment := range state.Assignments {
		if assignment.JourneyID != journey.ID || assignment.Kind == "collection" {
			continue
		}
		switch assignment.Status {
		case "completed", "delivered", "cancelled":
			continue
		case "pending":
			delete(state.Assignments, id)
		default:
			return core.Fail(409, "work_in_progress", "accepted or in-progress route work must be rerouted instead of edited")
		}
	}

	stops := stopsForJourney(state, journey.ID)
	locations := collapsedStopLocations(stops)
	if len(locations) < 2 {
		return core.Fail(409, "invalid_route", "edited plan must retain at least one movement")
	}
	for _, locationID := range locations {
		if !routeLocationAvailable(state, locationID, locations[0]) {
			return core.Fail(409, "invalid_route", "edited route contains an inactive location")
		}
	}
	if !laboratoryAcceptsJourney(state, locations[len(locations)-1], journey) {
		return core.Fail(409, "invalid_destination", "edited route must end at a laboratory that accepts every sample")
	}

	releaseReservations(state, journey.ID)
	cooled := journeyRequiresCooling(state, journey)
	edges, err := editedRouteEdges(state, locations, cooled, journey.CapacityUsed)
	if err != nil {
		return err
	}
	resources := append([]string{}, locations...)
	for _, routeEdge := range edges {
		resources = append(resources, routeEdge.id)
	}
	capacityAvailable := editedRouteCapacity(state, uniqueStrings(resources), cooled)
	if capacityAvailable < journey.CapacityUsed {
		return core.Fail(409, "capacity_exhausted", "edited route has insufficient regular or cooled capacity")
	}
	if err := reserveRouteResources(state, journey.ID, resources, cooled, journey.CapacityUsed); err != nil {
		return err
	}

	now := core.Now()
	for _, sampleID := range journey.SampleIDs {
		sample := state.Samples[sampleID]
		if sample == nil {
			return core.Fail(409, "invalid_plan", "journey sample is missing")
		}
		sampleType := state.SampleTypes[sample.SampleTypeID]
		if sampleType == nil {
			return core.Fail(409, "invalid_plan", "sample type is missing")
		}
		for index, routeEdge := range edges {
			legSequence := index + 1
			completed := completedTransportAt(state, journey.ID, sampleID, legSequence)
			if completed != nil {
				if completed.FromLocationID != routeEdge.from || completed.ToLocationID != routeEdge.to {
					return core.Fail(409, "completed_work_immutable", "edited route does not preserve a completed movement")
				}
				continue
			}
			worker := fairWorker(state, "delivery_partner", "", false, nil, "")
			if stringsEqualFold(sampleType.Name, "Pulse") && worker == "" {
				return core.Fail(409, "pulse_no_wait", "Pulse transport work cannot wait for a worker")
			}
			id := core.UUID()
			state.Assignments[id] = newAssignment(
				id, journey.ID, sampleID, "transport", legSequence,
				routeEdge.from, routeEdge.to, worker, now,
			)
		}
		if err := rebuildFacilityWork(state, journey, sampleID, locations, sampleType, now); err != nil {
			return err
		}
	}

	recalculateEditedRouteTimes(state, journey, stops, edges)
	journey.TotalCostUnits = 0
	for _, routeEdge := range edges {
		journey.TotalCostUnits += routeEdge.cost
	}
	journey.CapacityAvailable = capacityAvailable
	if len(journey.SampleIDs) > 1 {
		journey.RouteMode = "batched"
	} else if len(edges) == 1 {
		journey.RouteMode = "direct"
	} else {
		journey.RouteMode = "multi_stop"
	}
	return nil
}

func collapsedStopLocations(stops []*mongostore.JourneyStop) []string {
	locations := []string{}
	for _, stop := range stops {
		if len(locations) == 0 || locations[len(locations)-1] != stop.LocationID {
			locations = append(locations, stop.LocationID)
		}
	}
	return locations
}

func editedRouteEdges(
	state *mongostore.State,
	locations []string,
	cooled bool,
	units int,
) ([]edge, error) {
	out := make([]edge, 0, len(locations)-1)
	for index := 0; index < len(locations)-1; index++ {
		from, to := locations[index], locations[index+1]
		candidates := []edge{}
		for _, connection := range state.Connections {
			if !connection.Active || connection.FromLocationID != from ||
				connection.ToLocationID != to || cooled && !connection.Cooled {
				continue
			}
			if capacity := state.Capacities[connection.ID]; capacity != nil {
				if capacity.CapacityUnits-capacity.ReservedUnits < units ||
					cooled && capacity.CooledUnits-capacity.CooledReserved < units {
					continue
				}
			}
			candidates = append(candidates, edge{
				id: connection.ID, from: from, to: to,
				minutes: connection.TravelMinutes + locationHandlingMinutes(state, to),
				cost:    connection.CostUnits, cooled: connection.Cooled,
			})
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].cost != candidates[j].cost {
				return candidates[i].cost < candidates[j].cost
			}
			if candidates[i].minutes != candidates[j].minutes {
				return candidates[i].minutes < candidates[j].minutes
			}
			return candidates[i].id < candidates[j].id
		})
		if len(candidates) == 0 {
			return nil, core.Fail(409, "invalid_route", "edited stops are not connected by an eligible route")
		}
		out = append(out, candidates[0])
	}
	return out, nil
}

func editedRouteCapacity(state *mongostore.State, resourceIDs []string, cooled bool) int {
	available := math.MaxInt
	for _, resourceID := range resourceIDs {
		capacity := state.Capacities[resourceID]
		if capacity == nil {
			continue
		}
		resourceAvailable := capacity.CapacityUnits - capacity.ReservedUnits
		if cooled {
			resourceAvailable = minInt(resourceAvailable, capacity.CooledUnits-capacity.CooledReserved)
		}
		available = minInt(available, resourceAvailable)
	}
	if available == math.MaxInt {
		return math.MaxInt32
	}
	return available
}

func completedTransportAt(state *mongostore.State, journeyID, sampleID string, sequence int) *mongostore.Assignment {
	for _, assignment := range state.Assignments {
		if assignment.JourneyID == journeyID && assignment.Kind == "transport" &&
			assignment.SampleID == sampleID &&
			assignment.LegSequence == sequence &&
			(assignment.Status == "completed" || assignment.Status == "delivered") {
			return assignment
		}
	}
	return nil
}

func rebuildFacilityWork(
	state *mongostore.State,
	journey *mongostore.Journey,
	sampleID string,
	locations []string,
	sampleType *mongostore.SampleType,
	now time.Time,
) error {
	for index := 1; index < len(locations)-1; index++ {
		locationID := locations[index]
		if !servesTransferStop(state, locationID) ||
			completedFacilityAt(state, journey.ID, sampleID, "node", index, locationID) {
			continue
		}
		worker := fairWorker(state, "node_handler", "", false, nil, locationID)
		if stringsEqualFold(sampleType.Name, "Pulse") && worker == "" {
			return core.Fail(409, "pulse_no_wait", "Pulse node work cannot wait for a handler")
		}
		id := core.UUID()
		state.Assignments[id] = newAssignment(
			id, journey.ID, sampleID, "node", index,
			locations[index-1], locationID, worker, now,
		)
	}
	laboratorySequence := len(locations)
	laboratoryID := locations[len(locations)-1]
	if !completedFacilityAt(state, journey.ID, sampleID, "laboratory", laboratorySequence, laboratoryID) {
		worker := fairWorker(state, "laboratory_team", "", false, nil, laboratoryID)
		if stringsEqualFold(sampleType.Name, "Pulse") && worker == "" {
			return core.Fail(409, "pulse_no_wait", "Pulse laboratory work cannot wait for an operator")
		}
		id := core.UUID()
		state.Assignments[id] = newAssignment(
			id, journey.ID, sampleID, "laboratory", laboratorySequence,
			locations[len(locations)-2], laboratoryID, worker, now,
		)
	}
	return nil
}

func completedFacilityAt(
	state *mongostore.State,
	journeyID, sampleID, kind string,
	sequence int,
	locationID string,
) bool {
	for _, assignment := range state.Assignments {
		if assignment.JourneyID == journeyID && assignment.Kind == kind &&
			assignment.SampleID == sampleID &&
			assignment.LegSequence == sequence && assignment.ToLocationID == locationID &&
			(assignment.Status == "completed" || assignment.Status == "delivered") {
			return true
		}
	}
	return false
}

func recalculateEditedRouteTimes(
	state *mongostore.State,
	journey *mongostore.Journey,
	stops []*mongostore.JourneyStop,
	edges []edge,
) {
	startedAt := journey.CreatedAt
	for _, sampleID := range journey.SampleIDs {
		sample := state.Samples[sampleID]
		if sample == nil {
			continue
		}
		if request := state.Requests[sample.RequestID]; request != nil && request.RequestedAt.After(startedAt) {
			startedAt = request.RequestedAt
		}
	}
	currentAt := startedAt
	edgeIndex := 0
	for index, stop := range stops {
		if index > 0 && stops[index-1].LocationID != stop.LocationID {
			currentAt = currentAt.Add(time.Duration(edges[edgeIndex].minutes) * time.Minute)
			edgeIndex++
		}
		if stop.CompletedAt == nil {
			stop.EstimatedAt = currentAt
		} else if stop.CompletedAt.After(currentAt) {
			currentAt = *stop.CompletedAt
		}
		currentAt = currentAt.Add(time.Duration(stop.DwellMinutes) * time.Minute)
	}
	journey.EstimatedDeliveryAt = currentAt
}

type batchSample struct {
	sample     *mongostore.Sample
	sampleType *mongostore.SampleType
	deadline   time.Time
}

func mergeSamples(state *mongostore.State, journey *mongostore.Journey, sampleIDs []string) error {
	if err := ensureRouteWorkCanBeRebuilt(state, journey); err != nil {
		return err
	}
	samples := make([]batchSample, 0, len(sampleIDs))
	for _, sampleID := range sampleIDs {
		sample := state.Samples[sampleID]
		if sample == nil {
			return notFound("sample")
		}
		sampleType := state.SampleTypes[sample.SampleTypeID]
		request := state.Requests[sample.RequestID]
		if sampleType == nil || request == nil {
			return core.Fail(409, "invalid_batch", "sample configuration is incomplete")
		}
		if !sampleType.BatchingAllowed || stringsEqualFold(sampleType.Name, "Pulse") {
			return core.Fail(409, "pulse_no_batch", "Pulse samples cannot join a batch")
		}
		if journey.EstimatedDeliveryAt.After(request.DeliveryDeadline) {
			return core.Fail(409, "deadline_exceeded", "batch misses a sample deadline")
		}
		if terminalSampleStatus(sample.Status) {
			return core.Fail(409, "invalid_batch", "completed, cancelled, or exception samples cannot be batched")
		}
		samples = append(samples, batchSample{sample: sample, sampleType: sampleType, deadline: request.DeliveryDeadline})
	}
	for i := range samples {
		for j := i + 1; j < len(samples); j++ {
			a, b := samples[i].sample, samples[j].sample
			if a.ContainerType != b.ContainerType ||
				a.TargetLaboratoryID != b.TargetLaboratoryID ||
				stringSliceContains(a.IncompatibleSampleTypes, b.SampleTypeID) ||
				stringSliceContains(b.IncompatibleSampleTypes, a.SampleTypeID) ||
				mathMax(a.TemperatureMinC, b.TemperatureMinC) > mathMin(a.TemperatureMaxC, b.TemperatureMaxC) ||
				samples[i].sampleType.RequiresCooledRoute != samples[j].sampleType.RequiresCooledRoute {
				return core.Fail(409, "incompatible_batch", "samples have incompatible handling, destination, or separation rules")
			}
		}
	}
	previousCount := len(journey.SampleIDs)
	sourceJourneyIDs := map[string]bool{}
	for _, item := range samples {
		sample := item.sample
		if sample.ActiveJourneyID != "" && sample.ActiveJourneyID != journey.ID {
			source := state.Journeys[sample.ActiveJourneyID]
			if source != nil && source.Status == "active" {
				if err := ensureRouteWorkCanBeRebuilt(state, source); err != nil {
					return err
				}
				for _, sourceSampleID := range source.SampleIDs {
					if !stringSliceContains(sampleIDs, sourceSampleID) {
						return core.Fail(409, "partial_journey_merge", "all samples on a source journey must be merged together")
					}
				}
				sourceJourneyIDs[source.ID] = true
			}
		}
	}
	for sourceJourneyID := range sourceJourneyIDs {
		source := state.Journeys[sourceJourneyID]
		for _, assignment := range state.Assignments {
			if assignment.JourneyID == source.ID && assignment.Kind == "collection" {
				assignment.JourneyID = journey.ID
			}
		}
		source.Status, source.UpdatedAt = "replaced", core.Now()
		source.PlanVersion++
		cancelUnfinishedAssignments(state, source.ID)
		releaseReservations(state, source.ID)
	}
	journey.SampleIDs = uniqueStrings(sampleIDs)
	added := len(journey.SampleIDs) - previousCount
	if added > 0 {
		if err := increaseJourneyReservations(state, journey, added); err != nil {
			return err
		}
	}
	for _, item := range samples {
		item.sample.ActiveJourneyID, item.sample.UpdatedAt = journey.ID, core.Now()
	}
	return nil
}

func ensureRouteWorkCanBeRebuilt(state *mongostore.State, journey *mongostore.Journey) error {
	for _, handoff := range state.Handoffs {
		if handoff.Status == "pending" && stringSliceContains(journey.SampleIDs, handoff.SampleID) {
			return core.Fail(409, "work_in_progress", "a journey with a pending custody handoff cannot be merged")
		}
	}
	for _, assignment := range state.Assignments {
		if assignment.JourneyID != journey.ID || assignment.Kind == "collection" {
			continue
		}
		switch assignment.Status {
		case "pending", "cancelled":
			continue
		default:
			return core.Fail(409, "work_in_progress", "accepted or in-progress route work cannot be merged")
		}
	}
	return nil
}

func increaseJourneyReservations(state *mongostore.State, journey *mongostore.Journey, units int) error {
	cooled := journeyRequiresCooling(state, journey)
	resourceIDs := []string{}
	for _, reservation := range state.Reservations {
		if reservation.JourneyID == journey.ID && reservation.Status == "active" {
			resourceIDs = append(resourceIDs, reservation.ResourceID)
		}
	}
	for _, resourceID := range uniqueStrings(resourceIDs) {
		if err := reserveJourneyResource(state, journey.ID, resourceID, cooled, units); err != nil {
			return err
		}
	}
	return nil
}

func journeyRequiresCooling(state *mongostore.State, journey *mongostore.Journey) bool {
	for _, sampleID := range journey.SampleIDs {
		sample := state.Samples[sampleID]
		if sample != nil {
			sampleType := state.SampleTypes[sample.SampleTypeID]
			if sampleType != nil && sampleType.RequiresCooledRoute {
				return true
			}
		}
	}
	return false
}

func releaseReservations(state *mongostore.State, journeyID string) {
	for _, reservation := range state.Reservations {
		if reservation.JourneyID != journeyID || reservation.Status != "active" {
			continue
		}
		if capacity := state.Capacities[reservation.ResourceID]; capacity != nil {
			capacity.ReservedUnits = maxInt(0, capacity.ReservedUnits-reservation.Units)
			if reservation.Cooled {
				capacity.CooledReserved = maxInt(0, capacity.CooledReserved-reservation.Units)
			}
			capacity.UpdatedAt = core.Now()
		}
		reservation.Status = "released"
	}
}

func (s *Service) splitJourney(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "dispatcher", "administrator"); err != nil {
		return nil, err
	}
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	journeyID := core.String(in["journeyId"])
	return s.mutate(ctx, p, "split_journey", key, in, func(state *mongostore.State) (txResult, error) {
		journey, err := activeExpectedJourney(state, journeyID, core.Int(in["expectedPlanVersion"]))
		if err != nil {
			return txResult{}, err
		}
		split, err := splitJourneyState(state, journey, uniqueStrings(core.Strings(in["sampleIds"])))
		if err != nil {
			return txResult{}, err
		}
		if err := validateJourneyPlan(state, journey); err != nil {
			return txResult{}, err
		}
		if err := validateJourneyPlan(state, split); err != nil {
			return txResult{}, err
		}
		journey.PlanVersion++
		journey.UpdatedAt = core.Now()
		out := core.Map{"journey": journeyMap(state, journey), "splitJourney": journeyMap(state, split)}
		audit(state, p, "journey", journeyID, "journey_split", out)
		return txResult{body: out, eventID: emit(state, []string{"dispatcher"}, "journey.split", "journey", journeyID, out)}, nil
	})
}

func splitJourneyState(state *mongostore.State, journey *mongostore.Journey, selected []string) (*mongostore.Journey, error) {
	if len(selected) == 0 || len(selected) >= len(journey.SampleIDs) {
		return nil, core.Fail(409, "invalid_split", "split must move at least one but not all samples")
	}
	for _, assignment := range assignmentsForJourney(state, journey.ID) {
		if assignment.Status != "pending" && assignment.Status != "completed" &&
			assignment.Status != "delivered" && assignment.Status != "cancelled" {
			return nil, core.Fail(409, "split_in_progress", "accepted or in-progress work must finish or be rerouted before splitting")
		}
	}
	for _, sampleID := range selected {
		if !stringSliceContains(journey.SampleIDs, sampleID) {
			return nil, core.Fail(409, "invalid_split", "split sample does not belong to the journey")
		}
	}
	remaining := []string{}
	for _, sampleID := range journey.SampleIDs {
		if !stringSliceContains(selected, sampleID) {
			remaining = append(remaining, sampleID)
		}
	}
	now, splitID := core.Now(), core.UUID()
	split := &mongostore.Journey{
		ID: splitID, PrimarySampleID: selected[0], SampleIDs: append([]string{}, selected...),
		ParentJourneyID: journey.ID, IsReplacement: true, PlanVersion: 1,
		RouteMode: routeModeForCount(journey.RouteMode, len(selected)), Status: "active",
		EstimatedDeliveryAt: journey.EstimatedDeliveryAt, TotalCostUnits: journey.TotalCostUnits,
		CapacityUsed: len(selected), CapacityAvailable: journey.CapacityAvailable,
		CreatedAt: now, UpdatedAt: now,
	}
	state.Journeys[splitID] = split
	for _, stop := range stopsForJourney(state, journey.ID) {
		id := core.UUID()
		copy := *stop
		copy.ID, copy.JourneyID = id, splitID
		state.Stops[id] = &copy
	}
	for _, assignment := range state.Assignments {
		if assignment.JourneyID == journey.ID && stringSliceContains(selected, assignment.SampleID) {
			assignment.JourneyID = splitID
		}
	}
	for _, reservation := range state.Reservations {
		if reservation.JourneyID != journey.ID || reservation.Status != "active" {
			continue
		}
		if reservation.Units < len(journey.SampleIDs) {
			return nil, core.Fail(409, "invalid_reservation", "source journey is missing capacity needed for a safe split")
		}
		moveUnits := len(selected)
		reservation.Units -= moveUnits
		id := core.UUID()
		state.Reservations[id] = &mongostore.Reservation{
			ID: id, JourneyID: splitID, ResourceID: reservation.ResourceID,
			Units: moveUnits, Cooled: reservation.Cooled, Status: "active",
		}
	}
	for _, sampleID := range selected {
		state.Samples[sampleID].ActiveJourneyID = splitID
		state.Samples[sampleID].UpdatedAt = now
	}
	journey.SampleIDs, journey.PrimarySampleID = remaining, remaining[0]
	journey.CapacityUsed = len(remaining)
	journey.RouteMode = routeModeForJourneyAssignments(state, journey)
	split.RouteMode = routeModeForJourneyAssignments(state, split)
	return split, nil
}

func routeModeForJourneyAssignments(state *mongostore.State, journey *mongostore.Journey) string {
	if len(journey.SampleIDs) > 1 {
		return "batched"
	}
	if transportAssignmentCountForSample(state, journey.ID, journey.PrimarySampleID) == 1 {
		return "direct"
	}
	return "multi_stop"
}

func routeModeForCount(existing string, count int) string {
	if count > 1 {
		return "batched"
	}
	if existing == "batched" {
		return "multi_stop"
	}
	return existing
}

func (s *Service) mergeJourneys(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "dispatcher", "administrator"); err != nil {
		return nil, err
	}
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	targetID := core.String(in["targetJourneyId"])
	return s.mutate(ctx, p, "merge_journeys", key, in, func(state *mongostore.State) (txResult, error) {
		target, err := activeExpectedJourney(state, targetID, core.Int(in["expectedPlanVersion"]))
		if err != nil {
			return txResult{}, err
		}
		sampleIDs := append([]string{}, target.SampleIDs...)
		sourceCount := 0
		for _, sourceID := range uniqueStrings(core.Strings(in["sourceJourneyIds"])) {
			if sourceID == targetID {
				continue
			}
			source := state.Journeys[sourceID]
			if source == nil || source.Status != "active" {
				return txResult{}, core.Fail(409, "invalid_merge", "all source journeys must be active")
			}
			if err := validateJourneyPlan(state, source); err != nil {
				return txResult{}, err
			}
			sourceCount++
			sampleIDs = append(sampleIDs, source.SampleIDs...)
		}
		if sourceCount == 0 {
			return txResult{}, core.Fail(409, "invalid_merge", "at least one different source journey is required")
		}
		sampleIDs = uniqueStrings(sampleIDs)
		if len(sampleIDs) > target.CapacityAvailable {
			return txResult{}, core.Fail(409, "capacity_exceeded", "merged journey exceeds route capacity")
		}
		if err := mergeSamples(state, target, sampleIDs); err != nil {
			return txResult{}, err
		}
		target.RouteMode, target.CapacityUsed = "batched", len(sampleIDs)
		if err := rebuildEditedJourneyRoute(state, target); err != nil {
			return txResult{}, err
		}
		if err := validateJourneyPlan(state, target); err != nil {
			return txResult{}, err
		}
		target.PlanVersion++
		target.UpdatedAt = core.Now()
		out := journeyMap(state, target)
		audit(state, p, "journey", targetID, "journeys_merged", out)
		return txResult{body: out, eventID: emit(state, []string{"dispatcher"}, "journey.merged", "journey", targetID, out)}, nil
	})
}

func (s *Service) cancelJourney(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "dispatcher", "administrator"); err != nil {
		return nil, err
	}
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	journeyID := core.String(in["journeyId"])
	return s.mutate(ctx, p, "cancel_journey", key, in, func(state *mongostore.State) (txResult, error) {
		journey, err := activeExpectedJourney(state, journeyID, core.Int(in["expectedPlanVersion"]))
		if err != nil {
			return txResult{}, err
		}
		cancelJourneyState(state, journey, core.String(in["reason"]))
		journey.PlanVersion++
		out := core.Map{"journeyId": journeyID, "status": "cancelled", "sampleIds": append([]string{}, journey.SampleIDs...)}
		audit(state, p, "journey", journeyID, "journey_cancelled", out)
		return txResult{body: out, eventID: emit(state, []string{"dispatcher"}, "journey.cancelled", "journey", journeyID, out)}, nil
	})
}

func cancelJourneyState(state *mongostore.State, journey *mongostore.Journey, reason string) {
	journey.Status, journey.UpdatedAt = "cancelled", core.Now()
	cancelUnfinishedAssignments(state, journey.ID)
	releaseReservations(state, journey.ID)
	for _, sampleID := range journey.SampleIDs {
		sample := state.Samples[sampleID]
		if sample == nil || terminalSampleStatus(sample.Status) {
			continue
		}
		sample.Status, sample.ActiveJourneyID = "cancelled", ""
		sample.Exception = core.Map{"reason": reason, "cancelledAt": formatTime(core.Now())}
		sample.Version++
		sample.UpdatedAt = core.Now()
	}
}

func (s *Service) rerouteJourney(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "dispatcher", "administrator"); err != nil {
		return nil, err
	}
	key := core.String(in["idempotencyKey"])
	if key == "" {
		return nil, core.Fail(400, "idempotency_required", "idempotencyKey is required")
	}
	journeyID := core.String(in["journeyId"])
	return s.mutate(ctx, p, "reroute_journey", key, in, func(state *mongostore.State) (txResult, error) {
		journey, err := activeExpectedJourney(state, journeyID, core.Int(in["expectedPlanVersion"]))
		if err != nil {
			return txResult{}, err
		}
		replacement, status, err := replaceJourneyRoute(state, journey, core.Now(), core.String(in["reason"]))
		if err != nil {
			return txResult{}, err
		}
		out := core.Map{"journeyId": journeyID, "replanStatus": status}
		if replacement != nil {
			out["replacementPlanId"] = replacement.ID
			out["replacementJourney"] = journeyMap(state, replacement)
		}
		audit(state, p, "journey", journeyID, "journey_rerouted", out)
		return txResult{body: out, eventID: emit(state, []string{"dispatcher"}, "journey.rerouted", "journey", journeyID, out)}, nil
	})
}

func replaceJourneyRoute(state *mongostore.State, journey *mongostore.Journey, occurredAt time.Time, reason string) (*mongostore.Journey, string, error) {
	primary := state.Samples[journey.PrimarySampleID]
	if primary == nil {
		return nil, "", core.Fail(409, "invalid_plan", "primary sample is missing")
	}
	deadline, err := earliestJourneyDeadline(state, journey)
	if err != nil {
		return nil, "", err
	}
	origin := markCompletedStopsAndCurrentOrigin(state, journey)
	if origin == "" {
		return nil, "", core.Fail(409, "invalid_plan", "current route origin cannot be determined")
	}
	// A replacement plan can leave no earlier than the disruption, and no earlier
	// than the collection window of any sample it has still to collect.
	departAt := replanDepartureTime(state, journey, occurredAt)
	// Free only the old unfinished route's reservations before recomputing.
	releaseReservations(state, journey.ID)
	choice, routeErr := findRoute(state, origin, primary.SampleTypeID, departAt, deadline, journey.CapacityUsed)
	if routeErr != nil || !laboratoryAcceptsJourney(state, choice.labID, journey) {
		journey.Status, journey.UpdatedAt = "cancelled", core.Now()
		journey.PlanVersion++
		cancelUnfinishedAssignments(state, journey.ID)
		status := "expired"
		for _, sampleID := range journey.SampleIDs {
			sample := state.Samples[sampleID]
			if sample == nil {
				continue
			}
			sampleStatus := "exception"
			if request := state.Requests[sample.RequestID]; request != nil && !request.DeliveryDeadline.After(occurredAt) {
				sampleStatus = "expired"
			} else {
				status = "exception"
			}
			sample.Status, sample.ActiveJourneyID = sampleStatus, ""
			sample.Exception = core.Map{"reason": reason, "occurredAt": formatTime(occurredAt)}
			sample.Version++
			sample.UpdatedAt = core.Now()
		}
		return nil, status, nil
	}
	journey.Status, journey.UpdatedAt = "replaced", core.Now()
	journey.PlanVersion++
	cancelUnfinishedAssignments(state, journey.ID)
	now, replacementID := core.Now(), core.UUID()
	replacement := &mongostore.Journey{
		ID: replacementID, PrimarySampleID: journey.PrimarySampleID,
		SampleIDs:       append([]string{}, journey.SampleIDs...),
		ParentJourneyID: journey.ID, IsReplacement: true, PlanVersion: 1,
		RouteMode: "multi_stop", Status: "active",
		EstimatedDeliveryAt: departAt.Add(time.Duration(choice.minutes) * time.Minute),
		TotalCostUnits:      choice.cost, CapacityUsed: journey.CapacityUsed,
		CapacityAvailable: journey.CapacityAvailable, CreatedAt: now, UpdatedAt: now,
	}
	if len(choice.edges) == 1 {
		replacement.RouteMode = "direct"
	}
	if len(replacement.SampleIDs) > 1 {
		replacement.RouteMode = "batched"
	}
	state.Journeys[replacementID] = replacement

	sequence := 0
	locationIDs := []string{origin}
	for _, routeEdge := range choice.edges {
		locationIDs = append(locationIDs, routeEdge.to)
	}
	for index, locationID := range locationIDs {
		sequence++
		stopType := "handoff"
		if index == 0 {
			stopType = "pickup"
		} else if index == len(locationIDs)-1 {
			stopType = "delivery"
		}
		id := core.UUID()
		state.Stops[id] = &mongostore.JourneyStop{
			ID: id, JourneyID: replacementID, LocationID: locationID,
			StopType: stopType, Sequence: sequence,
			EstimatedAt: departAt.Add(time.Duration(choice.minutes*index/maxInt(1, len(locationIDs)-1)) * time.Minute),
		}
	}
	sampleType := state.SampleTypes[primary.SampleTypeID]
	if sampleType == nil {
		return nil, "", core.Fail(409, "invalid_plan", "primary sample type is missing")
	}
	cooled := sampleType.RequiresCooledRoute
	for _, sampleID := range replacement.SampleIDs {
		sample := state.Samples[sampleID]
		if sample == nil {
			return nil, "", core.Fail(409, "invalid_plan", "replacement sample is missing")
		}
		currentType := state.SampleTypes[sample.SampleTypeID]
		if currentType == nil {
			return nil, "", core.Fail(409, "invalid_plan", "replacement sample type is missing")
		}
		if sample.SealedAt == nil {
			collector := fairWorker(
				state, "collection_professional",
				currentType.RequiredQualification, currentType.QualifiedCollectorRequired, nil, "",
			)
			if stringsEqualFold(currentType.Name, "Pulse") && collector == "" {
				return nil, "", core.Fail(409, "pulse_no_wait", "Pulse collection work cannot wait for a worker")
			}
			id := core.UUID()
			state.Assignments[id] = newAssignment(id, replacementID, sampleID, "collection", 0, "", "", collector, now)
		}
		for index, routeEdge := range choice.edges {
			worker := fairWorker(state, "delivery_partner", "", false, nil, "")
			if stringsEqualFold(currentType.Name, "Pulse") && worker == "" {
				return nil, "", core.Fail(409, "pulse_no_wait", "Pulse transport work cannot wait for a worker")
			}
			id := core.UUID()
			state.Assignments[id] = newAssignment(id, replacementID, sampleID, "transport", index+1, routeEdge.from, routeEdge.to, worker, now)
		}
		if _, _, err := createFacilityAssignments(state, replacementID, sampleID, locationIDs, currentType, now); err != nil {
			return nil, "", err
		}
	}
	resources := append([]string{}, locationIDs...)
	for _, routeEdge := range choice.edges {
		resources = append(resources, routeEdge.id)
	}
	if err := reserveRouteResources(state, replacementID, resources, cooled, replacement.CapacityUsed); err != nil {
		return nil, "", err
	}
	for _, sampleID := range replacement.SampleIDs {
		sample := state.Samples[sampleID]
		sample.ActiveJourneyID, sample.TargetLaboratoryID = replacementID, choice.labID
		sample.Version++
		sample.UpdatedAt = core.Now()
	}
	if err := validateJourneyPlan(state, replacement); err != nil {
		return nil, "", err
	}
	return replacement, "replaced", nil
}

// replanDepartureTime is the earliest moment a replacement plan may depart. A
// sample that is not yet sealed cannot be collected before its collection window
// opens, so a replan that departed at the disruption time would schedule
// collection before the window and report an arrival estimate the plan can never
// meet. Sealed samples are already in custody and impose no such bound.
func replanDepartureTime(
	state *mongostore.State,
	journey *mongostore.Journey,
	occurredAt time.Time,
) time.Time {
	departAt := occurredAt
	for _, sampleID := range journey.SampleIDs {
		sample := state.Samples[sampleID]
		if sample == nil || sample.SealedAt != nil {
			continue
		}
		request := state.Requests[sample.RequestID]
		if request == nil {
			continue
		}
		if earliest, _ := requestCollectionWindow(request); earliest.After(departAt) {
			departAt = earliest
		}
	}
	return departAt
}

func markCompletedStopsAndCurrentOrigin(state *mongostore.State, journey *mongostore.Journey) string {
	stops := stopsForJourney(state, journey.ID)
	if len(stops) == 0 {
		return ""
	}
	origin := stops[0].LocationID
	lastCompletedStop := 0
	primary := state.Samples[journey.PrimarySampleID]
	if primary != nil && primary.SealedAt != nil {
		if stops[0].CompletedAt == nil {
			stops[0].CompletedAt = pointerTime(*primary.SealedAt)
		}
	}
	for _, assignment := range assignmentsForJourney(state, journey.ID) {
		if assignment.Kind != "transport" ||
			assignment.SampleID != journey.PrimarySampleID ||
			(assignment.Status != "completed" && assignment.Status != "delivered") ||
			assignment.ToLocationID == "" {
			continue
		}
		completedAt := assignment.CompletedAt
		if completedAt == nil {
			completedAt = pointerTime(journey.UpdatedAt)
		}
		for index := lastCompletedStop + 1; index < len(stops); index++ {
			if stops[index].LocationID != assignment.ToLocationID {
				continue
			}
			if stops[index].CompletedAt == nil {
				stops[index].CompletedAt = pointerTime(*completedAt)
			}
			lastCompletedStop = index
			origin = assignment.ToLocationID
			break
		}
	}

	if primary == nil || primary.CurrentHolderID == "" {
		return origin
	}
	highestLeg := -1
	for _, assignment := range assignmentsForJourney(state, journey.ID) {
		if assignment.Kind != "transport" || assignment.WorkerID != primary.CurrentHolderID ||
			assignment.SampleID != journey.PrimarySampleID ||
			assignment.LegSequence < highestLeg {
			continue
		}
		switch assignment.Status {
		case "accepted", "at_pickup", "picked_up", "in_progress", "in_transit":
			if assignment.FromLocationID != "" {
				highestLeg, origin = assignment.LegSequence, assignment.FromLocationID
			}
		case "arrived", "delivered", "completed":
			if assignment.ToLocationID != "" {
				highestLeg, origin = assignment.LegSequence, assignment.ToLocationID
			}
		}
	}
	return origin
}

func earliestJourneyDeadline(state *mongostore.State, journey *mongostore.Journey) (time.Time, error) {
	var earliest time.Time
	for _, sampleID := range journey.SampleIDs {
		sample := state.Samples[sampleID]
		if sample == nil {
			return time.Time{}, core.Fail(409, "invalid_plan", "journey references a missing sample")
		}
		request := state.Requests[sample.RequestID]
		if request == nil {
			return time.Time{}, core.Fail(409, "invalid_plan", "sample request is missing")
		}
		if earliest.IsZero() || request.DeliveryDeadline.Before(earliest) {
			earliest = request.DeliveryDeadline
		}
	}
	if earliest.IsZero() {
		return time.Time{}, core.Fail(409, "invalid_plan", "journey has no delivery deadline")
	}
	return earliest, nil
}

func laboratoryAcceptsJourney(state *mongostore.State, laboratoryID string, journey *mongostore.Journey) bool {
	if laboratoryID == "" {
		return false
	}
	if !servesLaboratory(state, laboratoryID) {
		return false
	}
	for _, sampleID := range journey.SampleIDs {
		sample := state.Samples[sampleID]
		if sample == nil {
			return false
		}
		accepted := false
		for _, acceptance := range state.Acceptances {
			if acceptance.LaboratoryID == laboratoryID && acceptance.SampleTypeID == sample.SampleTypeID && acceptance.Accepts {
				accepted = true
				break
			}
		}
		if !accepted {
			return false
		}
	}
	return true
}

func reserveRouteResources(state *mongostore.State, journeyID string, resources []string, cooled bool, units int) error {
	if units <= 0 {
		return core.Fail(400, "invalid_capacity", "reservation units must be positive")
	}
	resourceIDs := uniqueStrings(resources)
	for _, resourceID := range resourceIDs {
		if capacity := state.Capacities[resourceID]; capacity != nil {
			if capacity.ReservedUnits+units > capacity.CapacityUnits {
				return core.Fail(409, "capacity_exhausted", "replacement route capacity is exhausted")
			}
			if cooled && capacity.CooledReserved+units > capacity.CooledUnits {
				return core.Fail(409, "cooled_capacity_exhausted", "replacement cooled capacity is exhausted")
			}
		}
	}
	for _, resourceID := range resourceIDs {
		if err := reserveJourneyResource(state, journeyID, resourceID, cooled, units); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reportDisruption(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "dispatcher", "administrator"); err != nil {
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
	resourceType, resourceID := core.String(in["resourceType"]), core.String(in["resourceId"])
	return s.mutate(ctx, p, "report_disruption", key, in, func(state *mongostore.State) (txResult, error) {
		if err := markResourceUnavailable(state, resourceType, resourceID); err != nil {
			return txResult{}, err
		}
		disruptionID := core.UUID()
		affectedSamples, replacements := []string{}, []string{}
		replanStatus := "not_required"
		journeys := activeJourneys(state)
		for _, journey := range journeys {
			if !journeyAffected(state, journey, resourceType, resourceID) {
				continue
			}
			affectedSamples = append(affectedSamples, journey.SampleIDs...)
			replacement, status, err := replaceJourneyRoute(state, journey, occurredAt, core.String(in["reason"]))
			if err != nil {
				return txResult{}, err
			}
			if replacement != nil {
				replacements = append(replacements, replacement.ID)
			}
			replanStatus = combinedReplanStatus(replanStatus, status)
		}
		affectedSamples = uniqueStrings(affectedSamples)
		replacements = uniqueStrings(replacements)
		state.Disruptions[disruptionID] = &mongostore.Disruption{
			ID: disruptionID, ResourceType: resourceType, ResourceID: resourceID,
			Reason: core.String(in["reason"]), OccurredAt: occurredAt,
			ReplanStatus: replanStatus, AffectedSampleIDs: affectedSamples,
			ReplacementPlanIDs: replacements, CreatedAt: core.Now(),
		}
		out := core.Map{
			"disruptionId": disruptionID, "affectedSampleIds": affectedSamples,
			"replacementPlanIds": replacements, "replanStatus": replanStatus,
		}
		audit(state, p, "disruption", disruptionID, "disruption_reported", out)
		return txResult{body: out, eventID: emit(state, []string{"dispatcher"}, "disruption.reported", "disruption", disruptionID, out)}, nil
	})
}

func combinedReplanStatus(current, next string) string {
	priority := map[string]int{
		"not_required": 0,
		"replaced":     1,
		"expired":      2,
		"exception":    3,
	}
	if priority[next] > priority[current] {
		return next
	}
	return current
}

// locationResourceType reports whether a disruption resourceType targets a
// network location. "node" and "laboratory" remain accepted aliases of
// "location" now that both are the same entity.
func locationResourceType(resourceType string) bool {
	switch resourceType {
	case "location", "node", "laboratory":
		return true
	}
	return false
}

func markResourceUnavailable(state *mongostore.State, resourceType, resourceID string) error {
	now := core.Now()
	switch resourceType {
	case "connection", "route":
		item := state.Connections[resourceID]
		if item == nil {
			return notFound("connection")
		}
		item.Active, item.UpdatedAt = false, now
	// A disruption may target a location directly, or by either legacy alias.
	case "location", "node", "laboratory":
		item := state.Locations[resourceID]
		if item == nil {
			return notFound("location")
		}
		item.Active, item.UpdatedAt = false, now
	case "worker":
		item := state.Users[resourceID]
		if item == nil {
			return notFound("worker")
		}
		item.Available, item.UpdatedAt = false, now
	case "capacity":
		item := state.Capacities[resourceID]
		if item == nil {
			return notFound("capacity")
		}
		item.CapacityUnits, item.CooledUnits, item.UpdatedAt = 0, 0, now
	default:
		return core.Fail(400, "invalid_resource_type", "unsupported disruption resourceType")
	}
	return nil
}

func journeyAffected(state *mongostore.State, journey *mongostore.Journey, resourceType, resourceID string) bool {
	if locationResourceType(resourceType) {
		// A location disruption reaches a journey that targets the site as its
		// laboratory and a journey that merely routes through or stores at it.
		// Both checks run because one location can serve both purposes.
		for _, sampleID := range journey.SampleIDs {
			if sample := state.Samples[sampleID]; sample != nil && sample.TargetLaboratoryID == resourceID {
				return true
			}
		}
		return journeyReservesResource(state, journey, resourceID)
	}
	switch resourceType {
	case "connection", "route":
		// A reported network disruption invalidates the route assumptions for
		// every unfinished movement, even when the unavailable connection was
		// an alternate rather than the route's currently reserved edge.
		for _, assignment := range state.Assignments {
			if assignment.JourneyID == journey.ID && assignment.Kind == "transport" &&
				assignment.Status != "completed" && assignment.Status != "delivered" &&
				assignment.Status != "cancelled" {
				return true
			}
		}
		return false
	case "worker":
		for _, assignment := range state.Assignments {
			if assignment.JourneyID == journey.ID && assignment.WorkerID == resourceID &&
				assignment.Status != "completed" && assignment.Status != "delivered" && assignment.Status != "cancelled" {
				return true
			}
		}
	default:
		return journeyReservesResource(state, journey, resourceID)
	}
	return false
}

func journeyReservesResource(state *mongostore.State, journey *mongostore.Journey, resourceID string) bool {
	for _, reservation := range state.Reservations {
		if reservation.JourneyID == journey.ID && reservation.ResourceID == resourceID && reservation.Status == "active" {
			return true
		}
	}
	return false
}

// validateJourneyPlan checks the complete candidate rather than only the field
// touched by an edit. Service mutations run inside Store.Update, so returning an
// error here rolls back stops, assignments, reservations, samples, and versions
// together.
func validateJourneyPlan(state *mongostore.State, journey *mongostore.Journey) error {
	if journey == nil || journey.Status != "active" {
		return core.Fail(409, "invalid_plan", "candidate journey must be active")
	}
	sampleIDs := uniqueStrings(append([]string{}, journey.SampleIDs...))
	if len(sampleIDs) == 0 || len(sampleIDs) != len(journey.SampleIDs) ||
		!stringSliceContains(sampleIDs, journey.PrimarySampleID) {
		return core.Fail(409, "invalid_plan", "journey samples and primary sample are inconsistent")
	}
	if journey.CapacityUsed != len(sampleIDs) || journey.CapacityUsed <= 0 ||
		journey.CapacityUsed > journey.CapacityAvailable {
		return core.Fail(409, "capacity_exceeded", "journey sample count exceeds its valid capacity")
	}
	if len(sampleIDs) > 1 && journey.RouteMode != "batched" {
		return core.Fail(409, "invalid_route_mode", "a multi-sample journey must be marked as batched")
	}

	stops := stopsForJourney(state, journey.ID)
	if len(stops) < 2 || stops[0].StopType != "pickup" || stops[len(stops)-1].StopType != "delivery" {
		return core.Fail(409, "invalid_stop_order", "journey must begin at pickup and end at delivery")
	}
	locations := make([]string, 0, len(stops))
	var previousETA time.Time
	completedPrefix := true
	for index, stop := range stops {
		if stop.Sequence != index+1 || stop.LocationID == "" {
			return core.Fail(409, "invalid_stop_order", "stop sequences must be complete and ordered")
		}
		if index > 0 && index < len(stops)-1 && (stop.StopType == "pickup" || stop.StopType == "delivery") {
			return core.Fail(409, "invalid_stop_order", "pickup and delivery may only appear at route boundaries")
		}
		if !previousETA.IsZero() && stop.EstimatedAt.Before(previousETA) {
			return core.Fail(409, "invalid_stop_order", "stop estimates must be nondecreasing")
		}
		previousETA = stop.EstimatedAt
		if stop.CompletedAt == nil {
			completedPrefix = false
		} else if !completedPrefix {
			return core.Fail(409, "invalid_stop_order", "completed stops must form a route prefix")
		}
		if len(locations) == 0 || locations[len(locations)-1] != stop.LocationID {
			locations = append(locations, stop.LocationID)
		}
	}
	if len(locations) < 2 {
		return core.Fail(409, "invalid_route", "journey must contain at least one movement")
	}

	requiresCooling := false
	for _, sampleID := range sampleIDs {
		sample := state.Samples[sampleID]
		if sample == nil || sample.ActiveJourneyID != journey.ID || terminalSampleStatus(sample.Status) {
			return core.Fail(409, "invalid_plan", "every active journey sample must point to the candidate plan")
		}
		sampleType := state.SampleTypes[sample.SampleTypeID]
		request := state.Requests[sample.RequestID]
		if sampleType == nil || request == nil {
			return core.Fail(409, "invalid_plan", "sample configuration is incomplete")
		}
		if journey.EstimatedDeliveryAt.After(request.DeliveryDeadline) {
			return core.Fail(409, "deadline_exceeded", "candidate plan misses a sample deadline")
		}
		if sample.TargetLaboratoryID != locations[len(locations)-1] {
			return core.Fail(409, "invalid_destination", "sample destination and final route stop differ")
		}
		requiresCooling = requiresCooling || sampleType.RequiresCooledRoute
	}
	if !laboratoryAcceptsJourney(state, locations[len(locations)-1], journey) {
		return core.Fail(409, "invalid_destination", "final laboratory cannot accept every sample")
	}

	for i := range sampleIDs {
		left := state.Samples[sampleIDs[i]]
		leftType := state.SampleTypes[left.SampleTypeID]
		for j := i + 1; j < len(sampleIDs); j++ {
			right := state.Samples[sampleIDs[j]]
			rightType := state.SampleTypes[right.SampleTypeID]
			if left.ContainerType != right.ContainerType ||
				stringSliceContains(left.IncompatibleSampleTypes, right.SampleTypeID) ||
				stringSliceContains(right.IncompatibleSampleTypes, left.SampleTypeID) ||
				mathMax(left.TemperatureMinC, right.TemperatureMinC) > mathMin(left.TemperatureMaxC, right.TemperatureMaxC) ||
				leftType.RequiresCooledRoute != rightType.RequiresCooledRoute {
				return core.Fail(409, "incompatible_batch", "candidate batch contains incompatible samples")
			}
		}
	}

	transports := map[string][]*mongostore.Assignment{}
	collections := map[string][]*mongostore.Assignment{}
	nodes := map[string][]*mongostore.Assignment{}
	laboratories := map[string][]*mongostore.Assignment{}
	for _, assignment := range assignmentsForJourney(state, journey.ID) {
		if assignment.Status == "cancelled" {
			continue
		}
		if !stringSliceContains(sampleIDs, assignment.SampleID) {
			detachedSample := state.Samples[assignment.SampleID]
			if (assignment.Status == "completed" || assignment.Status == "delivered") &&
				(detachedSample == nil || detachedSample.ActiveJourneyID != journey.ID) {
				continue
			}
			return core.Fail(409, "invalid_assignment", "candidate plan assignment references a sample outside the journey")
		}
		switch assignment.Kind {
		case "transport":
			transports[assignment.SampleID] = append(transports[assignment.SampleID], assignment)
		case "collection":
			collections[assignment.SampleID] = append(collections[assignment.SampleID], assignment)
		case "node":
			nodes[assignment.SampleID] = append(nodes[assignment.SampleID], assignment)
		case "laboratory":
			laboratories[assignment.SampleID] = append(laboratories[assignment.SampleID], assignment)
		default:
			return core.Fail(409, "invalid_assignment", "candidate plan contains an unsupported assignment kind")
		}
		if err := validatePlanWorker(state, journey, assignment); err != nil {
			return err
		}
	}
	for _, sampleID := range sampleIDs {
		sampleTransports := transports[sampleID]
		if len(sampleTransports) != len(locations)-1 {
			return core.Fail(409, "invalid_assignment_sequence", "every sample requires an assignment for each route movement")
		}
		sort.Slice(sampleTransports, func(i, j int) bool {
			return sampleTransports[i].LegSequence < sampleTransports[j].LegSequence
		})
		for index, assignment := range sampleTransports {
			if assignment.LegSequence != index+1 ||
				assignment.FromLocationID != locations[index] ||
				assignment.ToLocationID != locations[index+1] {
				return core.Fail(409, "invalid_assignment_sequence", "transport assignment endpoints do not match ordered stops")
			}
		}
		sample := state.Samples[sampleID]
		if sample.SealedAt == nil && len(collections[sampleID]) != 1 {
			return core.Fail(409, "invalid_assignment_sequence", "every unsealed sample requires its own collection assignment")
		}
		for index := 1; index < len(locations)-1; index++ {
			// Every intermediate stop must currently serve as a transfer point or
			// hub — a location that also serves other functions still qualifies —
			// and must carry the matching facility assignment.
			if !servesTransferStop(state, locations[index]) ||
				!hasFacilityAssignment(nodes[sampleID], index, locations[index]) {
				return core.Fail(409, "invalid_assignment_sequence", "every sample requires node work at each intermediate node")
			}
		}
		if len(laboratories[sampleID]) != 1 ||
			!hasFacilityAssignment(laboratories[sampleID], len(locations), locations[len(locations)-1]) {
			return core.Fail(409, "invalid_assignment_sequence", "every sample requires target laboratory work")
		}
	}

	for index := 0; index < len(locations)-1; index++ {
		connectionID := activeReservedConnection(state, journey, locations[index], locations[index+1], requiresCooling)
		if connectionID == "" {
			return core.Fail(409, "invalid_route", "ordered stops are not joined by an active reserved connection")
		}
	}
	for _, locationID := range locations {
		if !hasJourneyReservation(state, journey.ID, locationID, journey.CapacityUsed, requiresCooling) {
			return core.Fail(409, "capacity_exhausted", "candidate plan lacks a valid location reservation")
		}
	}
	for _, capacity := range state.Capacities {
		if capacity.ReservedUnits > capacity.CapacityUnits ||
			capacity.CooledReserved > capacity.CooledUnits ||
			capacity.ReservedUnits < 0 || capacity.CooledReserved < 0 {
			return core.Fail(409, "capacity_exhausted", "candidate plan over-reserves a resource")
		}
	}
	return nil
}

func hasFacilityAssignment(assignments []*mongostore.Assignment, sequence int, locationID string) bool {
	for _, assignment := range assignments {
		if assignment.LegSequence == sequence && assignment.ToLocationID == locationID {
			return true
		}
	}
	return false
}

func activeReservedConnection(
	state *mongostore.State,
	journey *mongostore.Journey,
	from, to string,
	cooled bool,
) string {
	connectionIDs := []string{}
	for _, connection := range state.Connections {
		if connection.Active && connection.FromLocationID == from && connection.ToLocationID == to &&
			(!cooled || connection.Cooled) {
			connectionIDs = append(connectionIDs, connection.ID)
		}
	}
	sort.Strings(connectionIDs)
	for _, connectionID := range connectionIDs {
		if hasJourneyReservation(state, journey.ID, connectionID, journey.CapacityUsed, cooled) {
			return connectionID
		}
	}
	return ""
}

func hasJourneyReservation(
	state *mongostore.State,
	journeyID, resourceID string,
	units int,
	cooled bool,
) bool {
	for _, reservation := range state.Reservations {
		if reservation.JourneyID == journeyID && reservation.ResourceID == resourceID &&
			reservation.Status == "active" && reservation.Units >= units &&
			(!cooled || reservation.Cooled) {
			return true
		}
	}
	return false
}

func validatePlanWorker(state *mongostore.State, journey *mongostore.Journey, assignment *mongostore.Assignment) error {
	role := map[string]string{
		"collection": "collection_professional",
		"transport":  "delivery_partner",
		"node":       "node_handler",
		"laboratory": "laboratory_team",
	}[assignment.Kind]
	if assignment.WorkerID == "" {
		for _, sampleID := range journey.SampleIDs {
			sample := state.Samples[sampleID]
			sampleType := state.SampleTypes[sample.SampleTypeID]
			if sampleType != nil && stringsEqualFold(sampleType.Name, "Pulse") {
				return core.Fail(409, "pulse_no_wait", "Pulse work must have an eligible worker immediately")
			}
		}
		return nil
	}
	worker := state.Users[assignment.WorkerID]
	if worker == nil || !worker.Active || !stringSliceContains(worker.Roles, role) {
		return core.Fail(409, "ineligible_worker", "assignment worker is inactive or lacks the required role")
	}
	if assignment.Status == "pending" && !worker.Available {
		return core.Fail(409, "worker_unavailable", "pending work is offered to an unavailable worker")
	}
	if assignment.Kind == "collection" {
		sample := state.Samples[assignment.SampleID]
		sampleType := state.SampleTypes[sample.SampleTypeID]
		if sampleType != nil && sampleType.QualifiedCollectorRequired &&
			!userHasActiveCertification(worker, sampleType.RequiredQualification) {
			return core.Fail(409, "ineligible_worker", "collector is not qualified for the assigned sample")
		}
	}
	return nil
}

func activeJourneys(state *mongostore.State) []*mongostore.Journey {
	out := []*mongostore.Journey{}
	for _, journey := range state.Journeys {
		if journey.Status == "active" {
			out = append(out, journey)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func cloneUnfinishedAssignments(state *mongostore.State, fromJourney, toJourney, sampleID string) {
	for _, assignment := range assignmentsForJourney(state, fromJourney) {
		if assignment.Status == "completed" || assignment.Status == "delivered" || assignment.Status == "cancelled" {
			continue
		}
		id := core.UUID()
		copy := *assignment
		copy.ID, copy.JourneyID, copy.SampleID = id, toJourney, sampleID
		copy.Declines = append([]mongostore.AssignmentDecline{}, assignment.Declines...)
		state.Assignments[id] = &copy
	}
}

func cancelUnfinishedAssignments(state *mongostore.State, journeyID string) {
	for _, assignment := range state.Assignments {
		if assignment.JourneyID == journeyID &&
			assignment.Status != "completed" && assignment.Status != "delivered" && assignment.Status != "cancelled" {
			assignment.Status = "cancelled"
			assignment.CompletedAt = pointerTime(core.Now())
		}
	}
}

func stopsForJourney(state *mongostore.State, journeyID string) []*mongostore.JourneyStop {
	out := []*mongostore.JourneyStop{}
	for _, stop := range state.Stops {
		if stop.JourneyID == journeyID {
			out = append(out, stop)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sequence != out[j].Sequence {
			return out[i].Sequence < out[j].Sequence
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func assignmentsForJourney(state *mongostore.State, journeyID string) []*mongostore.Assignment {
	out := []*mongostore.Assignment{}
	for _, assignment := range state.Assignments {
		if assignment.JourneyID == journeyID {
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

func terminalSampleStatus(status string) bool {
	switch status {
	case "received", "cancelled", "exception", "expired":
		return true
	default:
		return false
	}
}

func stringsEqualFold(a, b string) bool {
	return len(a) == len(b) && (a == b || strings.ToLower(a) == strings.ToLower(b))
}

func mathMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func mathMax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
