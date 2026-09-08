package core

import (
	"math"
	"testing"
)

func TestDistanceBoundary(t *testing.T) {
	origin := Map{"latitude": 12.9716, "longitude": 77.5946}
	// One degree latitude is approximately 111.195 km on the same sphere used by DistanceKM.
	fiveKMNorth := Map{"latitude": 12.9716 + 5.0/111.1950802335329, "longitude": 77.5946}
	got := DistanceKM(origin, fiveKMNorth)
	if math.Abs(got-5.0) > 0.00001 {
		t.Fatalf("distance=%f, want 5km", got)
	}
}

// Authorization follows the role a user selected, not the union of the roles
// they hold. This replaces the earlier union behaviour, which let any held role
// authorize an action regardless of the role the user was working under.
func TestPrincipalAuthorizesOnlyTheSelectedRole(t *testing.T) {
	p := Principal{
		Roles:        []string{"requester", "delivery_partner"},
		SelectedRole: "delivery_partner",
	}
	if !p.Has("administrator", "delivery_partner") {
		t.Fatal("selected role did not authorize an action it permits")
	}
	if p.Has("requester") {
		t.Fatal("a held but unselected role authorized an action")
	}
	if p.Has("administrator", "dispatcher") {
		t.Fatal("principal matched a role it does not have")
	}
	if !p.Holds("requester") || !p.Holds("delivery_partner") {
		t.Fatalf("selection changed the roles the account holds: %v", p.Roles)
	}

	unselected := Principal{Roles: []string{"requester", "delivery_partner"}}
	if unselected.Has("requester") || unselected.Has("delivery_partner") {
		t.Fatal("a multi-role principal authorized an action without selecting a role")
	}
	if !unselected.RoleSelectionRequired() || unselected.ActiveRole() != "" {
		t.Fatal("a multi-role principal was not asked to select a role")
	}
	if unselected.AuditRole() != RoleUnselected {
		t.Fatalf("audit role = %q, want %q", unselected.AuditRole(), RoleUnselected)
	}

	single := Principal{Roles: []string{"dispatcher"}}
	if !single.Has("dispatcher") || single.ActiveRole() != "dispatcher" {
		t.Fatal("a single-role principal was not auto-selected into its only role")
	}
	if single.RoleSelectionRequired() {
		t.Fatal("a single-role principal was asked to select a role")
	}

	stale := Principal{Roles: []string{"requester"}, SelectedRole: "administrator"}
	if stale.Has("administrator") || stale.ActiveRole() != "requester" {
		t.Fatal("a selection for a role the account no longer holds was honoured")
	}
}

func TestUUIDShapeAndUniqueness(t *testing.T) {
	a, b := UUID(), UUID()
	if len(a) != 36 || a[14] != '4' {
		t.Fatalf("unexpected UUID v4 %q", a)
	}
	if a == b {
		t.Fatal("two generated UUIDs matched")
	}
}
