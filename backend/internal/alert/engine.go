// Package alert implements the composite fire early-warning state machine.
//
// The rule is deliberately two-factor: a gas surge and a rising temperature must
// both be present before a fire warning is raised, because either signal alone
// has ordinary causes in a lab room (someone opens a solvent bottle; the air
// conditioning cycles). A single sample can never raise a warning: the engine
// additionally requires a minimum number of samples spanning a minimum duration
// inside the evaluation window.
//
// The engine is pure with respect to persistence. It owns only the per-device
// sliding window and receives the currently stored alert state as input, so the
// whole decision table is unit-testable without a database or a broker.
package alert

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
)

// Config are the tunable parameters of the composite warning rule. Every field
// is part of the documented algorithm in backend/docs/api.md; changing a value
// changes alert behaviour for every device and must be recorded there.
type Config struct {
	// Window is the length of the evaluation window. Samples older than
	// Window behind the newest sample are dropped from the trend.
	Window time.Duration
	// MinSamples is the smallest number of in-window samples that may confirm a
	// composite warning. Values below 2 are rejected: one sample is not a trend.
	MinSamples int
	// MinDuration is the shortest span the in-window samples must cover before a
	// composite warning may be confirmed.
	MinDuration time.Duration
	// GasAdcRiseThreshold is the filtered-gas rise, in ADC codes, that counts as
	// a gas surge.
	GasAdcRiseThreshold int
	// TemperatureRateThresholdCPerMinute is the temperature slope, in degrees
	// Celsius per minute, that counts as a rapid rise.
	TemperatureRateThresholdCPerMinute float64
	// RecoveryHold is how long every condition must stay clear before an active
	// alert is closed. It suppresses flapping around the thresholds.
	RecoveryHold time.Duration
	// BaselineDivisor selects the baseline size: the first len(window)/divisor
	// samples, at least MinBaselineSamples of them, form the baseline the gas
	// rise is measured against.
	BaselineDivisor int
}

// MinBaselineSamples is the floor for the gas baseline size. A median over fewer
// than three samples does not resist one noisy reading.
const MinBaselineSamples = 3

// DefaultConfig returns the conservative defaults documented for phase 1.
func DefaultConfig() Config {
	return Config{
		Window:                             60 * time.Second,
		MinSamples:                         6,
		MinDuration:                        20 * time.Second,
		GasAdcRiseThreshold:                150,
		TemperatureRateThresholdCPerMinute: 3.0,
		RecoveryHold:                       30 * time.Second,
		BaselineDivisor:                    3,
	}
}

// Validate checks the configuration for values that would make the rule unsound.
func (c Config) Validate() error {
	if c.Window <= 0 {
		return fmt.Errorf("alert: window must be positive, got %s", c.Window)
	}
	if c.MinSamples < 2 {
		return fmt.Errorf("alert: minSamples must be at least 2 so a single sample cannot fire a warning, got %d", c.MinSamples)
	}
	if c.MinDuration <= 0 {
		return fmt.Errorf("alert: minDuration must be positive, got %s", c.MinDuration)
	}
	if c.MinDuration > c.Window {
		return fmt.Errorf("alert: minDuration %s cannot exceed window %s", c.MinDuration, c.Window)
	}
	if c.GasAdcRiseThreshold < 1 || c.GasAdcRiseThreshold > domain.MaxGasAdc {
		return fmt.Errorf("alert: gasAdcRiseThreshold must be within [1,%d], got %d", domain.MaxGasAdc, c.GasAdcRiseThreshold)
	}
	if math.IsNaN(c.TemperatureRateThresholdCPerMinute) || c.TemperatureRateThresholdCPerMinute <= 0 {
		return fmt.Errorf("alert: temperatureRateThresholdCPerMinute must be positive, got %v", c.TemperatureRateThresholdCPerMinute)
	}
	if c.RecoveryHold < 0 {
		return fmt.Errorf("alert: recoveryHold must not be negative, got %s", c.RecoveryHold)
	}
	if c.BaselineDivisor < 1 {
		return fmt.Errorf("alert: baselineDivisor must be at least 1, got %d", c.BaselineDivisor)
	}
	return nil
}

// Input is one evaluation request.
type Input struct {
	// Sample is the newly accepted telemetry.
	Sample domain.Telemetry
	// CurrentState is the alert state recorded for the device before this
	// sample. It distinguishes "never alarmed" from "recently recovered".
	CurrentState domain.AlertState
	// ActiveAlert is the alert event still open for the device, or nil.
	ActiveAlert *domain.AlertEvent
}

