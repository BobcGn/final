// Package api exposes the frozen REST and WebSocket contract over the domain
// services.
//
// Handlers do three things and nothing else: parse and validate the request into
// domain values, call one service method, and render the result. Every failure
// becomes a typed apiError so that the error code in the response is decided in
// one place instead of being scattered across handlers.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/BobcGn/final/backend/internal/command"
	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/ingest"
	"github.com/BobcGn/final/backend/internal/liveness"
	"github.com/BobcGn/final/backend/internal/store"
)

// Frozen error codes, matching docs/api/openapi.yaml.
const (
	codeInvalidRequest    = "invalid_request"
	codeUnauthenticated   = "unauthenticated"
	codeForbidden         = "forbidden"
	codeDeviceNotFound    = "device_not_found"
	codeCommandNotFound   = "command_not_found"
	codeVersionConflict   = "version_conflict"
	codeInvalidThreshold  = "invalid_threshold"
	codeRateLimited       = "rate_limited"
	codeInternalError     = "internal_error"
	codeBrokerUnavailable = "broker_unavailable"
	codeDeviceAckTimeout  = "device_ack_timeout"
)

// FrozenErrorCodes returns every error code the API may return, sorted. The
// contract test compares this with the ErrorCode enum in
// docs/api/openapi.yaml; a code that exists in only one of the two places is a
// contract defect.
func FrozenErrorCodes() []string {
	codes := []string{
		codeBrokerUnavailable, codeCommandNotFound, codeDeviceAckTimeout,
		codeDeviceNotFound, codeForbidden, codeInternalError, codeInvalidRequest,
		codeInvalidThreshold, codeRateLimited, codeUnauthenticated,
		codeVersionConflict,
	}
	sort.Strings(codes)
	return codes
}

// AuthMode selects how requests are authenticated.
type AuthMode string

// Supported authentication modes.
const (
	// AuthNone disables authentication. It exists for local development only and
	// must never be used on a reachable deployment: this mode leaves the control
	// routes open to anyone who can reach the port.
	AuthNone AuthMode = "none"
	// AuthBearer requires a bearer token from the configured token set.
	AuthBearer AuthMode = "bearer"
)

// Config are the dependencies and settings of the HTTP server.
type Config struct {
	Store    store.Store
	Ingest   *ingest.Service
	Commands *command.Service
	Hub      *Hub
	Tracker  *liveness.Tracker
	Logger   *slog.Logger
	Now      func() time.Time

	// AuthMode selects the authentication scheme. An empty value means AuthNone
	// so that tests and local runs need no configuration; a deployment must set
	// it explicitly and the process logs a warning when it is left at none.
	AuthMode AuthMode
	// Tokens maps a bearer token to the actor name recorded in the command audit
	// trail.
	Tokens map[string]string

	// OfflineAfter is the silence after which a device is reported offline. It is
	// echoed in the status response so a client can render the same expectation.
	OfflineAfter time.Duration

	// MaxWebSocketClients bounds concurrent realtime connections per server.
	MaxWebSocketClients int
}

// Route describes one registered route. The route table is exported through
// Routes so the contract test can compare it against docs/api/openapi.yaml and
// fail when the two drift.
type Route struct {
	Method string
	Path   string
}

// Server serves the frozen contract.
type Server struct {
	cfg     Config
	logger  *slog.Logger
	now     func() time.Time
	routes  []Route
	handler http.Handler
}

// NewServer validates the configuration and builds the router.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("api: store is required")
	}
	if cfg.Ingest == nil {
		return nil, errors.New("api: ingest service is required")
	}
	if cfg.Commands == nil {
		return nil, errors.New("api: command service is required")
	}
	if cfg.Tracker == nil {
		return nil, errors.New("api: liveness tracker is required")
	}
	if cfg.Hub == nil {
		return nil, errors.New("api: realtime hub is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	if cfg.OfflineAfter <= 0 {
		cfg.OfflineAfter = liveness.DefaultConfig().OfflineAfter
	}
	if cfg.AuthMode == "" {
		cfg.AuthMode = AuthNone
	}
	switch cfg.AuthMode {
	case AuthNone, AuthBearer:
	default:
		return nil, fmt.Errorf("api: unknown auth mode %q", cfg.AuthMode)
	}

	server := &Server{cfg: cfg, logger: logger, now: now}
	server.handler = server.buildRouter()
	return server, nil
}

