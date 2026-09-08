package service

import (
	"container/heap"
	"context"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

type networkNode struct {
	id         string
	coordinate core.Map
	distanceKM float64
}

type edge struct {
	id, from, to string
	minutes      int
	cost         float64
	capacity     int
	reserved     int
	cooled       bool
}

type routeChoice struct {
	labID   string
	edges   []edge
	cost    float64
	minutes int
}

type serviceRouteChoice struct {
	origin            networkNode
	route             routeChoice
	distanceKM        float64
	capacityAvailable int
}

type routeLabel struct {
	cost    float64
	minutes int
}

type routeItem struct {
	location string
	cost     float64
	minutes  int
	edges    []edge
	index    int
}

type routeQueue []*routeItem

func (q routeQueue) Len() int { return len(q) }
func (q routeQueue) Less(i, j int) bool {
	if q[i].cost == q[j].cost {
		return q[i].minutes < q[j].minutes
	}
	return q[i].cost < q[j].cost
}
func (q routeQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index, q[j].index = i, j
}
func (q *routeQueue) Push(value any) {
	item := value.(*routeItem)
	item.index = len(*q)
	*q = append(*q, item)
}
func (q *routeQueue) Pop() any {
	old := *q
	item := old[len(old)-1]
	*q = old[:len(old)-1]
	return item
}

func (s *Service) createCollectionRequest(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "requester", "administrator"); err != nil {
		return nil, err
	}
	return s.createCollectionRequestAuthorized(ctx, p, in)
}

func (s *Service) ingestPresealedSample(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "collection_professional", "delivery_partner", "node_handler", "laboratory_team"); err != nil {
		return nil, err
	}
	copy := cloneMap(in)
	copy["alreadySealed"] = true
	if core.String(copy["originType"]) == "" {
		copy["originType"] = "node"
	}
	return s.createCollectionRequestAuthorized(ctx, p, copy)
}

