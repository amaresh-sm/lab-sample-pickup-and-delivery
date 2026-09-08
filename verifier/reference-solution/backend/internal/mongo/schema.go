package mongo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"labgolden/internal/core"
)

var stateValidator = bson.D{
	{Key: "$jsonSchema", Value: bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: bson.A{
			"_id", "version", "snapshotId", "chunkCount", "sha256", "updatedAt",
		}},
		{Key: "properties", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "version", Value: bson.D{{Key: "bsonType", Value: bson.A{"long", "int"}}, {Key: "minimum", Value: 1}}},
			{Key: "snapshotId", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "chunkCount", Value: bson.D{{Key: "bsonType", Value: bson.A{"int", "long"}}, {Key: "minimum", Value: 1}}},
			{Key: "sha256", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "updatedAt", Value: bson.D{{Key: "bsonType", Value: "date"}}},
		}},
	}},
}

var snapshotValidator = bson.D{
	{Key: "$jsonSchema", Value: bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: bson.A{"_id", "snapshotId", "index", "payload", "createdAt"}},
		{Key: "properties", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "snapshotId", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "index", Value: bson.D{{Key: "bsonType", Value: bson.A{"int", "long"}}, {Key: "minimum", Value: 0}}},
			{Key: "payload", Value: bson.D{{Key: "bsonType", Value: "binData"}}},
			{Key: "createdAt", Value: bson.D{{Key: "bsonType", Value: "date"}}},
		}},
	}},
}

// EnsureSchema creates the MongoDB collections, validation rules, metadata,
// and initial transactional state. It is safe to call on every startup.
func (s *Store) EnsureSchema(ctx context.Context) error {
	createOptions := options.CreateCollection().
		SetValidator(stateValidator).
		SetValidationLevel("strict").
		SetValidationAction("error")
	err := s.Database.CreateCollection(ctx, StateCollection, createOptions)
	if err != nil && !isNamespaceExists(err) {
		return fmt.Errorf("create %s: %w", StateCollection, err)
	}
	if err := s.Database.RunCommand(ctx, bson.D{
		{Key: "collMod", Value: StateCollection},
		{Key: "validator", Value: stateValidator},
		{Key: "validationLevel", Value: "strict"},
		{Key: "validationAction", Value: "error"},
	}).Err(); err != nil {
		return fmt.Errorf("apply state validator: %w", err)
	}

	snapshotOptions := options.CreateCollection().
		SetValidator(snapshotValidator).
		SetValidationLevel("strict").
		SetValidationAction("error")
	err = s.Database.CreateCollection(ctx, SnapshotCollection, snapshotOptions)
	if err != nil && !isNamespaceExists(err) {
		return fmt.Errorf("create %s: %w", SnapshotCollection, err)
	}
	if err := s.Database.RunCommand(ctx, bson.D{
		{Key: "collMod", Value: SnapshotCollection},
		{Key: "validator", Value: snapshotValidator},
		{Key: "validationLevel", Value: "strict"},
		{Key: "validationAction", Value: "error"},
	}).Err(); err != nil {
		return fmt.Errorf("apply state chunk validator: %w", err)
	}
	if _, err := s.snapshots.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "snapshotId", Value: 1}, {Key: "index", Value: 1}},
			Options: options.Index().SetName("snapshot_chunk_unique").SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "createdAt", Value: 1}},
			Options: options.Index().SetName("snapshot_created_at"),
		},
	}); err != nil {
		return fmt.Errorf("create state chunk indexes: %w", err)
	}

	err = s.Database.CreateCollection(ctx, "schema_metadata")
	if err != nil && !isNamespaceExists(err) {
		return fmt.Errorf("create schema_metadata: %w", err)
	}
	metadata := s.Database.Collection("schema_metadata")
	if _, err := metadata.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "name", Value: 1}},
		Options: options.Index().SetName("schema_name_unique").SetUnique(true),
	}); err != nil {
		return fmt.Errorf("create schema metadata index: %w", err)
	}
	metadataDocument := bson.D{
		{Key: "_id", Value: "schema"},
		{Key: "name", Value: "lab-sample-logistics"},
		{Key: "version", Value: 2},
		{Key: "transactionModel", Value: "immutable-bounded-snapshots-with-versioned-head-cas"},
		{Key: "collections", Value: bson.A{StateCollection, SnapshotCollection, "schema_metadata"}},
		{Key: "invariants", Value: bson.A{
			"one-current-holder-after-sealing",
			"one-pending-handoff-per-sample",
			"one-active-plan-per-sample",
			"capacity-never-over-reserved",
			"idempotent-critical-commands",
			"append-only-audit-and-outbox",
			"no-single-state-document-size-ceiling",
		}},
	}
	_, err = metadata.ReplaceOne(
		ctx,
		bson.D{{Key: "_id", Value: "schema"}},
		metadataDocument,
		options.Replace().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("write schema metadata: %w", err)
	}

	return s.ensureInitialSnapshot(ctx)
}

