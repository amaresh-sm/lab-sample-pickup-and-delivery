package mongo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
	"golang.org/x/crypto/bcrypt"

	"labgolden/internal/core"
)

const (
	StateCollection    = "application_state"
	SnapshotCollection = "application_state_chunks"
	StateDocumentID    = "lab-sample-logistics"
	stateChunkBytes    = 8 * 1024 * 1024
	snapshotIOTimeout  = 5 * time.Second
)

type stateHead struct {
	ID         string    `bson:"_id"`
	Version    int64     `bson:"version"`
	SnapshotID string    `bson:"snapshotId"`
	ChunkCount int       `bson:"chunkCount"`
	SHA256     string    `bson:"sha256"`
	UpdatedAt  time.Time `bson:"updatedAt"`
}

type stateChunk struct {
	ID         string    `bson:"_id"`
	SnapshotID string    `bson:"snapshotId"`
	Index      int       `bson:"index"`
	Payload    []byte    `bson:"payload"`
	CreatedAt  time.Time `bson:"createdAt"`
}

// Store persists the invariant-bearing state as immutable bounded chunks and
// atomically swaps one small versioned head document. This remains compatible
// with standalone MongoDB while avoiding MongoDB's 16 MB document ceiling.
type Store struct {
	Client    *mongo.Client
	Database  *mongo.Database
	state     *mongo.Collection
	snapshots *mongo.Collection

	updateMu sync.Mutex
	subMu    sync.RWMutex
	subs     map[chan int64]struct{}

	replaceStateHeadFn   func(context.Context, any, any) (*mongo.UpdateResult, error)
	readReconciliationFn func(context.Context) (*stateHead, error)
}

// Open connects to the database named in MONGODB_URI. When the URI omits a
// database, "arena" is used to match the task runtime.
func Open(ctx context.Context, uri string) (*Store, error) {
	if uri == "" {
		return nil, errors.New("MONGODB_URI is required")
	}
	cs, err := connstring.ParseAndValidate(uri)
	if err != nil {
		return nil, fmt.Errorf("parse MONGODB_URI: %w", err)
	}
	databaseName := cs.Database
	if databaseName == "" {
		databaseName = "arena"
	}
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).
		SetRetryWrites(true).
		SetServerSelectionTimeout(10*time.Second))
	if err != nil {
		return nil, fmt.Errorf("connect mongodb: %w", err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping mongodb: %w", err)
	}
	store := &Store{
		Client: client, Database: client.Database(databaseName),
		subs: make(map[chan int64]struct{}),
	}
	store.state = store.Database.Collection(StateCollection)
	store.snapshots = store.Database.Collection(SnapshotCollection)
	if err := store.EnsureSchema(ctx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return store, nil
}

// URI returns the configured MongoDB connection string.
func URI() string { return os.Getenv("MONGODB_URI") }

// Close releases MongoDB client resources.
func (s *Store) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.Client.Disconnect(ctx)
}

// Ping verifies that the authoritative datastore is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.Client.Ping(ctx, readpref.Primary())
}

// View loads one consistent state snapshot.
func (s *Store) View(ctx context.Context, fn func(*State) error) error {
	state, err := s.load(ctx)
	if err != nil {
		return err
	}
	return fn(state)
}

// Update applies fn and atomically replaces the state only if its version is
// still current. Cross-process races retry from a fresh snapshot; in-process
// mutations are serialized to avoid needless retries.
func (s *Store) Update(ctx context.Context, fn func(*State) error) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	for attempt := 0; attempt < 8; attempt++ {
		state, head, err := s.loadSnapshot(ctx)
		if err != nil {
			return err
		}
		expected := state.Version
		previousOutboxID := state.NextOutboxID
		snapshot, err := cloneState(state)
		if err != nil {
			return err
		}
		if err := fn(state); err != nil {
			return err
		}
		if reflect.DeepEqual(snapshot, state) {
			return nil
		}
		state.Version = expected + 1
		state.UpdatedAt = core.Now()
		result, err := s.commitSnapshot(ctx, state, expected, head.SnapshotID)
		if err != nil {
			return fmt.Errorf("commit mongodb state: %w", err)
		}
		if result.MatchedCount == 1 {
			for id := previousOutboxID + 1; id <= state.NextOutboxID; id++ {
				s.Publish(id)
			}
			return nil
		}
	}
	return core.Fail(409, "concurrent_conflict", "concurrent update; retry with the same idempotency key")
}

func cloneState(state *State) (*State, error) {
	raw, err := bson.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("snapshot mongodb state: %w", err)
	}
	var snapshot State
	if err := bson.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("decode mongodb state snapshot: %w", err)
	}
	snapshot.ensureMaps()
	return &snapshot, nil
}

