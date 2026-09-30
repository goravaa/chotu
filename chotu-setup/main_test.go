package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/setup"
	"github.com/goravaa/chotu/chotu-setup/internals/state"
)

const (
	testAttempts   = 1
	testRetryDelay = time.Millisecond
)

func cycleDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := state.Open(filepath.Join(t.TempDir(), "chotu.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}

	t.Cleanup(func() { db.Close() })

	return db
}

func cycleConfig(t *testing.T, model string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")

	body := "machine:\n  instance_type: g6.xlarge\n" +
		"inference:\n  model: " + model + "\n"

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}

func machineState(t *testing.T, db *sql.DB) state.MachineState {
	t.Helper()

	machine, err := state.GetMachine(db)
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}

	if machine == nil {
		t.Fatal("machine row is missing")
	}

	return machine.State
}

func machineHash(t *testing.T, db *sql.DB) string {
	t.Helper()

	machine, err := state.GetMachine(db)
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}

	if machine == nil {
		t.Fatal("machine row is missing")
	}

	return machine.ConfigHash
}

func okTask(name string) *setup.Task {
	return &setup.Task{
		Name: name,
		Run:  func(context.Context) error { return nil },
	}
}

func failingTask(name string) *setup.Task {
	return &setup.Task{
		Name: name,
		Run: func(context.Context) error {
			return errors.New("service is down")
		},
	}
}

func TestRunCycleMarksMachineReady(t *testing.T) {
	db := cycleDB(t)

	tasks := []*setup.Task{okTask("storage"), okTask("docker")}

	failed, err := runCycle(
		context.Background(),
		db,
		cycleConfig(t, "model-a"),
		tasks,
		testAttempts,
		testRetryDelay,
	)
	if err != nil {
		t.Fatalf("runCycle: %v", err)
	}

	if len(failed) != 0 {
		t.Fatalf("failed = %v, want none", failed)
	}

	if got := machineState(t, db); got != state.StateReady {
		t.Errorf("machine = %s, want %s", got, state.StateReady)
	}
}

func TestRunCycleMarksMachineFailed(t *testing.T) {
	db := cycleDB(t)

	tasks := []*setup.Task{
		okTask("storage"),
		failingTask("docker"),
		{
			Name:         "docker-gpu",
			Dependencies: []string{"docker"},
			Run:          func(context.Context) error { return nil },
		},
	}

	failed, err := runCycle(
		context.Background(),
		db,
		cycleConfig(t, "model-a"),
		tasks,
		testAttempts,
		testRetryDelay,
	)
	if err != nil {
		t.Fatalf("runCycle: %v", err)
	}

	want := []string{"docker", "docker-gpu"}

	if len(failed) != len(want) {
		t.Fatalf("failed = %v, want %v", failed, want)
	}

	for i, name := range want {
		if failed[i] != name {
			t.Errorf("failed[%d] = %s, want %s (sorted)", i, failed[i], name)
		}
	}

	if got := machineState(t, db); got != state.StateFailed {
		t.Errorf("machine = %s, want %s", got, state.StateFailed)
	}
}

// A cycle cut short by shutdown must not report the machine as broken.
func TestRunCycleInterruptedDoesNotReportFailure(t *testing.T) {
	db := cycleDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tasks := []*setup.Task{{
		Name: "slow",
		Run:  func(context.Context) error { return nil },
	}}

	_, err := runCycle(
		ctx,
		db,
		cycleConfig(t, "model-a"),
		tasks,
		testAttempts,
		testRetryDelay,
	)

	if err == nil {
		t.Fatal("expected an error for an interrupted cycle")
	}

	if got := machineState(t, db); got != state.StateInitializing {
		t.Errorf("machine = %s, want %s", got, state.StateInitializing)
	}
}

// The machine must move from FAILED back to READY once the fault is fixed,
// without a restart.
func TestRunCycleRecoversAfterFailure(t *testing.T) {
	db := cycleDB(t)
	configPath := cycleConfig(t, "model-a")

	failing := []*setup.Task{failingTask("docker")}

	if _, err := runCycle(
		context.Background(),
		db,
		configPath,
		failing,
		testAttempts,
		testRetryDelay,
	); err != nil {
		t.Fatalf("runCycle: %v", err)
	}

	if got := machineState(t, db); got != state.StateFailed {
		t.Fatalf("machine = %s, want %s", got, state.StateFailed)
	}

	healthy := []*setup.Task{okTask("docker")}

	if _, err := runCycle(
		context.Background(),
		db,
		configPath,
		healthy,
		testAttempts,
		testRetryDelay,
	); err != nil {
		t.Fatalf("runCycle: %v", err)
	}

	if got := machineState(t, db); got != state.StateReady {
		t.Errorf("machine = %s, want %s", got, state.StateReady)
	}
}

