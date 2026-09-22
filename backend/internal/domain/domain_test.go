package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
)

// instant is a fixed time so validation cases are reproducible.
var instant = time.Date(2026, 9, 18, 11, 20, 0, 0, time.UTC)

// TestValidateDeviceID covers the frozen identifier contract.
func TestValidateDeviceID(t *testing.T) {
	cases := map[string]bool{
		"MCU001":                            true,
		"A":                                 true,
		"lab-room-1":                        true,
		"device_with_underscore":            true,
		"0123456789012345678901234567890":   true, // 31 characters
		"01234567890123456789012345678901":  true, // exactly 32
		"012345678901234567890123456789012": false,
		"":                                  false,
		"has space":                         false,
		"has/slash":                         false,
		"has.dot":                           false,
		"has:colon":                         false,
		"naïve":                             false,
		"'; DROP TABLE devices; --":         false,
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			err := domain.ValidateDeviceID(input)
			if want && err != nil {
				t.Fatalf("ValidateDeviceID(%q) = %v, want nil", input, err)
			}
			if !want {
				if err == nil {
					t.Fatalf("ValidateDeviceID(%q) = nil, want an error", input)
				}
				if !errors.Is(err, domain.ErrInvalidDeviceID) {
					t.Fatalf("ValidateDeviceID(%q) = %v, want ErrInvalidDeviceID", input, err)
				}
			}
		})
	}
}

// TestValidateBootID covers the boot identifier used in the dedup key.
func TestValidateBootID(t *testing.T) {
	cases := map[string]bool{
		"9f3ac21b":          true,
		"a":                 true,
		"0123456789abcdef":  true,
		"0123456789abcdef0": false,
		"":                  false,
		"has-dash":          false,
		"has space":         false,
	}
	for input, want := range cases {
		err := domain.ValidateBootID(input)
		if want && err != nil {
			t.Fatalf("ValidateBootID(%q) = %v, want nil", input, err)
		}
		if !want && err == nil {
			t.Fatalf("ValidateBootID(%q) = nil, want an error", input)
		}
	}
}

// validTelemetry returns a sample that passes every check, so each case can
// perturb exactly one field.
func validTelemetry() domain.Telemetry {
	gas := 25.0
	timestamp := instant
	return domain.Telemetry{
		DeviceID:         "MCU001",
		BootID:           "boot1",
		Sequence:         7,
		Timestamp:        &timestamp,
		UptimeMs:         125000,
		TemperatureC:     28,
		HumidityRh:       61,
		GasAdcRaw:        1350,
		GasAdcFiltered:   1328,
		GasPpm:           &gas,
		GasCalibrated:    false,
		LocalAlarm:       false,
		AlarmCauses:      []domain.AlarmCause{},
		Network:          domain.NetworkOnline,
		ThresholdVersion: 3,
		SensorFault:      false,
		ReceivedAt:       instant,
	}
}

// TestTelemetryValidateAcceptsTheContractExample verifies the baseline used by
// every rejection case below.
func TestTelemetryValidateAcceptsTheContractExample(t *testing.T) {
	if err := validTelemetry().Validate(); err != nil {
		t.Fatalf("the documented sample was rejected: %v", err)
	}
}

