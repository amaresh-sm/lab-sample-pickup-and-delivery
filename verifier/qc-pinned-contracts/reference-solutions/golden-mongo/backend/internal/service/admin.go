package service

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func (s *Service) createUser(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator"); err != nil {
		return nil, err
	}
	roles, qualifications := uniqueStrings(core.Strings(in["roles"])), uniqueStrings(core.Strings(in["qualifications"]))
	certifications, _, err := parseUserCertifications(in)
	if err != nil {
		return nil, err
	}
	username := strings.TrimSpace(core.String(in["username"]))
	password := core.String(in["initialPassword"])
	assignedLocationID := core.String(in["assignedLocationId"])
	if len(roles) == 0 || username == "" || password == "" {
		return nil, core.Fail(400, "invalid_user", "username, initialPassword, and at least one role are required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, err
	}
	id, now := core.UUID(), core.Now()
	out := core.Map{
		"userId": id, "username": username, "email": core.String(in["email"]),
		"fullName": core.String(in["fullName"]), "phone": core.String(in["phone"]),
		"roles": roles, "qualifications": qualifications,
		"certifications": certificationMaps(certifications), "active": core.Bool(in["active"]),
	}
	return s.mutate(ctx, p, "create_user", "", in, func(state *mongostore.State) (txResult, error) {
		for _, user := range state.Users {
			if strings.EqualFold(user.Username, username) {
				return txResult{}, core.Fail(409, "username_exists", "username already exists")
			}
		}
		state.Users[id] = &mongostore.User{
			ID: id, Username: username, PasswordHash: string(hash),
			Email: core.String(in["email"]), FullName: core.String(in["fullName"]),
			Phone: core.String(in["phone"]), Roles: roles, Qualifications: qualifications,
			Certifications:     certifications,
			AssignedLocationID: assignedLocationID,
			Active:             core.Bool(in["active"]), Available: true, CapacityUnits: 6,
			CreatedAt: now, UpdatedAt: now,
		}
		audit(state, p, "user", id, "user_created", core.Map{"roles": roles})
		eventID := emit(state, []string{"administrator"}, "user.created", "user", id, out)
		return txResult{body: out, eventID: eventID}, nil
	})
}

func (s *Service) readUser(ctx context.Context, p core.Principal, id string) (core.Map, error) {
	if !p.Has("administrator") && p.ID != id {
		return nil, core.Fail(403, "forbidden", "only administrators may read another user")
	}
	var out core.Map
	err := s.Store.View(ctx, func(state *mongostore.State) error {
		user := state.Users[id]
		if user == nil {
			return notFound("user")
		}
		out = userMap(user)
		return nil
	})
	return out, err
}

func userMap(user *mongostore.User) core.Map {
	return core.Map{
		"userId": user.ID, "username": user.Username, "email": user.Email,
		"fullName": user.FullName, "phone": user.Phone,
		"roles":          append([]string{}, user.Roles...),
		"qualifications": append([]string{}, user.Qualifications...),
		"certifications": certificationMaps(user.Certifications),
		"active":         user.Active,
	}
}

func (s *Service) updateUserAccess(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator"); err != nil {
		return nil, err
	}
	id := core.String(in["userId"])
	roles := uniqueStrings(core.Strings(in["roles"]))
	qualifications := uniqueStrings(core.Strings(in["qualifications"]))
	assignedLocationID := core.String(in["assignedLocationId"])
	certifications, certificationsProvided, err := parseUserCertifications(in)
	if err != nil {
		return nil, err
	}
	return s.mutate(ctx, p, "update_user_access", "", in, func(state *mongostore.State) (txResult, error) {
		user := state.Users[id]
		if user == nil {
			return txResult{}, notFound("user")
		}
		user.Roles, user.Qualifications, user.Active = roles, qualifications, core.Bool(in["active"])
		user.AssignedLocationID = assignedLocationID
		if certificationsProvided {
			user.Certifications = certifications
		}
		user.UpdatedAt = core.Now()
		out := core.Map{
			"userId": id, "roles": roles, "qualifications": qualifications,
			"certifications": certificationMaps(user.Certifications), "active": user.Active,
		}
		audit(state, p, "user", id, "user_access_updated", out)
		return txResult{body: out, eventID: emit(state, []string{"administrator", id}, "user.access_updated", "user", id, out)}, nil
	})
}

