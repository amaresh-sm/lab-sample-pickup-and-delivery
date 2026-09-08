package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"labgolden/internal/core"
	mongostore "labgolden/internal/mongo"
	"labgolden/internal/service"
)

type route struct {
	operation string
	method    string
	status    int
}

type Server struct {
	store    *mongostore.Store
	service  *service.Service
	frontend string
	logger   *slog.Logger
	routes   map[string]route
}

func New(store *mongostore.Store, frontend string, logger *slog.Logger) *Server {
	s := &Server{store: store, service: &service.Service{Store: store}, frontend: frontend, logger: logger, routes: map[string]route{}}
	s.add(http.MethodPost, "/api/authenticate-user", "authenticate_user", 200)
	s.add(http.MethodPost, "/api/select-active-role", "select_active_role", 200)
	s.add(http.MethodPost, "/api/create-user", "create_user", 201)
	s.add(http.MethodPost, "/api/update-user-access", "update_user_access", 201)
	s.add(http.MethodPost, "/api/create-location", "create_location", 201)
	s.add(http.MethodPost, "/api/update-location", "update_location", 201)
	s.add(http.MethodPost, "/api/set-location-status", "set_location_status", 200)
	s.add(http.MethodPost, "/api/set-location-function-status", "set_location_function_status", 200)
	s.add(http.MethodPost, "/api/create-sample-type", "create_sample_type", 201)
	s.add(http.MethodPost, "/api/update-sample-type", "update_sample_type", 201)
	s.add(http.MethodPost, "/api/configure-laboratory-acceptance", "configure_laboratory_acceptance", 200)
	s.add(http.MethodPost, "/api/create-connection", "create_connection", 201)
	s.add(http.MethodPost, "/api/set-connection-status", "set_connection_status", 200)
	s.add(http.MethodPost, "/api/configure-capacity", "configure_capacity", 200)
	s.add(http.MethodPost, "/api/set-worker-availability", "set_worker_availability", 200)
	s.add(http.MethodPost, "/api/check-serviceability", "check_serviceability", 200)
	s.add(http.MethodPost, "/api/read-serviceability-snapshot", "read_serviceability_snapshot", 200)
	s.add(http.MethodPost, "/api/create-collection-request", "create_collection_request", 201)
	s.add(http.MethodPost, "/api/ingest-presealed-sample", "ingest_presealed_sample", 201)
	s.add(http.MethodPost, "/api/read-sample-lifecycle", "read_sample_lifecycle", 200)
	s.add(http.MethodPost, "/api/read-journey-plan", "read_journey_plan", 200)
	s.add(http.MethodPost, "/api/edit-active-plan", "edit_active_plan", 201)
	s.add(http.MethodPost, "/api/split-journey", "split_journey", 201)
	s.add(http.MethodPost, "/api/merge-journeys", "merge_journeys", 201)
	s.add(http.MethodPost, "/api/cancel-journey", "cancel_journey", 201)
	s.add(http.MethodPost, "/api/reroute-journey", "reroute_journey", 201)
	s.add(http.MethodPost, "/api/accept-assignment", "accept_assignment", 201)
	s.add(http.MethodPost, "/api/decline-assignment", "decline_assignment", 201)
	s.add(http.MethodPost, "/api/collect-and-seal-sample", "collect_and_seal_sample", 201)
	s.add(http.MethodPost, "/api/initiate-handoff", "initiate_handoff", 201)
	s.add(http.MethodPost, "/api/confirm-handoff", "confirm_handoff", 201)
	s.add(http.MethodPost, "/api/update-assignment-progress", "update_assignment_progress", 201)
	s.add(http.MethodPost, "/api/record-handling-checkpoint", "record_handling_checkpoint", 201)
	s.add(http.MethodPost, "/api/record-worker-location", "record_worker_location", 201)
	s.add(http.MethodPost, "/api/confirm-laboratory-receipt", "confirm_laboratory_receipt", 201)
	s.add(http.MethodPost, "/api/record-laboratory-exception", "record_laboratory_exception", 201)
	s.add(http.MethodPost, "/api/report-disruption", "report_disruption", 201)
	s.add(http.MethodPost, "/api/list-role-work", "list_role_work", 200)
	s.add(http.MethodPost, "/api/read-operations-state", "read_operations_state", 200)
	s.add(http.MethodPost, "/api/search-audit-trail", "search_audit_trail", 200)
	return s
}

// Maintain advances durable assignment/deadline timers.
func (s *Server) Maintain(ctx context.Context) error {
	return s.service.Maintain(ctx)
}

func (s *Server) add(method, path, operation string, status int) {
	s.routes[method+" "+path] = route{operation, method, status}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", s.ready)
	mux.HandleFunc("/api/events", s.events)
	mux.HandleFunc("/api/ui/bootstrap", s.bootstrap)
	mux.HandleFunc("/api/read-user/", s.readUser)
	mux.HandleFunc("/api/read-sample-state/", s.readSample)
	mux.HandleFunc("/api/", s.api)
	mux.HandleFunc("/map-assets/", s.mapAsset)
	mux.HandleFunc("/", s.frontendHandler)
	return s.securityHeaders(s.accessLog(mux))
}

