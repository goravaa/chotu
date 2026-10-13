package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
	defaultInterval = 60 * time.Second
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
		"",
		"path to the Chotu state database (default: <layout.StateDir>/chotu.db)",
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
	monitorInterval := flag.Duration(
		"monitor-interval",
		envDuration("CHOTU_MONITOR_INTERVAL", publish.DefaultMonitorInterval),
		"how often metrics are collected and published while setup runs",
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

	if *monitorInterval <= 0 {
		slog.Error("invalid -monitor-interval: must be positive", "monitor-interval", *monitorInterval)
		return 2
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		return 1
	}

	// Resolve the DB path from the storage layout unless the operator overrode
	// it. The old hard-coded default (/var/lib/chotu/chotu.db) also set
	// CHOTU_STATE_DIR, which forced every machine onto the root-disk state path
	// and broke ephemeral mode (workspace/docker on /data, state on root).
	explicitDB := *dbPath != "" || os.Getenv("CHOTU_STATE_DB") != ""
	resolvedDB := *dbPath
	if resolvedDB == "" {
		resolvedDB = os.Getenv("CHOTU_STATE_DB")
	}

	if explicitDB && os.Getenv("CHOTU_STATE_DIR") == "" {
		if err := os.Setenv("CHOTU_STATE_DIR", filepath.Dir(resolvedDB)); err != nil {
			slog.Error("failed to set CHOTU_STATE_DIR", "error", err)
		}
	}

	layout := setup.ResolveLayout(cfg)
	if !explicitDB {
		resolvedDB = filepath.Join(layout.StateDir, "chotu.db")
	}

	db, err := state.Open(resolvedDB)
	if err != nil {
		slog.Error("failed to open state database", "error", err)
		return 1
	}
	defer db.Close() //nolint:errcheck // main cannot report it; the process is exiting

	slog.Info(
		"chotu-setup starting",
		"version", version,
		"config", *configPath,
		"db", resolvedDB,
		"state_dir", layout.StateDir,
		"storage_mode", layout.Mode,
		"interval", *interval,
		"monitor-interval", *monitorInterval,
		"once", *once,
		"attempts", *attempts,
		"retry-delay", *retryDelay,
	)

	if *once {
		collector := publish.NewCollector()
		monitor := publish.NewMonitor(collector, version, *monitorInterval)
		cycles := 1

		monitorCtx, stopMonitor := context.WithCancel(ctx)
		defer stopMonitor()
		go monitor.Run(monitorCtx)

		report, err := runCycle(
			ctx,
			db,
			*configPath,
			setup.Tasks,
			*attempts,
			*retryDelay,
			liveProgress(monitor, cycles),
		)
		stopMonitor()
		if err != nil {
			slog.Error("setup cycle failed", "error", err)
			return 1
		}

		publishStatus(ctx, collector, report, cycles)

		if report != nil && report.MachineState == string(state.StateRebootRequired) {
			slog.Info("reboot required, initiating system reboot")
			if err := exec.CommandContext(ctx, "systemctl", "reboot").Run(); err != nil {
				slog.Error("failed to initiate reboot", "error", err)
			}
		}

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
		*monitorInterval,
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
	monitorInterval time.Duration,
) int {
	cycles := 0

	collector := publish.NewCollector()
	monitor := publish.NewMonitor(collector, version, monitorInterval)

	// The monitor owns the 1s Collect+Publish cadence for the life of the
	// daemon. Setup goroutines only hand it snapshots (see liveProgress), so
	// long-running tasks can never starve metrics again.
	go monitor.Run(ctx)

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
			liveProgress(monitor, cycles),
		)
		if err != nil {
			slog.Error("setup cycle error", "cycle", cycles, "error", err)
		}

		// Publish after every cycle, including a failed one. The monitor
		// already kept the page live mid-cycle; this final document pins the
		// terminal machine state (READY/FAILED) and the full cycle duration.
		// The monitor snapshot is moved to the terminal state first so the
		// next 1s tick converges on it even if a stale INITIALIZING tick is
		// still in flight.
		if cycle != nil {
			monitor.Update(
				cycle.Config,
				cycle.Layout,
				cycle.Results,
				cycles,
				cycle.MachineState,
				time.Now().Add(-cycle.Duration),
			)
			publishStatus(ctx, collector, cycle, cycles)

			if cycle.MachineState == string(state.StateRebootRequired) {
				slog.Info("reboot required, initiating system reboot")
				if err := exec.CommandContext(ctx, "systemctl", "reboot").Run(); err != nil {
					slog.Error("failed to initiate reboot", "error", err)
				}
				// We don't exit immediately; wait for shutdown context.
			}
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
		if result.State != setup.Completed && result.State != setup.RebootRequired {
			failed = append(failed, name)
		}
	}

	sort.Strings(failed)

	return failed
}

func (r *cycleReport) rebootRequired() bool {
	for _, result := range r.Results {
		if result.State == setup.RebootRequired {
			return true
		}
	}
	return false
}

// liveProgress returns a hook that hands the latest task snapshot to the
// monitor as the graph advances. It must be cheap and never do I/O: it runs
// on the task goroutine that just recorded the change (see
// Runner.WithProgress), and the old implementation did Collect+POST here,
// which blocked setup tasks behind up to 10s of subprocess/HTTP timeouts and
// published nothing at all between state changes while gpu/vllm ran for
// minutes. The monitor's 1s ticker owns all collection now; this hook only
// copies the map and returns.
func liveProgress(
	monitor *publish.Monitor,
	cycles int,
) func(*config.Config, setup.Layout, map[string]setup.TaskResult, time.Duration) {
	return func(
		cfg *config.Config,
		layout setup.Layout,
		results map[string]setup.TaskResult,
		elapsed time.Duration,
	) {
		monitor.Update(
			cfg,
			layout,
			results,
			cycles,
			string(state.StateInitializing),
			time.Now().Add(-elapsed),
		)
	}
}

// publishStatus sends a status document to the status service. The publisher
// is built per call from the config that cycle used, so a port change in
// config.yaml takes effect without a restart.
//
// The collector is shared across cycles on purpose. It remembers the previous
// CPU reading, because utilisation is only a delta; a fresh collector per cycle
// would report zero forever and make the page useless.
func publishStatus(ctx context.Context, collector *publish.Collector, report *cycleReport, cycles int) {
	if collector == nil || report == nil || report.Config == nil {
		return
	}

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

// cycleProgress is called as the task graph advances during a cycle.
type cycleProgress func(
	cfg *config.Config,
	layout setup.Layout,
	results map[string]setup.TaskResult,
	elapsed time.Duration,
)

func runCycle(
	ctx context.Context,
	db *sql.DB,
	configPath string,
	tasksFor func(*config.Config) []*setup.Task,
	attempts int,
	retryDelay time.Duration,
	onProgress cycleProgress,
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

	if onProgress != nil {
		runner = runner.WithProgress(func(results map[string]setup.TaskResult) {
			onProgress(cfg, layout, results, time.Since(started))
		})
	}

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
	rebootRequired := report.rebootRequired()

	finalState := state.StateReady
	if rebootRequired {
		finalState = state.StateRebootRequired
	} else if len(failed) > 0 {
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
		if results[name].State != setup.Failed {
			continue
		}

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