func TestRunCycleRepeatedCyclesAreIdempotent(t *testing.T) {
	db := cycleDB(t)
	configPath := cycleConfig(t, "model-a")

	tasks := []*setup.Task{okTask("storage"), okTask("docker")}

	for cycle := range 5 {
		if _, err := runCycle(
			context.Background(),
			db,
			configPath,
			tasks,
			testAttempts,
			testRetryDelay,
		); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}

		if got := machineState(t, db); got != state.StateReady {
			t.Fatalf("cycle %d: machine = %s, want READY", cycle, got)
		}
	}

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM setup_tasks",
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}

	if count != 2 {
		t.Errorf("setup_tasks rows = %d, want 2 after 5 cycles", count)
	}
}

func TestRunCyclePicksUpConfigChange(t *testing.T) {
	db := cycleDB(t)

	tasks := []*setup.Task{okTask("storage")}

	configA := cycleConfig(t, "model-a")

	if _, err := runCycle(
		context.Background(),
		db,
		configA,
		tasks,
		testAttempts,
		testRetryDelay,
	); err != nil {
		t.Fatalf("runCycle: %v", err)
	}

	first := machineHash(t, db)

	configB := cycleConfig(t, "model-b")

	if _, err := runCycle(
		context.Background(),
		db,
		configB,
		tasks,
		testAttempts,
		testRetryDelay,
	); err != nil {
		t.Fatalf("runCycle: %v", err)
	}

	second := machineHash(t, db)

	if first == second {
		t.Error("config hash did not change after editing the config")
	}
}

func TestRunCycleMissingConfigIsAnError(t *testing.T) {
	db := cycleDB(t)

	_, err := runCycle(
		context.Background(),
		db,
		filepath.Join(t.TempDir(), "absent.yaml"),
		[]*setup.Task{okTask("storage")},
		testAttempts,
		testRetryDelay,
	)

	if err == nil {
		t.Fatal("expected an error for a missing config")
	}

	// The config is read before the machine row exists, so nothing may claim
	// the machine is ready.
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM machine WHERE state = ?",
		state.StateReady,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}

	if count != 0 {
		t.Errorf("machine reported READY %d times with no config", count)
	}

	var ready int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM setup_tasks",
	).Scan(&ready); err != nil {
		t.Fatalf("count: %v", err)
	}

	if ready != 0 {
		t.Errorf("setup_tasks rows = %d, want 0 when the config cannot be read", ready)
	}
}

func TestRunDaemonRepeatsCyclesAndStopsCleanly(t *testing.T) {
	db := cycleDB(t)

	var runs atomic.Int32

	tasks := []*setup.Task{{
		Name: "counter",
		Run: func(context.Context) error {
			runs.Add(1)
			return nil
		},
	}}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan int, 1)

	go func() {
		done <- runDaemon(
			ctx,
			db,
			cycleConfig(t, "model-a"),
			tasks,
			5*time.Millisecond,
			testAttempts,
			testRetryDelay,
		)
	}()

	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop after cancellation")
	}

	if got := runs.Load(); got < 2 {
		t.Errorf("cycles run = %d, want at least 2", got)
	}

	// The cancel can land between cycles or in the middle of one. A cycle cut
	// short leaves the machine INITIALIZING on purpose, so either outcome is
	// correct here; what must never happen is FAILED for a task that passes.
	switch got := machineState(t, db); got {
	case state.StateReady, state.StateInitializing:
	default:
		t.Errorf("machine = %s, want READY or INITIALIZING", got)
	}
}

func TestRunDaemonStopsImmediatelyOnCancelledContext(t *testing.T) {
	db := cycleDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()

	code := runDaemon(
		ctx,
		db,
		cycleConfig(t, "model-a"),
		[]*setup.Task{okTask("storage")},
		time.Hour,
		testAttempts,
		testRetryDelay,
	)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("took %s to notice the cancellation", elapsed)
	}
}
