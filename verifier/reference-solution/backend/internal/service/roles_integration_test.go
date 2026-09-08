package service

import (
	"context"
	"testing"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

// testSession is one signed-in browser session. The selected role lives on the
// session, so a test must resolve a fresh principal after switching roles.
type testSession struct {
	token string
	login core.Map
}

func signIn(t *testing.T, service *Service, username string) testSession {
	t.Helper()
	login, err := service.Store.Login(context.Background(), username, "password-123456")
	if err != nil {
		t.Fatalf("sign in %q: %v", username, err)
	}
	return testSession{token: core.String(login["sessionToken"]), login: login}
}

func (session testSession) principal(t *testing.T, service *Service) core.Principal {
	t.Helper()
	principal, err := service.Store.Principal(context.Background(), session.token)
	if err != nil {
		t.Fatalf("resolve principal: %v", err)
	}
	return principal
}

func (session testSession) selectRole(t *testing.T, service *Service, role string) (core.Map, error) {
	t.Helper()
	return service.Handle(
		context.Background(),
		"select_active_role",
		core.Map{"role": role},
		session.principal(t, service),
	)
}

// AC-5, AC-6: a user holding dispatcher and requester acts under exactly one of
// them. Selecting requester refuses dispatcher-only work; selecting dispatcher
// permits it. The account's roles never change.
func TestSelectedRoleAuthorizesInsteadOfTheUnionOfHeldRoles(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	createTestUser(t, service, admin, "dual-role", []string{"dispatcher", "requester"}, nil)
	session := signIn(t, service, "dual-role")

	if selected, err := session.selectRole(t, service, "requester"); err != nil {
		t.Fatalf("select requester: %v", err)
	} else if selected["activeRole"] != "requester" {
		t.Fatalf("selection result = %v", selected)
	}
	_, err := service.Handle(ctx, "read_operations_state", core.Map{}, session.principal(t, service))
	assertHTTPErrorStatus(t, err, 403)
	assertHTTPErrorCode(t, err, "forbidden")

	if _, err := session.selectRole(t, service, "dispatcher"); err != nil {
		t.Fatalf("select dispatcher: %v", err)
	}
	if _, err := service.Handle(
		ctx, "read_operations_state", core.Map{}, session.principal(t, service),
	); err != nil {
		t.Fatalf("selected dispatcher was refused dispatcher work: %v", err)
	}
}

// AC-5: selecting a role the account does not hold is refused, and the session
// keeps whatever role it was already working under.
func TestSelectingARoleTheAccountDoesNotHoldIsRefused(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	createTestUser(t, service, admin, "not-a-dispatcher", []string{"requester", "node_handler"}, nil)
	session := signIn(t, service, "not-a-dispatcher")
	if _, err := session.selectRole(t, service, "requester"); err != nil {
		t.Fatal(err)
	}

	_, err := session.selectRole(t, service, "dispatcher")
	assertHTTPErrorStatus(t, err, 403)
	assertHTTPErrorCode(t, err, "role_not_held")

	if got := session.principal(t, service).ActiveRole(); got != "requester" {
		t.Fatalf("active role after a refused selection = %q, want requester", got)
	}
	_, err = session.selectRole(t, service, "administrator")
	assertHTTPErrorCode(t, err, "role_not_held")

	// A refused role selection is audited against the account it was attempted on.
	principal := session.principal(t, service)
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		for _, event := range state.AuditEvents {
			if event.Action != "permission_denied" ||
				core.String(event.Details["operation"]) != "select_active_role" {
				continue
			}
			if event.Success ||
				event.EntityType != "user" ||
				event.EntityID != principal.ID ||
				core.String(event.Details["code"]) != "role_not_held" {
				t.Fatalf("refused role selection audit = %+v", event)
			}
			return nil
		}
		t.Fatal("a refused role selection was not recorded in the audit trail")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-7: switching role context must not add or remove any role on the account.
func TestRoleSwitchingNeverChangesTheAccountRoles(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	held := []string{"requester", "dispatcher", "laboratory_team"}
	user := createTestUser(t, service, admin, "role-switcher", held, nil)
	session := signIn(t, service, "role-switcher")

	before, err := service.readUser(ctx, admin, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range held {
		if _, err := session.selectRole(t, service, role); err != nil {
			t.Fatalf("select %q: %v", role, err)
		}
		after, readErr := service.readUser(ctx, admin, user.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		beforeRoles, afterRoles := core.Strings(before["roles"]), core.Strings(after["roles"])
		if len(beforeRoles) != len(afterRoles) {
			t.Fatalf("selecting %q changed the account roles: %v -> %v", role, beforeRoles, afterRoles)
		}
		for index := range beforeRoles {
			if beforeRoles[index] != afterRoles[index] {
				t.Fatalf(
					"selecting %q changed the account roles: %v -> %v",
					role, beforeRoles, afterRoles,
				)
			}
		}
	}
	// The user may also read their own record while acting under any of the roles.
	self, err := service.Handle(ctx, "read_user", core.Map{"userId": user.ID}, session.principal(t, service))
	if err != nil {
		t.Fatal(err)
	}
	if len(core.Strings(self["roles"])) != len(held) {
		t.Fatalf("self-read roles = %v, want %v", self["roles"], held)
	}
}

// AC-5: a multi-role account has no active role until it selects one, so every
// role-scoped action is refused with a clear, distinct outcome.
func TestMultiRoleUserMustSelectARoleBeforeActing(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	createTestUser(t, service, admin, "undecided", []string{"dispatcher", "administrator"}, nil)
	session := signIn(t, service, "undecided")

	if session.login["activeRole"] != "" {
		t.Fatalf("multi-role sign-in pre-selected a role: %v", session.login)
	}
	if len(core.Strings(session.login["roles"])) != 2 {
		t.Fatalf("sign-in roles = %v", session.login["roles"])
	}
	principal := session.principal(t, service)
	if !principal.RoleSelectionRequired() {
		t.Fatal("multi-role principal did not require a role selection")
	}
	for _, operation := range []string{
		"read_operations_state", "read_serviceability_snapshot", "search_audit_trail",
	} {
		_, err := service.Handle(ctx, operation, core.Map{}, principal)
		assertHTTPErrorStatus(t, err, 409)
		assertHTTPErrorCode(t, err, "role_not_selected")
	}
	_, err := service.Handle(ctx, "list_role_work", core.Map{"role": "dispatcher"}, principal)
	assertHTTPErrorCode(t, err, "role_not_selected")

	if _, err := session.selectRole(t, service, "dispatcher"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Handle(
		ctx, "read_operations_state", core.Map{}, session.principal(t, service),
	); err != nil {
		t.Fatalf("dispatcher work refused after selecting dispatcher: %v", err)
	}
}

// AC-5: holding exactly one role needs no explicit selection, so no existing
// single-role client has to change to keep working.
func TestSingleRoleUserIsAutoSelectedAtSignIn(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	createTestUser(t, service, admin, "only-dispatcher", []string{"dispatcher"}, nil)
	session := signIn(t, service, "only-dispatcher")

	if session.login["activeRole"] != "dispatcher" {
		t.Fatalf("single-role sign-in activeRole = %v, want dispatcher", session.login["activeRole"])
	}
	principal := session.principal(t, service)
	if principal.RoleSelectionRequired() || principal.ActiveRole() != "dispatcher" {
		t.Fatalf("single-role principal = %+v", principal)
	}
	if _, err := service.Handle(ctx, "read_operations_state", core.Map{}, principal); err != nil {
		t.Fatalf("auto-selected dispatcher was refused: %v", err)
	}
	bootstrapped, err := service.Bootstrap(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	if core.Object(bootstrapped["user"])["activeRole"] != "dispatcher" {
		t.Fatalf("bootstrap user = %v", bootstrapped["user"])
	}
}

// AC-6: there is no blanket administrator bypass. An administrator working as a
// requester is refused administrator-only work and administrator work views.
func TestAdministratorActingAsRequesterHasNoBypass(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	createTestUser(t, service, admin, "admin-and-requester", []string{"administrator", "requester"}, nil)
	session := signIn(t, service, "admin-and-requester")
	if _, err := session.selectRole(t, service, "requester"); err != nil {
		t.Fatal(err)
	}
	principal := session.principal(t, service)

	_, err := service.Handle(ctx, "create_user", core.Map{
		"username": "smuggled", "initialPassword": "password-123456",
		"roles": []string{"administrator"}, "active": true,
	}, principal)
	assertHTTPErrorStatus(t, err, 403)

	_, err = service.Handle(ctx, "read_operations_state", core.Map{}, principal)
	assertHTTPErrorStatus(t, err, 403)

	// AC-3/AC-6: work is listed for the selected role only, administrators
	// included, so an administrator acting as a requester cannot list a
	// dispatcher's or an administrator's work.
	for _, role := range []string{"administrator", "dispatcher", "delivery_partner"} {
		_, err = service.Handle(ctx, "list_role_work", core.Map{"role": role}, principal)
		assertHTTPErrorStatus(t, err, 403)
	}
	if _, err := service.Handle(
		ctx, "list_role_work", core.Map{"role": "requester"}, principal,
	); err != nil {
		t.Fatalf("requester work refused to a selected requester: %v", err)
	}

	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		for _, user := range state.Users {
			if user.Username != "admin-and-requester" {
				continue
			}
			if len(user.Roles) != 2 ||
				!stringSliceContains(user.Roles, "administrator") ||
				!stringSliceContains(user.Roles, "requester") {
				t.Fatalf("stored roles = %v", user.Roles)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// streamedEvents is the realtime feed as one signed-in session would receive it.
// The websocket loop and the catch-up read share Store.EventsSince, so what this
// returns is exactly what that session's socket would be sent.
func streamedEvents(t *testing.T, service *Service, session testSession) []core.Map {
	t.Helper()
	events, err := service.Store.EventsSince(
		context.Background(), 0, 200, session.principal(t, service),
	)
	if err != nil {
		t.Fatalf("read realtime events: %v", err)
	}
	return events
}

func streamedEvent(events []core.Map, eventType, entityID string) core.Map {
	for _, event := range events {
		if core.String(event["type"]) == eventType && core.String(event["entityId"]) == entityID {
			return event
		}
	}
	return nil
}

// AC-6: the realtime event stream is a role-scoped surface, so its audience
// follows the SELECTED role and not the union of held roles. A user holding
// dispatcher and requester receives a dispatcher-audience event only while acting
// as dispatcher; the same event is withheld while they act as requester.
//
// An event addressed to the user personally is unaffected, because it is
// addressed to the account rather than to a role.
func TestRealtimeEventAudienceFollowsTheSelectedRole(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	user := createTestUser(
		t, service, admin, "dispatcher-and-requester",
		[]string{"dispatcher", "requester"}, nil,
	)
	session := signIn(t, service, "dispatcher-and-requester")

	// A configuration change published to administrators and dispatchers.
	location, err := service.createLocation(ctx, admin, core.Map{
		"name": "Audience site", "active": true,
		"coordinate": core.Map{"latitude": 12.9716, "longitude": 77.5946},
		"functions":  []string{mongostore.FunctionCollectionPoint},
	})
	if err != nil {
		t.Fatalf("create location: %v", err)
	}
	locationID := core.String(location["locationId"])

	// A multi-role account has no active role until it selects one, so a
	// role-addressed event reaches it under no role at all.
	if streamedEvent(streamedEvents(t, service, session), "location.created", locationID) != nil {
		t.Fatal("a dispatcher-audience event reached an account with no selected role")
	}

	if _, err := session.selectRole(t, service, "requester"); err != nil {
		t.Fatalf("select requester: %v", err)
	}
	if streamedEvent(streamedEvents(t, service, session), "location.created", locationID) != nil {
		t.Fatal("a dispatcher-audience event reached a user acting as a requester")
	}

	if _, err := session.selectRole(t, service, "dispatcher"); err != nil {
		t.Fatalf("select dispatcher: %v", err)
	}
	delivered := streamedEvent(streamedEvents(t, service, session), "location.created", locationID)
	if delivered == nil {
		t.Fatal("a dispatcher-audience event did not reach a user acting as a dispatcher")
	}
	if core.String(delivered["entityType"]) != "location" {
		t.Fatalf("delivered event = %v", delivered)
	}

	// Switching back withholds it again: the audience follows the selection each
	// time it is read, rather than being decided once at sign-in.
	if _, err := session.selectRole(t, service, "requester"); err != nil {
		t.Fatalf("reselect requester: %v", err)
	}
	if streamedEvent(streamedEvents(t, service, session), "location.created", locationID) != nil {
		t.Fatal("a dispatcher-audience event survived a switch back to requester")
	}

	// An event addressed to the account itself is delivered whatever role the
	// account is working under.
	if _, err := service.Handle(ctx, "update_user_access", core.Map{
		"userId": user.ID, "roles": []string{"dispatcher", "requester"},
		"qualifications": []string{}, "active": true,
	}, admin); err != nil {
		t.Fatalf("update user access: %v", err)
	}
	if streamedEvent(
		streamedEvents(t, service, session), "user.access_updated", user.ID,
	) == nil {
		t.Fatal("an event addressed to the account was withheld from the account")
	}
}