// Decision is the outcome of one evaluation.
type Decision struct {
	// State is the alert state after the evaluation.
	State domain.AlertState
	// Changed reports whether State differs from Input.CurrentState.
	Changed bool
	// Opened reports that a new alert event must be created with State.
	Opened bool
	// Escalated reports that the open alert event must adopt State and Evidence.
	Escalated bool
	// Closed reports that the open alert event must be ended with State.
	Closed bool
	// Evaluated is false when the window held too little data to decide anything.
	// In that case State equals Input.CurrentState and no transition is requested.
	Evaluated bool
	// Evidence explains the decision. It is populated whenever Evaluated is true.
	Evidence domain.AlertEvidence
}

// Engine evaluates the composite rule per device. It is safe for concurrent use.
type Engine struct {
	cfg Config

	mu      sync.Mutex
	windows map[string]*deviceWindow
}

// NewEngine validates the configuration and returns an engine.
func NewEngine(cfg Config) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Engine{cfg: cfg, windows: make(map[string]*deviceWindow)}, nil
}

// Config returns the configuration the engine was built with.
func (e *Engine) Config() Config { return e.cfg }

// deviceWindow is the per-device sliding window plus the recovery timer.
type deviceWindow struct {
	bootID  string
	samples []domain.Telemetry
	// clearSince is when every condition last became false, or nil while at
	// least one condition holds.
	clearSince *time.Time
}

// Ingest folds a sample into the device window without producing a decision.
// It is used for samples that are older than the newest stored one: a late
// arrival must still contribute to the trend it belongs to, but it must not be
// allowed to drive a state transition, because that would let stale data raise
// or clear an alert. Re-ingesting the same sample is a no-op.
func (e *Engine) Ingest(sample domain.Telemetry) {
	e.mu.Lock()
	defer e.mu.Unlock()

	window := e.windows[sample.DeviceID]
	if window == nil || window.bootID != sample.BootID {
		window = &deviceWindow{bootID: sample.BootID}
		e.windows[sample.DeviceID] = window
	}
	window.append(sample, e.cfg.Window)
}

// Evaluate folds one sample into the device window and returns the resulting
// alert decision. It has no side effects on the store; the caller applies the
// decision.
func (e *Engine) Evaluate(input Input) Decision {
	e.mu.Lock()
	defer e.mu.Unlock()

	window := e.windows[input.Sample.DeviceID]
	if window == nil || window.bootID != input.Sample.BootID {
		// A device restart invalidates every trend: the sequence counter, and
		// possibly the sensor warm-up state, start over. Discarding the window
		// avoids firing on a meaningless jump across the restart.
		window = &deviceWindow{bootID: input.Sample.BootID}
		e.windows[input.Sample.DeviceID] = window
	}
	window.append(input.Sample, e.cfg.Window)

	decision := Decision{State: input.CurrentState}
	evidence, ok := e.measure(window)
	decision.Evidence = evidence
	if !ok {
		return decision
	}
	decision.Evaluated = true

	gasSurge := evidence.GasAdcRise >= e.cfg.GasAdcRiseThreshold
	rapidRise := evidence.TemperatureRateCPerMinute >= e.cfg.TemperatureRateThresholdCPerMinute
	composite := gasSurge && rapidRise
	confirmed := composite &&
		evidence.SampleCount >= e.cfg.MinSamples &&
		time.Duration(evidence.WindowSeconds)*time.Second >= e.cfg.MinDuration

	eventTime := input.Sample.EventTime()
	switch {
	case confirmed:
		window.clearSince = nil
		decision.State = domain.AlertFireWarning
	case gasSurge || rapidRise:
		// Part of the evidence is present but the composite rule is not
		// satisfied. The device is suspect, not alarming.
		window.clearSince = nil
		decision.State = domain.AlertSuspect
	default:
		return e.applyRecovery(window, input, eventTime, decision)
	}

	decision.Changed = decision.State != input.CurrentState
	switch {
	case input.ActiveAlert == nil && (decision.State == domain.AlertFireWarning || decision.State == domain.AlertSuspect):
		decision.Opened = true
	case input.ActiveAlert != nil && input.ActiveAlert.State != decision.State:
		decision.Escalated = true
	}
	return decision
}

// applyRecovery handles the case where no condition holds. An active alert is
// held open until every condition has been clear for RecoveryHold, so that a
// single sample dipping below a threshold cannot end an episode.
//
// A device whose episode has already ended stays in recovered rather than
// decaying to normal. Both states mean "not currently alarming"; the difference
// is that recovered tells an operator the device has alarmed at some point, and
// keeping it that way needs no extra timer and cannot flap. normal is for a
// device with no composite warning in its recorded history.
func (e *Engine) applyRecovery(window *deviceWindow, input Input, eventTime time.Time, decision Decision) Decision {
	if input.ActiveAlert == nil {
		if input.CurrentState == domain.AlertRecovered {
			decision.State = domain.AlertRecovered
			return decision
		}
		decision.State = domain.AlertNormal
		decision.Changed = decision.State != input.CurrentState
		return decision
	}

	if window.clearSince == nil {
		clear := eventTime
		window.clearSince = &clear
		// The alert stays open and unchanged until the hold elapses.
		decision.State = input.ActiveAlert.State
		return decision
	}
	if eventTime.Sub(*window.clearSince) < e.cfg.RecoveryHold {
		decision.State = input.ActiveAlert.State
		return decision
	}

	decision.State = domain.AlertRecovered
	decision.Changed = decision.State != input.CurrentState
	decision.Closed = true
	return decision
}