func (s *Store) load(ctx context.Context) (*State, error) {
	state, _, err := s.loadSnapshot(ctx)
	return state, err
}

func (s *Store) loadSnapshot(ctx context.Context) (*State, *stateHead, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var head stateHead
		err := s.state.FindOne(ctx, bson.M{"_id": StateDocumentID}).Decode(&head)
		if errors.Is(err, mongo.ErrNoDocuments) {
			if err := s.EnsureSchema(ctx); err != nil {
				return nil, nil, err
			}
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("load mongodb state head: %w", err)
		}
		cursor, err := s.snapshots.Find(
			ctx,
			bson.M{"snapshotId": head.SnapshotID},
			options.Find().SetSort(bson.D{{Key: "index", Value: 1}}),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("load mongodb state chunks: %w", err)
		}
		var chunks []stateChunk
		if err := cursor.All(ctx, &chunks); err != nil {
			return nil, nil, fmt.Errorf("decode mongodb state chunks: %w", err)
		}
		raw, err := validateSnapshotChunks(chunks, head.SnapshotID, head.ChunkCount, head.SHA256)
		if err != nil {
			continue
		}
		var state State
		if err := bson.Unmarshal(raw, &state); err != nil {
			return nil, nil, fmt.Errorf("decode mongodb state snapshot: %w", err)
		}
		if state.Version != head.Version {
			continue
		}
		state.ensureMaps()
		return &state, &head, nil
	}
	return nil, nil, core.Fail(409, "state_snapshot_changed", "state snapshot changed while it was being read; retry")
}

func (s *Store) commitSnapshot(
	ctx context.Context,
	state *State,
	expectedVersion int64,
	previousSnapshotID string,
) (*mongo.UpdateResult, error) {
	raw, err := bson.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode state snapshot: %w", err)
	}
	snapshotID := core.UUID()
	chunks := snapshotChunks(snapshotID, raw, core.Now())
	digest := snapshotDigest(raw)
	if err := s.persistSnapshotChunks(ctx, chunks, digest); err != nil {
		return nil, err
	}
	head := stateHead{
		ID: StateDocumentID, Version: state.Version, SnapshotID: snapshotID,
		ChunkCount: len(chunks), SHA256: digest, UpdatedAt: state.UpdatedAt,
	}
	result, err := s.replaceStateHead(
		ctx,
		bson.M{"_id": StateDocumentID, "version": expectedVersion},
		head,
	)
	if err == nil {
		if result.MatchedCount == 0 {
			s.deleteSnapshot(snapshotID)
			return result, nil
		}
		s.deletePreviousSnapshot(previousSnapshotID, snapshotID)
		return result, nil
	}

	outcome, reconciliationErr := s.reconcileSnapshotCommit(
		head,
		expectedVersion,
		previousSnapshotID,
		chunks,
	)
	switch outcome {
	case snapshotCommitConfirmed:
		s.deletePreviousSnapshot(previousSnapshotID, snapshotID)
		return &mongo.UpdateResult{MatchedCount: 1, ModifiedCount: 1}, nil
	case snapshotCommitRejected:
		s.deleteSnapshot(snapshotID)
		return nil, fmt.Errorf("replace mongodb state head: %w", err)
	default:
		return nil, fmt.Errorf(
			"replace mongodb state head returned an ambiguous result: %v; reconciliation failed: %w",
			err,
			reconciliationErr,
		)
	}
}

type snapshotCommitOutcome int

const (
	snapshotCommitUnknown snapshotCommitOutcome = iota
	snapshotCommitConfirmed
	snapshotCommitRejected
)

func classifySnapshotCommit(
	current stateHead,
	candidate stateHead,
	expectedVersion int64,
	previousSnapshotID string,
) snapshotCommitOutcome {
	if current.ID == candidate.ID &&
		current.Version == candidate.Version &&
		current.SnapshotID == candidate.SnapshotID &&
		current.ChunkCount == candidate.ChunkCount &&
		current.SHA256 == candidate.SHA256 {
		return snapshotCommitConfirmed
	}
	if current.Version >= candidate.Version &&
		current.SnapshotID != candidate.SnapshotID {
		return snapshotCommitRejected
	}
	// Seeing the previous head after a timeout is not definitive: the original
	// command may still be in flight on another connection. Retain the candidate
	// until a later committed version makes it impossible for that CAS to land.
	if current.Version == expectedVersion &&
		current.SnapshotID == previousSnapshotID {
		return snapshotCommitUnknown
	}
	return snapshotCommitUnknown
}

