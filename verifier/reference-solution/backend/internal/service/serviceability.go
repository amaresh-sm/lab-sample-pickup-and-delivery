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

// Stable machine reason codes for a serviceability decision. The positive path
// reports serviceableReason; every negative path reports exactly one clause of
// the serviceability rule that could not be satisfied.
const (
	serviceableReason                = "serviceable"
	reasonOutsideServiceCoverage     = "outside_service_coverage"
	reasonNoAcceptingLaboratory      = "no_accepting_laboratory"
	reasonNoRoute                    = "no_route"
	reasonInsufficientCapacity       = "insufficient_capacity"
	reasonDeadlineUnreachable        = "deadline_unreachable"
	reasonHandlingRulesUnsatisfiable = "handling_rules_unsatisfiable"
)

// collectionWindow is the interval inside which collection may take place. It
// is a separate constraint from the delivery deadline.
type collectionWindow struct {
	earliestAt time.Time
	latestAt   time.Time
}

// parseCollectionWindow reads the optional collection-window fields. When both
// are absent the window defaults to [requestedAt, deliveryDeadline], so a
// client that sends no window keeps the previous behaviour exactly.
func parseCollectionWindow(in core.Map, requestedAt, deadline time.Time) (collectionWindow, error) {
	window := collectionWindow{earliestAt: requestedAt, latestAt: deadline}
	if value, present := in["earliestCollectionAt"]; present && core.String(value) != "" {
		parsed, err := core.Time(value)
		if err != nil {
			return collectionWindow{}, err
		}
		window.earliestAt = parsed
	}
	if value, present := in["latestCollectionAt"]; present && core.String(value) != "" {
		parsed, err := core.Time(value)
		if err != nil {
			return collectionWindow{}, err
		}
		window.latestAt = parsed
	}
	if !window.earliestAt.Before(window.latestAt) {
		return collectionWindow{}, core.Fail(
			400,
			"invalid_collection_window",
			"earliestCollectionAt must precede latestCollectionAt",
		)
	}
	if window.latestAt.After(deadline) {
		return collectionWindow{}, core.Fail(
			400,
			"invalid_collection_window",
			"latestCollectionAt cannot be after the delivery deadline",
		)
	}
	return window, nil
}

// requestCollectionWindow reads a stored window, defaulting a record written
// before the window existed to [requestedAt, deliveryDeadline].
func requestCollectionWindow(request *mongostore.CollectionRequest) (time.Time, time.Time) {
	earliest, latest := request.EarliestCollectionAt, request.LatestCollectionAt
	if earliest.IsZero() {
		earliest = request.RequestedAt
	}
	if latest.IsZero() {
		latest = request.DeliveryDeadline
	}
	return earliest, latest
}

// planningDepartureTime is the earliest moment a sample can leave its origin.
// The journey's collection step cannot be planned before the window opens, and
// nothing can be planned before the request itself was made.
func planningDepartureTime(requestedAt time.Time, window collectionWindow) time.Time {
	if window.earliestAt.After(requestedAt) {
		return window.earliestAt
	}
	return requestedAt
}

// normalizeOriginType validates the origin classification shared by the dry run
// and the real request.
func normalizeOriginType(value string) (string, error) {
	originType := strings.ToLower(strings.TrimSpace(value))
	if originType == "" {
		originType = "home"
	}
	if originType != "home" && originType != "clinic" && originType != "node" {
		return "", core.Fail(400, "invalid_origin_type", "originType must be home, clinic, or node")
	}
	return originType, nil
}

// serviceabilityInputs is everything the serviceability decision reads from the
// caller. Notably absent: any worker, vehicle or staffing input.
type serviceabilityInputs struct {
	origin        core.Map
	originNodeID  string
	sampleTypeID  string
	requestedAt   time.Time
	deadline      time.Time
	window        collectionWindow
	requiredUnits int
}

type serviceabilityVerdict struct {
	serviceable        bool
	reason             string
	route              serviceRouteChoice
	targetLaboratoryID string
	estimatedArrivalAt time.Time
}

// parseServiceabilityRequest validates the request fields that the dry-run
// check and create_collection_request share, in the same order, so the two
// report the same error for the same input.
func parseServiceabilityRequest(in core.Map) (serviceabilityInputs, error) {
	origin := core.Object(in["origin"])
	if origin == nil {
		return serviceabilityInputs{}, core.Fail(400, "invalid_origin", "origin coordinate is required")
	}
	latitude, longitude := core.Float(origin["latitude"]), core.Float(origin["longitude"])
	if latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		return serviceabilityInputs{}, core.Fail(400, "invalid_origin", "origin coordinate is invalid")
	}
	requestedAt, err := core.Time(in["requestedAt"])
	if err != nil {
		return serviceabilityInputs{}, err
	}
	deadline, err := core.Time(in["deliveryDeadline"])
	if err != nil {
		return serviceabilityInputs{}, err
	}
	if !deadline.After(requestedAt) {
		return serviceabilityInputs{}, core.Fail(400, "invalid_deadline", "delivery deadline must follow requested time")
	}
	if _, err := normalizeOriginType(core.String(in["originType"])); err != nil {
		return serviceabilityInputs{}, err
	}
	window, err := parseCollectionWindow(in, requestedAt, deadline)
	if err != nil {
		return serviceabilityInputs{}, err
	}
	return serviceabilityInputs{
		origin:        origin,
		originNodeID:  core.String(in["originNodeId"]),
		sampleTypeID:  core.String(in["sampleTypeId"]),
		requestedAt:   requestedAt,
		deadline:      deadline,
		window:        window,
		requiredUnits: 1,
	}, nil
}