func coordinate(in core.Map) (float64, float64, error) {
	coordinate := core.Object(in["coordinate"])
	if coordinate == nil {
		return 0, 0, core.Fail(400, "invalid_coordinate", "coordinate is required")
	}
	lat, lon := core.Float(coordinate["latitude"]), core.Float(coordinate["longitude"])
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, core.Fail(400, "invalid_coordinate", "invalid latitude or longitude")
	}
	return lat, lon, nil
}

// parseLocationFunctions reads the requested function set. Every entry must be
// one of the four canonical function names. A newly provided function starts
// active; a function that is already provided keeps its current activation so
// editing a location's name or coordinate never silently re-enables a function
// an administrator switched off.
func parseLocationFunctions(in core.Map, current map[string]bool) (map[string]bool, bool, error) {
	raw, present := in["functions"]
	if !present {
		return cloneFunctions(current), false, nil
	}
	functions := map[string]bool{}
	for _, name := range core.Strings(raw) {
		normalized := strings.ToLower(strings.TrimSpace(name))
		if !mongostore.IsLocationFunction(normalized) {
			return nil, false, core.Fail(
				400,
				"invalid_location_function",
				"functions must name collection_point, transfer_point, hub, or laboratory",
			)
		}
		active := true
		if wasActive, ok := current[normalized]; ok {
			active = wasActive
		}
		functions[normalized] = active
	}
	return functions, true, nil
}

func cloneFunctions(functions map[string]bool) map[string]bool {
	out := map[string]bool{}
	for name, active := range functions {
		out[name] = active
	}
	return out
}

// functionsView renders the function map in a stable, JSON-friendly shape.
func functionsView(functions map[string]bool) core.Map {
	out := core.Map{}
	for _, name := range mongostore.LocationFunctions() {
		if active, ok := functions[name]; ok {
			out[name] = active
		}
	}
	return out
}

func locationMap(location *mongostore.Location) core.Map {
	return core.Map{
		"locationId": location.ID, "name": location.Name,
		"coordinate":      core.Map{"latitude": location.Latitude, "longitude": location.Longitude},
		"coverageKm":      locationCoverageKM(location),
		"handlingMinutes": location.HandlingMinutes,
		"active":          location.Active,
		"functions":       functionsView(location.Functions),
	}
}

func (s *Service) createLocation(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator"); err != nil {
		return nil, err
	}
	latitude, longitude, err := coordinate(in)
	if err != nil {
		return nil, err
	}
	coverageKM, handlingMinutes, err := locationSettings(in, 5, 0)
	if err != nil {
		return nil, err
	}
	functions, _, err := parseLocationFunctions(in, nil)
	if err != nil {
		return nil, err
	}
	// A location that provides no function is inert: it can neither be a
	// collection point, a stop, nor a destination. Refuse to create one.
	if len(functions) == 0 {
		return nil, core.Fail(
			400,
			"invalid_location_function",
			"a location must provide at least one of collection_point, transfer_point, hub, or laboratory",
		)
	}
	id, now := core.UUID(), core.Now()
	location := &mongostore.Location{
		ID: id, Name: core.String(in["name"]), Latitude: latitude,
		Longitude: longitude, CoverageKM: coverageKM,
		HandlingMinutes: handlingMinutes, Active: core.Bool(in["active"]),
		Functions: functions, CreatedAt: now, UpdatedAt: now,
	}
	out := locationMap(location)
	return s.mutate(ctx, p, "create_location", "", in, func(state *mongostore.State) (txResult, error) {
		state.Locations[id] = location
		bumpNetworkRevision(state)
		audit(state, p, "location", id, "location_created", out)
		return txResult{body: out, eventID: emit(state, []string{"administrator", "dispatcher"}, "location.created", "location", id, out)}, nil
	})
}

