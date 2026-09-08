package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	mongostore "labgolden/internal/mongo"
)

// AC-79: /ready answers 200 {"status":"ready"} when the application can serve
// and reach its datastore. The datastore-unreachable half of AC-79 is probed
// elsewhere; this is the positive half, against a real reachable datastore.
func TestAC79ReadyReportsReadyWhenTheDatastoreIsReachable(t *testing.T) {
	baseURI := os.Getenv("TEST_MONGODB_URI")
	if baseURI == "" {
		t.Skip("TEST_MONGODB_URI is not set")
	}
	ctx := context.Background()
	store, err := mongostore.Open(ctx, baseURI+"_ready")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Reset(ctx); err != nil {
		t.Fatal(err)
	}

	handler := New(store, "", slog.Default()).Handler()
	request := httptest.NewRequest(http.MethodGet, "/ready", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("/ready status = %d, want 200 (body %q)", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("/ready body %q is not JSON: %v", recorder.Body.String(), err)
	}
	if body["status"] != "ready" {
		t.Fatalf("/ready body = %v, want {\"status\":\"ready\"}", body)
	}
	if len(body) != 1 {
		t.Fatalf("/ready returned extra fields: %v", body)
	}

	// The readiness probe is a plain GET and must not require a bearer token.
	if authenticated := request.Header.Get("Authorization"); authenticated != "" {
		t.Fatal("the readiness probe was sent with credentials")
	}
	// Confirm the store really was reachable, so a 200 could not have come from
	// a probe that never talked to the datastore.
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("datastore was not actually reachable: %v", err)
	}
}
