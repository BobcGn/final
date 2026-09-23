// Package main implements a device simulator for integration testing.
//
// It stands in for the STM32 firmware over the real MQTT contract described in
// docs/device-protocol.md: it connects, subscribes to device/control, publishes
// telemetry on device/telemetry, validates every command the way the firmware
// must, and answers on device/command-ack with the frozen status vocabulary.
//
// It exists because the parts of the system that need a real board are the
// sampling and the radio, not the protocol. A simulator that speaks the frozen
// contract exactly lets the backend half of the end-to-end path be exercised
// without hardware, and lets the failure modes a board cannot produce on demand
// - duplicate delivery, out-of-order samples, a device that goes silent, a clock
// that is not synced - be produced deliberately.
//
// It is deliberately strict about what it accepts, because a simulator that is
// more permissive than the firmware would hide contract defects rather than
// expose them.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/mqtt"
	"github.com/BobcGn/final/backend/internal/protocol"
)

// Default values for the device parameters the contract fixes.
const (
	// DefaultReportInterval is the five-second telemetry period of §5.2.
	DefaultReportInterval = 5 * time.Second
	// DefaultKeepAlive is the thirty-second MQTT keep-alive of §1.
	DefaultKeepAlive = 30 * time.Second
	// DefaultThresholdVersion is the compile-time default of §4.3, so a
	// simulated device never reports version zero.
	DefaultThresholdVersion = 1
)

// Sample is one telemetry reading the simulator will publish.
type Sample struct {
	TemperatureC   float64
	HumidityRh     float64
	GasAdcRaw      int
	GasAdcFiltered int
	GasPpm         *float64
	LocalAlarm     bool
	AlarmCauses    []domain.AlarmCause
	SensorFault    bool
}

// QuietSample is a reading below every threshold.
func QuietSample() Sample {
	gas := 25.0
	return Sample{
		TemperatureC:   25,
		HumidityRh:     50,
		GasAdcRaw:      1000,
		GasAdcFiltered: 1000,
		GasPpm:         &gas,
		AlarmCauses:    []domain.AlarmCause{},
	}
}

// GasSurgeSample is a reading whose gas estimate exceeds the default limit, with
// the matching cause set so the payload is internally consistent.
func GasSurgeSample() Sample {
	gas := 500.0
	return Sample{
		TemperatureC:   26,
		HumidityRh:     52,
		GasAdcRaw:      3000,
		GasAdcFiltered: 2950,
		GasPpm:         &gas,
		LocalAlarm:     true,
		AlarmCauses:    []domain.AlarmCause{domain.AlarmGasHigh},
	}
}

// Faults are the failure modes the simulator can produce on demand.
type Faults struct {
	// DuplicateEvery republishes every Nth sample a second time, to exercise the
	// dedup key. Zero disables it.
	DuplicateEvery int
	// SkipEvery drops every Nth sample, leaving a gap the backend must tolerate.
	SkipEvery int
	// UnsyncedClock publishes a null timestamp instead of a device time.
	UnsyncedClock bool
	// SilentSeconds pauses publishing for that long, to let the backend declare
	// the device offline without stopping the MQTT session.
	SilentSeconds int
}

// Config configures a simulator.
type Config struct {
	BrokerAddress string
	Username      string
	Password      string
	DeviceID      string
	BootID        string
	Interval      time.Duration
	KeepAlive     time.Duration
	Logger        *slog.Logger
	// Scenario returns the sample for a step index, so a test can drive a
	// deterministic series rather than a random one.
	Scenario func(step int) Sample
	Faults   Faults
}

// Acknowledgement is a command acknowledgement the simulator published, retained
// so a test can assert on what the device answered.
type Acknowledgement struct {
	RequestID string
	Status    string
	ErrorCode string
	// ReportsVersion records whether this acknowledgement carries the device's
	// threshold version. Only a set_thresholds acknowledgement may, because only
	// that command can change it: reporting it for any other command would tell
	// the backend the device adopted a configuration the command never carried.
	ReportsVersion bool
}