func (s *Server) mapAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, core.Fail(405, "method_not_allowed", "GET required"))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/map-assets/")
	cleanName := filepath.Clean(name)
	if name == "" || cleanName != name || filepath.IsAbs(cleanName) || strings.HasPrefix(cleanName, "..") {
		http.NotFound(w, r)
		return
	}
	root := os.Getenv("OFFLINE_MAP_ASSET_DIR")
	if root == "" {
		root = "/opt/arena/map-assets"
	}
	http.ServeFile(w, r, filepath.Join(root, cleanName))
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, 503, core.Map{"status": "starting"})
		return
	}
	writeJSON(w, 200, core.Map{"status": "ready"})
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.routes[r.Method+" "+r.URL.Path]
	if !ok {
		writeError(w, core.Fail(404, "not_found", "operation not found"))
		return
	}
	in, err := decodeBody(r)
	if err != nil {
		writeError(w, err)
		return
	}
	p := core.Principal{}
	if rt.operation != "authenticate_user" {
		p, err = s.authenticate(r)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	out, err := s.service.Handle(r.Context(), rt.operation, in, p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, rt.status, core.Map{"data": out})
}

func (s *Server) readUser(w http.ResponseWriter, r *http.Request) {
	s.pathRead(w, r, "read_user", "userId", strings.TrimPrefix(r.URL.Path, "/api/read-user/"))
}
func (s *Server) readSample(w http.ResponseWriter, r *http.Request) {
	s.pathRead(w, r, "read_sample_state", "sampleId", strings.TrimPrefix(r.URL.Path, "/api/read-sample-state/"))
}
func (s *Server) pathRead(w http.ResponseWriter, r *http.Request, operation, field, id string) {
	if r.Method != http.MethodGet || id == "" || strings.Contains(id, "/") {
		writeError(w, core.Fail(404, "not_found", "operation not found"))
		return
	}
	p, err := s.authenticate(r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.service.Handle(r.Context(), operation, core.Map{field: id}, p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, core.Map{"data": out})
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, core.Fail(405, "method_not_allowed", "GET required"))
		return
	}
	p, err := s.authenticate(r)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.service.Bootstrap(r.Context(), p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, core.Map{"data": out})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, core.Fail(405, "method_not_allowed", "GET required"))
		return
	}
	token := websocketToken(r)
	p, err := s.store.Principal(r.Context(), token)
	if err != nil {
		writeError(w, err)
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if !websocket.IsWebSocketUpgrade(r) {
		events, err := s.store.EventsSince(r.Context(), since, 200, p)
		if err != nil {
			writeError(w, err)
			return
		}
		cursor := since
		if len(events) > 0 {
			cursor = int64(core.Float(events[len(events)-1]["id"]))
		}
		writeJSON(w, 200, core.Map{
			"data": core.Map{"events": events, "cursor": cursor},
		})
		return
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize: 1024, WriteBufferSize: 4096,
		Subprotocols: []string{"lab-logistics-v1"},
		CheckOrigin:  sameOriginWebSocket,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1024)
	_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(70 * time.Second)) })
	updates, cancel := s.store.Subscribe()
	defer cancel()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		events, err := s.store.EventsSince(r.Context(), since, 200, p)
		if err != nil {
			return
		}
		for _, event := range events {
			if err := conn.WriteJSON(event); err != nil {
				return
			}
			since = int64(core.Float(event["id"]))
		}
		select {
		case <-r.Context().Done():
			return
		case <-updates:
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte("keepalive"), time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}
}

func sameOriginWebSocket(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") &&
		strings.EqualFold(parsed.Host, r.Host)
}

func (s *Server) authenticate(r *http.Request) (core.Principal, error) {
	return s.store.Principal(r.Context(), bearerToken(r))
}
func bearerToken(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
}

func websocketToken(r *http.Request) string {
	if token := bearerToken(r); token != "" {
		return token
	}
	for _, protocol := range websocket.Subprotocols(r) {
		if !strings.HasPrefix(protocol, "bearer.") {
			continue
		}
		encoded := strings.TrimPrefix(protocol, "bearer.")
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err == nil && len(raw) > 0 {
			return string(raw)
		}
	}
	return ""
}

func decodeBody(r *http.Request) (core.Map, error) {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var in core.Map
	if err := decoder.Decode(&in); err != nil {
		return nil, core.Fail(400, "invalid_json", "request body must be one JSON object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, core.Fail(400, "invalid_json", "request body must contain exactly one JSON object")
	}
	return in, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error) {
	var apiErr *core.HTTPError
	if errors.As(err, &apiErr) {
		writeJSON(w, apiErr.Status, core.Map{"error": apiErr.Error(), "code": apiErr.Code})
		return
	}
	writeJSON(w, 500, core.Map{"error": "internal server error", "code": "internal_error"})
}

func (s *Server) frontendHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	clean := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.frontend, clean)
	if clean == "." {
		path = filepath.Join(s.frontend, "index.html")
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		path = filepath.Join(s.frontend, "index.html")
	}
	http.ServeFile(w, r, path)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; connect-src 'self' ws: wss:; form-action 'self'; frame-ancestors 'none'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; worker-src 'self' blob:")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}
