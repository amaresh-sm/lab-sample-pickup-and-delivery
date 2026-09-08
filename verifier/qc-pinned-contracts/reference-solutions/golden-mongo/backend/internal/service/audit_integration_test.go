package service

import (
	"context"
	"testing"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
)

func auditEvents(t *testing.T, service *Service) []*mongostore.AuditEvent {
	t.Helper()
	var events []*mongostore.AuditEvent
	if err := service.Store.View(context.Background(), func(state *mongostore.State) error {
		for _, event := range state.AuditEvents {
			copied := *event
			events = append(events, &copied)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return events
}

// searchAuditAs runs the searchable audit trail the way a caller would.
func searchAuditAs(t *testing.T, service *Service, p core.Principal, filters core.Map) []core.Map {
	t.Helper()
	out, err := service.Handle(context.Background(), "search_audit_trail", filters, p)
	if err != nil {
		t.Fatalf("search audit trail: %v", err)
	}
	entries, ok := out["entries"].([]core.Map)
	if !ok {
		t.Fatalf("audit entries = %#v", out["entries"])
	}
	return entries
}

func hasAuditAction(events []*mongostore.AuditEvent, action string) *mongostore.AuditEvent {
	for _, event := range events {
		if event.Action == action {
			return event
		}
	}
	return nil
}

// AC-72: a rejected action is recorded with a failure outcome, and rejecting it
// leaves nothing else behind. The failing command rolls its whole transaction
// back, so the rejection audit is written afterwards in its own transaction.
func TestRejectedActionIsAuditedWithAFailureOutcomeAndLeavesNoOtherState(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "denied-requester", []string{"requester"}, nil)
	network := buildServiceableNetwork(t, service, admin, "denial")

	before := captureStateFingerprint(t, service)
	// A requester may not edit an active plan: dispatcher-only work.
	_, err := service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": "journey-that-does-not-matter", "expectedPlanVersion": 1,
		"changeType": "cancel_work", "changes": core.Map{},
		"idempotencyKey": "denied-plan-edit",
	}, requester)
	assertHTTPErrorStatus(t, err, 403)
	after := captureStateFingerprint(t, service)

	if after.auditEvents != before.auditEvents+1 {
		t.Fatalf("audit records = %d, want exactly one more than %d", after.auditEvents, before.auditEvents)
	}
	// Everything except the audit trail and the state version it advanced must be
	// byte-for-byte unchanged: no journey, no idempotency record, no reservation.
	before.auditEvents, after.auditEvents = 0, 0
	before.nextAuditID, after.nextAuditID = 0, 0
	before.version, after.version = 0, 0
	if before != after {
		t.Fatalf("a rejected action left state behind:\nbefore=%+v\nafter =%+v", before, after)
	}

	entries := searchAuditAs(t, service, admin, core.Map{"action": "permission_denied"})
	if len(entries) != 1 {
		t.Fatalf("permission_denied audit entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if core.Bool(entry["success"]) {
		t.Fatalf("rejected action recorded as successful: %v", entry)
	}
	if entry["actorId"] != requester.ID || entry["role"] != "requester" {
		t.Fatalf("rejection audit actor/role = %v/%v", entry["actorId"], entry["role"])
	}
	details := core.Object(entry["details"])
	if details["operation"] != "edit_active_plan" || details["code"] != "forbidden" {
		t.Fatalf("rejection details = %v", details)
	}
	if entry["entityType"] != "journey" || entry["entityId"] != "journey-that-does-not-matter" {
		t.Fatalf("rejection audit did not name the affected item: %v", entry)
	}

	// The unauthorized attempt did not create the request the requester was
	// otherwise entitled to create, so nothing partial was committed.
	if _, err := service.Handle(ctx, "create_collection_request", core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": network.sampleTypeID,
		"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T12:00:00Z",
		"containerType": "amber-container", "temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "idempotencyKey": "denied-plan-edit",
	}, requester); err != nil {
		t.Fatalf("the rejected edit consumed an unrelated idempotency key: %v", err)
	}
}

