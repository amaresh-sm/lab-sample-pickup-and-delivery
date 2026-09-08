package mongo

import (
	"bytes"
	"testing"
	"time"
)

func TestSnapshotChunksStayBelowMongoDocumentLimit(t *testing.T) {
	raw := bytes.Repeat([]byte("state"), (17*1024*1024/5)+1)
	chunks := snapshotChunks("snapshot", raw, time.Unix(0, 0).UTC())
	if len(chunks) < 3 {
		t.Fatalf("chunks=%d, want at least 3 for a snapshot larger than 16 MB", len(chunks))
	}
	rebuilt := make([]byte, 0, len(raw))
	for index, chunk := range chunks {
		if chunk.Index != index {
			t.Fatalf("chunk %d has index %d", index, chunk.Index)
		}
		if len(chunk.Payload) > stateChunkBytes {
			t.Fatalf("chunk %d has %d bytes, limit is %d", index, len(chunk.Payload), stateChunkBytes)
		}
		rebuilt = append(rebuilt, chunk.Payload...)
	}
	if !bytes.Equal(rebuilt, raw) {
		t.Fatal("chunked snapshot did not round-trip")
	}
	if snapshotDigest(rebuilt) != snapshotDigest(raw) {
		t.Fatal("chunked snapshot digest changed")
	}
}

func TestValidateSnapshotChunksRejectsIncompleteOrCorruptedSnapshots(t *testing.T) {
	raw := bytes.Repeat([]byte("snapshot"), stateChunkBytes/8+1)
	chunks := snapshotChunks("candidate", raw, time.Unix(0, 0).UTC())
	digest := snapshotDigest(raw)

	if _, err := validateSnapshotChunks(chunks, "candidate", len(chunks), digest); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	if _, err := validateSnapshotChunks(chunks[:len(chunks)-1], "candidate", len(chunks), digest); err == nil {
		t.Fatal("incomplete snapshot was accepted")
	}

	corrupted := append([]stateChunk(nil), chunks...)
	corrupted[0] = chunks[0]
	corrupted[0].Payload = append([]byte(nil), chunks[0].Payload...)
	corrupted[0].Payload[0] ^= 0xff
	if _, err := validateSnapshotChunks(corrupted, "candidate", len(corrupted), digest); err == nil {
		t.Fatal("snapshot with a mismatched digest was accepted")
	}

	wrongIndex := append([]stateChunk(nil), chunks...)
	wrongIndex[0] = chunks[0]
	wrongIndex[0].Index = 1
	if _, err := validateSnapshotChunks(wrongIndex, "candidate", len(wrongIndex), digest); err == nil {
		t.Fatal("snapshot with a non-contiguous index was accepted")
	}
}

func TestClassifySnapshotCommitIsConservative(t *testing.T) {
	candidate := stateHead{
		ID: StateDocumentID, Version: 4, SnapshotID: "candidate",
		ChunkCount: 2, SHA256: "digest",
	}
	tests := []struct {
		name     string
		current  stateHead
		expected snapshotCommitOutcome
	}{
		{
			name:     "candidate is authoritative",
			current:  candidate,
			expected: snapshotCommitConfirmed,
		},
		{
			name: "previous head remains authoritative",
			current: stateHead{
				ID: StateDocumentID, Version: 3, SnapshotID: "previous",
			},
			expected: snapshotCommitUnknown,
		},
		{
			name: "another writer won the same version",
			current: stateHead{
				ID: StateDocumentID, Version: 4, SnapshotID: "other",
			},
			expected: snapshotCommitRejected,
		},
		{
			name: "later authoritative version no longer references candidate",
			current: stateHead{
				ID: StateDocumentID, Version: 5, SnapshotID: "later",
			},
			expected: snapshotCommitRejected,
		},
		{
			name: "candidate metadata mismatch is not accepted",
			current: stateHead{
				ID: StateDocumentID, Version: 4, SnapshotID: "candidate",
				ChunkCount: 1, SHA256: "digest",
			},
			expected: snapshotCommitUnknown,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifySnapshotCommit(test.current, candidate, 3, "previous")
			if got != test.expected {
				t.Fatalf("outcome=%d, want %d", got, test.expected)
			}
		})
	}
}