func (s *Service) updateLocation(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator"); err != nil {
		return nil, err
	}
	latitude, longitude, err := coordinate(in)
	if err != nil {
		return nil, err
	}
	id := core.String(in["locationId"])
	return s.mutate(ctx, p, "update_location", "", in, func(state *mongostore.State) (txResult, error) {
		location := state.Locations[id]
		if location == nil {
			return txResult{}, notFound("location")
		}
		coverageKM, handlingMinutes, settingsErr := locationSettings(
			in,
			locationCoverageKM(location),
			location.HandlingMinutes,
		)
		if settingsErr != nil {
			return txResult{}, settingsErr
		}
		functions, _, functionsErr := parseLocationFunctions(in, location.Functions)
		if functionsErr != nil {
			return txResult{}, functionsErr
		}
		// An update may not strip a location down to no function either: a site
		// that provides nothing can be neither a collection point, a stop, nor a
		// destination, so it would silently disappear from every plan.
		if len(functions) == 0 {
			return txResult{}, core.Fail(
				400,
				"invalid_location_function",
				"a location must provide at least one of collection_point, transfer_point, hub, or laboratory",
			)
		}
		location.Name, location.Latitude, location.Longitude = core.String(in["name"]), latitude, longitude
		location.CoverageKM, location.HandlingMinutes = coverageKM, handlingMinutes
		location.Functions = functions
		location.UpdatedAt = core.Now()
		bumpNetworkRevision(state)
		out := locationMap(location)
		audit(state, p, "location", id, "location_updated", out)
		return txResult{body: out, eventID: emit(state, []string{"administrator", "dispatcher"}, "location.updated", "location", id, out)}, nil
	})
}

// applyLocationFunctionStatus enables or disables exactly one function of one
// location, leaving the site's activation and its other functions untouched. An
// unknown function name is a 400; a location that does not provide the named
// function at all is a 404.
func applyLocationFunctionStatus(
	state *mongostore.State,
	locationID, function string,
	active bool,
) (core.Map, error) {
	if !mongostore.IsLocationFunction(function) {
		return nil, core.Fail(
			400,
			"invalid_location_function",
			"function must be collection_point, transfer_point, hub, or laboratory",
		)
	}
	location := state.Locations[locationID]
	if location == nil {
		return nil, notFound("location")
	}
	if !location.Provides(function) {
		return nil, notFound("location function")
	}
	location.Functions[function] = active
	location.UpdatedAt = core.Now()
	return core.Map{
		"locationId": locationID, "function": function, "active": active,
		"functions": functionsView(location.Functions),
	}, nil
}

// setLocationFunctionStatus enables or disables one function of a location
// without touching the site itself or its other functions.
func (s *Service) setLocationFunctionStatus(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator"); err != nil {
		return nil, err
	}
	id := core.String(in["locationId"])
	function := strings.ToLower(strings.TrimSpace(core.String(in["function"])))
	if !mongostore.IsLocationFunction(function) {
		return nil, core.Fail(
			400,
			"invalid_location_function",
			"function must be collection_point, transfer_point, hub, or laboratory",
		)
	}
	active := core.Bool(in["active"])
	return s.mutate(ctx, p, "set_location_function_status", "", in, func(state *mongostore.State) (txResult, error) {
		out, err := applyLocationFunctionStatus(state, id, function, active)
		if err != nil {
			return txResult{}, err
		}
		bumpNetworkRevision(state)
		audit(state, p, "location", id, "location_function_status_changed", out)
		return txResult{body: out, eventID: emit(state, []string{"administrator", "dispatcher"}, "location.function_status_changed", "location", id, out)}, nil
	})
}

func locationSettings(in core.Map, currentCoverage float64, currentHandling int) (float64, int, error) {
	coverageKM, handlingMinutes := currentCoverage, currentHandling
	if _, present := in["coverageKm"]; present {
		coverageKM = core.Float(in["coverageKm"])
	}
	if _, present := in["handlingMinutes"]; present {
		handlingMinutes = core.Int(in["handlingMinutes"])
	}
	if coverageKM <= 0 || coverageKM > 5 {
		return 0, 0, core.Fail(400, "invalid_node_coverage", "coverageKm must be greater than zero and no more than 5 km")
	}
	if handlingMinutes < 0 || handlingMinutes > 1440 {
		return 0, 0, core.Fail(400, "invalid_handling_time", "handlingMinutes must be between 0 and 1440")
	}
	return coverageKM, handlingMinutes, nil
}

func locationCoverageKM(location *mongostore.Location) float64 {
	if location.CoverageKM > 0 {
		return location.CoverageKM
	}
	return 5
}

