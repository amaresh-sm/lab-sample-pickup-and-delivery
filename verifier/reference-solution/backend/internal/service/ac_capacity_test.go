package service

import (
	"context"
	"testing"

	"labgolden/internal/core"
)

// capacityKeys is the complete set of numbers AC-46 requires: configured,
// reserved and available, separately for regular and cooled.
var capacityKeys = []string{
	"capacityUnits", "reservedUnits", "availableUnits",
	"cooledCapacityUnits", "cooledReservedUnits", "cooledAvailableUnits",
}

// operationsCapacityFor finds the capacity block a resource is reported with in
// read_operations_state, whichever array of the operations view carries it. It
// returns nil when the resource is not reported with capacity anywhere.
func operationsCapacityFor(operations core.Map, resourceID string) core.Map {
	identityKeys := []string{"id", "locationId", "connectionId", "resourceId", "routeId"}
	for _, value := range operations {
		items, ok := value.([]core.Map)
		if !ok {
			continue
		}
		for _, item := range items {
			matched := false
			for _, key := range identityKeys {
				if core.String(item[key]) == resourceID {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			if capacity, ok := item["capacity"].(core.Map); ok {
				return capacity
			}
		}
	}
	return nil
}

func assertCapacityNumbers(t *testing.T, label string, capacity core.Map, want map[string]int) {
	t.Helper()
	if capacity == nil {
		t.Fatalf("%s reports no capacity block at all", label)
	}
	for _, key := range capacityKeys {
		if _, present := capacity[key]; !present {
			t.Fatalf("%s capacity is missing %q: %v", label, key, capacity)
		}
	}
	for key, expected := range want {
		if got := core.Int(capacity[key]); got != expected {
			t.Fatalf("%s capacity %s = %d, want %d (whole block: %v)", label, key, got, expected, capacity)
		}
	}
}

// AC-45, AC-46: regular and cooled capacity are configured and reported as two
// independent dimensions, with configured, reserved and available numbers for
// each — six numbers per resource — and only the roles that run operations may
// read them.
func TestAC45AC46BothCapacityDimensionsAreReadableAndRoleScoped(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "ac45-requester", []string{"requester"}, nil)
	courier := createTestUser(t, service, admin, "ac45-courier", []string{"delivery_partner"}, nil)
	dispatcher := createTestUser(t, service, admin, "ac45-dispatcher", []string{"dispatcher"}, nil)
	createTestUser(t, service, admin, "ac45-collector", []string{"collection_professional"}, []string{"LX-A"})
	network := buildServiceableNetwork(t, service, admin, "ac45")

	// A location and a connection are given deliberately different regular and
	// cooled configurations, so a view that conflated the two dimensions, or the
	// two resource kinds, could not pass.
	for _, configuration := range []struct {
		resourceID    string
		regular       int
		cooled        int
		resourceLabel string
	}{
		{network.nodeID, 12, 5, "collection point"},
		{network.connectionID, 8, 3, "connection"},
	} {
		if _, err := service.configureCapacity(ctx, admin, core.Map{
			"resourceId": configuration.resourceID, "capacityUnits": configuration.regular,
			"cooledCapacityUnits": configuration.cooled,
		}); err != nil {
			t.Fatalf("configure %s capacity: %v", configuration.resourceLabel, err)
		}
	}

	// One journey reserves one regular unit at every resource on its route, so
	// reserved and available are not both trivially zero and full.
	if _, err := service.createCollectionRequest(
		ctx, requester, standardRequest(network, "ac45-request"),
	); err != nil {
		t.Fatal(err)
	}

	operations, err := service.Handle(ctx, "read_operations_state", core.Map{}, admin)
	if err != nil {
		t.Fatal(err)
	}

	// The location's six numbers, exactly.
	assertCapacityNumbers(t, "collection point", operationsCapacityFor(operations, network.nodeID), map[string]int{
		"capacityUnits": 12, "reservedUnits": 1, "availableUnits": 11,
		"cooledCapacityUnits": 5, "cooledReservedUnits": 0, "cooledAvailableUnits": 5,
	})
	// The laboratory is a second location on the same route, still at the
	// fixture's 10/10 configuration.
	assertCapacityNumbers(t, "laboratory", operationsCapacityFor(operations, network.laboratoryID), map[string]int{
		"capacityUnits": 10, "reservedUnits": 1, "availableUnits": 9,
		"cooledCapacityUnits": 10, "cooledReservedUnits": 0, "cooledAvailableUnits": 10,
	})

	// Changing only the cooled dimension must move only the cooled numbers.
	if _, err := service.configureCapacity(ctx, admin, core.Map{
		"resourceId": network.nodeID, "capacityUnits": 12, "cooledCapacityUnits": 9,
	}); err != nil {
		t.Fatal(err)
	}
	operations, err = service.Handle(ctx, "read_operations_state", core.Map{}, dispatcher)
	if err != nil {
		t.Fatalf("dispatcher read of operations state: %v", err)
	}
	assertCapacityNumbers(t, "collection point after a cooled-only change",
		operationsCapacityFor(operations, network.nodeID), map[string]int{
			"capacityUnits": 12, "reservedUnits": 1, "availableUnits": 11,
			"cooledCapacityUnits": 9, "cooledReservedUnits": 0, "cooledAvailableUnits": 9,
		})

	// Roles that do not run operations cannot read capacity at all.
	for _, outsider := range []core.Principal{requester, courier} {
		out, err := service.Handle(ctx, "read_operations_state", core.Map{}, outsider)
		assertHTTPErrorStatus(t, err, 403)
		assertHTTPErrorCode(t, err, "forbidden")
		if out != nil {
			t.Fatalf("a refused operations read still returned data to %v", outsider.Roles)
		}
	}

	// AC-45 says connections carry regular and cooled capacity of their own, and
	// AC-46 says an authorized user can read configured, reserved and available
	// for both dimensions. The connection's configuration above was accepted and
	// the journey reserved a unit on it, so its six numbers must be readable.
	//
	// This was a confirmed defect: read_operations_state had no connections array,
	// so capacityMap was only ever called for a location and the six numbers
	// configured against a connection were write-only. read_operations_state now
	// reports a connections array carrying the same capacity block, and
	// app-setup/openapi.json documents it as OperationsConnection. The message
	// below is kept as the diagnostic should that array ever be dropped again.
	connectionCapacity := operationsCapacityFor(operations, network.connectionID)
	if connectionCapacity == nil {
		t.Fatalf(
			"connection %s was configured with 8 regular / 3 cooled units and holds 1 reserved unit, "+
				"but no operation reports its capacity: read_operations_state exposes capacity for "+
				"locations only and has no connections array (internal/service/views.go:728-813). "+
				"AC-46's six numbers are unreadable for connections.",
			network.connectionID,
		)
	}
	assertCapacityNumbers(t, "connection", connectionCapacity, map[string]int{
		"capacityUnits": 8, "reservedUnits": 1, "availableUnits": 7,
		"cooledCapacityUnits": 3, "cooledReservedUnits": 0, "cooledAvailableUnits": 3,
	})
}
