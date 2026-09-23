// Command device-sim runs the device simulator against a real broker.
//
// It is the counterpart of the firmware for integration testing: it performs the
// contract described in docs/device-protocol.md and nothing else, so a failure
// observed here is a failure of the backend or of the contract rather than of a
// particular board.
//
// Usage:
//
//	device-sim -broker localhost:1883 -device MCU001 -interval 5s -scenario quiet
//
// See docs/integration-testing.md for the reproducible environment this is meant
// to be run in.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/BobcGn/final/backend/internal/domain"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "device-sim: %v\n", err)
		os.Exit(1)
	}
}

// run parses the flags and runs the simulator until interrupted.
func run() error {
	broker := flag.String("broker", "localhost:1883", "MQTT broker host:port")
	username := flag.String("username", "", "broker user name")
	password := flag.String("password", "", "broker password")
	deviceID := flag.String("device", "MCU001", "device identifier; also the MQTT client identifier")
	bootID := flag.String("boot", "sim0001", "boot identifier, unique per power cycle")
	interval := flag.Duration("interval", DefaultReportInterval, "telemetry report period")
	scenarioName := flag.String("scenario", "quiet", "quiet, gas-surge, warm-up, or fire-alarm")
	duplicateEvery := flag.Int("duplicate-every", 0, "republish every Nth sample, to exercise the dedup key")
	skipEvery := flag.Int("skip-every", 0, "drop every Nth sample, leaving a gap in the stream")
	unsyncedClock := flag.Bool("unsynced-clock", false, "publish a null timestamp, as an unsynced device does")
	silentSeconds := flag.Int("silent-after", 0, "stop publishing after the first report for this many seconds")
	logLevel := flag.String("log-level", "info", "debug, info, warn or error")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(*logLevel)}))

	scenario, err := scenarioFor(*scenarioName)
	if err != nil {
		return err
	}

	simulator, err := New(Config{
		BrokerAddress: *broker,
		Username:      *username,
		Password:      *password,
		DeviceID:      *deviceID,
		BootID:        *bootID,
		Interval:      *interval,
		Logger:        logger,
		Scenario:      scenario,
		Faults: Faults{
			DuplicateEvery: *duplicateEvery,
			SkipEvery:      *skipEvery,
			UnsyncedClock:  *unsyncedClock,
			SilentSeconds:  *silentSeconds,
		},
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := simulator.Run(ctx); err != nil && ctx.Err() == nil {
			logger.LogAttrs(ctx, slog.LevelError, "simulator stopped", slog.String("error", err.Error()))
			stop()
		}
	}()
	if err := simulator.WaitConnected(ctx); err != nil {
		return fmt.Errorf("could not connect to %s: %w", *broker, err)
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "simulated device connected",
		slog.String("device_id", *deviceID), slog.String("broker", *broker), slog.String("scenario", *scenarioName))

	return simulator.RunReporting(ctx)
}

// scenarioFor maps a scenario name to a sample generator.
func scenarioFor(name string) (func(int) Sample, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "quiet":
		return func(int) Sample { return QuietSample() }, nil
	case "gas-surge":
		return func(int) Sample { return GasSurgeSample() }, nil
	case "warm-up":
		// A slow climb with a gas rise on top, which is what the backend's
		// composite rule is meant to notice: neither factor alone is a fire.
		return func(step int) Sample {
			sample := QuietSample()
			minutes := float64(step) / 12.0
			sample.TemperatureC = 24 + minutes*5
			sample.GasAdcFiltered = 1000 + step*40
			sample.GasAdcRaw = sample.GasAdcFiltered
			gas := 20.0 + float64(step)*4
			sample.GasPpm = &gas
			if sample.TemperatureC >= 30 || gas >= 80 {
				sample.LocalAlarm = true
				sample.AlarmCauses = []domain.AlarmCause{domain.AlarmGasHigh}
			}
			return sample
		}, nil
	case "fire-alarm":
		// Simulates a realistic early fire outbreak:
		// - Steps 0..3: quiet ambient baseline (~25°C, ~1000 ADC)
		// - Steps 4..15: sudden fire event, rapid gas surge (+400 ADC) and steep
		//   temperature climb (>3°C/min), triggering the composite fire_warning rule
		// - Steps 16+: cooldown and gradual recovery back to safe levels
		return func(step int) Sample {
			sample := QuietSample()
			switch {
			case step < 4:
				return sample
			case step < 16:
				fireStep := step - 4
				sample.TemperatureC = 25.0 + float64(fireStep)*0.6
				sample.GasAdcFiltered = 1000 + 350 + fireStep*30
				sample.GasAdcRaw = sample.GasAdcFiltered
				gas := 50.0 + float64(fireStep)*10
				sample.GasPpm = &gas
				sample.LocalAlarm = true
				sample.AlarmCauses = []domain.AlarmCause{domain.AlarmRapidTemperatureRise, domain.AlarmRapidGasRise}
				return sample
			default:
				coolStep := step - 16
				temp := 32.2 - float64(coolStep)*0.8
				if temp < 25.0 {
					temp = 25.0
				}
				sample.TemperatureC = temp
				sample.GasAdcFiltered = 1050
				sample.GasAdcRaw = 1050
				return sample
			}
		}, nil
	default:
		return nil, fmt.Errorf("unknown scenario %q; expected quiet, gas-surge, warm-up, or fire-alarm", name)
	}
}

// parseLevel converts a log level name.
func parseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