func (s *Service) setActive(ctx context.Context, p core.Principal, entity, id string, active bool) (core.Map, error) {
	if err := require(p, "administrator"); err != nil {
		return nil, err
	}
	in := core.Map{"id": id, "active": active}
	return s.mutate(ctx, p, "set_"+entity+"_status", "", in, func(state *mongostore.State) (txResult, error) {
		now, found := core.Now(), false
		switch entity {
		case "location":
			if item := state.Locations[id]; item != nil {
				item.Active, item.UpdatedAt, found = active, now, true
			}
		case "connection":
			if item := state.Connections[id]; item != nil {
				item.Active, item.UpdatedAt, found = active, now, true
			}
		default:
			return txResult{}, core.Fail(500, "configuration_error", "invalid active-state entity")
		}
		if !found {
			return txResult{}, notFound(entity)
		}
		bumpNetworkRevision(state)
		out := core.Map{entity + "Id": id, "active": active}
		audit(state, p, entity, id, entity+"_status_changed", out)
		return txResult{body: out, eventID: emit(state, []string{"administrator", "dispatcher"}, entity+".status_changed", entity, id, out)}, nil
	})
}

func (s *Service) createSampleType(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator"); err != nil {
		return nil, err
	}
	return s.writeSampleType(ctx, p, in, true)
}

func (s *Service) updateSampleType(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator"); err != nil {
		return nil, err
	}
	return s.writeSampleType(ctx, p, in, false)
}

func (s *Service) writeSampleType(ctx context.Context, p core.Principal, in core.Map, create bool) (core.Map, error) {
	id := core.String(in["sampleTypeId"])
	if create {
		id = core.UUID()
	}
	rules := sampleTypeRules{
		qualifiedCollectorRequired: core.Bool(in["qualifiedCollectorRequired"]),
		requiredQualification:      core.String(in["requiredQualification"]),
		containerType:              strings.TrimSpace(core.String(in["containerType"])),
		temperatureMinC:            core.Float(in["temperatureMinC"]),
		temperatureMaxC:            core.Float(in["temperatureMaxC"]),
		incompatibleSampleTypes:    uniqueStrings(core.Strings(in["incompatibleSampleTypes"])),
		requiresCooledRoute:        core.Bool(in["requiresCooledRoute"]),
		batchingAllowed:            true,
	}
	if value, ok := in["batchingAllowed"]; ok {
		rules.batchingAllowed = core.Bool(value)
	}
	if rules.temperatureMinC > rules.temperatureMaxC || rules.containerType == "" {
		return nil, core.Fail(400, "invalid_handling_rule", "container and valid temperature range are required")
	}
	name := strings.TrimSpace(core.String(in["name"]))
	operation := "update_sample_type"
	if create {
		operation = "create_sample_type"
	}
	return s.mutate(ctx, p, operation, "", in, func(state *mongostore.State) (txResult, error) {
		now := core.Now()
		if !create {
			current := state.SampleTypes[id]
			if current == nil {
				return txResult{}, notFound("sample type")
			}
			name = current.Name
		}
		rules = enforceNamedSamplePolicy(name, rules)
		if err := validateNamedSamplePolicy(name, rules); err != nil {
			return txResult{}, err
		}
		item := &mongostore.SampleType{
			ID: id, Name: name, QualifiedCollectorRequired: rules.qualifiedCollectorRequired,
			RequiredQualification: rules.requiredQualification, ContainerType: rules.containerType,
			TemperatureMinC: rules.temperatureMinC, TemperatureMaxC: rules.temperatureMaxC,
			IncompatibleSampleTypes: rules.incompatibleSampleTypes, RequiresCooledRoute: rules.requiresCooledRoute,
			BatchingAllowed: rules.batchingAllowed, CreatedAt: now, UpdatedAt: now,
		}
		if current := state.SampleTypes[id]; current != nil {
			item.CreatedAt = current.CreatedAt
		}
		state.SampleTypes[id] = item
		applyNamedCompatibilityRules(state, item)
		out := sampleTypeMap(item)
		action := "sample_type_updated"
		if create {
			action = "sample_type_created"
		}
		audit(state, p, "sample_type", id, action, out)
		return txResult{body: out, eventID: emit(state, []string{"administrator"}, "sample_type.changed", "sample_type", id, out)}, nil
	})
}