func (s *Store) ensureInitialSnapshot(ctx context.Context) error {
	var existing stateHead
	err := s.state.FindOne(ctx, bson.D{{Key: "_id", Value: StateDocumentID}}).Decode(&existing)
	if err == nil && existing.SnapshotID != "" && existing.ChunkCount > 0 {
		return nil
	}
	if err == nil {
		return s.migrateLegacyState(ctx, existing.Version)
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return fmt.Errorf("read initial application state: %w", err)
	}
	// The four canonical named sample types (Amber/Indigo/Frost/Pulse) are
	// NOT seeded into this bootstrap state directly: they are created by
	// Service.EnsureNamedSampleTypesSeeded, called from cmd/server/main.go on
	// every startup through the exact same createSampleType path an
	// administrator's own request would take. That keeps this DB-level
	// bootstrap document empty and byte-stable, and leaves the named-type
	// policy (enforceNamedSamplePolicy) as the single source of truth for
	// what those four records look like.
	initial := NewState()
	raw, err := bson.Marshal(initial)
	if err != nil {
		return fmt.Errorf("encode initial application state: %w", err)
	}
	snapshotID := "initial-" + initial.ID
	chunks := snapshotChunks(snapshotID, raw, initial.UpdatedAt)
	digest := snapshotDigest(raw)
	if err := s.persistSnapshotChunks(ctx, chunks, digest); err != nil {
		return fmt.Errorf("persist initial application state: %w", err)
	}
	head := stateHead{
		ID: StateDocumentID, Version: initial.Version, SnapshotID: snapshotID,
		ChunkCount: len(chunks), SHA256: digest, UpdatedAt: initial.UpdatedAt,
	}
	_, err = s.state.UpdateOne(
		ctx,
		bson.D{{Key: "_id", Value: StateDocumentID}},
		bson.D{{Key: "$setOnInsert", Value: head}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("initialize application state head: %w", err)
	}
	return nil
}

func (s *Store) migrateLegacyState(ctx context.Context, expectedVersion int64) error {
	var legacy State
	if err := s.state.FindOne(ctx, bson.D{{Key: "_id", Value: StateDocumentID}}).Decode(&legacy); err != nil {
		return fmt.Errorf("decode legacy application state: %w", err)
	}
	legacy.ensureMaps()
	raw, err := bson.Marshal(&legacy)
	if err != nil {
		return fmt.Errorf("encode legacy application state: %w", err)
	}
	snapshotID := "migration-" + snapshotDigest(raw)[:24]
	chunks := snapshotChunks(snapshotID, raw, time.Now().UTC())
	digest := snapshotDigest(raw)
	if err := s.persistSnapshotChunks(ctx, chunks, digest); err != nil {
		return fmt.Errorf("persist migrated application state: %w", err)
	}
	head := stateHead{
		ID: StateDocumentID, Version: legacy.Version, SnapshotID: snapshotID,
		ChunkCount: len(chunks), SHA256: digest, UpdatedAt: legacy.UpdatedAt,
	}
	result, err := s.state.ReplaceOne(
		ctx,
		bson.D{{Key: "_id", Value: StateDocumentID}, {Key: "version", Value: expectedVersion}},
		head,
	)
	if err != nil {
		return fmt.Errorf("replace legacy application state head: %w", err)
	}
	if result.MatchedCount != 1 {
		current, readErr := s.readHeadForReconciliation(ctx)
		if readErr == nil &&
			classifySnapshotCommit(*current, head, expectedVersion, "") == snapshotCommitConfirmed {
			return nil
		}
		return core.Fail(409, "concurrent_migration", "another process changed state during schema migration")
	}
	return nil
}

// Reset drops only the database selected by MONGODB_URI, then recreates the
// canonical schema and empty state. The operation is intentionally repeatable.
func (s *Store) Reset(ctx context.Context) error {
	if err := s.Database.Drop(ctx); err != nil {
		return fmt.Errorf("drop mongodb database: %w", err)
	}
	s.state = s.Database.Collection(StateCollection)
	s.snapshots = s.Database.Collection(SnapshotCollection)
	return s.EnsureSchema(ctx)
}

func isNamespaceExists(err error) bool {
	var commandError mongo.CommandError
	return errors.As(err, &commandError) && commandError.Code == 48
}