func (s *Service) createCollectionRequestAuthorized(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	key := core.String(in["idempotencyKey"])
	requesterID := core.String(in["requesterId"])
	if p.Has("requester") && !p.Has("administrator") {
		requesterID = p.ID
	}
	inputs, err := parseServiceabilityRequest(in)
	if err != nil {
		return nil, err
	}
	origin := inputs.origin
	latitude, longitude := core.Float(origin["latitude"]), core.Float(origin["longitude"])
	requestedAt, deadline, window := inputs.requestedAt, inputs.deadline, inputs.window
	// The collection window bounds when pickup may happen, so it also bounds the
	// earliest possible departure the route is planned from.
	departAt := planningDepartureTime(requestedAt, window)
	originType, err := normalizeOriginType(core.String(in["originType"]))
	if err != nil {
		return nil, err
	}
	alreadySealed := core.Bool(in["alreadySealed"])
	if originType == "home" && alreadySealed {
		return nil, core.Fail(400, "invalid_origin_state", "home samples require professional collection and sealing")
	}

	return s.mutate(ctx, p, "create_collection_request", key, in, func(state *mongostore.State) (txResult, error) {
		if state.Users[requesterID] == nil {
			return txResult{}, notFound("requester")
		}
		sampleType := state.SampleTypes[core.String(in["sampleTypeId"])]
		if sampleType == nil {
			return txResult{}, notFound("sample type")
		}
		requestMinimum := core.Float(in["temperatureMinC"])
		requestMaximum := core.Float(in["temperatureMaxC"])
		if core.String(in["containerType"]) != sampleType.ContainerType ||
			requestMinimum > requestMaximum ||
			requestMinimum < sampleType.TemperatureMinC ||
			requestMaximum > sampleType.TemperatureMaxC {
			return txResult{}, core.Fail(400, "handling_mismatch", "request handling values violate the sample type")
		}

		// One shared decision, so a dry run and the request that follows it can
		// never disagree.
		verdict, verdictErr := evaluateServiceability(state, inputs)
		if verdictErr != nil {
			return txResult{}, verdictErr
		}
		serviceRoute := verdict.route
		requestID, sampleID := core.UUID(), core.UUID()
		serviceability := "unserviceable"
		if verdict.serviceable {
			serviceability = "serviceable"
		}
		now := core.Now()
		state.Requests[requestID] = &mongostore.CollectionRequest{
			ID: requestID, RequesterID: requesterID,
			Latitude: latitude, Longitude: longitude, OriginType: originType,
			RequestedAt:          requestedAt,
			EarliestCollectionAt: window.earliestAt,
			LatestCollectionAt:   window.latestAt,
			DeliveryDeadline:     deadline,
			Serviceability:       serviceability, CreatedAt: now,
		}
		incompatible := effectiveSampleIncompatibilities(
			sampleType.IncompatibleSampleTypes,
			core.Strings(in["incompatibleSampleTypes"]),
		)
		sample := &mongostore.Sample{
			ID: sampleID, RequestID: requestID, RequesterID: requesterID,
			SampleTypeID: sampleType.ID, ContainerType: sampleType.ContainerType,
			TemperatureMinC:         requestMinimum,
			TemperatureMaxC:         requestMaximum,
			IncompatibleSampleTypes: incompatible, Status: "requested",
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		state.Samples[sampleID] = sample
		base := core.Map{
			"requestId": requestID, "sampleId": sampleID,
			"serviceability": serviceability, "activeAssignmentIds": []string{},
			"earliestCollectionAt": formatTime(window.earliestAt),
			"latestCollectionAt":   formatTime(window.latestAt),
		}
		if serviceability == "unserviceable" {
			base["serviceabilityReason"] = verdict.reason
			audit(state, p, "sample", sampleID, "unserviceable_request_recorded", core.Map{
				"distanceKm": serviceRoute.distanceKM, "reason": verdict.reason,
			})
			return txResult{body: base, eventID: emit(state, []string{requesterID, "dispatcher"}, "sample.unserviceable", "sample", sampleID, base)}, nil
		}

		choice := serviceRoute.route

		locationIDs := []string{serviceRoute.origin.id}
		for _, routeEdge := range choice.edges {
			locationIDs = append(locationIDs, routeEdge.to)
		}
		capacityAvailable := serviceRoute.capacityAvailable

		journeyID := core.UUID()
		routeMode := "multi_stop"
		if len(choice.edges) == 1 {
			routeMode = "direct"
		}
		eta := departAt.Add(time.Duration(choice.minutes) * time.Minute)
		state.Journeys[journeyID] = &mongostore.Journey{
			ID: journeyID, PrimarySampleID: sampleID, SampleIDs: []string{sampleID},
			PlanVersion: 1, RouteMode: routeMode, Status: "active",
			EstimatedDeliveryAt: eta, TotalCostUnits: choice.cost,
			CapacityUsed: 1, CapacityAvailable: capacityAvailable,
			CreatedAt: now, UpdatedAt: now,
		}

		assignmentIDs, workerIDs := []string{}, []string{}
		if !alreadySealed {
			collectorID := fairWorker(state, "collection_professional", sampleType.RequiredQualification, sampleType.QualifiedCollectorRequired, nil, "")
			if strings.EqualFold(sampleType.Name, "Pulse") && collectorID == "" {
				return txResult{}, core.Fail(409, "pulse_no_wait", "Pulse samples cannot wait for an eligible collector")
			}
			collectionID := core.UUID()
			state.Assignments[collectionID] = newAssignment(collectionID, journeyID, sampleID, "collection", 0, "", "", collectorID, now)
			assignmentIDs = append(assignmentIDs, collectionID)
			if collectorID != "" {
				workerIDs = append(workerIDs, collectorID)
			}
		} else {
			// The caller IS the holder: a pre-sealed sample is self-attested by
			// whoever currently holds it, one atomic step with no separate
			// confirmation, exactly mirroring collectAndSeal's pattern.
			holderID := p.ID
			holder := state.Users[holderID]
			if holder == nil || !holder.Active {
				return txResult{}, core.Fail(400, "invalid_holder", "pre-sealed intake requires an active caller")
			}
			sample.Status, sample.SealedAt, sample.CurrentHolderID = "sealed", pointerTime(requestedAt), holderID
		}

		for index, routeEdge := range choice.edges {
			deliveryID := fairWorker(state, "delivery_partner", "", false, nil, "")
			if strings.EqualFold(sampleType.Name, "Pulse") && deliveryID == "" {
				return txResult{}, core.Fail(409, "pulse_no_wait", "Pulse samples cannot wait for an eligible delivery partner")
			}
			assignmentID := core.UUID()
			state.Assignments[assignmentID] = newAssignment(
				assignmentID, journeyID, sampleID, "transport", index+1,
				routeEdge.from, routeEdge.to, deliveryID, now,
			)
			assignmentIDs = append(assignmentIDs, assignmentID)
			if deliveryID != "" {
				workerIDs = append(workerIDs, deliveryID)
			}
			if err := reserveJourneyResource(state, journeyID, routeEdge.id, sampleType.RequiresCooledRoute, 1); err != nil {
				return txResult{}, err
			}
		}
		facilityAssignmentIDs, facilityWorkerIDs, err := createFacilityAssignments(
			state,
			journeyID,
			sampleID,
			locationIDs,
			sampleType,
			now,
		)
		if err != nil {
			return txResult{}, err
		}
		assignmentIDs = append(assignmentIDs, facilityAssignmentIDs...)
		workerIDs = append(workerIDs, facilityWorkerIDs...)
		for _, locationID := range locationIDs {
			if err := reserveJourneyResource(state, journeyID, locationID, sampleType.RequiresCooledRoute, 1); err != nil {
				return txResult{}, err
			}
		}
		for index, locationID := range locationIDs {
			stopType := "handoff"
			switch index {
			case 0:
				stopType = "pickup"
			case len(locationIDs) - 1:
				stopType = "delivery"
			}
			stopID := core.UUID()
			stopETA := departAt.Add(time.Duration(choice.minutes*index/maxInt(1, len(locationIDs)-1)) * time.Minute)
			state.Stops[stopID] = &mongostore.JourneyStop{
				ID: stopID, JourneyID: journeyID, LocationID: locationID,
				StopType: stopType, Sequence: index + 1, EstimatedAt: stopETA,
			}
		}
		sample.ActiveJourneyID, sample.TargetLaboratoryID = journeyID, choice.labID
		if !alreadySealed {
			sample.Status = "planned"
		}
		sample.UpdatedAt = core.Now()
		if err := validateJourneyPlan(state, state.Journeys[journeyID]); err != nil {
			return txResult{}, err
		}
		base["journeyId"], base["targetLaboratoryId"], base["activeAssignmentIds"] = journeyID, choice.labID, assignmentIDs
		base["alreadySealed"], base["originType"] = alreadySealed, originType
		audit(state, p, "sample", sampleID, "journey_planned", core.Map{"journeyId": journeyID, "cost": choice.cost})
		audience := append([]string{requesterID, "dispatcher"}, workerIDs...)
		eventID := emit(state, uniqueStrings(audience), "journey.planned", "journey", journeyID, core.Map{"sampleId": sampleID, "assignedWorkerIds": uniqueStrings(workerIDs)})
		return txResult{body: base, eventID: eventID}, nil
	})
}

// servesTransferStop reports whether a location can act as an intermediate stop
// where custody changes or a sample moves between transport legs. The location
// must be active and its transfer-point or hub function must be enabled. A
// location that also serves other functions (a hub that is also a laboratory,
// for example) still qualifies.
func servesTransferStop(state *mongostore.State, locationID string) bool {
	location := state.Locations[locationID]
	return location.Serves(mongostore.FunctionTransferPoint) ||
		location.Serves(mongostore.FunctionHub)
}

// servesLaboratory reports whether a location is active and currently provides
// the laboratory function, regardless of which other functions it also serves.
// Planning a journey TO a laboratory requires this: an inactive laboratory
// cannot be chosen as a final destination.
func servesLaboratory(state *mongostore.State, locationID string) bool {
	return state.Locations[locationID].Serves(mongostore.FunctionLaboratory)
}

// providesLaboratory reports whether a location is a laboratory at all, whether
// or not it is currently active. Recording a laboratory exception is exactly
// what happens when a laboratory has gone down, so that path must accept a
// deactivated laboratory while planning must not.
func providesLaboratory(state *mongostore.State, locationID string) bool {
	return state.Locations[locationID].Provides(mongostore.FunctionLaboratory)
}

func createFacilityAssignments(
	state *mongostore.State,
	journeyID string,
	sampleID string,
	locationIDs []string,
	sampleType *mongostore.SampleType,
	now time.Time,
) ([]string, []string, error) {
	assignmentIDs, workerIDs := []string{}, []string{}
	for index := 1; index < len(locationIDs)-1; index++ {
		locationID := locationIDs[index]
		if !servesTransferStop(state, locationID) {
			continue
		}
		workerID := fairWorker(state, "node_handler", "", false, nil, locationID)
		if strings.EqualFold(sampleType.Name, "Pulse") && workerID == "" {
			return nil, nil, core.Fail(409, "pulse_no_wait", "Pulse samples cannot wait for an eligible node handler")
		}
		assignmentID := core.UUID()
		state.Assignments[assignmentID] = newAssignment(
			assignmentID,
			journeyID,
			sampleID,
			"node",
			index,
			locationIDs[index-1],
			locationID,
			workerID,
			now,
		)
		assignmentIDs = append(assignmentIDs, assignmentID)
		if workerID != "" {
			workerIDs = append(workerIDs, workerID)
		}
	}
	if len(locationIDs) == 0 {
		return assignmentIDs, workerIDs, nil
	}
	laboratoryID := locationIDs[len(locationIDs)-1]
	if !servesLaboratory(state, laboratoryID) {
		return nil, nil, core.Fail(409, "invalid_route", "the final route location must be a laboratory")
	}
	workerID := fairWorker(state, "laboratory_team", "", false, nil, laboratoryID)
	if strings.EqualFold(sampleType.Name, "Pulse") && workerID == "" {
		return nil, nil, core.Fail(409, "pulse_no_wait", "Pulse samples cannot wait for an eligible laboratory operator")
	}
	assignmentID := core.UUID()
	state.Assignments[assignmentID] = newAssignment(
		assignmentID,
		journeyID,
		sampleID,
		"laboratory",
		len(locationIDs),
		locationIDs[maxInt(0, len(locationIDs)-2)],
		laboratoryID,
		workerID,
		now,
	)
	assignmentIDs = append(assignmentIDs, assignmentID)
	if workerID != "" {
		workerIDs = append(workerIDs, workerID)
	}
	return assignmentIDs, workerIDs, nil
}

// serviceNodesInRange returns the active collection points whose inclusive
// coverage — capped at 5 km — reaches the request origin. Only locations whose
// collection-point function is currently enabled participate, so disabling that
// one function removes the site from the serviceable region while its other
// functions keep working.
func serviceNodesInRange(state *mongostore.State, origin core.Map, preferredID string) ([]networkNode, float64) {
	nodes, bestDistance := []networkNode{}, math.MaxFloat64
	for _, location := range state.Locations {
		if !location.Serves(mongostore.FunctionCollectionPoint) {
			continue
		}
		if preferredID != "" && location.ID != preferredID {
			continue
		}
		coordinate := core.Map{"latitude": location.Latitude, "longitude": location.Longitude}
		distance := core.DistanceKM(origin, coordinate)
		if distance < bestDistance {
			bestDistance = distance
		}
		coverageKM := math.Min(5, locationCoverageKM(location))
		if distance <= coverageKM+0.0001 {
			nodes = append(nodes, networkNode{id: location.ID, coordinate: coordinate, distanceKM: distance})
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].distanceKM != nodes[j].distanceKM {
			return nodes[i].distanceKM < nodes[j].distanceKM
		}
		return nodes[i].id < nodes[j].id
	})
	return nodes, bestDistance
}