// namedSampleTypeSeeds is the CONFIGURABLE starting point (name, container,
// temperature range) for each of the four canonical sample-type profiles that
// must exist immediately after reset (instruction.md; AC-101). The
// non-configurable safety overlay -- qualifiedCollectorRequired,
// requiredQualification, requiresCooledRoute, batchingAllowed -- is not set
// here at all: enforceNamedSamplePolicy forces it from the name alone, the
// same way it would for anything an administrator submitted under these
// names, so there is nothing for this seed data to get out of sync with.
var namedSampleTypeSeeds = []core.Map{
	{"name": "Amber", "containerType": "standard_vial", "temperatureMinC": 15.0, "temperatureMaxC": 25.0},
	{"name": "Indigo", "containerType": "standard_vial", "temperatureMinC": 15.0, "temperatureMaxC": 25.0},
	// Frost's container must independently pass validateNamedSamplePolicy's
	// own cooling-capability check (it looks for "cooled"/"refrigerated"/
	// "cryo" in the container text), or seeding would fail the same way a
	// real administrator's bad request would.
	{"name": "Frost", "containerType": "cooled_vial", "temperatureMinC": -20.0, "temperatureMaxC": -10.0},
	{"name": "Pulse", "containerType": "pulse_vial", "temperatureMinC": 2.0, "temperatureMaxC": 8.0},
}

// EnsureNamedSampleTypesSeeded creates whichever of the four canonical named
// sample types (Amber, Indigo, Frost, Pulse) do not already exist (matched
// case-insensitively on the trimmed name, exactly as enforceNamedSamplePolicy
// itself matches), so they are available with zero administrator action
// immediately after reset. Mirrors EnsureInitialAdmin: safe -- and a no-op --
// to call on every startup. An administrator who has already created one of
// these names (at an earlier boot, or by hand) is never touched again.
//
// Each missing type is created through createSampleType -- the exact path an
// administrator hitting POST /api/create-sample-type would take -- so the
// seeded record can never drift from what that endpoint actually produces for
// the same name; there is no separate, hand-rolled insertion to keep in sync.
func (s *Service) EnsureNamedSampleTypesSeeded(ctx context.Context) error {
	existingNames := map[string]bool{}
	if err := s.Store.View(ctx, func(state *mongostore.State) error {
		for _, item := range state.SampleTypes {
			existingNames[strings.ToLower(strings.TrimSpace(item.Name))] = true
		}
		return nil
	}); err != nil {
		return err
	}
	system := core.Principal{Roles: []string{"administrator"}}
	for _, seed := range namedSampleTypeSeeds {
		name := strings.ToLower(strings.TrimSpace(core.String(seed["name"])))
		if existingNames[name] {
			continue
		}
		if _, err := s.createSampleType(ctx, system, seed); err != nil {
			return fmt.Errorf("seed named sample type %q: %w", seed["name"], err)
		}
	}
	return nil
}

type sampleTypeRules struct {
	qualifiedCollectorRequired bool
	requiredQualification      string
	containerType              string
	temperatureMinC            float64
	temperatureMaxC            float64
	incompatibleSampleTypes    []string
	requiresCooledRoute        bool
	batchingAllowed            bool
}

// enforceNamedSamplePolicy applies the non-configurable safety rules of the
// canonical sample profiles. Configurable container and temperature values are
// retained when safe and are enforced for every sample at request and handling
// time.
func enforceNamedSamplePolicy(name string, rules sampleTypeRules) sampleTypeRules {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "amber":
		rules.qualifiedCollectorRequired, rules.requiredQualification = true, "LX-A"
	case "indigo":
		rules.qualifiedCollectorRequired, rules.requiredQualification = true, "LX-I"
	case "frost":
		rules.qualifiedCollectorRequired = true
		rules.requiredQualification = "LX-F"
		rules.requiresCooledRoute = true
	case "pulse":
		rules.qualifiedCollectorRequired = true
		rules.requiredQualification = "LX-P"
		rules.batchingAllowed = false
	}
	return rules
}

func validateNamedSamplePolicy(name string, rules sampleTypeRules) error {
	if strings.EqualFold(strings.TrimSpace(name), "Frost") &&
		!isCoolingCapableContainer(rules.containerType) {
		return core.Fail(
			400,
			"invalid_handling_rule",
			"Frost requires a cooled, refrigerated, or cryogenic container",
		)
	}
	return nil
}