// Routes returns the registered route table in documentation order.
func (s *Server) Routes() []Route { return append([]Route(nil), s.routes...) }

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// buildRouter registers every frozen route.
//
// The method-and-path patterns rely on the Go 1.22 ServeMux, which matches
// methods and path wildcards directly. That keeps the router on the standard
// library, so the routing behaviour under test is the routing behaviour in
// production rather than a framework's interpretation of it.
func (s *Server) buildRouter() http.Handler {
	mux := http.NewServeMux()

	register := func(method, path string, handler http.HandlerFunc, authenticated bool) {
		s.routes = append(s.routes, Route{Method: method, Path: path})
		wrapped := handler
		if authenticated {
			wrapped = s.requireAuth(handler)
		}
		mux.HandleFunc(method+" "+path, wrapped)
	}

	// Liveness stays unauthenticated: an orchestrator probing the process must
	// not need a credential, and the endpoint exposes nothing but a constant.
	register(http.MethodGet, "/healthz", s.handleHealth, false)

	register(http.MethodGet, "/api/v1/devices/{deviceId}/status", s.handleDeviceStatus, true)
	register(http.MethodGet, "/api/v1/devices/{deviceId}/telemetry/latest", s.handleLatestTelemetry, true)
	register(http.MethodGet, "/api/v1/devices/{deviceId}/telemetry", s.handleListTelemetry, true)
	register(http.MethodGet, "/api/v1/devices/{deviceId}/alerts", s.handleListAlerts, true)
	register(http.MethodGet, "/api/v1/devices/{deviceId}/thresholds", s.handleGetThresholds, true)
	register(http.MethodPut, "/api/v1/devices/{deviceId}/thresholds", s.handleUpdateThresholds, true)
	register(http.MethodGet, "/api/v1/devices/{deviceId}/commands/{requestId}", s.handleCommandStatus, true)
	register(http.MethodGet, "/ws/v1/devices/{deviceId}/telemetry", s.handleWebSocket, true)

	// The remote-mute route was deleted. Without this, POST to the historical
	// URL falls through to GET /commands/{requestId} and ServeMux answers 405.
	// Old clients must see a plain 404 that the route is gone. Registered
	// outside register(): it is not part of the frozen route table and does
	// not implement mute.
	mux.HandleFunc("POST /api/v1/devices/{deviceId}/commands/mute", http.NotFound)

	return mux
}

// requireAuth enforces the configured authentication scheme.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AuthMode == AuthNone {
			next(w, r)
			return
		}
		token, ok := bearerToken(r)
		if !ok {
			s.fail(w, r, newAPIError(http.StatusUnauthorized, codeUnauthenticated, "a bearer token is required", nil))
			return
		}
		actor, ok := s.lookupToken(token)
		if !ok {
			s.fail(w, r, newAPIError(http.StatusUnauthorized, codeUnauthenticated, "the bearer token is not valid", nil))
			return
		}
		next(w, r.WithContext(withActor(r.Context(), actor)))
	}
}

// lookupToken resolves a bearer token to an actor name using a constant-time
// comparison, so that a timing side channel cannot be used to recover a token
// byte by byte.
func (s *Server) lookupToken(token string) (string, bool) {
	for candidate, actor := range s.cfg.Tokens {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			return actor, true
		}
	}
	return "", false
}

// bearerToken extracts the bearer credential from the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// handleHealth reports process liveness only.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// deviceID reads and validates the path parameter.
func (s *Server) deviceID(r *http.Request) (string, error) {
	deviceID := r.PathValue("deviceId")
	if err := domain.ValidateDeviceID(deviceID); err != nil {
		return "", newAPIError(http.StatusBadRequest, codeInvalidRequest, "deviceId must match ^[A-Za-z0-9_-]{1,32}$", map[string]any{
			"field": "deviceId",
		})
	}
	return deviceID, nil
}

// apiError is a failure with a frozen HTTP status and error code.
type apiError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
	// cause is the internal error. It is logged but never sent to the client,
	// which must not learn about storage or broker internals.
	cause error
}

// Error implements the error interface.
func (e *apiError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the internal cause for logging and errors.Is.
func (e *apiError) Unwrap() error { return e.cause }

// newAPIError builds an apiError.
func newAPIError(status int, code, message string, details map[string]any) *apiError {
	return &apiError{Status: status, Code: code, Message: message, Details: details}
}

// wrap converts an internal failure into a 500 without leaking it to the client.
func wrap(err error, message string) *apiError {
	return &apiError{Status: http.StatusInternalServerError, Code: codeInternalError, Message: message, cause: err}
}

// fail renders an error response, mapping any error that is not already an
// apiError onto a 500 so that a handler can return domain and store errors
// directly without every call site repeating the mapping.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		apiErr = wrap(err, "the request could not be completed")
	}
	if apiErr.Status >= http.StatusInternalServerError {
		s.logger.LogAttrs(r.Context(), slog.LevelError, "request failed",
			slog.String("error", apiErr.Error()),
			slog.String("request_id", requestID(r)))
	}
	body := errorEnvelope{Error: errorBody{
		Code:      apiErr.Code,
		Message:   apiErr.Message,
		RequestID: requestID(r),
		Details:   apiErr.Details,
	}}
	writeJSON(w, apiErr.Status, body)
}

// writeJSON renders a JSON response body.
func writeJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}

// requestID returns the client trace id, generating none if the client did not
// supply one so that responses are never attributed to an invented identifier.
func requestID(r *http.Request) string {
	return r.Header.Get("X-Request-ID")
}

// actorKey is the context key under which the authenticated actor is stored.
type actorKey struct{}

// withActor attaches the authenticated actor to the request context.
func withActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// actorFrom returns the authenticated actor, or an empty string in AuthNone mode.
func actorFrom(ctx context.Context) string {
	actor, _ := ctx.Value(actorKey{}).(string)
	return actor
}
