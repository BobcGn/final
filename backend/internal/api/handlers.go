package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BobcGn/final/backend/internal/command"
	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/store"
)

// maxRequestBody bounds a control request body. The largest defined body is a
// few dozen bytes; the limit stops a malformed client from allocating memory.
const maxRequestBody = 8192

// defaultQueryWindow is the history range used when the client supplies none.
const defaultQueryWindow = time.Hour

// listParams are the shared pagination parameters of the history routes.
type listParams struct {
	Range  store.TimeRange
	Limit  int
	Order  store.Order
	Cursor string
}

// parseListParams reads and validates the shared pagination parameters.
//
// A cursored request must repeat `from` and `to`: they have no stable default,
// so substituting "now minus one hour" would silently paginate a different
// series and the cursor scope check would reject it with a confusing message.
func (s *Server) parseListParams(r *http.Request) (listParams, error) {
	query := r.URL.Query()
	now := s.now().UTC()

	rawFrom := query.Get("from")
	rawTo := query.Get("to")
	cursor := query.Get("cursor")

	if cursor != "" && (rawFrom == "" || rawTo == "") {
		return listParams{}, newAPIError(http.StatusBadRequest, codeInvalidRequest,
			"a cursored request must repeat the first page's from and to parameters", map[string]any{
				"fields": []string{"from", "to"},
			})
	}

	from := now.Add(-defaultQueryWindow)
	if rawFrom != "" {
		parsed, err := parseTimestamp("from", rawFrom)
		if err != nil {
			return listParams{}, err
		}
		from = parsed
	}
	to := now
	if rawTo != "" {
		parsed, err := parseTimestamp("to", rawTo)
		if err != nil {
			return listParams{}, err
		}
		to = parsed
	}

	limit := store.DefaultPageLimit
	if rawLimit := query.Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil {
			return listParams{}, newAPIError(http.StatusBadRequest, codeInvalidRequest, "limit must be an integer", map[string]any{"field": "limit"})
		}
		if parsed < 1 || parsed > store.MaxPageLimit {
			return listParams{}, newAPIError(http.StatusBadRequest, codeInvalidRequest,
				fmt.Sprintf("limit must be between 1 and %d", store.MaxPageLimit), map[string]any{
					"field":   "limit",
					"minimum": 1,
					"maximum": store.MaxPageLimit,
				})
		}
		limit = parsed
	}

	order := store.OrderAsc
	if rawOrder := query.Get("order"); rawOrder != "" {
		order = store.Order(rawOrder)
		if !order.Valid() {
			return listParams{}, newAPIError(http.StatusBadRequest, codeInvalidRequest, "order must be asc or desc", map[string]any{"field": "order"})
		}
	}

	window := store.TimeRange{From: from, To: to}
	if err := window.Validate(); err != nil {
		return listParams{}, newAPIError(http.StatusBadRequest, codeInvalidRequest, err.Error(), map[string]any{
			"field":   "from/to",
			"maximum": store.MaxQuerySpan.String(),
		})
	}
	return listParams{Range: window, Limit: limit, Order: order, Cursor: cursor}, nil
}

// parseTimestamp parses an RFC 3339 timestamp parameter.
func parseTimestamp(field, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, newAPIError(http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("%s must be an RFC 3339 timestamp", field), map[string]any{"field": field})
	}
	return parsed.UTC(), nil
}