func isCoolingCapableContainer(containerType string) bool {
	normalized := strings.ToLower(strings.TrimSpace(containerType))
	return strings.Contains(normalized, "cooled") ||
		strings.Contains(normalized, "refrigerated") ||
		strings.Contains(normalized, "cryo")
}

func sampleTypeMap(item *mongostore.SampleType) core.Map {
	return core.Map{
		"sampleTypeId": item.ID, "name": item.Name,
		"qualifiedCollectorRequired": item.QualifiedCollectorRequired,
		"requiredQualification":      item.RequiredQualification,
		"containerType":              item.ContainerType,
		"temperatureMinC":            item.TemperatureMinC,
		"temperatureMaxC":            item.TemperatureMaxC,
		"incompatibleSampleTypes":    append([]string{}, item.IncompatibleSampleTypes...),
		"requiresCooledRoute":        item.RequiresCooledRoute,
		"batchingAllowed":            item.BatchingAllowed,
	}
}

func applyNamedCompatibilityRules(state *mongostore.State, _ *mongostore.SampleType) {
	ambers, indigos := []*mongostore.SampleType{}, []*mongostore.SampleType{}
	for _, item := range state.SampleTypes {
		switch strings.ToLower(strings.TrimSpace(item.Name)) {
		case "amber":
			ambers = append(ambers, item)
		case "indigo":
			indigos = append(indigos, item)
		}
	}
	now := core.Now()
	for _, amber := range ambers {
		for _, indigo := range indigos {
			amber.IncompatibleSampleTypes = uniqueStrings(append(amber.IncompatibleSampleTypes, indigo.ID))
			indigo.IncompatibleSampleTypes = uniqueStrings(append(indigo.IncompatibleSampleTypes, amber.ID))
			amber.UpdatedAt, indigo.UpdatedAt = now, now
		}
	}
}

func (s *Service) configureAcceptance(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator"); err != nil {
		return nil, err
	}
	laboratoryID, sampleTypeID := core.String(in["laboratoryId"]), core.String(in["sampleTypeId"])
	return s.mutate(ctx, p, "configure_laboratory_acceptance", "", in, func(state *mongostore.State) (txResult, error) {
		// laboratoryId now identifies a location; it must exist and provide the
		// laboratory function before sample-type capability can be configured.
		location := state.Locations[laboratoryID]
		if location == nil || state.SampleTypes[sampleTypeID] == nil {
			return txResult{}, core.Fail(400, "invalid_acceptance", "laboratory or sample type does not exist")
		}
		if !location.Provides(mongostore.FunctionLaboratory) {
			return txResult{}, core.Fail(400, "invalid_acceptance", "location does not provide the laboratory function")
		}
		key := laboratoryID + "|" + sampleTypeID
		item := state.Acceptances[key]
		if item == nil {
			item = &mongostore.Acceptance{ID: core.UUID(), LaboratoryID: laboratoryID, SampleTypeID: sampleTypeID}
			state.Acceptances[key] = item
		}
		item.Accepts, item.UpdatedAt = core.Bool(in["accepts"]), core.Now()
		bumpNetworkRevision(state)
		out := core.Map{"acceptanceId": item.ID, "accepts": item.Accepts}
		audit(state, p, "laboratory", laboratoryID, "laboratory_acceptance_changed", core.Map{"sampleTypeId": sampleTypeID, "accepts": item.Accepts})
		return txResult{body: out, eventID: emit(state, []string{"administrator", "dispatcher"}, "laboratory.acceptance_changed", "laboratory", laboratoryID, out)}, nil
	})
}