// Simulator is a device stand-in. It is safe for concurrent use.
type Simulator struct {
	cfg    Config
	client *mqtt.Client
	logger *slog.Logger

	mu              sync.Mutex
	sequence        uint32
	step            int
	thresholds      domain.Thresholds
	thresholdVer    int
	received        []domain.Command
	sentAcks        []Acknowledgement
	handledRequests map[string]Acknowledgement
}

// New builds a simulator. It does not connect.
func New(cfg Config) (*Simulator, error) {
	if cfg.BrokerAddress == "" {
		return nil, errors.New("device-sim: broker address is required")
	}
	if err := domain.ValidateDeviceID(cfg.DeviceID); err != nil {
		return nil, fmt.Errorf("device-sim: %w", err)
	}
	// The contract requires the clientId to equal the deviceId, because that is
	// what lets a broker ACL bind a session to a device.
	if len(cfg.DeviceID) > 23 {
		return nil, fmt.Errorf("device-sim: deviceId %q exceeds the 23-byte MQTT client identifier limit", cfg.DeviceID)
	}
	if err := domain.ValidateBootID(cfg.BootID); err != nil {
		return nil, fmt.Errorf("device-sim: %w", err)
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultReportInterval
	}
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = DefaultKeepAlive
	}
	if cfg.Scenario == nil {
		cfg.Scenario = func(int) Sample { return QuietSample() }
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	sim := &Simulator{
		cfg:             cfg,
		logger:          logger,
		thresholds:      domain.DefaultThresholds(),
		thresholdVer:    DefaultThresholdVersion,
		handledRequests: make(map[string]Acknowledgement),
	}

	client, err := mqtt.New(mqtt.Config{
		Address:       cfg.BrokerAddress,
		ClientID:      cfg.DeviceID,
		Username:      cfg.Username,
		Password:      cfg.Password,
		KeepAlive:     cfg.KeepAlive,
		CleanSession:  true,
		Logger:        logger,
		ReconnectMin:  100 * time.Millisecond,
		ReconnectMax:  time.Second,
		Subscriptions: []mqtt.TopicFilter{{Topic: protocol.TopicControl, QoS: 1}},
		Handler:       sim.handleMessage,
	})
	if err != nil {
		return nil, err
	}
	sim.client = client
	return sim, nil
}

// Run connects, subscribes and publishes until ctx is cancelled.
func (s *Simulator) Run(ctx context.Context) error {
	return s.client.Run(ctx)
}

// WaitConnected blocks until the session is up or ctx is done. A test needs this
// before publishing, because the client reports connected only after its
// subscription is confirmed.
func (s *Simulator) WaitConnected(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if s.client.Connected() {
				return nil
			}
		}
	}
}

// PublishOnce sends one telemetry sample and advances the sequence.
func (s *Simulator) PublishOnce(ctx context.Context) error {
	s.mu.Lock()
	sample := s.cfg.Scenario(s.step)
	s.step++
	sequence := s.sequence
	s.sequence++
	thresholdVer := s.thresholdVer
	s.mu.Unlock()

	payload, err := s.telemetryPayload(sample, sequence, thresholdVer)
	if err != nil {
		return err
	}
	if err := s.client.Publish(ctx, protocol.TopicTelemetry, payload, 1); err != nil {
		return err
	}

	// A duplicate is the same frame published again, which is what a QoS 1
	// redelivery looks like at the backend. It must be counted and dropped
	// rather than stored twice.
	if s.cfg.Faults.DuplicateEvery > 0 && (int(sequence)+1)%s.cfg.Faults.DuplicateEvery == 0 {
		if err := s.client.Publish(ctx, protocol.TopicTelemetry, payload, 1); err != nil {
			return err
		}
	}
	return nil
}