// evaluateServiceability is the single serviceability decision. Both the dry-run
// check and create_collection_request call it, so a dry run and the request it
// precedes can never disagree.
//
// It reads only the structure of the network — active collection points, active
// connections, laboratory acceptance, capacity pools and handling rules — and
// never any worker's availability, so the serviceable region does not move when
// staffing changes.
func evaluateServiceability(
	state *mongostore.State,
	in serviceabilityInputs,
) (serviceabilityVerdict, error) {
	if state.SampleTypes[in.sampleTypeID] == nil {
		return serviceabilityVerdict{}, notFound("sample type")
	}
	units := in.requiredUnits
	if units <= 0 {
		units = 1
	}
	departAt := planningDepartureTime(in.requestedAt, in.window)

	nodes, nearestDistance := serviceNodesInRange(state, in.origin, in.originNodeID)
	if len(nodes) == 0 {
		return serviceabilityVerdict{
			reason: reasonOutsideServiceCoverage,
			route:  serviceRouteChoice{distanceKM: nearestDistance},
		}, nil
	}
	if !acceptingLaboratoryExists(state, in.sampleTypeID) {
		return serviceabilityVerdict{
			reason: reasonNoAcceptingLaboratory,
			route:  serviceRouteChoice{distanceKM: nearestDistance},
		}, nil
	}
	route, err := findRelaxedServiceRoute(
		state, in.origin, in.originNodeID, in.sampleTypeID,
		departAt, in.deadline, units, routeRelaxation{},
	)
	if err == nil {
		return serviceabilityVerdict{
			serviceable:        true,
			reason:             serviceableReason,
			route:              route,
			targetLaboratoryID: route.route.labID,
			estimatedArrivalAt: departAt.Add(time.Duration(route.route.minutes) * time.Minute),
		}, nil
	}
	return serviceabilityVerdict{
		reason: diagnoseUnserviceable(state, in, departAt, units),
		route:  route,
	}, nil
}

// diagnoseUnserviceable re-runs the same search with one more class of
// constraint relaxed at each step, and names the first clause whose removal
// makes a route appear. The strict search has already decided the request is
// unserviceable; this only explains why.
func diagnoseUnserviceable(
	state *mongostore.State,
	in serviceabilityInputs,
	departAt time.Time,
	units int,
) string {
	ladder := []struct {
		relax  routeRelaxation
		reason string
	}{
		{routeRelaxation{ignoreDeadline: true}, reasonDeadlineUnreachable},
		{
			routeRelaxation{ignoreDeadline: true, ignoreCapacity: true},
			reasonInsufficientCapacity,
		},
		{
			routeRelaxation{ignoreDeadline: true, ignoreCapacity: true, ignoreCooling: true},
			reasonHandlingRulesUnsatisfiable,
		},
	}
	for _, step := range ladder {
		if _, err := findRelaxedServiceRoute(
			state, in.origin, in.originNodeID, in.sampleTypeID,
			departAt, in.deadline, units, step.relax,
		); err == nil {
			return step.reason
		}
	}
	return reasonNoRoute
}

// acceptingLaboratoryExists reports whether any location that currently serves
// the laboratory function accepts the sample type, independently of route and
// capacity.
func acceptingLaboratoryExists(state *mongostore.State, sampleTypeID string) bool {
	for _, acceptance := range state.Acceptances {
		if acceptance.SampleTypeID != sampleTypeID || !acceptance.Accepts {
			continue
		}
		if state.Locations[acceptance.LaboratoryID].Serves(mongostore.FunctionLaboratory) {
			return true
		}
	}
	return false
}

// bumpNetworkRevision records that a structural or policy input to the
// serviceability calculation changed, so the maintenance pass publishes a new
// complete calculation version.
func bumpNetworkRevision(state *mongostore.State) {
	state.NetworkRevision++
}

// serviceabilityRegions describes exactly the locations that currently serve as
// collection points, each with the effective coverage planning actually uses:
// the configured coverage capped at the fixed 5 km service boundary.
func serviceabilityRegions(state *mongostore.State) []*mongostore.ServiceRegion {
	regions := make([]*mongostore.ServiceRegion, 0, len(state.Locations))
	for _, location := range state.Locations {
		if !location.Serves(mongostore.FunctionCollectionPoint) {
			continue
		}
		regions = append(regions, &mongostore.ServiceRegion{
			LocationID: location.ID,
			Name:       location.Name,
			Latitude:   location.Latitude,
			Longitude:  location.Longitude,
			CoverageKM: math.Min(5, locationCoverageKM(location)),
		})
	}
	sort.Slice(regions, func(i, j int) bool {
		return regions[i].LocationID < regions[j].LocationID
	})
	return regions
}