// TestTelemetryValidateRejects covers every range, enum and consistency rule.
func TestTelemetryValidateRejects(t *testing.T) {
	cases := map[string]func(*domain.Telemetry){
		"empty device id":        func(s *domain.Telemetry) { s.DeviceID = "" },
		"invalid device id":      func(s *domain.Telemetry) { s.DeviceID = "has space" },
		"missing boot id":        func(s *domain.Telemetry) { s.BootID = "" },
		"temperature too low":    func(s *domain.Telemetry) { s.TemperatureC = domain.MinTemperatureC - 1 },
		"temperature too high":   func(s *domain.Telemetry) { s.TemperatureC = domain.MaxTemperatureC + 1 },
		"temperature NaN":        func(s *domain.Telemetry) { s.TemperatureC = nan() },
		"humidity too high":      func(s *domain.Telemetry) { s.HumidityRh = 101 },
		"humidity below zero":    func(s *domain.Telemetry) { s.HumidityRh = -1 },
		"raw ADC above range":    func(s *domain.Telemetry) { s.GasAdcRaw = domain.MaxGasAdc + 1 },
		"raw ADC below range":    func(s *domain.Telemetry) { s.GasAdcRaw = -1 },
		"filtered ADC overflow":  func(s *domain.Telemetry) { s.GasAdcFiltered = 4096 },
		"negative gas ppm":       func(s *domain.Telemetry) { value := -1.0; s.GasPpm = &value },
		"infinite gas ppm":       func(s *domain.Telemetry) { value := inf(); s.GasPpm = &value },
		"unknown network":        func(s *domain.Telemetry) { s.Network = "halfway" },
		"zero threshold version": func(s *domain.Telemetry) { s.ThresholdVersion = 0 },
		"unknown alarm cause":    func(s *domain.Telemetry) { s.AlarmCauses = []domain.AlarmCause{"zombies"} },
		"fault without cause":    func(s *domain.Telemetry) { s.SensorFault = true },
		"alarm without cause": func(s *domain.Telemetry) {
			s.LocalAlarm = true
			s.AlarmCauses = []domain.AlarmCause{}
		},
		"missing receive time": func(s *domain.Telemetry) { s.ReceivedAt = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			sample := validTelemetry()
			mutate(&sample)
			err := sample.Validate()
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if !errors.Is(err, domain.ErrInvalidTelemetry) && !errors.Is(err, domain.ErrInvalidDeviceID) {
				t.Fatalf("%s returned %v, want an ErrInvalidTelemetry or ErrInvalidDeviceID", name, err)
			}
		})
	}
}

// TestTelemetryAcceptsConsistentAlarms verifies the positive branches that the
// rejection table exercises negatively.
func TestTelemetryAcceptsConsistentAlarms(t *testing.T) {
	cases := map[string]func(*domain.Telemetry){
		"local alarm with a cause": func(s *domain.Telemetry) {
			s.LocalAlarm = true
			s.AlarmCauses = []domain.AlarmCause{domain.AlarmGasHigh}
		},
		"sensor fault with its cause": func(s *domain.Telemetry) {
			s.SensorFault = true
			s.AlarmCauses = []domain.AlarmCause{domain.AlarmSensorFault}
		},
		"null timestamp":    func(s *domain.Telemetry) { s.Timestamp = nil },
		"null gas estimate": func(s *domain.Telemetry) { s.GasPpm = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			sample := validTelemetry()
			mutate(&sample)
			if err := sample.Validate(); err != nil {
				t.Fatalf("%s was rejected: %v", name, err)
			}
		})
	}
}

// TestEventTimeFallsBackToReceivedAt verifies the ordering key when the device
// clock is not synced.
func TestEventTimeFallsBackToReceivedAt(t *testing.T) {
	sample := validTelemetry()
	sample.Timestamp = nil
	sample.ReceivedAt = instant.Add(time.Minute)

	if !sample.EventTime().Equal(sample.ReceivedAt) {
		t.Fatalf("EventTime = %s, want the receive time %s", sample.EventTime(), sample.ReceivedAt)
	}
	if source := sample.EventTimeSource(); source != "receivedAt" {
		t.Fatalf("EventTimeSource = %q, want receivedAt", source)
	}

	sample.Timestamp = &instant
	if !sample.EventTime().Equal(instant) {
		t.Fatalf("EventTime = %s, want the device time %s", sample.EventTime(), instant)
	}
	if source := sample.EventTimeSource(); source != "device" {
		t.Fatalf("EventTimeSource = %q, want device", source)
	}
}

// TestTelemetryKeyIncludesBootID is the property that keeps a restart from
// being mistaken for a redelivery.
func TestTelemetryKeyIncludesBootID(t *testing.T) {
	first := validTelemetry()
	second := validTelemetry()
	second.BootID = "boot2"

	if first.Key() == second.Key() {
		t.Fatal("two boots with the same sequence produced the same dedup key")
	}
}

