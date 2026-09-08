package httpapi

import (
	"encoding/base64"
	"log/slog"
	"net/http/httptest"
	"testing"
)

func TestCanonicalRouteInventory(t *testing.T) {
	server := New(nil, "", slog.Default())
	// The two extra operations are the path-addressed reads: read_user and
	// read_sample_state, which are not registered in the POST route table.
	if got := len(server.routes) + 2; got != 42 {
		t.Fatalf("canonical operations=%d, want 42", got)
	}
	if _, ok := server.routes["POST /api/select-active-role"]; !ok {
		t.Fatal("server-side active-role selection is not registered")
	}
	for key, rt := range server.routes {
		if rt.operation == "" || rt.status < 200 || rt.status > 299 {
			t.Fatalf("invalid route %s: %+v", key, rt)
		}
	}
}

func TestWebSocketSubprotocolBearer(t *testing.T) {
	token := "opaque.session-token"
	encoded := base64.RawURLEncoding.EncodeToString([]byte(token))
	request := httptest.NewRequest("GET", "/api/events?since=1", nil)
	request.Header.Set("Sec-WebSocket-Protocol", "lab-logistics-v1, bearer."+encoded)
	if got := websocketToken(request); got != token {
		t.Fatalf("token=%q, want %q", got, token)
	}
}

func TestAuthorizationHeaderWinsForWebSocket(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/events", nil)
	request.Header.Set("Authorization", "Bearer direct-token")
	request.Header.Set("Sec-WebSocket-Protocol", "lab-logistics-v1, bearer.invalid")
	if got := websocketToken(request); got != "direct-token" {
		t.Fatalf("token=%q", got)
	}
}

func TestWebSocketOriginMustMatchHostExactly(t *testing.T) {
	request := httptest.NewRequest("GET", "http://127.0.0.1:18082/api/events", nil)
	request.Host = "127.0.0.1:18082"
	request.Header.Set("Origin", "http://127.0.0.1:18082")
	if !sameOriginWebSocket(request) {
		t.Fatal("same-origin websocket request was rejected")
	}
	request.Header.Set("Origin", "https://evil.invalid/?host=127.0.0.1:18082")
	if sameOriginWebSocket(request) {
		t.Fatal("spoofed websocket origin was accepted")
	}
}