// telemetryPayload renders one sample through the frozen codec. Using the same
// encoder the device tests use means the simulator cannot drift from the
// contract silently.
func (s *Simulator) telemetryPayload(sample Sample, sequence uint32, thresholdVer int) ([]byte, error) {
	network := domain.NetworkOnline
	if !s.client.Connected() {
		network = domain.NetworkReconnecting
	}
	sample_ := domain.Telemetry{
		DeviceID:         s.cfg.DeviceID,
		BootID:           s.cfg.BootID,
		Sequence:         sequence,
		UptimeMs:         uint64(sequence) * uint64(s.cfg.Interval/time.Millisecond),
		TemperatureC:     sample.TemperatureC,
		HumidityRh:       sample.HumidityRh,
		GasAdcRaw:        sample.GasAdcRaw,
		GasAdcFiltered:   sample.GasAdcFiltered,
		GasPpm:           sample.GasPpm,
		GasCalibrated:    false,
		LocalAlarm:       sample.LocalAlarm,
		AlarmCauses:      sample.AlarmCauses,
		Network:          network,
		ThresholdVersion: thresholdVer,
		SensorFault:      sample.SensorFault,
		ReceivedAt:       time.Now().UTC(),
	}
	if !s.cfg.Faults.UnsyncedClock {
		// A synced device reports its event time. The simulator uses the wall
		// clock, which is what an NTP-synced device would do.
		now := time.Now().UTC().Truncate(time.Second)
		sample_.Timestamp = &now
	}
	return protocol.EncodeTelemetry(sample_)
}

// RunReporting publishes until ctx is cancelled, honouring the fault settings.
func (s *Simulator) RunReporting(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	silentUntil := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if s.cfg.Faults.SilentSeconds > 0 && time.Now().Before(silentUntil) {
				continue
			}
			s.mu.Lock()
			step := s.step
			s.mu.Unlock()
			if s.cfg.Faults.SkipEvery > 0 && step%s.cfg.Faults.SkipEvery == 0 {
				// Advance the sequence anyway: a skipped sample is a gap in the
				// stream, not a reuse of its number.
				s.mu.Lock()
				s.step++
				s.sequence++
				s.mu.Unlock()
				continue
			}
			if err := s.PublishOnce(ctx); err != nil {
				s.logger.LogAttrs(ctx, slog.LevelWarn, "publish failed", slog.String("error", err.Error()))
			}
			if s.cfg.Faults.SilentSeconds > 0 && silentUntil.IsZero() {
				silentUntil = time.Now().Add(time.Duration(s.cfg.Faults.SilentSeconds) * time.Second)
			}
		}
	}
}

// handleMessage applies one inbound control command the way the firmware must.
func (s *Simulator) handleMessage(ctx context.Context, message mqtt.Message) {
	if message.Topic != protocol.TopicControl {
		return
	}
	command, err := protocol.DecodeControl(message.Payload, time.Now().UTC())
	if err != nil {
		// A malformed command cannot be acknowledged: the contract requires the
		// acknowledgement to carry the requestId, which could not be read.
		s.logger.LogAttrs(ctx, slog.LevelWarn, "control command rejected before parsing",
			slog.String("error", err.Error()))
		return
	}

	ack := s.applyCommand(command)
	s.recordAck(ack)
	s.publishAck(ctx, ack)
}

// applyCommand validates and applies a command, returning the acknowledgement.
//
// The validation order follows docs/device-protocol.md §4.1: identity, then
// duplication, then the deadline, then the type and its values. A step that
// fails returns immediately and produces no side effect.
func (s *Simulator) applyCommand(command domain.Command) Acknowledgement {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.received = append(s.received, command)

	// A command addressed to another device is refused rather than acted on.
	if command.DeviceID != s.cfg.DeviceID {
		return Acknowledgement{RequestID: command.RequestID, Status: string(domain.AckRejected), ErrorCode: string(domain.AckErrDeviceMismatch)}
	}
	// A requestId already handled returns its original result, so a redelivery
	// cannot be executed twice. The whole acknowledgement is remembered rather
	// than only its status, because the error code has to be repeated too.
	if previous, seen := s.handledRequests[command.RequestID]; seen {
		previous.Status = string(domain.AckDuplicate)
		return previous
	}
	// The device has no trusted clock, so a deadline that is not after the issue
	// time is unusable: the window the backend declared is the only basis the
	// device has.
	if !command.ExpiresAt.After(command.AcceptedAt) {
		return s.rememberLocked(command.RequestID, Acknowledgement{
			RequestID: command.RequestID, Status: string(domain.AckRejected), ErrorCode: string(domain.AckErrOutOfRange),
		})
	}

	switch command.Type {
	case domain.CommandSetThresholds:
		if command.Payload.Thresholds == nil || command.Payload.ThresholdVersion == nil {
			return s.rememberLocked(command.RequestID, Acknowledgement{
				RequestID: command.RequestID, Status: string(domain.AckRejected), ErrorCode: string(domain.AckErrOutOfRange),
			})
		}
		if err := command.Payload.Thresholds.Validate(); err != nil {
			return s.rememberLocked(command.RequestID, Acknowledgement{
				RequestID: command.RequestID, Status: string(domain.AckRejected), ErrorCode: string(domain.AckErrOutOfRange),
			})
		}
		if *command.Payload.ThresholdVersion <= s.thresholdVer {
			// A version that does not move forward would let a replay undo a
			// newer configuration.
			return s.rememberLocked(command.RequestID, Acknowledgement{
				RequestID: command.RequestID, Status: string(domain.AckRejected), ErrorCode: string(domain.AckErrStaleVersion),
			})
		}
		s.thresholds = *command.Payload.Thresholds
		s.thresholdVer = *command.Payload.ThresholdVersion
		return s.rememberLocked(command.RequestID, Acknowledgement{
			RequestID: command.RequestID, Status: string(domain.AckApplied), ReportsVersion: true,
		})

	default:
		return s.rememberLocked(command.RequestID, Acknowledgement{
			RequestID: command.RequestID, Status: string(domain.AckRejected), ErrorCode: string(domain.AckErrBadRequestType),
		})
	}
}