func closestServiceNode(state *mongostore.State, origin core.Map, preferredID string) (networkNode, float64) {
	nodes, bestDistance := serviceNodesInRange(state, origin, preferredID)
	if len(nodes) == 0 {
		return networkNode{}, bestDistance
	}
	return nodes[0], bestDistance
}

// routeRelaxation temporarily removes one class of constraint from the route
// search. It exists only so an unserviceable request can be explained with a
// specific reason code: the strict search always runs first and is the only one
// that decides whether a request is serviceable.
type routeRelaxation struct {
	ignoreDeadline bool
	ignoreCapacity bool
	ignoreCooling  bool
}

// findCheapestServiceRoute evaluates active nodes covering the request and
// selects the least-cost valid end-to-end route. A feasible node at the exact
// request coordinate is authoritative; otherwise all covered nodes remain
// eligible so an exact dead end cannot hide a feasible route.
func findCheapestServiceRoute(
	state *mongostore.State,
	origin core.Map,
	preferredID string,
	sampleTypeID string,
	departAt time.Time,
	deadline time.Time,
	requiredUnits int,
) (serviceRouteChoice, error) {
	return findRelaxedServiceRoute(
		state, origin, preferredID, sampleTypeID,
		departAt, deadline, requiredUnits, routeRelaxation{},
	)
}