// AC-70: sign-ins are audited, successful and failed alike, and a failed sign-in
// still returns the same opaque 401.
func TestSignInSuccessAndFailureAreBothAudited(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	user := createTestUser(t, service, admin, "audited-signin", []string{"requester"}, nil)

	signIn(t, service, "audited-signin")

	_, wrongPassword := service.Handle(ctx, "authenticate_user", core.Map{
		"username": "audited-signin", "password": "not-the-password",
	}, core.Principal{})
	assertHTTPErrorStatus(t, wrongPassword, 401)
	assertHTTPErrorCode(t, wrongPassword, "invalid_credentials")

	_, unknownUser := service.Handle(ctx, "authenticate_user", core.Map{
		"username": "no-such-account", "password": "not-the-password",
	}, core.Principal{})
	assertHTTPErrorStatus(t, unknownUser, 401)
	assertHTTPErrorCode(t, unknownUser, "invalid_credentials")
	if unknownUser.Error() != wrongPassword.Error() {
		t.Fatalf(
			"failed sign-in revealed whether the account exists: %q vs %q",
			unknownUser.Error(), wrongPassword.Error(),
		)
	}

	events := auditEvents(t, service)
	success := hasAuditAction(events, "user_signed_in")
	if success == nil || !success.Success || success.ActorID != user.ID ||
		success.Role != "requester" {
		t.Fatalf("successful sign-in audit = %+v", success)
	}
	failures := 0
	sawKnownAccount, sawUnknownAccount := false, false
	for _, event := range events {
		if event.Action != "user_sign_in_failed" {
			continue
		}
		failures++
		if event.Success {
			t.Fatalf("a failed sign-in was recorded as successful: %+v", event)
		}
		if event.Role != core.RoleUnselected {
			t.Fatalf("failed sign-in role = %q", event.Role)
		}
		switch core.String(event.Details["username"]) {
		case "audited-signin":
			sawKnownAccount = true
		case "no-such-account":
			sawUnknownAccount = true
			if event.ActorID != mongostore.SystemActorID {
				t.Fatalf("an unknown username was attributed to an account: %+v", event)
			}
		}
	}
	if failures != 2 || !sawKnownAccount || !sawUnknownAccount {
		t.Fatalf("failed sign-in audit records = %d (known=%v unknown=%v)", failures, sawKnownAccount, sawUnknownAccount)
	}
}