// publishServiceability computes one whole new calculation version. The result
// is built completely in ServiceabilityDraft under status "computing" and only
// then swapped into Serviceability as "complete", so Serviceability is never
// mutated in place and a reader can never observe a partial calculation.
func publishServiceability(state *mongostore.State) *mongostore.ServiceabilityCalculation {
	version := int64(1)
	if state.Serviceability != nil {
		version = state.Serviceability.Version + 1
	}
	state.ServiceabilityDraft = &mongostore.ServiceabilityCalculation{
		Version:         version,
		Status:          mongostore.ServiceabilityComputing,
		NetworkRevision: state.NetworkRevision,
		ComputedAt:      core.Now(),
		Regions:         serviceabilityRegions(state),
	}
	complete := *state.ServiceabilityDraft
	complete.Status = mongostore.ServiceabilityComplete
	state.Serviceability = &complete
	state.ServiceabilityDraft = nil
	return state.Serviceability
}

// serviceabilityUpToDate reports whether the published calculation is a
// complete description of the current network revision.
func serviceabilityUpToDate(state *mongostore.State) bool {
	return state.Serviceability != nil &&
		state.Serviceability.Status == mongostore.ServiceabilityComplete &&
		state.Serviceability.NetworkRevision == state.NetworkRevision
}

// ensureServiceabilityPublished publishes the next complete version when the
// published one is missing or describes an older network revision.
func ensureServiceabilityPublished(state *mongostore.State) *mongostore.ServiceabilityCalculation {
	if serviceabilityUpToDate(state) {
		return state.Serviceability
	}
	return publishServiceability(state)
}

// servedServiceability returns the calculation that answers a serviceability
// question. It is always a complete published version — ServiceabilityDraft is
// never served, so no request is checked against an in-progress calculation.
//
// On the read path the state is a snapshot loaded by Store.View, which never
// commits, so publishing a missing or out-of-date version here is an in-memory
// projection only. The durable publish happens in the maintenance pass.
func servedServiceability(state *mongostore.State) *mongostore.ServiceabilityCalculation {
	return ensureServiceabilityPublished(state)
}

func serviceRegionMaps(regions []*mongostore.ServiceRegion) []core.Map {
	out := make([]core.Map, 0, len(regions))
	for _, region := range regions {
		out = append(out, core.Map{
			"locationId": region.LocationID,
			"name":       region.Name,
			"latitude":   region.Latitude,
			"longitude":  region.Longitude,
			"coverageKm": region.CoverageKM,
		})
	}
	return out
}

func serviceabilitySnapshotView(calculation *mongostore.ServiceabilityCalculation) core.Map {
	return core.Map{
		"version":                    calculation.Version,
		"status":                     calculation.Status,
		"computedAt":                 formatTime(calculation.ComputedAt),
		"networkRevision":            calculation.NetworkRevision,
		"regions":                    serviceRegionMaps(calculation.Regions),
		"activeCollectionPointCount": len(calculation.Regions),
	}
}

// checkServiceability answers the same question create_collection_request
// answers, without creating anything.
//
// It is deliberately implemented on the read path. Store.View loads one
// consistent snapshot and never commits, so this operation writes no request,
// sample, journey, assignment, stop, reservation, handoff, capacity record,
// idempotency record, worker fairness counter or audit event, and it never
// consumes an idempotency key.
func (s *Service) checkServiceability(
	ctx context.Context,
	p core.Principal,
	in core.Map,
) (core.Map, error) {
	if err := require(p, "requester", "dispatcher", "administrator"); err != nil {
		return nil, err
	}
	inputs, err := parseServiceabilityRequest(in)
	if err != nil {
		return nil, err
	}
	var out core.Map
	if err := s.Store.View(ctx, func(state *mongostore.State) error {
		published := servedServiceability(state)
		verdict, verdictErr := evaluateServiceability(state, inputs)
		if verdictErr != nil {
			return verdictErr
		}
		out = core.Map{
			"serviceable":           verdict.serviceable,
			"reason":                verdict.reason,
			"serviceabilityVersion": published.Version,
		}
		if verdict.serviceable {
			out["targetLaboratoryId"] = verdict.targetLaboratoryID
			out["estimatedArrivalAt"] = formatTime(verdict.estimatedArrivalAt)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// readServiceabilitySnapshot exposes the active collection points and coverage
// regions the published serviceability calculation is built from.
func (s *Service) readServiceabilitySnapshot(
	ctx context.Context,
	p core.Principal,
) (core.Map, error) {
	if err := require(p, "administrator", "dispatcher"); err != nil {
		return nil, err
	}
	var out core.Map
	if err := s.Store.View(ctx, func(state *mongostore.State) error {
		out = serviceabilitySnapshotView(servedServiceability(state))
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}