// TestThresholdsValidate covers the frozen ranges.
func TestThresholdsValidate(t *testing.T) {
	valid := domain.Thresholds{TemperatureHighC: 30, HumidityHighRh: 80, GasHighPpm: 80}
	if err := valid.Validate(); err != nil {
		t.Fatalf("documented thresholds were rejected: %v", err)
	}

	cases := map[string]domain.Thresholds{
		"temperature below range": {TemperatureHighC: -1, HumidityHighRh: 80, GasHighPpm: 80},
		"temperature above range": {TemperatureHighC: 81, HumidityHighRh: 80, GasHighPpm: 80},
		"humidity above 100":      {TemperatureHighC: 30, HumidityHighRh: 101, GasHighPpm: 80},
		"humidity below zero":     {TemperatureHighC: 30, HumidityHighRh: -0.5, GasHighPpm: 80},
		"gas at zero":             {TemperatureHighC: 30, HumidityHighRh: 80, GasHighPpm: 0},
		"gas above range":         {TemperatureHighC: 30, HumidityHighRh: 80, GasHighPpm: 1000},
		"gas NaN":                 {TemperatureHighC: 30, HumidityHighRh: 80, GasHighPpm: nan()},
	}
	for name, thresholds := range cases {
		t.Run(name, func(t *testing.T) {
			err := thresholds.Validate()
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if !errors.Is(err, domain.ErrInvalidThreshold) {
				t.Fatalf("%s returned %v, want ErrInvalidThreshold", name, err)
			}
		})
	}

	// The frozen bounds themselves must be accepted.
	edge := domain.Thresholds{TemperatureHighC: 0, HumidityHighRh: 0, GasHighPpm: 1}
	if err := edge.Validate(); err != nil {
		t.Fatalf("lower bounds were rejected: %v", err)
	}
	edge = domain.Thresholds{TemperatureHighC: 80, HumidityHighRh: 100, GasHighPpm: 999}
	if err := edge.Validate(); err != nil {
		t.Fatalf("upper bounds were rejected: %v", err)
	}
}

// TestDefaultThresholdsMatchFirmware verifies that the backend's notion of the
// compile-time defaults stays aligned with app_config.h. A drift here would make
// GET /thresholds report limits the device is not actually enforcing.
func TestDefaultThresholdsMatchFirmware(t *testing.T) {
	defaults := domain.DefaultThresholds()
	if defaults.TemperatureHighC != 30 {
		t.Fatalf("default temperatureHighC = %v, want 30 (app_config.h)", defaults.TemperatureHighC)
	}
	if defaults.HumidityHighRh != 80 {
		t.Fatalf("default humidityHighRh = %v, want 80 (app_config.h)", defaults.HumidityHighRh)
	}
	if defaults.GasHighPpm != 20 {
		t.Fatalf("default gasHighPpm = %v, want 20 (app_config.h)", defaults.GasHighPpm)
	}
	if err := defaults.Validate(); err != nil {
		t.Fatalf("the defaults are outside the frozen range: %v", err)
	}
}

// TestThresholdsEqual verifies the idempotency comparison.
func TestThresholdsEqual(t *testing.T) {
	base := domain.Thresholds{TemperatureHighC: 30, HumidityHighRh: 80, GasHighPpm: 80}
	same := base
	if !base.Equal(same) {
		t.Fatal("identical thresholds compared unequal")
	}
	different := base
	different.GasHighPpm = 81
	if base.Equal(different) {
		t.Fatal("different thresholds compared equal")
	}
}