// handleDeviceStatus implements GET /api/v1/devices/{deviceId}/status.
func (s *Server) handleDeviceStatus(w http.ResponseWriter, r *http.Request) {
	deviceID, err := s.deviceID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	state, err := s.cfg.Store.Device(r.Context(), deviceID)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, newAPIError(http.StatusNotFound, codeDeviceNotFound, "device has never reported valid telemetry", map[string]any{"deviceId": deviceID}))
		return
	}
	if err != nil {
		s.fail(w, r, wrap(err, "the device state could not be read"))
		return
	}
	thresholds, err := s.cfg.Store.Thresholds(r.Context(), deviceID)
	if err != nil {
		s.fail(w, r, wrap(err, "the threshold record could not be read"))
		return
	}

	// The tracker is the live authority on connectivity; the stored value is a
	// cache that a restart seeds. Reporting the tracker avoids answering from a
	// snapshot that a sweep has not yet updated.
	connectivity := s.cfg.Tracker.State(deviceID)
	lastSeen := state.LastSeenAt
	if tracked, ok := s.cfg.Tracker.LastSeen(deviceID); ok {
		lastSeen = tracked
		connectivity = s.cfg.Tracker.State(deviceID)
	}

	alarmState := state.AlarmState
	if !alarmState.Valid() {
		alarmState = domain.AlertNormal
	}
	var lastSeenPtr *time.Time
	if !lastSeen.IsZero() {
		value := timestamp(lastSeen)
		lastSeenPtr = &value
	}

	writeJSON(w, http.StatusOK, deviceStatusResponse{
		DeviceID:            deviceID,
		Connectivity:        connectivity,
		AlarmState:          alarmState,
		LocalAlarm:          state.LocalAlarm,
		OfflineAfterSeconds: int(s.cfg.OfflineAfter / time.Second),
		LastSeenAt:          lastSeenPtr,
		ThresholdVersion: thresholdVersion{
			Desired:   thresholds.DesiredVersion,
			Confirmed: thresholds.ConfirmedVersion,
		},
	})
}

// handleLatestTelemetry implements GET /api/v1/devices/{deviceId}/telemetry/latest.
func (s *Server) handleLatestTelemetry(w http.ResponseWriter, r *http.Request) {
	deviceID, err := s.deviceID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sample, err := s.cfg.Store.LatestTelemetry(r.Context(), deviceID)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, newAPIError(http.StatusNotFound, codeDeviceNotFound, "no valid telemetry has been stored for this device", map[string]any{"deviceId": deviceID}))
		return
	}
	if err != nil {
		s.fail(w, r, wrap(err, "the latest telemetry could not be read"))
		return
	}
	writeJSON(w, http.StatusOK, newTelemetryPoint(sample))
}

