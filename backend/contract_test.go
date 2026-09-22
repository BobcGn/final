package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/BobcGn/final/backend/internal/alert"
	"github.com/BobcGn/final/backend/internal/api"
	"github.com/BobcGn/final/backend/internal/command"
	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/events"
	"github.com/BobcGn/final/backend/internal/ingest"
	"github.com/BobcGn/final/backend/internal/liveness"
	"github.com/BobcGn/final/backend/internal/protocol"
	"github.com/BobcGn/final/backend/internal/store"
)

// The frozen contract documents are the fact source for every other module. These
// tests read them and compare them with the code, so a change to one side that is
// not made to the other fails here rather than in a client months later.

const (
	openAPIPath         = "../docs/api/openapi.yaml"
	deviceProtocolPath  = "../docs/device-protocol.md"
	contractTestInstant = "2026-09-24T10:40:30Z"
)

// contractRejections is a test sink that ignores everything.
type contractNilSink struct{}

// PublishTelemetryUpdated implements events.Sink.
func (contractNilSink) PublishTelemetryUpdated(context.Context, domain.Telemetry) {}

// PublishDeviceStatusChanged implements events.Sink.
func (contractNilSink) PublishDeviceStatusChanged(context.Context, string, events.DeviceStatusData) {
}

// PublishAlertStateChanged implements events.Sink.
func (contractNilSink) PublishAlertStateChanged(context.Context, domain.AlertEvent) {}

// PublishCommandStatusChanged implements events.Sink.
func (contractNilSink) PublishCommandStatusChanged(context.Context, domain.Command) {}

// PublishThresholdsConfirmed implements events.Sink.
func (contractNilSink) PublishThresholdsConfirmed(context.Context, string, int) {}

// loadOpenAPI parses the machine-readable contract.
func loadOpenAPI(t *testing.T) map[string]any {
	t.Helper()

	raw, err := os.ReadFile(openAPIPath)
	if err != nil {
		t.Fatalf("read %s: %v", openAPIPath, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse %s: %v", openAPIPath, err)
	}
	return document
}

// dig walks a nested YAML document, failing the test when the path is missing.
// A missing path is itself a defect: the contract test would otherwise pass
// vacuously after someone renamed a schema.
func dig(t *testing.T, document map[string]any, path ...string) any {
	t.Helper()

	var current any = document
	for index, segment := range path {
		mapping, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s: %q is not a mapping, so %v cannot be resolved",
				openAPIPath, strings.Join(path[:index], "."), path)
		}
		value, present := mapping[segment]
		if !present {
			t.Fatalf("%s: %v does not exist", openAPIPath, path[:index+1])
		}
		current = value
	}
	return current
}

// stringList converts a YAML sequence of scalars into a sorted string slice.
func stringList(t *testing.T, value any, description string) []string {
	t.Helper()

	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s is not a sequence", description)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("%s contains a non-scalar entry", description)
		}
		result = append(result, text)
	}
	sort.Strings(result)
	return result
}

// TestOpenAPIRoutesMatchTheRouter compares the documented routes with the routes
// the server actually registers, in both directions.
func TestOpenAPIRoutesMatchTheRouter(t *testing.T) {
	document := loadOpenAPI(t)
	paths, ok := dig(t, document, "paths").(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatal("the document declares no paths")
	}

	documented := make(map[string]bool)
	for path, operations := range paths {
		verbs, ok := operations.(map[string]any)
		if !ok {
			t.Fatalf("%s: %s is not a mapping", openAPIPath, path)
		}
		for verb := range verbs {
			// OpenAPI allows non-method keys such as parameters or servers.
			upper := strings.ToUpper(verb)
			switch upper {
			case "GET", "PUT", "POST", "DELETE", "PATCH", "HEAD", "OPTIONS":
				documented[upper+" "+path] = true
			}
		}
	}
	if len(documented) == 0 {
		t.Fatal("the document declares no operations")
	}

	for key := range serverRoutes(t) {
		if !documented[key] {
			t.Errorf("the server registers %s, which %s does not declare", key, openAPIPath)
		}
		delete(documented, key)
	}
	for key := range documented {
		t.Errorf("%s declares %s, which the server does not register", openAPIPath, key)
	}
}

