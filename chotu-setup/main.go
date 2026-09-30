package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/config"
	"github.com/goravaa/chotu/chotu-setup/internals/setup"
	"github.com/goravaa/chotu/chotu-setup/internals/state"
)

const (
	defaultStateDBPath = "/var/lib/chotu/chotu.db"
	defaultInterval    = 60 * time.Second
)

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

	flag.Parse()

	if *interval <= 0 {
		log.Printf("invalid -interval %s: must be positive", *interval)
		return 2
	}

	if *attempts < 1 {
		log.Printf("invalid -attempts %d: must be at least 1", *attempts)
		return 2
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	db, err := state.Open(*dbPath)
	if err != nil {
		log.Printf("open state database: %v", err)
		return 1
	}
	defer db.Close()

	log.Printf(
		"chotu-setup starting: config=%s db=%s interval=%s once=%t attempts=%d retry-delay=%s",
		*configPath,
		*dbPath,
		*interval,
		*once,
		*attempts,
		*retryDelay,
	)

	if *once {
		failed, err := runCycle(
			ctx,
			db,
			*configPath,
			*attempts,
			*retryDelay,
		)
		if err != nil {
			log.Printf("setup cycle failed: %v", err)
			return 1
		}

		if len(failed) > 0 {
			return 1
		}

		return 0
	}

	return runDaemon(ctx, db, *configPath, *interval, *attempts, *retryDelay)
}

func runDaemon(
	ctx context.Context,
	db *sql.DB,
	configPath string,
	interval time.Duration,
	attempts int,
	retryDelay time.Duration,
) int {
	cycles := 0

	for {
		if ctx.Err() != nil {
			log.Printf("shutdown requested; stopping after %d cycles", cycles)
			return 0
		}

		cycles++

		started := time.Now()

		log.Printf("setup cycle %d starting", cycles)

		if _, err := runCycle(
			ctx,
			db,
			configPath,
			attempts,
			retryDelay,
		); err != nil {
			log.Printf("setup cycle %d error: %v", cycles, err)
		}

		log.Printf(
			"setup cycle %d finished in %s",
			cycles,
			time.Since(started).Round(time.Millisecond),
		)

		select {
		case <-ctx.Done():
			log.Printf("shutdown requested; stopping after %d cycles", cycles)
			return 0
		case <-time.After(interval):
		}
	}
}

func runCycle(
	ctx context.Context,
	db *sql.DB,
	configPath string,
	attempts int,
	retryDelay time.Duration,
) ([]string, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}

	configHash, err := config.Hash(cfg)
	if err != nil {
		return nil, err
	}

	machine, err := state.GetMachine(db)
	if err != nil {
		return nil, err
	}

	if machine == nil {
		if err := state.CreateMachine(db, configHash); err != nil {
			return nil, err
		}
	} else if machine.ConfigHash != configHash {
		log.Printf(
			"config changed: %s -> %s, re-running setup",
			machine.ConfigHash,
			configHash,
		)
	}

	if err := state.UpdateMachineState(
		db,
		state.StateInitializing,
		configHash,
	); err != nil {
		return nil, err
	}

	runner := setup.NewRunner(setup.Tasks(), state.NewStore(db)).
		WithRetryPolicy(attempts, retryDelay)

	results := runner.Run(ctx)

	// A cancelled context means the daemon is shutting down, not that the
	// machine is broken. Leave the state at INITIALIZING and let the next
	// cycle decide it, so a clean stop never reports FAILED.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("cycle interrupted: %w", err)
	}

	failed := make([]string, 0)

	for name, result := range results {
		if result.State != setup.Completed {
			failed = append(failed, name)
		}
	}

	sort.Strings(failed)

	finalState := state.StateReady
	if len(failed) > 0 {
		finalState = state.StateFailed
	}

	if err := state.UpdateMachineState(
		db,
		finalState,
		configHash,
	); err != nil {
		return failed, err
	}

	for _, name := range failed {
		log.Printf("task %s failed: %v", name, results[name].Error)
	}

	log.Printf(
		"setup finished: %d tasks, machine state %s",
		len(results),
		finalState,
	)

	return failed, nil
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
		log.Printf(
			"%s=%q is not a valid integer (%v), using %d",
			name,
			raw,
			err,
			fallback,
		)

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
		log.Printf(
			"%s=%q is not a valid duration (%v), using %s",
			name,
			raw,
			err,
			fallback,
		)

		return fallback
	}

	return parsed
}