// handleListTelemetry implements GET /api/v1/devices/{deviceId}/telemetry.
func (s *Server) handleListTelemetry(w http.ResponseWriter, r *http.Request) {
	deviceID, err := s.deviceID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	params, err := s.parseListParams(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.cfg.Store.ListTelemetry(r.Context(), store.TelemetryQuery{
		DeviceID: deviceID,
		Range:    params.Range,
		Limit:    params.Limit,
		Order:    params.Order,
		Cursor:   params.Cursor,
	})
	if errors.Is(err, store.ErrInvalidCursor) {
		s.fail(w, r, newAPIError(http.StatusBadRequest, codeInvalidRequest, err.Error(), map[string]any{"field": "cursor"}))
		return
	}
	if err != nil {
		s.fail(w, r, wrap(err, "the telemetry page could not be read"))
		return
	}

	items := make([]telemetryPoint, 0, len(page.Items))
	for _, sample := range page.Items {
		items = append(items, newTelemetryPoint(sample))
	}
	writeJSON(w, http.StatusOK, telemetryPage{Items: items, NextCursor: optionalString(page.NextCursor)})
}

// handleListAlerts implements GET /api/v1/devices/{deviceId}/alerts.
func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	deviceID, err := s.deviceID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	params, err := s.parseListParams(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	query := store.AlertQuery{
		DeviceID: deviceID,
		Range:    params.Range,
		Limit:    params.Limit,
		Order:    params.Order,
		Cursor:   params.Cursor,
	}
	if rawState := r.URL.Query().Get("state"); rawState != "" {
		query.State = domain.AlertState(rawState)
		if !query.State.Valid() {
			s.fail(w, r, newAPIError(http.StatusBadRequest, codeInvalidRequest,
				"state must be one of normal, suspect, fire_warning, recovered", map[string]any{"field": "state"}))
			return
		}
	}
	if rawActive := r.URL.Query().Get("active"); rawActive != "" {
		active, err := strconv.ParseBool(rawActive)
		if err != nil {
			s.fail(w, r, newAPIError(http.StatusBadRequest, codeInvalidRequest, "active must be a boolean", map[string]any{"field": "active"}))
			return
		}
		query.ActiveOnly = active
	}

	page, err := s.cfg.Store.ListAlerts(r.Context(), query)
	if errors.Is(err, store.ErrInvalidCursor) {
		s.fail(w, r, newAPIError(http.StatusBadRequest, codeInvalidRequest, err.Error(), map[string]any{"field": "cursor"}))
		return
	}
	if err != nil {
		s.fail(w, r, wrap(err, "the alert page could not be read"))
		return
	}

	items := make([]alertEventResource, 0, len(page.Items))
	for _, event := range page.Items {
		items = append(items, newAlertEventResource(event))
	}
	writeJSON(w, http.StatusOK, alertPage{Items: items, NextCursor: optionalString(page.NextCursor)})
}

// handleGetThresholds implements GET /api/v1/devices/{deviceId}/thresholds.
func (s *Server) handleGetThresholds(w http.ResponseWriter, r *http.Request) {
	deviceID, err := s.deviceID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	record, err := s.cfg.Store.Thresholds(r.Context(), deviceID)
	if err != nil {
		s.fail(w, r, wrap(err, "the threshold record could not be read"))
		return
	}
	writeJSON(w, http.StatusOK, newThresholdsResource(record))
}

// handleUpdateThresholds implements PUT /api/v1/devices/{deviceId}/thresholds.
//
// The 202 means the backend accepted and published the command. It does not mean
// the device wrote Flash; the client must wait for the acknowledgement or the
// timeout, which it can observe on the realtime stream or by polling the command
// resource.
func (s *Server) handleUpdateThresholds(w http.ResponseWriter, r *http.Request) {
	deviceID, err := s.deviceID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	idempotencyKey, err := requiredIdempotencyKey(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	var body thresholdUpdateRequest
	if err := decodeStrictBody(r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	missing := []string{}
	if body.TemperatureHighC == nil {
		missing = append(missing, "temperatureHighC")
	}
	if body.HumidityHighRh == nil {
		missing = append(missing, "humidityHighRh")
	}
	if body.GasHighPpm == nil {
		missing = append(missing, "gasHighPpm")
	}
	if len(missing) > 0 {
		s.fail(w, r, newAPIError(http.StatusBadRequest, codeInvalidRequest, "every threshold field is required", map[string]any{"missing": missing}))
		return
	}

	desired := domain.Thresholds{
		TemperatureHighC: *body.TemperatureHighC,
		HumidityHighRh:   *body.HumidityHighRh,
		GasHighPpm:       *body.GasHighPpm,
	}
	accepted, err := s.cfg.Commands.RequestThresholdUpdate(r.Context(), deviceID, desired, idempotencyKey, actorFrom(r.Context()))
	if err != nil {
		s.fail(w, r, mapCommandError(err))
		return
	}
	writeJSON(w, http.StatusAccepted, commandAcceptedResponse{
		RequestID:      accepted.RequestID,
		Status:         "pending",
		DesiredVersion: accepted.DesiredVersion,
		ExpiresAt:      timestampPtr(&accepted.ExpiresAt),
	})
}

// handleCommandStatus implements GET /api/v1/devices/{deviceId}/commands/{requestId}.
func (s *Server) handleCommandStatus(w http.ResponseWriter, r *http.Request) {
	deviceID, err := s.deviceID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	requestID := r.PathValue("requestId")
	if requestID == "" || len(requestID) > 64 {
		s.fail(w, r, newAPIError(http.StatusBadRequest, codeInvalidRequest, "requestId must be 1 to 64 characters", map[string]any{"field": "requestId"}))
		return
	}
	record, err := s.cfg.Commands.Command(r.Context(), deviceID, requestID)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, newAPIError(http.StatusNotFound, codeCommandNotFound, "no such command for this device", map[string]any{"requestId": requestID}))
		return
	}
	if err != nil {
		s.fail(w, r, wrap(err, "the command could not be read"))
		return
	}
	writeJSON(w, http.StatusOK, newCommandStatusResponse(record))
}

// mapCommandError converts a command service failure into the frozen HTTP error.
func mapCommandError(err error) error {
	switch {
	case errors.Is(err, command.ErrIdempotencyConflict):
		return newAPIError(http.StatusConflict, codeVersionConflict,
			"the Idempotency-Key was already used with a different payload", nil)
	case errors.Is(err, command.ErrPublishFailed):
		return &apiError{Status: http.StatusServiceUnavailable, Code: codeBrokerUnavailable,
			Message: "the control command could not be published to the broker", cause: err}
	case errors.Is(err, domain.ErrInvalidThreshold):
		return &apiError{Status: http.StatusUnprocessableEntity, Code: codeInvalidThreshold,
			Message: err.Error(), cause: err}
	case errors.Is(err, domain.ErrInvalidDeviceID), errors.Is(err, domain.ErrInvalidCommand):
		return &apiError{Status: http.StatusBadRequest, Code: codeInvalidRequest,
			Message: err.Error(), cause: err}
	case errors.Is(err, store.ErrConflict):
		return newAPIError(http.StatusConflict, codeVersionConflict, err.Error(), nil)
	default:
		return wrap(err, "the control command could not be accepted")
	}
}

// requiredIdempotencyKey reads the mandatory Idempotency-Key header.
func requiredIdempotencyKey(r *http.Request) (string, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return "", newAPIError(http.StatusBadRequest, codeInvalidRequest,
			"control requests require an Idempotency-Key header", map[string]any{"header": "Idempotency-Key"})
	}
	if len(key) > 64 {
		return "", newAPIError(http.StatusBadRequest, codeInvalidRequest,
			"Idempotency-Key must be at most 64 characters", map[string]any{"header": "Idempotency-Key"})
	}
	return key, nil
}

