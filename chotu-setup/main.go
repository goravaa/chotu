package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/config"
	"github.com/goravaa/chotu/chotu-setup/internals/publish"
	"github.com/goravaa/chotu/chotu-setup/internals/setup"
	"github.com/goravaa/chotu/chotu-setup/internals/state"
)

const (
	defaultStateDBPath = "/var/lib/chotu/chotu.db"
	defaultInterval    = 60 * time.Second
)

// version is stamped in at build time by the makefile. The daemon runs for the
// life of the machine, so the version it came from has to be recoverable from
// the logs and the status page.
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	configPath := flag.String(
		"config",
		envString("CHOTU_CONFIG_FILE", defaultConfigPath()),
		"path to the Chotu config file",
	)
	dbPath := flag.String(
		"db",
		envString("CHOTU_STATE_DB", defaultStateDBPath),
		"path to the Chotu state database",
	)
	interval := flag.Duration(
		"interval",
		envDuration("CHOTU_SETUP_INTERVAL", defaultInterval),
		"delay between setup cycles in daemon mode",
	)
	once := flag.Bool(
		"once",
		false,
		"run a single setup cycle and exit instead of looping",
	)
	attempts := flag.Int(
		"attempts",
		envInt("CHOTU_SETUP_ATTEMPTS", setup.DefaultAttempts),
		"attempts per task within a cycle before it is marked failed",
	)
	retryDelay := flag.Duration(
		"retry-delay",
		envDuration("CHOTU_SETUP_RETRY_DELAY", setup.DefaultRetryDelay),
		"delay before the first task retry, doubled on each subsequent retry",
	)

	logLevelFlag := flag.String(
		"log-level",
		envString("CHOTU_LOG_LEVEL", "info"),
		"logging level (debug, info, warn, error)",
	)

	flag.Parse()

	var logLevel slog.Level
	switch *logLevelFlag {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn", "warning":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	if *interval <= 0 {
		slog.Error("invalid -interval: must be positive", "interval", *interval)
		return 2
	}

	if *attempts < 1 {
		slog.Error("invalid -attempts: must be at least 1", "attempts", *attempts)
		return 2
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	// The storage task creates the directory the database lives in, so keep the
	// two in step when -db points somewhere other than the default.
	if os.Getenv("CHOTU_STATE_DIR") == "" {
		if err := os.Setenv("CHOTU_STATE_DIR", filepath.Dir(*dbPath)); err != nil {
			slog.Error("failed to set CHOTU_STATE_DIR", "error", err)
		}
	}

	db, err := state.Open(*dbPath)
	if err != nil {
		slog.Error("failed to open state database", "error", err)
		return 1
	}
	defer db.Close() //nolint:errcheck // main cannot report it; the process is exiting

	slog.Info(
		"chotu-setup starting",
		"version", version,
		"config", *configPath,
		"db", *dbPath,
		"interval", *interval,
		"once", *once,
		"attempts", *attempts,
		"retry-delay", *retryDelay,
	)

	if *once {
		report, err := runCycle(
			ctx,
			db,
			*configPath,
			setup.Tasks,
			*attempts,
			*retryDelay,
		)
		if err != nil {
			slog.Error("setup cycle failed", "error", err)
			return 1
		}

		publishStatus(ctx, publish.NewCollector(), report, 1)

		if failed := report.failed(); len(failed) > 0 {
			return 1
		}

		return 0
	}

	return runDaemon(
		ctx,
		db,
		*configPath,
		setup.Tasks,
		*interval,
		*attempts,
		*retryDelay,
	)
}

func runDaemon(
	ctx context.Context,
	db *sql.DB,
	configPath string,
	tasksFor func(*config.Config) []*setup.Task,
	interval time.Duration,
	attempts int,
	retryDelay time.Duration,
) int {
	cycles := 0

	collector := publish.NewCollector()

	for {
		if ctx.Err() != nil {
			slog.Info("shutdown requested", "cycles", cycles)
			return 0
		}

		cycles++

		started := time.Now()

		slog.Info("setup cycle starting", "cycle", cycles)

		cycle, err := runCycle(
			ctx,
			db,
			configPath,
			tasksFor,
			attempts,
			retryDelay,
		)
		if err != nil {
			slog.Error("setup cycle error", "cycle", cycles, "error", err)
		}

		// Publish after every cycle, including a failed one. The user watching
		// the page during a slow first boot is exactly who needs to see which
		// task is stuck, so a cycle that reports nothing is the one case where
		// staying quiet is wrong.
		if cycle != nil {
			publishStatus(ctx, collector, cycle, cycles)
		}

		slog.Info(
			"setup cycle finished",
			"cycle", cycles,
			"duration", time.Since(started).Round(time.Millisecond),
		)

		select {
		case <-ctx.Done():
			slog.Info("shutdown requested", "cycles", cycles)
			return 0
		case <-time.After(interval):
		}
	}
}