func findRelaxedServiceRoute(
	state *mongostore.State,
	origin core.Map,
	preferredID string,
	sampleTypeID string,
	departAt time.Time,
	deadline time.Time,
	requiredUnits int,
	relax routeRelaxation,
) (serviceRouteChoice, error) {
	nodes, nearestDistance := serviceNodesInRange(state, origin, preferredID)
	best := serviceRouteChoice{distanceKM: nearestDistance}
	requiredCapacity := requiredUnits
	if relax.ignoreCapacity {
		requiredCapacity = 0
	}
	found, exactOriginFound := false, false
	for _, node := range nodes {
		if exactOriginFound && node.distanceKM > 0.0001 {
			break
		}
		choice, err := findRelaxedRoute(
			state, node.id, sampleTypeID, departAt, deadline, requiredUnits, relax,
		)
		if err != nil {
			continue
		}
		sampleType := state.SampleTypes[sampleTypeID]
		capacityAvailable := routeCapacityAvailable(
			state, node.id, choice, sampleType.RequiresCooledRoute && !relax.ignoreCooling,
		)
		if capacityAvailable < requiredCapacity {
			continue
		}
		candidate := serviceRouteChoice{
			origin: node, route: choice, distanceKM: node.distanceKM,
			capacityAvailable: capacityAvailable,
		}
		if node.distanceKM <= 0.0001 {
			if !exactOriginFound || cheaperServiceRoute(candidate, best) {
				best = candidate
			}
			found, exactOriginFound = true, true
			continue
		}
		if !found || cheaperServiceRoute(candidate, best) {
			best, found = candidate, true
		}
	}
	if !found {
		return best, core.Fail(409, "no_valid_route", "no in-range node has a valid route to an accepting laboratory")
	}
	return best, nil
}