func (s *Store) reconcileSnapshotCommit(
	candidate stateHead,
	expectedVersion int64,
	previousSnapshotID string,
	chunks []stateChunk,
) (snapshotCommitOutcome, error) {
	ctx, cancel := context.WithTimeout(context.Background(), snapshotIOTimeout)
	defer cancel()

	current, err := s.readHeadForReconciliation(ctx)
	if err != nil {
		return snapshotCommitUnknown, fmt.Errorf("read state head after ambiguous replacement: %w", err)
	}
	outcome := classifySnapshotCommit(*current, candidate, expectedVersion, previousSnapshotID)
	if outcome != snapshotCommitConfirmed {
		if outcome == snapshotCommitUnknown {
			return outcome, errors.New("state head no longer proves whether the candidate committed")
		}
		return outcome, nil
	}
	if err := s.persistSnapshotChunks(ctx, chunks, candidate.SHA256); err != nil {
		return snapshotCommitUnknown, fmt.Errorf("repair committed state snapshot: %w", err)
	}
	return snapshotCommitConfirmed, nil
}

func (s *Store) replaceStateHead(
	ctx context.Context,
	filter any,
	replacement any,
) (*mongo.UpdateResult, error) {
	if s.replaceStateHeadFn != nil {
		return s.replaceStateHeadFn(ctx, filter, replacement)
	}
	return s.state.ReplaceOne(ctx, filter, replacement)
}

func (s *Store) readHeadForReconciliation(ctx context.Context) (*stateHead, error) {
	if s.readReconciliationFn != nil {
		return s.readReconciliationFn(ctx)
	}
	var head stateHead
	if err := s.state.FindOne(ctx, bson.M{"_id": StateDocumentID}).Decode(&head); err != nil {
		return nil, err
	}
	return &head, nil
}

func (s *Store) deletePreviousSnapshot(previousSnapshotID, currentSnapshotID string) {
	if previousSnapshotID == "" || previousSnapshotID == currentSnapshotID {
		return
	}
	// A reader that observed the previous head will retry against the new one if
	// these immutable chunks disappear before its read completes.
	s.deleteSnapshot(previousSnapshotID)
}

func (s *Store) deleteSnapshot(snapshotID string) {
	if snapshotID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), snapshotIOTimeout)
	defer cancel()
	_, _ = s.snapshots.DeleteMany(ctx, bson.M{"snapshotId": snapshotID})
}

func (s *Store) persistSnapshotChunks(
	ctx context.Context,
	chunks []stateChunk,
	expectedDigest string,
) error {
	if len(chunks) == 0 {
		return errors.New("state snapshot must contain at least one chunk")
	}
	snapshotID := chunks[0].SnapshotID
	models := make([]mongo.WriteModel, 0, len(chunks))
	for _, chunk := range chunks {
		models = append(models, mongo.NewReplaceOneModel().
			SetFilter(bson.M{"_id": chunk.ID}).
			SetReplacement(chunk).
			SetUpsert(true))
	}
	_, writeErr := s.snapshots.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	verifyErr := s.verifySnapshotChunks(ctx, snapshotID, len(chunks), expectedDigest)
	if verifyErr == nil {
		return nil
	}
	if writeErr != nil {
		return fmt.Errorf("write state snapshot chunks: %v; verify persisted snapshot: %w", writeErr, verifyErr)
	}
	return fmt.Errorf("verify persisted state snapshot: %w", verifyErr)
}

func (s *Store) verifySnapshotChunks(
	ctx context.Context,
	snapshotID string,
	expectedCount int,
	expectedDigest string,
) error {
	cursor, err := s.snapshots.Find(
		ctx,
		bson.M{"snapshotId": snapshotID},
		options.Find().SetSort(bson.D{{Key: "index", Value: 1}}),
	)
	if err != nil {
		return fmt.Errorf("read state snapshot chunks: %w", err)
	}
	var chunks []stateChunk
	if err := cursor.All(ctx, &chunks); err != nil {
		return fmt.Errorf("decode state snapshot chunks: %w", err)
	}
	_, err = validateSnapshotChunks(chunks, snapshotID, expectedCount, expectedDigest)
	return err
}

