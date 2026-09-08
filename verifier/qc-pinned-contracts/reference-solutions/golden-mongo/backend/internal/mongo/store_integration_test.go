package mongo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"golang.org/x/crypto/bcrypt"

	"labgolden/internal/core"
)

func integrationStore(t *testing.T) *Store {
	t.Helper()
	baseURI := os.Getenv("TEST_MONGODB_URI")
	if baseURI == "" {
		t.Skip("TEST_MONGODB_URI is not set")
	}
	store, err := Open(context.Background(), baseURI+"_mongo")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(context.Background()); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestInitialAdminLoginRoundTrip(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	if err := store.EnsureInitialAdmin(ctx, "golden-admin", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	var storedHash string
	var version int64
	if err := store.View(ctx, func(state *State) error {
		version = state.Version
		if len(state.Users) != 1 {
			t.Fatalf("users=%d, want 1", len(state.Users))
		}
		for _, user := range state.Users {
			storedHash = user.PasswordHash
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte("correct horse battery staple")); err != nil {
		t.Fatalf("bootstrapped administrator password hash did not verify: %v", err)
	}
	login, err := store.Login(ctx, "golden-admin", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := store.Principal(ctx, login["sessionToken"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !principal.Has("administrator") {
		t.Fatalf("principal roles=%v", principal.Roles)
	}
	if err := store.EnsureInitialAdmin(ctx, "other-admin", "other-password"); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, func(state *State) error {
		// Login adds one state version; the no-op bootstrap must not add another.
		if state.Version != version+1 {
			t.Fatalf("version=%d, want %d after login and no-op restart", state.Version, version+1)
		}
		if len(state.Users) != 1 {
			t.Fatalf("users=%d, want 1", len(state.Users))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshotIDs, err := store.snapshots.Distinct(ctx, "snapshotId", bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshotIDs) != 1 {
		t.Fatalf("committed snapshot count=%d, want 1", len(snapshotIDs))
	}
}

func TestResetProducesStableBootstrapDocuments(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	firstState, firstMetadata := bootstrapRaw(t, ctx, store)
	firstContent := decodedBootstrapState(t, ctx, store)
	if err := store.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	secondState, secondMetadata := bootstrapRaw(t, ctx, store)
	secondContent := decodedBootstrapState(t, ctx, store)
	// sha256 is excluded from the head comparison: it digests the raw
	// snapshot bytes, and BSON map-key order is not guaranteed stable across
	// separate encodings of the same Go map (the driver does not sort them,
	// and Go's own map iteration order is intentionally randomized). This
	// bootstrap document itself stays empty -- the four named sample types
	// are seeded separately, by Service.EnsureNamedSampleTypesSeeded on
	// server startup, not by Store.Reset/EnsureSchema -- but the state still
	// carries other Go maps (e.g. ensureMaps' collections), so the same
	// non-determinism risk applies on principle. Every other head field, and
	// the full decoded state content, must still match exactly.
	if !sameHeadExceptDigest(t, firstState, secondState) {
		t.Fatalf("application state head changed across reset\nfirst=%v\nsecond=%v", firstState, secondState)
	}
	if !reflect.DeepEqual(firstContent, secondContent) {
		t.Fatalf("application state content changed across reset\nfirst=%+v\nsecond=%+v", firstContent, secondContent)
	}
	if !reflect.DeepEqual(firstMetadata, secondMetadata) {
		t.Fatalf("schema metadata changed across reset\nfirst=%v\nsecond=%v", firstMetadata, secondMetadata)
	}
}

func TestLegacyMigrationRepairsAPartiallyWrittenSnapshotBeforePublishingHead(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	legacy := NewState()
	legacy.Version = 7
	legacy.UpdatedAt = time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC)
	legacy.AuditEvents = []*AuditEvent{{
		ID: 1, EntityType: "migration", EntityID: "legacy", Action: "large_state",
		Success: true, Details: core.Map{"payload": string(bytes.Repeat([]byte("x"), 9*1024*1024))},
		OccurredAt: legacy.UpdatedAt,
	}}
	raw, err := bson.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= stateChunkBytes || len(raw) >= 16*1024*1024 {
		t.Fatalf("legacy fixture has %d bytes, want between one chunk and MongoDB's document limit", len(raw))
	}

	if _, err := store.state.ReplaceOne(
		ctx,
		bson.M{"_id": StateDocumentID},
		legacy,
		options.Replace().SetBypassDocumentValidation(true),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.snapshots.DeleteMany(ctx, bson.D{}); err != nil {
		t.Fatal(err)
	}

	digest := snapshotDigest(raw)
	snapshotID := "migration-" + digest[:24]
	chunks := snapshotChunks(snapshotID, raw, legacy.UpdatedAt)
	if len(chunks) < 2 {
		t.Fatalf("migration fixture produced %d chunks, want at least 2", len(chunks))
	}
	if _, err := store.snapshots.InsertOne(ctx, chunks[0]); err != nil {
		t.Fatal(err)
	}

	if err := store.migrateLegacyState(ctx, legacy.Version); err != nil {
		t.Fatalf("resume partial migration: %v", err)
	}
	if err := store.verifySnapshotChunks(ctx, snapshotID, len(chunks), digest); err != nil {
		t.Fatalf("published migration snapshot is incomplete: %v", err)
	}
	if err := store.View(ctx, func(state *State) error {
		if state.Version != legacy.Version || len(state.AuditEvents) != 1 {
			t.Fatalf("migrated state version/events=(%d,%d)", state.Version, len(state.AuditEvents))
		}
		if len(core.String(state.AuditEvents[0].Details["payload"])) != 9*1024*1024 {
			t.Fatal("migrated state lost the large audit payload")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAmbiguousHeadReplacementReconcilesCommittedCandidate(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	store.replaceStateHeadFn = func(
		_ context.Context,
		filter any,
		replacement any,
	) (*mongo.UpdateResult, error) {
		if _, err := store.state.ReplaceOne(context.Background(), filter, replacement); err != nil {
			return nil, err
		}
		return nil, context.DeadlineExceeded
	}
	defer func() { store.replaceStateHeadFn = nil }()

	if err := store.Update(ctx, func(state *State) error {
		state.Locations["reconciled"] = &Location{
			ID: "reconciled", Name: "Reconciled", Active: true,
			Functions: map[string]bool{FunctionCollectionPoint: true},
		}
		return nil
	}); err != nil {
		t.Fatalf("ambiguous committed update was not reconciled: %v", err)
	}
	store.replaceStateHeadFn = nil

	if err := store.View(ctx, func(state *State) error {
		if state.Locations["reconciled"] == nil {
			t.Fatal("reconciled mutation was not durable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := distinctSnapshotCount(t, ctx, store); got != 1 {
		t.Fatalf("snapshot count=%d, want only the committed candidate", got)
	}
}

func TestAmbiguousHeadReplacementRetainsCandidateWhenReconciliationFails(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	store.replaceStateHeadFn = func(
		_ context.Context,
		filter any,
		replacement any,
	) (*mongo.UpdateResult, error) {
		if _, err := store.state.ReplaceOne(context.Background(), filter, replacement); err != nil {
			return nil, err
		}
		return nil, context.DeadlineExceeded
	}
	store.readReconciliationFn = func(context.Context) (*stateHead, error) {
		return nil, errors.New("injected reconciliation outage")
	}
	defer func() {
		store.replaceStateHeadFn = nil
		store.readReconciliationFn = nil
	}()

	err := store.Update(ctx, func(state *State) error {
		state.Locations["ambiguous"] = &Location{
			ID: "ambiguous", Name: "Ambiguous", Active: true,
			Functions: map[string]bool{FunctionCollectionPoint: true},
		}
		return nil
	})
	if err == nil {
		t.Fatal("update unexpectedly hid an unreconciled ambiguous result")
	}
	store.replaceStateHeadFn = nil
	store.readReconciliationFn = nil

	if err := store.View(ctx, func(state *State) error {
		if state.Locations["ambiguous"] == nil {
			t.Fatal("candidate chunks were deleted after an ambiguous committed replacement")
		}
		return nil
	}); err != nil {
		t.Fatalf("authoritative candidate became unreadable: %v", err)
	}
	if got := distinctSnapshotCount(t, ctx, store); got != 2 {
		t.Fatalf("snapshot count=%d, want previous and conservatively retained candidate", got)
	}
}

func TestAmbiguousUnchangedHeadRetainsPossiblyInFlightCandidate(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	store.replaceStateHeadFn = func(
		context.Context,
		any,
		any,
	) (*mongo.UpdateResult, error) {
		return nil, context.DeadlineExceeded
	}
	defer func() { store.replaceStateHeadFn = nil }()

	err := store.Update(ctx, func(state *State) error {
		state.Locations["possibly-in-flight"] = &Location{
			ID: "possibly-in-flight", Name: "Possibly in flight", Active: true,
			Functions: map[string]bool{FunctionCollectionPoint: true},
		}
		return nil
	})
	if err == nil {
		t.Fatal("unchanged head incorrectly resolved an ambiguous replacement")
	}
	store.replaceStateHeadFn = nil

	if err := store.View(ctx, func(state *State) error {
		if state.Locations["possibly-in-flight"] != nil {
			t.Fatal("ambiguous candidate became authoritative without a head swap")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := distinctSnapshotCount(t, ctx, store); got != 2 {
		t.Fatalf("snapshot count=%d, want previous and possibly in-flight candidate", got)
	}
}

func TestCleanCASRejectionCleansUpCandidateSnapshots(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	store.replaceStateHeadFn = func(
		context.Context,
		any,
		any,
	) (*mongo.UpdateResult, error) {
		return &mongo.UpdateResult{MatchedCount: 0}, nil
	}
	defer func() { store.replaceStateHeadFn = nil }()

	err := store.Update(ctx, func(state *State) error {
		state.Locations["rejected"] = &Location{
			ID: "rejected", Name: "Rejected", Active: true,
			Functions: map[string]bool{FunctionCollectionPoint: true},
		}
		return nil
	})
	if err == nil {
		t.Fatal("repeated zero-match replacements unexpectedly succeeded")
	}
	store.replaceStateHeadFn = nil

	if err := store.View(ctx, func(state *State) error {
		if state.Locations["rejected"] != nil {
			t.Fatal("rejected mutation became visible")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := distinctSnapshotCount(t, ctx, store); got != 1 {
		t.Fatalf("snapshot count=%d, want rejected candidate cleaned up", got)
	}
}

func distinctSnapshotCount(t *testing.T, ctx context.Context, store *Store) int {
	t.Helper()
	snapshotIDs, err := store.snapshots.Distinct(ctx, "snapshotId", bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	return len(snapshotIDs)
}

func bootstrapRaw(t *testing.T, ctx context.Context, store *Store) (bson.Raw, bson.Raw) {
	t.Helper()
	var state bson.Raw
	if err := store.Database.Collection(StateCollection).
		FindOne(ctx, bson.D{{Key: "_id", Value: StateDocumentID}}).Decode(&state); err != nil {
		t.Fatal(err)
	}
	var metadata bson.Raw
	if err := store.Database.Collection("schema_metadata").
		FindOne(ctx, bson.D{{Key: "_id", Value: "schema"}}).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	return state, metadata
}

// decodedBootstrapState reads back the application-level content of the
// bootstrap state, independent of how its raw BSON happened to be encoded.
func decodedBootstrapState(t *testing.T, ctx context.Context, store *Store) State {
	t.Helper()
	var content State
	if err := store.View(ctx, func(state *State) error {
		content = *state
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return content
}

// sameHeadExceptDigest compares two state-head documents field by field,
// except for sha256: that field digests the raw snapshot bytes, and a Go
// map's BSON key order -- unlike every other field here -- is not guaranteed
// stable across separate encodings of equal content.
func sameHeadExceptDigest(t *testing.T, first, second bson.Raw) bool {
	t.Helper()
	var firstDoc, secondDoc bson.M
	if err := bson.Unmarshal(first, &firstDoc); err != nil {
		t.Fatal(err)
	}
	if err := bson.Unmarshal(second, &secondDoc); err != nil {
		t.Fatal(err)
	}
	delete(firstDoc, "sha256")
	delete(secondDoc, "sha256")
	return reflect.DeepEqual(firstDoc, secondDoc)
}