func cheaperServiceRoute(candidate, current serviceRouteChoice) bool {
	if candidate.route.cost != current.route.cost {
		return candidate.route.cost < current.route.cost
	}
	if candidate.route.minutes != current.route.minutes {
		return candidate.route.minutes < current.route.minutes
	}
	if candidate.distanceKM != current.distanceKM {
		return candidate.distanceKM < current.distanceKM
	}
	if candidate.origin.id != current.origin.id {
		return candidate.origin.id < current.origin.id
	}
	return candidate.route.labID < current.route.labID
}

func routeCapacityAvailable(state *mongostore.State, originID string, choice routeChoice, cooled bool) int {
	available := math.MaxInt32
	locationIDs := []string{originID}
	for _, routeEdge := range choice.edges {
		locationIDs = append(locationIDs, routeEdge.to)
		available = minInt(available, routeEdge.capacity-routeEdge.reserved)
	}
	for _, locationID := range locationIDs {
		if capacity := state.Capacities[locationID]; capacity != nil {
			locationAvailable := capacity.CapacityUnits - capacity.ReservedUnits
			if cooled {
				locationAvailable = minInt(locationAvailable, capacity.CooledUnits-capacity.CooledReserved)
			}
			available = minInt(available, locationAvailable)
		}
	}
	if available == math.MaxInt32 {
		return 1
	}
	return available
}

func effectiveSampleIncompatibilities(configured, requested []string) []string {
	combined := append(append([]string{}, configured...), requested...)
	return uniqueStrings(combined)
}