// TestCommandTransitionTable is the command state machine, including the rule
// that a terminal state is final.
func TestCommandTransitionTable(t *testing.T) {
	cases := []struct {
		from domain.CommandState
		to   domain.CommandState
		want bool
	}{
		{domain.CommandAccepted, domain.CommandPublished, true},
		{domain.CommandAccepted, domain.CommandPublishFailed, true},
		{domain.CommandAccepted, domain.CommandTimedOut, true},
		{domain.CommandAccepted, domain.CommandApplied, false},
		{domain.CommandAccepted, domain.CommandRejected, false},

		{domain.CommandPublished, domain.CommandApplied, true},
		{domain.CommandPublished, domain.CommandRejected, true},
		{domain.CommandPublished, domain.CommandExpired, true},
		{domain.CommandPublished, domain.CommandDuplicate, true},
		{domain.CommandPublished, domain.CommandFailed, true},
		{domain.CommandPublished, domain.CommandTimedOut, true},
		{domain.CommandPublished, domain.CommandPublishFailed, false},
		{domain.CommandPublished, domain.CommandPublished, false},
	}
	for _, testCase := range cases {
		got := domain.CanTransition(testCase.from, testCase.to)
		if got != testCase.want {
			t.Errorf("CanTransition(%s, %s) = %v, want %v", testCase.from, testCase.to, got, testCase.want)
		}
	}

	// Terminal states are final: a late acknowledgement must not reopen a
	// command whose outcome the client may already have observed.
	for _, terminal := range []domain.CommandState{
		domain.CommandApplied, domain.CommandRejected, domain.CommandExpired,
		domain.CommandDuplicate, domain.CommandFailed, domain.CommandTimedOut,
		domain.CommandPublishFailed,
	} {
		if !terminal.Terminal() {
			t.Errorf("%s is not reported as terminal", terminal)
		}
		for _, target := range []domain.CommandState{
			domain.CommandAccepted, domain.CommandPublished, domain.CommandApplied,
			domain.CommandRejected, domain.CommandTimedOut,
		} {
			if domain.CanTransition(terminal, target) {
				t.Errorf("CanTransition(%s, %s) allowed leaving a terminal state", terminal, target)
			}
		}
	}

	// Unknown states never transition.
	if domain.CanTransition("nonsense", domain.CommandPublished) {
		t.Error("an unknown source state was allowed to transition")
	}
	if domain.CanTransition(domain.CommandAccepted, "nonsense") {
		t.Error("an unknown target state was allowed")
	}
}

// TestCommandStateVocabulary verifies the accepted and published distinction the
// contract is built on.
func TestCommandStateVocabulary(t *testing.T) {
	if !domain.CommandAccepted.Valid() || !domain.CommandPublished.Valid() {
		t.Fatal("accepted and published must be valid states")
	}
	if domain.CommandAccepted.Terminal() || domain.CommandPublished.Terminal() {
		t.Fatal("accepted and published must not be terminal: the device has not answered yet")
	}
	if domain.CommandState("pending").Valid() {
		t.Fatal("the device acknowledgement vocabulary leaked into the backend states")
	}
}