// rememberLocked records an acknowledgement and returns it. The caller must hold
// the mutex.
func (s *Simulator) rememberLocked(requestID string, ack Acknowledgement) Acknowledgement {
	s.handledRequests[requestID] = ack
	return ack
}

// recordAck keeps the acknowledgements for a test to assert on.
func (s *Simulator) recordAck(ack Acknowledgement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sentAcks = append(s.sentAcks, ack)
}

// publishAck sends an acknowledgement on the frozen acknowledgement topic.
func (s *Simulator) publishAck(ctx context.Context, ack Acknowledgement) {
	s.mu.Lock()
	s.sequence++
	sequence := s.sequence
	thresholdVer := s.thresholdVer
	s.mu.Unlock()

	status := domain.AckStatus(ack.Status)
	// A version is reported only for a threshold command that took effect.
	// Reporting one for a command the device refused, or for a command that never
	// concerned thresholds, would claim it adopted a configuration it did not.
	var version *int
	if ack.ReportsVersion && (status == domain.AckApplied || status == domain.AckDuplicate) {
		version = &thresholdVer
	}
	now := time.Now().UTC()
	payload, err := protocol.EncodeCommandAck(protocol.CommandAck{
		DeviceID:         s.cfg.DeviceID,
		BootID:           s.cfg.BootID,
		Sequence:         sequence,
		Timestamp:        &now,
		ReceivedAt:       now,
		UptimeMs:         uint64(sequence) * uint64(s.cfg.Interval/time.Millisecond),
		RequestID:        ack.RequestID,
		Status:           status,
		ThresholdVersion: version,
		ErrorCode:        ack.ErrorCode,
	})
	if err != nil {
		s.logger.LogAttrs(ctx, slog.LevelError, "could not encode acknowledgement", slog.String("error", err.Error()))
		return
	}
	if err := s.client.Publish(ctx, protocol.TopicCommandAck, payload, 1); err != nil {
		s.logger.LogAttrs(ctx, slog.LevelWarn, "could not publish acknowledgement", slog.String("error", err.Error()))
	}
}

// ReceivedCommands returns the commands the simulator accepted for handling.
func (s *Simulator) ReceivedCommands() []domain.Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Command(nil), s.received...)
}

// Acknowledgements returns the acknowledgements the simulator published.
func (s *Simulator) Acknowledgements() []Acknowledgement {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Acknowledgement(nil), s.sentAcks...)
}

// ThresholdVersion reports the version the simulated device has in force.
func (s *Simulator) ThresholdVersion() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.thresholdVer
}

// clientConnected reports whether the MQTT session is currently up.
func (s *Simulator) clientConnected() bool { return s.client.Connected() }

// LastSequence reports the highest sequence the simulator has used.
func (s *Simulator) LastSequence() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sequence
}