func newAssignment(id, journeyID, sampleID, kind string, leg int, from, to, workerID string, now time.Time) *mongostore.Assignment {
	var expiresAt *time.Time
	if workerID != "" {
		expiresAt = pointerTime(now.Add(assignmentOfferTTL()))
	}
	return &mongostore.Assignment{
		ID: id, JourneyID: journeyID, SampleID: sampleID, Kind: kind,
		LegSequence: leg, FromLocationID: from, ToLocationID: to,
		WorkerID: workerID, Status: "pending", OfferExpiresAt: expiresAt,
		Declines: []mongostore.AssignmentDecline{}, CreatedAt: now,
	}
}

func assignmentOfferTTL() time.Duration {
	seconds := 300
	if value, err := strconv.Atoi(os.Getenv("ASSIGNMENT_OFFER_TTL_SECONDS")); err == nil && value >= 15 && value <= 86400 {
		seconds = value
	}
	return time.Duration(seconds) * time.Second
}

func reserveJourneyResource(state *mongostore.State, journeyID, resourceID string, cooled bool, units int) error {
	if units <= 0 {
		return core.Fail(400, "invalid_capacity", "reservation units must be positive")
	}
	if capacity := state.Capacities[resourceID]; capacity != nil {
		if capacity.ReservedUnits+units > capacity.CapacityUnits {
			return core.Fail(409, "capacity_exhausted", "resource capacity was reserved concurrently")
		}
		if cooled && capacity.CooledReserved+units > capacity.CooledUnits {
			return core.Fail(409, "cooled_capacity_exhausted", "cooled resource capacity is exhausted")
		}
		capacity.ReservedUnits += units
		if cooled {
			capacity.CooledReserved += units
		}
		capacity.UpdatedAt = core.Now()
	}
	for _, reservation := range state.Reservations {
		if reservation.JourneyID == journeyID && reservation.ResourceID == resourceID && reservation.Status == "active" {
			reservation.Units += units
			reservation.Cooled = reservation.Cooled || cooled
			return nil
		}
	}
	id := core.UUID()
	state.Reservations[id] = &mongostore.Reservation{
		ID: id, JourneyID: journeyID, ResourceID: resourceID,
		Units: units, Cooled: cooled, Status: "active",
	}
	return nil
}

func findRoute(state *mongostore.State, origin, sampleTypeID string, departAt, deadline time.Time, requiredUnits int) (routeChoice, error) {
	return findRelaxedRoute(state, origin, sampleTypeID, departAt, deadline, requiredUnits, routeRelaxation{})
}