func validateSnapshotChunks(
	chunks []stateChunk,
	snapshotID string,
	expectedCount int,
	expectedDigest string,
) ([]byte, error) {
	if expectedCount <= 0 || len(chunks) != expectedCount {
		return nil, fmt.Errorf(
			"state snapshot %q has %d chunks, want %d",
			snapshotID,
			len(chunks),
			expectedCount,
		)
	}
	size := 0
	for index, chunk := range chunks {
		if chunk.SnapshotID != snapshotID ||
			chunk.Index != index ||
			chunk.ID != fmt.Sprintf("%s:%06d", snapshotID, index) {
			return nil, fmt.Errorf("state snapshot %q has an invalid chunk at index %d", snapshotID, index)
		}
		if len(chunk.Payload) > stateChunkBytes {
			return nil, fmt.Errorf("state snapshot %q chunk %d exceeds the chunk limit", snapshotID, index)
		}
		size += len(chunk.Payload)
	}
	raw := make([]byte, 0, size)
	for _, chunk := range chunks {
		raw = append(raw, chunk.Payload...)
	}
	if digest := snapshotDigest(raw); digest != expectedDigest {
		return nil, fmt.Errorf(
			"state snapshot %q digest is %s, want %s",
			snapshotID,
			digest,
			expectedDigest,
		)
	}
	return raw, nil
}

func snapshotChunks(snapshotID string, raw []byte, createdAt time.Time) []stateChunk {
	chunks := make([]stateChunk, 0, (len(raw)+stateChunkBytes-1)/stateChunkBytes)
	for start, index := 0, 0; start < len(raw); start, index = start+stateChunkBytes, index+1 {
		end := start + stateChunkBytes
		if end > len(raw) {
			end = len(raw)
		}
		payload := append([]byte(nil), raw[start:end]...)
		chunks = append(chunks, stateChunk{
			ID: fmt.Sprintf("%s:%06d", snapshotID, index), SnapshotID: snapshotID,
			Index: index, Payload: payload, CreatedAt: createdAt,
		})
	}
	if len(chunks) == 0 {
		chunks = append(chunks, stateChunk{
			ID: snapshotID + ":000000", SnapshotID: snapshotID,
			Index: 0, Payload: []byte{}, CreatedAt: createdAt,
		})
	}
	return chunks
}

func snapshotDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// EnsureInitialAdmin creates exactly one bootstrap administrator when the user
// store is empty. Existing credentials are never replaced on restart.
func (s *Store) EnsureInitialAdmin(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return errors.New("ADMIN_USERNAME and ADMIN_PASSWORD are required")
	}
	hasUsers := false
	if err := s.View(ctx, func(state *State) error {
		hasUsers = len(state.Users) > 0
		return nil
	}); err != nil {
		return err
	}
	if hasUsers {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	return s.Update(ctx, func(state *State) error {
		if len(state.Users) > 0 {
			return nil
		}
		now := core.Now()
		id := core.UUID()
		state.Users[id] = &User{
			ID: id, Username: username, PasswordHash: string(hash),
			Email: username + "@local.invalid", FullName: "Initial administrator",
			Roles: []string{"administrator"}, Qualifications: []string{},
			Active: true, Available: true, CapacityUnits: 6,
			CreatedAt: now, UpdatedAt: now,
		}
		return nil
	})
}

// AutoSelectedRole is the role a sign-in starts under. A user holding exactly
// one role is placed in it immediately; a user holding several must call
// select_active_role before any role-scoped action is permitted.
func AutoSelectedRole(roles []string) string {
	if len(roles) == 1 {
		return roles[0]
	}
	return ""
}