func (s *Service) createConnection(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator"); err != nil {
		return nil, err
	}
	travelMinutes, costUnits := core.Int(in["travelMinutes"]), core.Float(in["costUnits"])
	if travelMinutes <= 0 || costUnits < 0 {
		return nil, core.Fail(400, "invalid_connection", "travelMinutes must be positive and costUnits nonnegative")
	}
	id, from, to := core.UUID(), core.String(in["fromLocationId"]), core.String(in["toLocationId"])
	if from == to {
		return nil, core.Fail(400, "invalid_connection", "connection endpoints must differ")
	}
	out := core.Map{
		"connectionId": id, "fromLocationId": from, "toLocationId": to,
		"travelMinutes": travelMinutes, "costUnits": costUnits,
		"cooled": core.Bool(in["cooled"]), "active": core.Bool(in["active"]),
	}
	return s.mutate(ctx, p, "create_connection", "", in, func(state *mongostore.State) (txResult, error) {
		if !locationExists(state, from) || !locationExists(state, to) {
			return txResult{}, core.Fail(400, "invalid_connection", "both connection locations must exist")
		}
		now := core.Now()
		state.Connections[id] = &mongostore.Connection{
			ID: id, FromLocationID: from, ToLocationID: to,
			TravelMinutes: travelMinutes, CostUnits: costUnits,
			Cooled: core.Bool(in["cooled"]), Active: core.Bool(in["active"]),
			CreatedAt: now, UpdatedAt: now,
		}
		bumpNetworkRevision(state)
		audit(state, p, "connection", id, "connection_created", out)
		return txResult{body: out, eventID: emit(state, []string{"administrator", "dispatcher"}, "connection.created", "connection", id, out)}, nil
	})
}

func locationExists(state *mongostore.State, id string) bool {
	return state.Locations[id] != nil
}

func (s *Service) configureCapacity(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator", "node_handler", "laboratory_team"); err != nil {
		return nil, err
	}
	resourceID, units := core.String(in["resourceId"]), core.Int(in["capacityUnits"])
	cooledUnits := core.Int(in["cooledCapacityUnits"])
	if units < 0 || cooledUnits < 0 || cooledUnits > units {
		return nil, core.Fail(400, "invalid_capacity", "capacity must be nonnegative and cooled capacity cannot exceed total capacity")
	}
	return s.mutate(ctx, p, "configure_capacity", "", in, func(state *mongostore.State) (txResult, error) {
		// A node handler or laboratory operator may configure only their own
		// assigned location's capacity. An administrator stays unscoped: any
		// node, laboratory, or transport connection.
		if !p.Has("administrator") {
			caller := state.Users[p.ID]
			if caller == nil || caller.AssignedLocationID == "" || caller.AssignedLocationID != resourceID {
				return txResult{}, core.Fail(403, "forbidden", "capacity may only be configured at the caller's own assigned location")
			}
		}
		capacity := state.Capacities[resourceID]
		if capacity == nil {
			capacity = &mongostore.Capacity{ResourceID: resourceID}
			state.Capacities[resourceID] = capacity
		}
		if capacity.ReservedUnits > units || capacity.CooledReserved > cooledUnits {
			return txResult{}, core.Fail(409, "capacity_reserved", "capacity cannot be reduced below existing reservations")
		}
		capacity.CapacityUnits, capacity.CooledUnits, capacity.UpdatedAt = units, cooledUnits, core.Now()
		bumpNetworkRevision(state)
		out := core.Map{"resourceId": resourceID, "capacityUnits": units, "cooledCapacityUnits": cooledUnits}
		audit(state, p, "capacity", resourceID, "capacity_configured", out)
		return txResult{body: out, eventID: emit(state, []string{"administrator", "dispatcher", "node_handler", "laboratory_team"}, "capacity.changed", "capacity", resourceID, out)}, nil
	})
}

func (s *Service) setWorkerAvailability(ctx context.Context, p core.Principal, in core.Map) (core.Map, error) {
	if err := require(p, "administrator", "dispatcher"); err != nil {
		return nil, err
	}
	workerID, units := core.String(in["workerId"]), core.Int(in["capacityUnits"])
	if units < 0 {
		return nil, core.Fail(400, "invalid_capacity", "capacity cannot be negative")
	}
	return s.mutate(ctx, p, "set_worker_availability", "", in, func(state *mongostore.State) (txResult, error) {
		worker := state.Users[workerID]
		if worker == nil {
			return txResult{}, notFound("worker")
		}
		activeWork := activeWorkerAssignments(state, workerID)
		if units < activeWork {
			return txResult{}, core.Fail(409, "capacity_reserved", "capacity cannot be reduced below active assignments")
		}
		worker.Available, worker.CapacityUnits, worker.UpdatedAt = core.Bool(in["available"]), units, core.Now()
		out := core.Map{"workerId": workerID, "available": worker.Available, "capacityUnits": units}
		audit(state, p, "user", workerID, "worker_availability_changed", out)
		return txResult{body: out, eventID: emit(state, []string{"administrator", "dispatcher", workerID}, "worker.availability_changed", "user", workerID, out)}, nil
	})
}