func findRelaxedRoute(
	state *mongostore.State,
	origin, sampleTypeID string,
	departAt, deadline time.Time,
	requiredUnits int,
	relax routeRelaxation,
) (routeChoice, error) {
	sampleType := state.SampleTypes[sampleTypeID]
	if sampleType == nil {
		return routeChoice{}, notFound("sample type")
	}
	requiresCooled := sampleType.RequiresCooledRoute && !relax.ignoreCooling
	requiredCapacity := requiredUnits
	if relax.ignoreCapacity {
		requiredCapacity = 0
	}
	// beatsDeadline is the single place the deadline is applied, so relaxing it
	// cannot accidentally relax anything else.
	beatsDeadline := func(minutes int) bool {
		return relax.ignoreDeadline ||
			!departAt.Add(time.Duration(minutes)*time.Minute).After(deadline)
	}
	adjacency := map[string][]edge{}
	for _, connection := range state.Connections {
		if !connection.Active || requiresCooled && !connection.Cooled ||
			!routeLocationAvailable(state, connection.FromLocationID, origin) ||
			!routeLocationAvailable(state, connection.ToLocationID, origin) ||
			!routeResourceHasCapacity(state, connection.FromLocationID, requiresCooled, requiredCapacity) ||
			!routeResourceHasCapacity(state, connection.ToLocationID, requiresCooled, requiredCapacity) {
			continue
		}
		capacity, reserved := math.MaxInt32, 0
		if configured := state.Capacities[connection.ID]; configured != nil {
			available := configured.CapacityUnits - configured.ReservedUnits
			if requiresCooled {
				available = minInt(available, configured.CooledUnits-configured.CooledReserved)
			}
			capacity, reserved = available, 0
		}
		if capacity-reserved < requiredCapacity {
			continue
		}
		adjacency[connection.FromLocationID] = append(adjacency[connection.FromLocationID], edge{
			id: connection.ID, from: connection.FromLocationID, to: connection.ToLocationID,
			minutes:  connection.TravelMinutes + locationHandlingMinutes(state, connection.ToLocationID),
			cost:     connection.CostUnits,
			capacity: capacity, reserved: reserved, cooled: connection.Cooled,
		})
	}
	laboratories := map[string]bool{}
	for _, acceptance := range state.Acceptances {
		laboratory := state.Locations[acceptance.LaboratoryID]
		if acceptance.SampleTypeID != sampleTypeID || !acceptance.Accepts ||
			!laboratory.Serves(mongostore.FunctionLaboratory) {
			continue
		}
		if capacity := state.Capacities[laboratory.ID]; capacity != nil {
			if capacity.CapacityUnits-capacity.ReservedUnits < requiredCapacity {
				continue
			}
			if requiresCooled && capacity.CooledUnits-capacity.CooledReserved < requiredCapacity {
				continue
			}
		}
		laboratories[laboratory.ID] = true
	}

	queue := &routeQueue{}
	heap.Init(queue)
	heap.Push(queue, &routeItem{location: origin})
	frontier := map[string][]routeLabel{}
	for queue.Len() > 0 {
		item := heap.Pop(queue).(*routeItem)
		if dominated(frontier[item.location], routeLabel{cost: item.cost, minutes: item.minutes}, true) {
			continue
		}
		if laboratories[item.location] {
			if !beatsDeadline(item.minutes) {
				continue
			}
			return routeChoice{labID: item.location, edges: item.edges, cost: item.cost, minutes: item.minutes}, nil
		}
		frontier[item.location] = addLabel(frontier[item.location], routeLabel{cost: item.cost, minutes: item.minutes})
		for _, routeEdge := range adjacency[item.location] {
			nextCost := item.cost + routeEdge.cost
			nextMinutes := item.minutes + routeEdge.minutes
			if !beatsDeadline(nextMinutes) {
				continue
			}
			if dominated(frontier[routeEdge.to], routeLabel{cost: nextCost, minutes: nextMinutes}, false) {
				continue
			}
			path := append(append([]edge{}, item.edges...), routeEdge)
			heap.Push(queue, &routeItem{location: routeEdge.to, cost: nextCost, minutes: nextMinutes, edges: path})
		}
	}
	return routeChoice{}, core.Fail(409, "no_valid_route", "no accepting laboratory can be reached before the deadline")
}

func locationHandlingMinutes(state *mongostore.State, locationID string) int {
	if location := state.Locations[locationID]; location != nil && location.HandlingMinutes > 0 {
		return location.HandlingMinutes
	}
	return 0
}

func routeLocationAvailable(state *mongostore.State, locationID, origin string) bool {
	if locationID == origin {
		return true
	}
	if location := state.Locations[locationID]; location != nil {
		return location.Active
	}
	return false
}

func routeResourceHasCapacity(state *mongostore.State, resourceID string, cooled bool, requiredUnits int) bool {
	capacity := state.Capacities[resourceID]
	if capacity == nil {
		return true
	}
	if capacity.CapacityUnits-capacity.ReservedUnits < requiredUnits {
		return false
	}
	return !cooled || capacity.CooledUnits-capacity.CooledReserved >= requiredUnits
}

func dominated(labels []routeLabel, candidate routeLabel, requireStrict bool) bool {
	for _, label := range labels {
		noWorse := label.cost <= candidate.cost && label.minutes <= candidate.minutes
		strict := label.cost < candidate.cost || label.minutes < candidate.minutes
		if noWorse && (!requireStrict || strict) {
			return true
		}
	}
	return false
}