// cycleReport is what a finished cycle leaves behind. It carries the config and
// layout the cycle ran against, because a report published from a later cycle's
// config would describe a machine that never existed.
type cycleReport struct {
	Config       *config.Config
	Layout       setup.Layout
	Results      map[string]setup.TaskResult
	MachineState string
	Duration     time.Duration
}

func (r *cycleReport) failed() []string {
	failed := make([]string, 0)

	for name, result := range r.Results {
		if result.State != setup.Completed {
			failed = append(failed, name)
		}
	}

	sort.Strings(failed)

	return failed
}

// publishStatus sends the cycle's outcome to the status service. The publisher
// is built per cycle from the config that cycle used, so a port change in
// config.yaml takes effect without a restart.
//
// The collector is shared across cycles on purpose. It remembers the previous
// CPU reading, because utilisation is only a delta; a fresh collector per cycle
// would report zero forever and make the page useless.
func publishStatus(ctx context.Context, collector *publish.Collector, report *cycleReport, cycles int) {
	publisher := publish.NewPublisher(
		"",
		report.Config.StatusPage.Port,
		report.Config.StatusPage.Enabled,
	)

	document := collector.Collect(ctx, publish.Options{
		Version:      version,
		Cycle:        cycles,
		MachineState: report.MachineState,
		Results:      report.Results,
		Layout:       report.Layout,
		StateDir:     report.Layout.StateDir,
		Duration:     report.Duration,
	})

	publisher.Publish(ctx, document)
}

func runCycle(
	ctx context.Context,
	db *sql.DB,
	configPath string,
	tasksFor func(*config.Config) []*setup.Task,
	attempts int,
	retryDelay time.Duration,
) (*cycleReport, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}

	configHash, err := config.Hash(cfg)
	if err != nil {
		return nil, err
	}

	// The task list is built from the config that was just loaded, so a
	// change to config.yaml takes effect on the next cycle.
	layout := setup.ResolveLayout(cfg)
	tasks := tasksFor(cfg)

	machine, err := state.GetMachine(db)
	if err != nil {
		return nil, err
	}

	if machine == nil {
		if err := state.CreateMachine(db, configHash); err != nil {
			return nil, err
		}
	} else if machine.ConfigHash != configHash {
		slog.Info("config changed, re-running setup", "old_hash", machine.ConfigHash, "new_hash", configHash)
	}

	if err := state.UpdateMachineState(
		db,
		state.StateInitializing,
		configHash,
	); err != nil {
		return nil, err
	}

	started := time.Now()

	runner := setup.NewRunner(tasks, state.NewStore(db)).
		WithRetryPolicy(attempts, retryDelay)

	results := runner.Run(ctx)

	// A cancelled context means the daemon is shutting down, not that the
	// machine is broken. Leave the state at INITIALIZING and let the next
	// cycle decide it, so a clean stop never reports FAILED.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("cycle interrupted: %w", err)
	}

	report := &cycleReport{
		Config:  cfg,
		Layout:  layout,
		Results: results,
	}

	failed := report.failed()

	finalState := state.StateReady
	if len(failed) > 0 {
		finalState = state.StateFailed
	}

	if err := state.UpdateMachineState(
		db,
		finalState,
		configHash,
	); err != nil {
		return report, err
	}

	report.MachineState = string(finalState)
	report.Duration = time.Since(started)

	for _, name := range failed {
		slog.Error("task failed", "task", name, "error", results[name].Error)
	}

	slog.Info(
		"setup finished",
		"tasks_count", len(results),
		"state", finalState,
	)

	return report, nil
}

func defaultConfigPath() string {
	const installed = "/etc/chotu/config.yaml"

	if _, err := os.Stat(installed); err == nil {
		return installed
	}

	return "config.yaml"
}

func envString(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}

func envInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("invalid integer environment variable, using fallback", "name", name, "raw", raw, "error", err, "fallback", fallback)

		return fallback
	}

	return parsed
}

func envDuration(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("invalid duration environment variable, using fallback", "name", name, "raw", raw, "error", err, "fallback", fallback)

		return fallback
	}

	return parsed
}