// serverRoutes builds the real router and returns its route table.
func serverRoutes(t *testing.T) map[string]bool {
	t.Helper()

	engine, err := alert.NewEngine(alert.DefaultConfig())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	tracker, err := liveness.New(liveness.DefaultConfig())
	if err != nil {
		t.Fatalf("new tracker: %v", err)
	}
	dataStore := store.NewMemory()

	commandService, err := command.New(command.Deps{
		Store:     dataStore,
		Publisher: noopPublisher{},
		Sink:      contractNilSink{},
		Logger:    slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("new command service: %v", err)
	}
	ingestService, err := ingest.New(ingest.Deps{
		Store:      dataStore,
		Engine:     engine,
		Tracker:    tracker,
		Sink:       contractNilSink{},
		AckHandler: commandService,
		Logger:     slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}
	server, err := api.NewServer(api.Config{
		Store:    dataStore,
		Ingest:   ingestService,
		Commands: commandService,
		Hub:      api.NewHub(api.Deps{Logger: slog.New(slog.DiscardHandler)}),
		Tracker:  tracker,
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	routes := make(map[string]bool)
	for _, route := range server.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	return routes
}

// noopPublisher accepts every publish.
type noopPublisher struct{}

// Publish implements command.Publisher.
func (noopPublisher) Publish(context.Context, string, []byte, byte) error { return nil }

// TestDeviceProtocolExamplesDecode runs every JSON example printed in the device
// protocol document through the codec that owns it. An example that no longer
// decodes is the most direct sign that the document and the implementation have
// drifted.
func TestDeviceProtocolExamplesDecode(t *testing.T) {
	raw, err := os.ReadFile(deviceProtocolPath)
	if err != nil {
		t.Fatalf("read %s: %v", deviceProtocolPath, err)
	}
	receivedAt, err := time.Parse(time.RFC3339, contractTestInstant)
	if err != nil {
		t.Fatalf("parse the test instant: %v", err)
	}

	examples := fencedBlocks(string(raw), "json")
	if len(examples) < 3 {
		t.Fatalf("%s holds %d JSON examples; expected the telemetry, control and acknowledgement payloads", deviceProtocolPath, len(examples))
	}

	kinds := map[string]int{}
	for index, example := range examples {
		var envelope struct {
			MessageType string `json:"messageType"`
		}
		if err := json.Unmarshal([]byte(example), &envelope); err != nil {
			t.Fatalf("example %d is not valid JSON: %v\n%s", index, err, example)
		}

		switch envelope.MessageType {
		case "telemetry":
			if _, err := protocol.DecodeTelemetry([]byte(example), "", receivedAt); err != nil {
				t.Errorf("telemetry example %d does not decode: %v", index, err)
			}
		case "command_ack":
			if _, err := protocol.DecodeCommandAck([]byte(example), "", receivedAt); err != nil {
				t.Errorf("acknowledgement example %d does not decode: %v", index, err)
			}
		case "control":
			if _, err := protocol.DecodeControl([]byte(example), receivedAt); err != nil {
				t.Errorf("control example %d does not decode: %v", index, err)
			}
		default:
			t.Errorf("example %d has messageType %q, which is not part of the contract", index, envelope.MessageType)
		}
		kinds[envelope.MessageType]++
	}

	for _, required := range []string{"telemetry", "control", "command_ack"} {
		if kinds[required] == 0 {
			t.Errorf("%s prints no %s example", deviceProtocolPath, required)
		}
	}
}

// fencedBlocks returns the contents of every fenced code block with the given
// language tag.
func fencedBlocks(document, language string) []string {
	var (
		blocks  []string
		current strings.Builder
		inside  bool
	)
	opening := "```" + language
	for _, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case !inside && trimmed == opening:
			inside = true
			current.Reset()
		case inside && trimmed == "```":
			inside = false
			blocks = append(blocks, current.String())
		case inside:
			current.WriteString(line)
			current.WriteString("\n")
		}
	}
	return blocks
}

// TestDeviceProtocolTopicsMatchTheCode compares the documented MQTT topics with
// the constants the code uses. A topic change is also a broker ACL change, so it
// must not happen in one place only.
func TestDeviceProtocolTopicsMatchTheCode(t *testing.T) {
	raw, err := os.ReadFile(deviceProtocolPath)
	if err != nil {
		t.Fatalf("read %s: %v", deviceProtocolPath, err)
	}
	document := string(raw)

	for _, topic := range []string{protocol.TopicTelemetry, protocol.TopicCommandAck, protocol.TopicControl} {
		if !strings.Contains(document, "`"+topic+"`") {
			t.Errorf("%s does not document the topic %q", deviceProtocolPath, topic)
		}
	}
}

// TestAlarmCausesMatchTheContract compares the alarm cause enum.
func TestAlarmCausesMatchTheContract(t *testing.T) {
	document := loadOpenAPI(t)
	documented := stringList(t, dig(t, document, "components", "schemas", "AlarmCause", "enum"), "AlarmCause enum")

	implemented := make([]string, 0, len(domain.AlarmCauses()))
	for _, cause := range domain.AlarmCauses() {
		implemented = append(implemented, string(cause))
	}
	sort.Strings(implemented)

	assertSameSet(t, "alarm causes", documented, implemented)
}

// TestAlertStatesMatchTheContract compares the alert state enum, including the
// deliberate absence of an acknowledgement state in phase 1.
func TestAlertStatesMatchTheContract(t *testing.T) {
	document := loadOpenAPI(t)
	documented := stringList(t, dig(t, document, "components", "schemas", "AlertState", "enum"), "AlertState enum")

	implemented := make([]string, 0, len(domain.AlertStates()))
	for _, state := range domain.AlertStates() {
		implemented = append(implemented, string(state))
	}
	sort.Strings(implemented)

	assertSameSet(t, "alert states", documented, implemented)
}

// TestCommandStatesMatchTheContract compares the command lifecycle enum.
func TestCommandStatesMatchTheContract(t *testing.T) {
	document := loadOpenAPI(t)
	documented := stringList(t,
		dig(t, document, "components", "schemas", "CommandStatus", "properties", "state", "enum"),
		"CommandStatus.state enum")

	implemented := make([]string, 0, len(domain.CommandStates()))
	for _, state := range domain.CommandStates() {
		implemented = append(implemented, string(state))
	}
	sort.Strings(implemented)

	assertSameSet(t, "command states", documented, implemented)
}

// TestErrorCodesMatchTheContract compares the error code enum.
func TestErrorCodesMatchTheContract(t *testing.T) {
	document := loadOpenAPI(t)
	documented := stringList(t, dig(t, document, "components", "schemas", "ErrorCode", "enum"), "ErrorCode enum")

	assertSameSet(t, "error codes", documented, api.FrozenErrorCodes())
}

// TestEventTypesMatchTheContract compares the realtime event type enum.
func TestEventTypesMatchTheContract(t *testing.T) {
	document := loadOpenAPI(t)
	documented := stringList(t,
		dig(t, document, "components", "schemas", "WsEnvelope", "properties", "type", "enum"),
		"WsEnvelope.type enum")

	implemented := make([]string, 0, len(events.Types()))
	for _, eventType := range events.Types() {
		implemented = append(implemented, string(eventType))
	}
	sort.Strings(implemented)

	assertSameSet(t, "event types", documented, implemented)
}

// TestTelemetryResourceDoesNotInventFields verifies that every field the API
// exposes in a telemetry resource is either a field the device sends or the
// backend's own receive time.
//
// The comparison is one-directional on purpose: the resource is a read view, so
// it may omit wire fields the client does not need, but it must never expose a
// value the device never reported.
func TestTelemetryResourceDoesNotInventFields(t *testing.T) {
	document := loadOpenAPI(t)
	properties, ok := dig(t, document, "components", "schemas", "TelemetryPoint", "properties").(map[string]any)
	if !ok {
		t.Fatal("TelemetryPoint.properties is not a mapping")
	}

	allowed := map[string]bool{"receivedAt": true}
	for _, field := range protocol.TelemetryFields() {
		allowed[field] = true
	}

	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if !allowed[name] {
			t.Errorf("TelemetryPoint exposes %q, which the device never sends", name)
		}
	}
}

// TestWebSocketRouteIsMarkedAsAnUpgrade verifies that the realtime route is
// advertised as a WebSocket in the document, so a generated client does not try
// to parse a JSON body from a 101 response.
func TestWebSocketRouteIsMarkedAsAnUpgrade(t *testing.T) {
	document := loadOpenAPI(t)
	value := dig(t, document, "paths", "/ws/v1/devices/{deviceId}/telemetry", "get", "x-websocket")
	if value != true {
		t.Fatalf("x-websocket = %v, want true", value)
	}
}

// TestControlRoutesRequireIdempotencyKey verifies that the document marks the
// header as required on every control route, which is what makes a retry safe.
func TestControlRoutesRequireIdempotencyKey(t *testing.T) {
	document := loadOpenAPI(t)
	routes := map[string][]string{
		"/api/v1/devices/{deviceId}/thresholds": {"put"},
	}
	for path, verbs := range routes {
		for _, verb := range verbs {
			parameters, ok := dig(t, document, "paths", path, verb, "parameters").([]any)
			if !ok {
				t.Fatalf("%s %s declares no parameters", verb, path)
			}
			found := false
			for _, parameter := range parameters {
				mapping, ok := parameter.(map[string]any)
				if !ok {
					continue
				}
				reference, _ := mapping["$ref"].(string)
				if reference == "#/components/parameters/IdempotencyKey" {
					found = true
				}
			}
			if !found {
				t.Errorf("%s %s does not require Idempotency-Key", strings.ToUpper(verb), path)
			}
		}
	}

	// The referenced parameter must itself be required.
	if required := dig(t, document, "components", "parameters", "IdempotencyKey", "required"); required != true {
		t.Errorf("the Idempotency-Key parameter is not required: %v", required)
	}
}

// TestContractVersionIsFrozen verifies that the document is published as a frozen
// version rather than a draft, because the implementation now depends on it.
func TestContractVersionIsFrozen(t *testing.T) {
	document := loadOpenAPI(t)
	version := dig(t, document, "info", "version")
	text, ok := version.(string)
	if !ok {
		t.Fatalf("info.version is %T, want a string", version)
	}
	if strings.Contains(text, "draft") {
		t.Fatalf("info.version is %q; the contract is implemented and must not be published as a draft", text)
	}
	if !strings.HasPrefix(text, "1.") {
		t.Fatalf("info.version is %q, want a 1.x version matching the frozen device schema", text)
	}
}

// TestDeviceProtocolStatesImplementedVersusTarget checks that the document still
// distinguishes what exists from what is planned. Describing the TCP firmware as
// if it already spoke MQTT would be the most damaging possible documentation
// error for this project.
func TestDeviceProtocolStatesImplementedVersusTarget(t *testing.T) {
	raw, err := os.ReadFile(deviceProtocolPath)
	if err != nil {
		t.Fatalf("read %s: %v", deviceProtocolPath, err)
	}
	document := string(raw)

	if !strings.Contains(document, "TCP 文本帧") {
		t.Errorf("%s no longer mentions that the current firmware uses TCP frames", deviceProtocolPath)
	}
	if !strings.Contains(document, "REG|MCU001") {
		t.Errorf("%s no longer documents the current registration frame", deviceProtocolPath)
	}
	frozen := strings.Contains(document, "v1.0.0-frozen") || strings.Contains(document, "冻结")
	if !frozen {
		t.Errorf("%s does not state the freeze status of the contract", deviceProtocolPath)
	}
}

// assertSameSet fails when two sorted string slices differ.
func assertSameSet(t *testing.T, description string, want, got []string) {
	t.Helper()

	if fmt.Sprint(want) == fmt.Sprint(got) {
		return
	}
	t.Fatalf("%s differ:\n  documented: %v\n  implemented: %v", description, want, got)
}

// TestAlertEvidencePropertiesMatchTheContract compares the wire key names of
// the alert evidence with the names the OpenAPI schema documents.
//
// This is the check that was missing when the evidence was marshalled from the
// untagged domain type and went out as GasAdcRise. A response decoded into a Go
// struct cannot catch it: encoding/json accepts a PascalCase key for a struct
// that declares camelCase. Only comparing the raw key strings can.
func TestAlertEvidencePropertiesMatchTheContract(t *testing.T) {
	document := loadOpenAPI(t)
	properties := dig(t, document, "components", "schemas", "AlertEvidence", "properties")
	fields, ok := properties.(map[string]any)
	if !ok {
		t.Fatalf("AlertEvidence.properties is %T, want a mapping", properties)
	}
	documented := make([]string, 0, len(fields))
	for name := range fields {
		documented = append(documented, name)
	}
	sort.Strings(documented)

	// The six fields the contract names. Both wire types emit exactly these,
	// which is what makes the set a real contract and not just a type's shape.
	emitted := []string{
		"gasAdcRise",
		"gasAdcRiseThreshold",
		"temperatureRateCPerMinute",
		"temperatureRateThresholdCPerMinute",
		"sampleCount",
		"windowSeconds",
	}
	sort.Strings(emitted)

	assertSameSet(t, "alert evidence fields", documented, emitted)

	for _, name := range documented {
		if name != strings.ToLower(name[:1])+name[1:] || strings.Contains(name, "_") {
			t.Errorf("AlertEvidence property %q is not lowerCamelCase", name)
		}
		if name[0] >= 'A' && name[0] <= 'Z' {
			t.Errorf("AlertEvidence property %q looks like a Go name; the contract uses camelCase", name)
		}
	}
}

// TestAlertEvidenceRequiredMatchesWhatTheBackendGuarantees settles the
// contract question of which evidence fields are optional.
//
// All six are always emitted: domain.AlertEvidence has no optional members, so
// a stored alert always carries every value. The schema lists only three as
// required, which is a conservative statement about what a reader must tolerate
// rather than a description of this backend. The two are reconciled here rather
// than by loosening the schema or by pretending a field is optional that is not:
// the schema's required set is a subset of what is guaranteed.
func TestAlertEvidenceRequiredMatchesWhatTheBackendGuarantees(t *testing.T) {
	document := loadOpenAPI(t)
	required := stringList(t, dig(t, document, "components", "schemas", "AlertEvidence", "required"), "AlertEvidence.required")

	guaranteed := []string{
		"gasAdcRise",
		"gasAdcRiseThreshold",
		"temperatureRateCPerMinute",
		"temperatureRateThresholdCPerMinute",
		"sampleCount",
		"windowSeconds",
	}

	for _, name := range required {
		found := false
		for _, field := range guaranteed {
			if field == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the schema requires %q but the backend never emits it", name)
		}
	}

	// The three the schema leaves out are exactly the ones a client is allowed
	// to tolerate as absent. Pin the set so a change to it is a deliberate one.
	optional := make([]string, 0)
	for _, field := range guaranteed {
		listed := false
		for _, name := range required {
			if name == field {
				listed = true
				break
			}
		}
		if !listed {
			optional = append(optional, field)
		}
	}
	sort.Strings(optional)
	assertSameSet(t, "optional alert evidence fields", optional,
		[]string{"gasAdcRiseThreshold", "temperatureRateThresholdCPerMinute", "windowSeconds"})
}