// TestApplyAck covers the acknowledgement rules, including the statuses that
// must and must not carry an error code.
func TestApplyAck(t *testing.T) {
	version := 4

	t.Run("applied", func(t *testing.T) {
		patch, err := domain.ApplyAck(domain.AckApplied, "", &version, instant)
		if err != nil {
			t.Fatalf("applied was rejected: %v", err)
		}
		if patch.State != domain.CommandApplied {
			t.Fatalf("state = %s, want applied", patch.State)
		}
		if patch.ConfirmedVersion == nil || *patch.ConfirmedVersion != version {
			t.Fatalf("confirmed version = %v, want %d", patch.ConfirmedVersion, version)
		}
	})

	t.Run("duplicate keeps the reported version", func(t *testing.T) {
		patch, err := domain.ApplyAck(domain.AckDuplicate, "", &version, instant)
		if err != nil {
			t.Fatalf("duplicate was rejected: %v", err)
		}
		if patch.State != domain.CommandDuplicate {
			t.Fatalf("state = %s, want duplicate", patch.State)
		}
	})

	t.Run("rejected requires an error code", func(t *testing.T) {
		if _, err := domain.ApplyAck(domain.AckRejected, "", nil, instant); err == nil {
			t.Fatal("rejected without an error code was accepted")
		}
		patch, err := domain.ApplyAck(domain.AckRejected, string(domain.AckErrOutOfRange), &version, instant)
		if err != nil {
			t.Fatalf("rejected with a valid error code was refused: %v", err)
		}
		if patch.State != domain.CommandRejected {
			t.Fatalf("state = %s, want rejected", patch.State)
		}
		// A rejection never changed the device configuration, so echoing the
		// version back would claim a write that did not happen.
		if patch.ConfirmedVersion != nil {
			t.Fatalf("a rejected command reported confirmed version %v", *patch.ConfirmedVersion)
		}
	})

	t.Run("failed requires an error code", func(t *testing.T) {
		if _, err := domain.ApplyAck(domain.AckFailed, "", nil, instant); err == nil {
			t.Fatal("failed without an error code was accepted")
		}
		patch, err := domain.ApplyAck(domain.AckFailed, string(domain.AckErrFlashWriteFailed), &version, instant)
		if err != nil {
			t.Fatalf("failed with a valid error code was refused: %v", err)
		}
		if patch.ConfirmedVersion != nil {
			t.Fatalf("a failed command reported confirmed version %v", *patch.ConfirmedVersion)
		}
	})

	t.Run("expired requires no error code and keeps no version", func(t *testing.T) {
		patch, err := domain.ApplyAck(domain.AckExpired, "", &version, instant)
		if err != nil {
			t.Fatalf("expired was rejected: %v", err)
		}
		if patch.ConfirmedVersion != nil {
			t.Fatalf("an expired command reported confirmed version %v", *patch.ConfirmedVersion)
		}
	})

	t.Run("success must not carry an error code", func(t *testing.T) {
		if _, err := domain.ApplyAck(domain.AckApplied, string(domain.AckErrOutOfRange), &version, instant); err == nil {
			t.Fatal("applied with an error code was accepted")
		}
	})

	t.Run("unknown values are refused", func(t *testing.T) {
		if _, err := domain.ApplyAck("invented", "", nil, instant); err == nil {
			t.Fatal("an unknown status was accepted")
		}
		if _, err := domain.ApplyAck(domain.AckRejected, "invented", nil, instant); err == nil {
			t.Fatal("an unknown error code was accepted")
		}
	})
}

// TestAlertEvidenceValidate covers the alert event consistency rules.
func TestAlertEvidenceValidate(t *testing.T) {
	valid := domain.AlertEvent{
		ID:        "01ALERT",
		DeviceID:  "MCU001",
		State:     domain.AlertFireWarning,
		StartedAt: instant,
		Evidence: domain.AlertEvidence{
			GasAdcRise: 200, GasAdcRiseThreshold: 150,
			TemperatureRateCPerMinute: 4, TemperatureRateThresholdCPerMinute: 3,
			SampleCount: 8, WindowSeconds: 60,
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a well-formed alert was rejected: %v", err)
	}

	cases := map[string]func(*domain.AlertEvent){
		"bad device id":     func(e *domain.AlertEvent) { e.DeviceID = "" },
		"unknown state":     func(e *domain.AlertEvent) { e.State = "panic" },
		"missing start":     func(e *domain.AlertEvent) { e.StartedAt = time.Time{} },
		"zero sample count": func(e *domain.AlertEvent) { e.Evidence.SampleCount = 0 },
		"zero window":       func(e *domain.AlertEvent) { e.Evidence.WindowSeconds = 0 },
		"end before start":  func(e *domain.AlertEvent) { earlier := instant.Add(-time.Second); e.EndedAt = &earlier },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			event := valid
			mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

// TestAlertStateActive verifies which states count as running.
func TestAlertStateActive(t *testing.T) {
	if !domain.AlertSuspect.Active() || !domain.AlertFireWarning.Active() {
		t.Fatal("suspect and fire_warning must count as active")
	}
	if domain.AlertNormal.Active() || domain.AlertRecovered.Active() {
		t.Fatal("normal and recovered must not count as active")
	}
	if domain.AlertState("acknowledged").Valid() {
		t.Fatal("acknowledged is out of phase-1 scope and must not validate")
	}
}

// nan and inf build the non-finite values JSON can produce.
func nan() float64 {
	var zero float64
	return zero / zero
}

// inf builds a positive infinity.
func inf() float64 {
	var zero float64
	return 1 / zero
}