func addLabel(labels []routeLabel, candidate routeLabel) []routeLabel {
	out := labels[:0]
	for _, label := range labels {
		if candidate.cost <= label.cost && candidate.minutes <= label.minutes &&
			(candidate.cost < label.cost || candidate.minutes < label.minutes) {
			continue
		}
		out = append(out, label)
	}
	return append(out, candidate)
}

// fairWorker selects the fairest eligible worker for the given role. When
// locationID is non-empty, candidates are additionally restricted to workers
// whose AssignedLocationID matches it — used only where an offer must be
// scoped to a specific node or laboratory; every other caller passes "" and
// sees no location restriction.
func fairWorker(state *mongostore.State, role, qualification string, qualificationRequired bool, excluded map[string]bool, locationID string) string {
	type candidate struct {
		user          *mongostore.User
		activeWork    int
		roleOffers    int
		creationOrder int64
	}
	candidates := []candidate{}
	now := core.Now()
	for _, user := range state.Users {
		if excluded[user.ID] || !user.Active || !user.Available ||
			!stringSliceContains(user.Roles, role) ||
			qualificationRequired && !userHasActiveCertification(user, qualification) ||
			locationID != "" && user.AssignedLocationID != locationID {
			continue
		}
		activeWork := activeWorkerAssignments(state, user.ID)
		if activeWork >= user.CapacityUnits {
			continue
		}
		candidates = append(candidates, candidate{
			user:          user,
			activeWork:    activeWork,
			roleOffers:    workerRoleOfferCount(state, user.ID, role, now),
			creationOrder: userCreationOrder(state, user.ID),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.activeWork != b.activeWork {
			return a.activeWork < b.activeWork
		}
		if a.roleOffers != b.roleOffers {
			return a.roleOffers < b.roleOffers
		}
		if (a.user.LastOfferedAt == nil) != (b.user.LastOfferedAt == nil) {
			return a.user.LastOfferedAt == nil
		}
		if a.user.LastOfferedAt != nil && !a.user.LastOfferedAt.Equal(*b.user.LastOfferedAt) {
			return a.user.LastOfferedAt.Before(*b.user.LastOfferedAt)
		}
		if a.creationOrder != b.creationOrder {
			return a.creationOrder < b.creationOrder
		}
		if a.user.Username != b.user.Username {
			return a.user.Username < b.user.Username
		}
		return a.user.ID < b.user.ID
	})
	if len(candidates) == 0 {
		return ""
	}
	selected := candidates[0].user
	selected.AssignmentCount++
	selected.LastOfferedAt = &now
	selected.UpdatedAt = now
	return selected.ID
}

func userCreationOrder(state *mongostore.State, userID string) int64 {
	for _, event := range state.AuditEvents {
		if event.EntityType == "user" &&
			event.EntityID == userID &&
			event.Action == "user_created" {
			return event.ID
		}
	}
	return math.MaxInt64
}

// workerRoleOfferCount is the fairness load a worker already carries in one
// role: the work currently offered to or held by them, plus the declines that
// are still inside the decay window.
//
// A decline outside the window contributes nothing, so once the window has
// passed a worker who declined ranks exactly as if they never had. The penalty
// is a temporary priority adjustment and never an exclusion.
func workerRoleOfferCount(state *mongostore.State, workerID, role string, now time.Time) int {
	kind := map[string]string{
		"collection_professional": "collection",
		"delivery_partner":        "transport",
		"node_handler":            "node",
		"laboratory_team":         "laboratory",
	}[role]
	if kind == "" {
		return 0
	}
	total := 0
	for _, assignment := range state.Assignments {
		if assignment.Kind != kind {
			continue
		}
		if assignment.WorkerID == workerID {
			total++
		}
		for _, decline := range assignment.Declines {
			if decline.WorkerID == workerID && declinePenaltyActive(decline.At, now) {
				total++
			}
		}
	}
	return total
}

func activeWorkerAssignments(state *mongostore.State, workerID string) int {
	total := 0
	for _, assignment := range state.Assignments {
		if assignment.WorkerID != workerID {
			continue
		}
		switch assignment.Status {
		case "accepted", "at_pickup", "picked_up", "in_progress", "in_transit", "arrived":
			total++
		}
	}
	return total
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sortStringsUnique(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