// AC-70: every stated audit category is genuinely covered, and AC-71: every
// record — whichever category it came from — carries all seven required fields.
func TestAuditCoversEveryCategoryAndEveryRecordCarriesAllSevenFields(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	requester := createTestUser(t, service, admin, "audit-requester", []string{"requester"}, nil)
	collector := createTestUser(t, service, admin, "audit-collector", []string{"collection_professional"}, []string{"LX-A"})
	courierA := createTestUser(t, service, admin, "audit-courier-a", []string{"delivery_partner"}, nil)
	courierB := createTestUser(t, service, admin, "audit-courier-b", []string{"delivery_partner"}, nil)
	dispatcher := createTestUser(t, service, admin, "audit-dispatcher", []string{"dispatcher"}, nil)
	network := buildServiceableNetwork(t, service, admin, "audit")
	operator := createTestUserWithLocation(t, service, admin, "audit-operator", []string{"laboratory_team"}, nil, network.laboratoryID)

	// Sign-in, successful and refused.
	signIn(t, service, "audit-requester")
	_, refusedSignIn := service.Handle(ctx, "authenticate_user", core.Map{
		"username": "audit-requester", "password": "wrong-password",
	}, core.Principal{})
	assertHTTPErrorStatus(t, refusedSignIn, 401)

	created, err := service.createCollectionRequest(ctx, requester, core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": network.sampleTypeID,
		"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T12:00:00Z",
		"containerType": "amber-container", "temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "idempotencyKey": "audit-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	sampleID, journeyID := core.String(created["sampleId"]), core.String(created["journeyId"])
	plan, err := service.readJourneyPlan(ctx, admin, journeyID)
	if err != nil {
		t.Fatal(err)
	}
	collectionID := core.String(plan["collectionAssignmentId"])
	transportID := core.String(plan["firstTransportAssignmentId"])

	// Offers and overrides: the dispatcher manually swaps the transport worker.
	offeredCourier := courierA
	replacementCourier := courierB
	if err := service.Store.View(ctx, func(state *mongostore.State) error {
		if state.Assignments[transportID].WorkerID == courierB.ID {
			offeredCourier, replacementCourier = courierB, courierA
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = offeredCourier
	if _, err := service.Handle(ctx, "edit_active_plan", core.Map{
		"journeyId": journeyID, "expectedPlanVersion": 1,
		"changeType": "replace_worker",
		"changes": core.Map{
			"assignmentId": transportID, "workerId": replacementCourier.ID,
			"reason": "manual dispatcher intervention",
		},
		"idempotencyKey": "audit-override",
	}, dispatcher); err != nil {
		t.Fatalf("dispatcher override: %v", err)
	}

	// Collection, sealing.
	if _, err := service.acceptAssignment(ctx, collector, core.Map{
		"assignmentId": collectionID, "idempotencyKey": "audit-accept-collection",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.collectAndSeal(ctx, collector, core.Map{
		"sampleId": sampleID, "assignmentId": collectionID,
		"containerType": "amber-container", "observedTemperatureC": 5.0,
		"collectedAt": "2026-07-30T09:05:00Z", "idempotencyKey": "audit-seal",
	}); err != nil {
		t.Fatal(err)
	}

	// Custody.
	if _, err := service.acceptAssignment(ctx, replacementCourier, core.Map{
		"assignmentId": transportID, "idempotencyKey": "audit-accept-transport",
	}); err != nil {
		t.Fatal(err)
	}
	handoff, err := service.initiateHandoff(ctx, collector, core.Map{
		"sampleId": sampleID, "receiverId": replacementCourier.ID,
		"fromAssignmentId": collectionID, "toAssignmentId": transportID,
		"initiatedAt": "2026-07-30T09:06:00Z", "idempotencyKey": "audit-handoff",
	})
	if err != nil {
		t.Fatal(err)
	}
	handoffID := core.String(handoff["handoffId"])
	for _, side := range []struct {
		p    core.Principal
		side string
		at   string
	}{
		{collector, "sender", "2026-07-30T09:07:00Z"},
		{replacementCourier, "receiver", "2026-07-30T09:08:00Z"},
	} {
		if _, err := service.confirmHandoff(ctx, side.p, core.Map{
			"handoffId": handoffID, "confirmationSide": side.side,
			"confirmedAt": side.at, "idempotencyKey": "audit-confirm-" + side.side,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Worker locations.
	if _, err := service.recordLocation(ctx, replacementCourier, core.Map{
		"assignmentId": transportID, "sequence": 1,
		"coordinate": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"recordedAt": "2026-07-30T09:09:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	// Route progress and completion.
	for index, update := range []struct{ status, at string }{
		{"at_pickup", "2026-07-30T09:10:00Z"},
		{"picked_up", "2026-07-30T09:11:00Z"},
		{"in_progress", "2026-07-30T09:12:00Z"},
		{"arrived", "2026-07-30T09:35:00Z"},
		{"delivered", "2026-07-30T09:36:00Z"},
	} {
		if _, err := service.updateProgress(ctx, replacementCourier, core.Map{
			"assignmentId": transportID, "status": update.status, "occurredAt": update.at,
			"idempotencyKey": "audit-progress-" + core.String(index),
		}); err != nil {
			t.Fatalf("progress %s: %v", update.status, err)
		}
	}
	if _, err := service.confirmReceipt(ctx, operator, core.Map{
		"sampleId": sampleID, "laboratoryId": network.laboratoryID,
		"receivedAt": "2026-07-30T09:37:00Z", "idempotencyKey": "audit-receipt",
	}); err != nil {
		t.Fatal(err)
	}

	// Permission checks.
	_, denied := service.Handle(ctx, "read_operations_state", core.Map{}, requester)
	assertHTTPErrorStatus(t, denied, 403)

	// Nodes and capacity.
	if _, err := service.setLocationFunctionStatus(ctx, admin, core.Map{
		"locationId": network.nodeID, "function": mongostore.FunctionTransferPoint,
		"active": false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.configureCapacity(ctx, admin, core.Map{
		"resourceId": network.nodeID, "capacityUnits": 12, "cooledCapacityUnits": 4,
	}); err != nil {
		t.Fatal(err)
	}

	// Exceptions: a second sample that a laboratory refuses.
	exception, err := service.createCollectionRequest(ctx, requester, core.Map{
		"origin": core.Map{
			"latitude": fixtureOriginLatitude, "longitude": fixtureOriginLongitude,
		},
		"originType": "home", "sampleTypeId": network.sampleTypeID,
		"requestedAt": "2026-07-30T09:00:00Z", "deliveryDeadline": "2026-07-30T12:00:00Z",
		"containerType": "amber-container", "temperatureMinC": 2.0, "temperatureMaxC": 8.0,
		"incompatibleSampleTypes": []string{}, "idempotencyKey": "audit-exception-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	exceptionSampleID := core.String(exception["sampleId"])
	if err := service.Store.Update(ctx, func(state *mongostore.State) error {
		sample := state.Samples[exceptionSampleID]
		sample.Status = "delivered"
		sample.SealedAt = pointerTime(core.Now())
		sample.CurrentHolderID = operator.ID
		for _, assignment := range state.Assignments {
			if assignment.SampleID != exceptionSampleID {
				continue
			}
			switch assignment.Kind {
			case "transport":
				assignment.Status = "delivered"
				assignment.CompletedAt = pointerTime(core.Now())
			case "laboratory":
				assignment.WorkerID, assignment.Status = operator.ID, "accepted"
				assignment.AcceptedAt = pointerTime(core.Now())
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.recordLabException(ctx, operator, core.Map{
		"sampleId": exceptionSampleID, "laboratoryId": network.laboratoryID,
		"reason": "container breach", "observedAt": "2026-07-30T09:40:00Z",
		"idempotencyKey": "audit-lab-exception",
	}); err != nil {
		t.Fatal(err)
	}

	// AC-70: one probe per stated category.
	events := auditEvents(t, service)
	categories := map[string][]string{
		"sign-ins":           {"user_signed_in", "user_sign_in_failed"},
		"permission checks":  {"permission_denied"},
		"offers":             {"assignment_accepted"},
		"collection":         {"sample_collected_and_sealed"},
		"sealing":            {"sample_collected_and_sealed"},
		"custody":            {"handoff_initiated", "handoff_confirmed_sender", "handoff_confirmed_receiver"},
		"routes":             {"journey_planned", "assignment_progress_updated"},
		"nodes":              {"location_created", "location_function_status_changed"},
		"capacity":           {"capacity_configured"},
		"overrides":          {"override"},
		"exceptions":         {"laboratory_exception_recorded"},
		"completion":         {"laboratory_receipt_confirmed"},
		"worker locations":   {"worker_location_recorded"},
		"role selection":     {"user_signed_in"},
		"user configuration": {"user_created"},
	}
	for category, actions := range categories {
		for _, action := range actions {
			if hasAuditAction(events, action) == nil {
				t.Fatalf("audit category %q has no %q record", category, action)
			}
		}
	}

	// AC-71: all seven fields on every record.
	for _, event := range events {
		if event.ActorID == "" {
			t.Fatalf("audit record has no actor: %+v", event)
		}
		if event.Role == "" {
			t.Fatalf("audit record has no role: %+v", event)
		}
		if event.Action == "" {
			t.Fatalf("audit record has no action: %+v", event)
		}
		if event.EntityType == "" || event.EntityID == "" {
			t.Fatalf("audit record has no affected item: %+v", event)
		}
		if event.OccurredAt.IsZero() {
			t.Fatalf("audit record has no time: %+v", event)
		}
		if event.Details == nil {
			t.Fatalf("audit record has no relevant changes: %+v", event)
		}
	}

	// The searchable surface reports the role and the outcome too.
	for _, entry := range searchAuditAs(t, service, admin, core.Map{}) {
		if core.String(entry["role"]) == "" || core.String(entry["actorId"]) == "" {
			t.Fatalf("searchable audit entry is missing actor or role: %v", entry)
		}
		if _, present := entry["success"]; !present {
			t.Fatalf("searchable audit entry is missing its outcome: %v", entry)
		}
	}

	// AC-70 "overrides" is genuinely distinguishable from ordinary plan edits.
	overrides := searchAuditAs(t, service, admin, core.Map{"action": "override"})
	if len(overrides) != 1 {
		t.Fatalf("override audit entries = %d, want 1", len(overrides))
	}
	overrideDetails := core.Object(overrides[0]["details"])
	if overrideDetails["changeType"] != "replace_worker" ||
		core.String(overrideDetails["workerId"]) != replacementCourier.ID {
		t.Fatalf("override details = %v", overrideDetails)
	}
	if core.String(overrides[0]["role"]) != "dispatcher" {
		t.Fatalf("override role = %v", overrides[0]["role"])
	}

	// AC-73: the whole chain of one sample is traceable.
	chain := searchAuditAs(t, service, admin, core.Map{"entityType": "sample", "entityId": sampleID})
	if len(chain) < 3 {
		t.Fatalf("sample audit chain = %d records, want the full history", len(chain))
	}
}

// A rejected action attributed to a caller with no selected role still records a
// role, so the outcome and the role remain readable together.
func TestRejectionAuditRecordsTheUnselectedRoleContext(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()
	createTestUser(t, service, admin, "unselected-actor", []string{"dispatcher", "requester"}, nil)
	session := signIn(t, service, "unselected-actor")

	_, err := service.Handle(ctx, "read_operations_state", core.Map{}, session.principal(t, service))
	assertHTTPErrorCode(t, err, "role_not_selected")

	entries := searchAuditAs(t, service, admin, core.Map{"action": "action_rejected"})
	if len(entries) != 1 {
		t.Fatalf("action_rejected entries = %d, want 1", len(entries))
	}
	if core.String(entries[0]["role"]) != core.RoleUnselected ||
		core.Bool(entries[0]["success"]) {
		t.Fatalf("rejection audit = %v", entries[0])
	}
	if core.Object(entries[0]["details"])["code"] != "role_not_selected" {
		t.Fatalf("rejection details = %v", entries[0]["details"])
	}
}

// searchAuditPageAs runs the searchable audit trail and returns the whole
// response, so a test can read the paging facts alongside the entries.
func searchAuditPageAs(t *testing.T, service *Service, p core.Principal, filters core.Map) core.Map {
	t.Helper()
	out, err := service.Handle(context.Background(), "search_audit_trail", filters, p)
	if err != nil {
		t.Fatalf("search audit trail: %v", err)
	}
	return out
}

// appendFillerAuditEvents grows the trail past one response page in a single
// committed transition, standing in for the ordinary accumulation of sign-ins
// and permission checks on a long-lived instance.
func appendFillerAuditEvents(t *testing.T, service *Service, actorID string, count int) {
	t.Helper()
	if err := service.Store.Update(context.Background(), func(state *mongostore.State) error {
		for index := 0; index < count; index++ {
			state.AddAudit(
				actorID, "administrator", "filler", core.String(index),
				"historic_event", true, core.Map{"index": index},
			)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AC-73: history stays reachable once the trail has grown past one response
// page. The trail now grows continuously — every permission denial and every
// failed sign-in appends to it — so an unfiltered search that returned the first
// page in insertion order would answer with the instance's oldest events forever
// and never surface anything recent.
//
// The search therefore returns newest-first and reports total plus nextOffset, so
// the newest record is in the first page and every older record is still
// reachable by paging.
func TestAuditSearchReachesRecentHistoryBeyondOnePage(t *testing.T) {
	t.Setenv("APPLICATION_FIXED_CLOCK", "2026-07-30T09:00:00Z")
	service, admin := integrationService(t)
	ctx := context.Background()

	traced := createTestUser(t, service, admin, "traced-user", []string{"requester"}, nil)
	appendFillerAuditEvents(t, service, admin.ID, auditPageDefault+120)

	// Recent, genuinely application-produced history on top of the filler.
	for index := 0; index < 3; index++ {
		if _, err := service.Handle(ctx, "update_user_access", core.Map{
			"userId": traced.ID, "roles": []string{"requester"},
			"qualifications": []string{}, "active": true,
		}, admin); err != nil {
			t.Fatalf("update user access: %v", err)
		}
	}
	_, err := service.Handle(ctx, "create_user", core.Map{
		"username": "", "initialPassword": "", "roles": []string{},
	}, admin)
	assertHTTPErrorStatus(t, err, 400)

	events := auditEvents(t, service)
	newest := events[len(events)-1]
	oldest := events[0]
	if len(events) <= auditPageDefault {
		t.Fatalf("audit trail = %d records, want more than one page of %d", len(events), auditPageDefault)
	}

	// The default, unfiltered search reaches the newest record.
	firstPage := searchAuditPageAs(t, service, admin, core.Map{})
	entries, ok := firstPage["entries"].([]core.Map)
	if !ok {
		t.Fatalf("audit entries = %#v", firstPage["entries"])
	}
	if core.Int(firstPage["total"]) != len(events) {
		t.Fatalf("audit total = %v, want %d", firstPage["total"], len(events))
	}
	if len(entries) != auditPageDefault {
		t.Fatalf("first page = %d entries, want %d", len(entries), auditPageDefault)
	}
	if core.String(entries[0]["auditId"]) != core.String(newest.ID) {
		t.Fatalf(
			"first page starts at audit %v, want the newest record %v",
			entries[0]["auditId"], newest.ID,
		)
	}
	if core.String(entries[0]["action"]) != newest.Action {
		t.Fatalf("newest entry action = %v, want %q", entries[0]["action"], newest.Action)
	}
	if !core.Bool(firstPage["hasMore"]) ||
		core.Int(firstPage["nextOffset"]) != auditPageDefault {
		t.Fatalf("first page paging = %v", firstPage)
	}

	// Paging from nextOffset reaches the oldest record, so nothing is stranded.
	lastPage := searchAuditPageAs(t, service, admin, core.Map{
		"offset": core.Int(firstPage["nextOffset"]),
	})
	tail, ok := lastPage["entries"].([]core.Map)
	if !ok || len(tail) == 0 {
		t.Fatalf("second page = %#v", lastPage["entries"])
	}
	if core.Bool(lastPage["hasMore"]) {
		t.Fatalf("second page still reports more history: %v", lastPage)
	}
	if core.String(tail[len(tail)-1]["auditId"]) != core.String(oldest.ID) {
		t.Fatalf(
			"last entry = %v, want the oldest record %v",
			tail[len(tail)-1]["auditId"], oldest.ID,
		)
	}

	// Walking the pages enumerates every record exactly once.
	seen := map[string]int{}
	for offset := 0; offset < len(events); offset += 200 {
		page := searchAuditPageAs(t, service, admin, core.Map{"limit": 200, "offset": offset})
		items, pageOK := page["entries"].([]core.Map)
		if !pageOK {
			t.Fatalf("page at offset %d = %#v", offset, page["entries"])
		}
		for _, item := range items {
			seen[core.String(item["auditId"])]++
		}
	}
	if len(seen) != len(events) {
		t.Fatalf("paged walk saw %d distinct records, want %d", len(seen), len(events))
	}
	for auditID, count := range seen {
		if count != 1 {
			t.Fatalf("audit record %s appeared %d times across pages", auditID, count)
		}
	}

	// Oldest-first is still available for a chronological read.
	chronological := searchAuditPageAs(t, service, admin, core.Map{"order": "oldest_first", "limit": 5})
	first, _ := chronological["entries"].([]core.Map)
	if len(first) != 5 || core.String(first[0]["auditId"]) != core.String(oldest.ID) {
		t.Fatalf("oldest-first page = %v", chronological)
	}

	// An entity trace is complete: pinning entityType + entityId returns the whole
	// chain for that item, not a page of the global trail.
	expected := 0
	for _, event := range events {
		if event.EntityType == "user" && event.EntityID == traced.ID {
			expected++
		}
	}
	if expected < 4 {
		t.Fatalf("traced user has %d audit records, want the full chain", expected)
	}
	trace := searchAuditPageAs(t, service, admin, core.Map{
		"entityType": "user", "entityId": traced.ID,
	})
	traceEntries, ok := trace["entries"].([]core.Map)
	if !ok {
		t.Fatalf("trace entries = %#v", trace["entries"])
	}
	if len(traceEntries) != expected || core.Int(trace["total"]) != expected {
		t.Fatalf("entity trace = %d entries (total %v), want %d", len(traceEntries), trace["total"], expected)
	}
	if core.Bool(trace["hasMore"]) {
		t.Fatalf("entity trace reported an incomplete chain: %v", trace)
	}
	for _, entry := range traceEntries {
		if entry["entityType"] != "user" || entry["entityId"] != traced.ID {
			t.Fatalf("entity trace leaked another item: %v", entry)
		}
	}

	// Filters still compose with paging, and the invalid controls are refused.
	failures := searchAuditPageAs(t, service, admin, core.Map{"success": false})
	failureEntries, _ := failures["entries"].([]core.Map)
	if len(failureEntries) == 0 {
		t.Fatal("no failed actions were searchable")
	}
	for _, entry := range failureEntries {
		if core.Bool(entry["success"]) {
			t.Fatalf("success=false filter returned a successful record: %v", entry)
		}
	}
	for _, invalid := range []struct {
		filters core.Map
		code    string
	}{
		{core.Map{"limit": 0}, "invalid_limit"},
		{core.Map{"limit": auditPageMax + 1}, "invalid_limit"},
		{core.Map{"offset": -1}, "invalid_offset"},
		{core.Map{"order": "sideways"}, "invalid_order"},
	} {
		_, invalidErr := service.Handle(ctx, "search_audit_trail", invalid.filters, admin)
		assertHTTPErrorStatus(t, invalidErr, 400)
		assertHTTPErrorCode(t, invalidErr, invalid.code)
	}
}