// measure computes the rise and slope evidence for the current window. It
// reports false when the window is too small to describe a trend at all.
//
// Design Rationale:
//  1. Gas baseline uses the median of the earliest window segment (len/3) rather than
//     a simple average to stay immune to solitary outlier spikes (e.g. temporary
//     chemical solvent bottle opening).
//  2. Gas difference is evaluated in 12-bit ADC raw codes (0..4095) rather than uncalibrated
//     ppm estimates to eliminate sensor calibration drift.
func (e *Engine) measure(window *deviceWindow) (domain.AlertEvidence, bool) {
	samples := window.samples
	if len(samples) < MinBaselineSamples+1 {
		return domain.AlertEvidence{}, false
	}

	baselineSize := len(samples) / e.cfg.BaselineDivisor
	if baselineSize < MinBaselineSamples {
		baselineSize = MinBaselineSamples
	}
	if baselineSize > len(samples)-1 {
		// Always keep at least one non-baseline sample so the rise is a
		// comparison rather than a self-comparison.
		baselineSize = len(samples) - 1
	}

	baseline := make([]int, 0, baselineSize)
	for _, sample := range samples[:baselineSize] {
		baseline = append(baseline, sample.GasAdcFiltered)
	}
	latest := samples[len(samples)-1]

	span := latest.EventTime().Sub(samples[0].EventTime())
	return domain.AlertEvidence{
		GasAdcRise:                         latest.GasAdcFiltered - median(baseline),
		GasAdcRiseThreshold:                e.cfg.GasAdcRiseThreshold,
		TemperatureRateCPerMinute:          temperatureSlopeCPerMinute(samples),
		TemperatureRateThresholdCPerMinute: e.cfg.TemperatureRateThresholdCPerMinute,
		SampleCount:                        len(samples),
		WindowSeconds:                      int(span / time.Second),
	}, true
}

// append inserts a sample into the window in event-time order, drops duplicates
// and trims samples that fell out of the window.
//
// Out-of-order arrivals are inserted at their event-time position rather than
// discarded, so a delayed sample still contributes to the trend it belongs to.
func (w *deviceWindow) append(sample domain.Telemetry, window time.Duration) {
	for _, existing := range w.samples {
		if existing.BootID == sample.BootID && existing.Sequence == sample.Sequence {
			return
		}
	}
	position := sort.Search(len(w.samples), func(i int) bool {
		return !w.samples[i].EventTime().Before(sample.EventTime())
	})
	w.samples = append(w.samples, domain.Telemetry{})
	copy(w.samples[position+1:], w.samples[position:])
	w.samples[position] = sample

	// Trim relative to the newest sample, not to the sample being inserted: a
	// late arrival must not evict the current end of the window.
	newest := w.samples[len(w.samples)-1].EventTime()
	cutoff := newest.Add(-window)
	keep := 0
	for keep < len(w.samples) && w.samples[keep].EventTime().Before(cutoff) {
		keep++
	}
	if keep > 0 {
		w.samples = append([]domain.Telemetry(nil), w.samples[keep:]...)
	}
}

// median returns the median of values. The input is copied before sorting so
// that the caller's baseline slice keeps its insertion order.
func median(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

// temperatureSlopeCPerMinute fits a least-squares line through the window and
// returns its slope scaled to one minute. It returns 0 for fewer than two
// distinct timestamps, where a slope is undefined.
//
// Design Rationale:
// DHT11 sensor readings have a ±0.5°C quantization step. Calculating temperature
// derivative from adjacent two-point delta (ΔT/Δt) introduces severe high-frequency
// noise. Ordinary Least Squares (OLS) regression over the full sliding window
// acts as a low-pass filter, yielding a robust physical rate of rise (slope * 60).
func temperatureSlopeCPerMinute(samples []domain.Telemetry) float64 {
	if len(samples) < 2 {
		return 0
	}
	// Time is expressed in seconds relative to the first sample so that the
	// squares stay small and the fit stays numerically stable.
	origin := samples[0].EventTime()
	var sumX, sumY, sumXY, sumXX float64
	for _, sample := range samples {
		x := sample.EventTime().Sub(origin).Seconds()
		y := sample.TemperatureC
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	n := float64(len(samples))
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return 0
	}
	slopePerSecond := (n*sumXY - sumX*sumY) / denominator
	return slopePerSecond * 60
}