// decodeStrictBody decodes a JSON request body, rejecting unknown fields and
// trailing content. The frozen contract marks control bodies
// `additionalProperties: false`, so a misspelled field must fail loudly instead
// of being dropped and leaving the caller believing it took effect.
func decodeStrictBody(r *http.Request, dst any) error {
	if contentType := r.Header.Get("Content-Type"); contentType != "" && !strings.HasPrefix(contentType, "application/json") {
		return newAPIError(http.StatusBadRequest, codeInvalidRequest, "Content-Type must be application/json", nil)
	}
	limited := io.LimitReader(r.Body, maxRequestBody+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return newAPIError(http.StatusBadRequest, codeInvalidRequest, "the request body could not be read", nil)
	}
	if len(raw) > maxRequestBody {
		return newAPIError(http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("the request body must not exceed %d bytes", maxRequestBody), nil)
	}
	if len(raw) == 0 {
		return newAPIError(http.StatusBadRequest, codeInvalidRequest, "a JSON request body is required", nil)
	}

	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return newAPIError(http.StatusBadRequest, codeInvalidRequest, "the request body is not valid for this route", map[string]any{"detail": err.Error()})
	}
	if decoder.More() {
		return newAPIError(http.StatusBadRequest, codeInvalidRequest, "the request body contains trailing content", nil)
	}
	return nil
}

// optionalString renders an empty cursor as an explicit null. The contract types
// nextCursor as nullable, so omitting it entirely would be a different shape.
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