// Login verifies credentials and persists a twelve-hour opaque session. Both a
// successful and a failed sign-in are audited; the failure record never changes
// the 401 the caller sees, so the response still does not reveal whether the
// username exists.
func (s *Store) Login(ctx context.Context, username, password string) (core.Map, error) {
	var user *User
	if err := s.View(ctx, func(state *State) error {
		for _, candidate := range state.Users {
			if candidate.Username == username {
				copy := *candidate
				user = &copy
				break
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if user == nil || !user.Active ||
		bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		s.auditSignInFailure(ctx, username, user)
		return nil, core.Fail(401, "invalid_credentials", "invalid credentials")
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	digest := tokenDigest(token)
	now := core.Now()
	activeRole := AutoSelectedRole(user.Roles)
	if err := s.Update(ctx, func(state *State) error {
		current := state.Users[user.ID]
		if current == nil || !current.Active {
			return core.Fail(401, "invalid_credentials", "invalid credentials")
		}
		for hash, session := range state.Sessions {
			if !session.ExpiresAt.After(now) {
				delete(state.Sessions, hash)
			}
		}
		state.Sessions[digest] = &Session{
			TokenHash: digest, UserID: user.ID, SelectedRole: activeRole,
			ExpiresAt: now.Add(12 * time.Hour), CreatedAt: now,
		}
		auditRole := activeRole
		if auditRole == "" {
			auditRole = core.RoleUnselected
		}
		state.AddAudit(user.ID, auditRole, "user", user.ID, "user_signed_in", true, core.Map{
			"username": current.Username, "outcome": "authenticated",
			"activeRole": activeRole, "roleSelectionRequired": activeRole == "",
		})
		return nil
	}); err != nil {
		return nil, err
	}
	return core.Map{
		"sessionToken": token, "userId": user.ID,
		"roles":      append([]string{}, user.Roles...),
		"activeRole": activeRole,
	}, nil
}

// auditSignInFailure records a rejected sign-in attempt. It is a separate
// committed write because the failed sign-in never opens a transaction of its
// own, and it appends nothing but the audit record so no other state is left
// behind. An unknown username is attributed to the system actor, which keeps
// the audit trail from asserting that an account exists.
func (s *Store) auditSignInFailure(ctx context.Context, username string, user *User) {
	// A failed attempt carries a caller-supplied username, so it is bounded before
	// it is written: an audit record must never be a place to store bulk input.
	username = truncateAuditValue(username)
	actorID, entityID := SystemActorID, username
	if user != nil {
		actorID, entityID = user.ID, user.ID
	}
	_ = s.Update(ctx, func(state *State) error {
		state.AddAudit(actorID, core.RoleUnselected, "user", entityID, "user_sign_in_failed", false, core.Map{
			"username": username, "outcome": "invalid_credentials",
			"code": "invalid_credentials",
		})
		return nil
	})
}

// Principal resolves an active user from an opaque bearer token, carrying the
// role selected on that session.
func (s *Store) Principal(ctx context.Context, token string) (core.Principal, error) {
	if token == "" {
		return core.Principal{}, core.Fail(401, "unauthorized", "missing bearer token")
	}
	digest := tokenDigest(token)
	var principal core.Principal
	err := s.View(ctx, func(state *State) error {
		session := state.Sessions[digest]
		if session == nil || !session.ExpiresAt.After(core.Now()) {
			return core.Fail(401, "unauthorized", "invalid or expired bearer token")
		}
		user := state.Users[session.UserID]
		if user == nil || !user.Active {
			return core.Fail(401, "unauthorized", "invalid or expired bearer token")
		}
		principal = core.Principal{
			ID: user.ID, SessionID: digest,
			Roles:        append([]string{}, user.Roles...),
			SelectedRole: session.SelectedRole,
		}
		return nil
	})
	return principal, err
}

// maxAuditValueBytes bounds a caller-supplied value copied into an audit record.
const maxAuditValueBytes = 256

func truncateAuditValue(value string) string {
	if len(value) <= maxAuditValueBytes {
		return value
	}
	return value[:maxAuditValueBytes]
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Subscribe registers an in-process wake-up channel for websocket consumers.
// Events themselves remain durable in MongoDB and are always replayed by ID.
func (s *Store) Subscribe() (<-chan int64, func()) {
	ch := make(chan int64, 8)
	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()
	cancel := func() {
		s.subMu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.subMu.Unlock()
	}
	return ch, cancel
}

// Publish wakes active websocket loops after an outbox commit.
func (s *Store) Publish(id int64) {
	s.subMu.RLock()
	defer s.subMu.RUnlock()
	for ch := range s.subs {
		select {
		case ch <- id:
		default:
		}
	}
}

// EventsSince returns authorized durable events in sequence order.
func (s *Store) EventsSince(ctx context.Context, since int64, limit int, p core.Principal) ([]core.Map, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	out := []core.Map{}
	err := s.View(ctx, func(state *State) error {
		for _, event := range state.OutboxEvents {
			if event.ID <= since || !audienceAllows(event.Audience, p) {
				continue
			}
			out = append(out, core.Map{
				"id": event.ID, "type": event.EventType,
				"entityType": event.EntityType, "entityId": event.EntityID,
				"payload":   event.Payload,
				"createdAt": event.CreatedAt.UTC().Format(time.RFC3339Nano),
			})
			if len(out) == limit {
				break
			}
		}
		return nil
	})
	return out, err
}

func audienceAllows(audience []string, p core.Principal) bool {
	if len(audience) == 0 {
		return true
	}
	for _, value := range audience {
		if value == p.ID || p.Has(value) {
			return true
		}
	}
	return false
}

// SortedOutboxIDs is useful in deterministic storage tests.
func SortedOutboxIDs(events []*OutboxEvent) []int64 {
	ids := make([]int64, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// IdempotencyID creates a map-safe key for an actor/operation/key tuple.
func IdempotencyID(actorID, operation, key string) string {
	raw := strings.Join([]string{actorID, operation, key}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
