package setup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/state"
)

func TestRunnerDependencies(t *testing.T) {
	var startedA, startedB time.Time
	var startedC time.Time

	tasks := []*Task{
		{
			Name: "A",
			Run: func(context.Context) error {
				startedA = time.Now()
				time.Sleep(500 * time.Millisecond)
				return nil
			},
		},
		{
			Name: "B",
			Run: func(context.Context) error {
				startedB = time.Now()
				time.Sleep(500 * time.Millisecond)
				return nil
			},
		},
		{
			Name:         "C",
			Dependencies: []string{"A", "B"},
			Run: func(context.Context) error {
				startedC = time.Now()
				return nil
			},
		},
	}

	runner := NewRunner(tasks, nil).WithRetryPolicy(1, time.Millisecond)

	results := runner.Run(context.Background())

	if results["A"].State != Completed {
		t.Fatalf("A = %s", results["A"].State)
	}

	if results["B"].State != Completed {
		t.Fatalf("B = %s", results["B"].State)
	}

	if results["C"].State != Completed {
		t.Fatalf("C = %s", results["C"].State)
	}

	if startedC.Before(startedA) || startedC.Before(startedB) {
		t.Fatal("C started before its dependencies completed")
	}

	if startedA.Sub(startedB) > 100*time.Millisecond ||
		startedB.Sub(startedA) > 100*time.Millisecond {
		t.Fatal("A and B did not run concurrently")
	}
}

func TestRunnerDependencyFailure(t *testing.T) {
	tasks := []*Task{
		{
			Name: "A",
			Run: func(context.Context) error {
				return errors.New("A failed")
			},
		},
		{
			Name:         "B",
			Dependencies: []string{"A"},
			Run: func(context.Context) error {
				t.Fatal("B should not run")
				return nil
			},
		},
	}

	runner := NewRunner(tasks, nil).WithRetryPolicy(1, time.Millisecond)

	results := runner.Run(context.Background())

	if results["A"].State != Failed {
		t.Fatalf("A = %s", results["A"].State)
	}

	if results["B"].State != Failed {
		t.Fatalf("B = %s", results["B"].State)
	}
}

func TestRunnerRetriesTransientFailure(t *testing.T) {
	var calls int32

	tasks := []*Task{
		{
			Name: "flaky",
			Run: func(context.Context) error {
				if atomic.AddInt32(&calls, 1) < 3 {
					return errors.New("transient")
				}

				return nil
			},
		},
	}

	results := NewRunner(tasks, nil).
		WithRetryPolicy(3, time.Millisecond).
		Run(context.Background())

	if results["flaky"].State != Completed {
		t.Fatalf("flaky = %s (%v)", results["flaky"].State, results["flaky"].Error)
	}

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestRunnerRetriesPanic(t *testing.T) {
	var calls int32

	tasks := []*Task{
		{
			Name: "panics-once",
			Run: func(context.Context) error {
				if atomic.AddInt32(&calls, 1) < 2 {
					panic("transient explosion")
				}

				return nil
			},
		},
	}

	results := NewRunner(tasks, nil).
		WithRetryPolicy(3, time.Millisecond).
		Run(context.Background())

	if results["panics-once"].State != Completed {
		t.Fatalf(
			"panics-once = %s (%v)",
			results["panics-once"].State,
			results["panics-once"].Error,
		)
	}

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestRunnerExhaustsAttempts(t *testing.T) {
	var calls int32

	tasks := []*Task{
		{
			Name: "always-fails",
			Run: func(context.Context) error {
				atomic.AddInt32(&calls, 1)
				return errors.New("nope")
			},
		},
	}

	results := NewRunner(tasks, nil).
		WithRetryPolicy(4, time.Millisecond).
		Run(context.Background())

	if results["always-fails"].State != Failed {
		t.Fatalf("state = %s", results["always-fails"].State)
	}

	if results["always-fails"].Error == nil {
		t.Fatal("expected an error on the result")
	}

	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Fatalf("attempts = %d, want 4", got)
	}
}

func TestRunnerTaskRetriesOverride(t *testing.T) {
	var calls int32

	tasks := []*Task{
		{
			Name:    "patient",
			Retries: 5,
			Run: func(context.Context) error {
				atomic.AddInt32(&calls, 1)
				return errors.New("nope")
			},
		},
	}

	results := NewRunner(tasks, nil).
		WithRetryPolicy(2, time.Millisecond).
		Run(context.Background())

	if results["patient"].State != Failed {
		t.Fatalf("state = %s", results["patient"].State)
	}

	if got := atomic.LoadInt32(&calls); got != 5 {
		t.Fatalf("attempts = %d, want 5 from the task override", got)
	}
}

// A panicking task must not take the process down, and must not leave its
// dependents blocked forever.
func TestRunnerPanicDoesNotKillProcessOrHang(t *testing.T) {
	var dependentRan int32

	tasks := []*Task{
		{
			Name: "panics",
			Run:  func(context.Context) error { panic("boom") },
		},
		{
			Name:         "dependent",
			Dependencies: []string{"panics"},
			Run: func(context.Context) error {
				atomic.AddInt32(&dependentRan, 1)
				return nil
			},
		},
		{
			Name: "independent",
			Run:  func(context.Context) error { return nil },
		},
	}

	finished := make(chan map[string]TaskResult, 1)

	go func() {
		finished <- NewRunner(tasks, nil).
			WithRetryPolicy(2, time.Millisecond).
			Run(context.Background())
	}()

	var results map[string]TaskResult

	select {
	case results = <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("runner hung: dependents were never released")
	}

	if results["panics"].State != Failed {
		t.Fatalf("panics = %s", results["panics"].State)
	}

	if results["panics"].Error == nil ||
		!strings.Contains(results["panics"].Error.Error(), "panic") {
		t.Fatalf("panics error = %v", results["panics"].Error)
	}

	if results["dependent"].State != Failed {
		t.Fatalf("dependent = %s", results["dependent"].State)
	}

	if atomic.LoadInt32(&dependentRan) != 0 {
		t.Fatal("dependent must not run after its dependency panicked")
	}

	if results["independent"].State != Completed {
		t.Fatalf("independent = %s", results["independent"].State)
	}
}

func TestRunnerRetryStopsOnContextCancel(t *testing.T) {
	var calls int32

	ctx, cancel := context.WithCancel(context.Background())

	tasks := []*Task{
		{
			Name: "always-fails",
			Run: func(context.Context) error {
				atomic.AddInt32(&calls, 1)
				return errors.New("nope")
			},
		},
	}

	finished := make(chan map[string]TaskResult, 1)

	go func() {
		finished <- NewRunner(tasks, nil).
			WithRetryPolicy(100, 50*time.Millisecond).
			Run(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case results := <-finished:
		if results["always-fails"].State != Failed {
			t.Fatalf("state = %s", results["always-fails"].State)
		}

		if got := atomic.LoadInt32(&calls); got >= 100 {
			t.Fatalf("attempts = %d, cancellation did not stop retries", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop after context cancellation")
	}
}

func TestRunnerNilRunIsReportedAsFailure(t *testing.T) {
	tasks := []*Task{
		{Name: "has-no-body"},
		{
			Name:         "after",
			Dependencies: []string{"has-no-body"},
			Run:          func(context.Context) error { return nil },
		},
	}

	results := NewRunner(tasks, nil).
		WithRetryPolicy(1, time.Millisecond).
		Run(context.Background())

	if results["has-no-body"].State != Failed {
		t.Fatalf("has-no-body = %s, want FAILED", results["has-no-body"].State)
	}

	if results["after"].State != Failed {
		t.Fatalf("after = %s, want FAILED", results["after"].State)
	}
}

func TestRunnerRetryBackoffGrows(t *testing.T) {
	tasks := []*Task{
		{
			Name: "always-fails",
			Run:  func(context.Context) error { return errors.New("nope") },
		},
	}

	started := time.Now()

	NewRunner(tasks, nil).
		WithRetryPolicy(3, 20*time.Millisecond).
		Run(context.Background())

	elapsed := time.Since(started)

	// Linear backoff would sleep 20ms + 20ms; exponential sleeps 20ms + 40ms.
	if elapsed < 60*time.Millisecond {
		t.Errorf("elapsed = %s, want at least 60ms for exponential backoff", elapsed)
	}

	if elapsed > 5*time.Second {
		t.Errorf("elapsed = %s, backoff is far too long", elapsed)
	}
}

// A store that always fails must not change the outcome of the run: the
// setup still executes and the results are still correct.
type failingStore struct {
	calls int32
}

func (f *failingStore) SetTaskState(string, string, error) error {
	atomic.AddInt32(&f.calls, 1)

	return errors.New("state database is down")
}

func TestRunnerSurvivesStoreFailure(t *testing.T) {
	store := &failingStore{}

	var ran int32

	tasks := []*Task{
		{
			Name: "ok",
			Run: func(context.Context) error {
				atomic.AddInt32(&ran, 1)
				return nil
			},
		},
		{
			Name: "broken",
			Run:  func(context.Context) error { return errors.New("nope") },
		},
	}

	results := NewRunner(tasks, store).
		WithRetryPolicy(1, time.Millisecond).
		Run(context.Background())

	if results["ok"].State != Completed {
		t.Errorf("ok = %s", results["ok"].State)
	}

	if results["broken"].State != Failed {
		t.Errorf("broken = %s", results["broken"].State)
	}

	if atomic.LoadInt32(&ran) != 1 {
		t.Errorf("task ran %d times, want 1", ran)
	}

	// Running then final state per task.
	if got := atomic.LoadInt32(&store.calls); got != 4 {
		t.Errorf("store calls = %d, want 4", got)
	}
}

func runnerTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := state.Open(filepath.Join(t.TempDir(), "chotu.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}

	t.Cleanup(func() { db.Close() })

	return db
}

func persistedStates(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()

	rows, err := db.Query(`
		SELECT name, state FROM setup_tasks ORDER BY name
	`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	states := map[string]string{}

	for rows.Next() {
		var name, taskState string

		if err := rows.Scan(&name, &taskState); err != nil {
			t.Fatalf("scan: %v", err)
		}

		states[name] = taskState
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	return states
}

func TestRunnerPersistsTaskStates(t *testing.T) {
	db := runnerTestDB(t)

	tasks := []*Task{
		{Name: "storage", Run: func(context.Context) error { return nil }},
		{Name: "docker", Run: func(context.Context) error { return nil }},
		{
			Name:         "docker-gpu",
			Dependencies: []string{"docker"},
			Run:          func(context.Context) error { return nil },
		},
	}

	NewRunner(tasks, state.NewStore(db)).
		WithRetryPolicy(1, time.Millisecond).
		Run(context.Background())

	want := map[string]string{
		"storage":    "COMPLETED",
		"docker":     "COMPLETED",
		"docker-gpu": "COMPLETED",
	}

	got := persistedStates(t, db)

	if len(got) != len(want) {
		t.Fatalf("persisted %v, want %v", got, want)
	}

	for name, wantState := range want {
		if got[name] != wantState {
			t.Errorf("%s = %s, want %s", name, got[name], wantState)
		}
	}
}

func TestRunnerPersistsFailureAndSkippedDependency(t *testing.T) {
	db := runnerTestDB(t)

	tasks := []*Task{
		{
			Name: "boom",
			Run:  func(context.Context) error { return errors.New("kaboom") },
		},
		{
			Name:         "dependent",
			Dependencies: []string{"boom"},
			Run:          func(context.Context) error { return nil },
		},
		{Name: "independent", Run: func(context.Context) error { return nil }},
	}

	NewRunner(tasks, state.NewStore(db)).
		WithRetryPolicy(1, time.Millisecond).
		Run(context.Background())

	rows, err := db.Query(`
		SELECT name, state, COALESCE(error, '')
		FROM setup_tasks
		ORDER BY name
	`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	got := map[string][2]string{}

	for rows.Next() {
		var name, taskState, taskErr string

		if err := rows.Scan(&name, &taskState, &taskErr); err != nil {
			t.Fatalf("scan: %v", err)
		}

		got[name] = [2]string{taskState, taskErr}
	}

	if len(got) != 3 {
		t.Fatalf("persisted %d rows, want 3: %v", len(got), got)
	}

	if got["boom"][0] != "FAILED" || got["boom"][1] != "kaboom" {
		t.Errorf("boom = %v", got["boom"])
	}

	wantDep := `dependency "boom" for task "dependent" did not complete`
	if got["dependent"][0] != "FAILED" || got["dependent"][1] != wantDep {
		t.Errorf("dependent = %v, want %v", got["dependent"], wantDep)
	}

	if got["independent"][0] != "COMPLETED" {
		t.Errorf("independent = %v", got["independent"])
	}
}

// TestRunnerPersistsEveryTaskUnderConcurrency is a regression test for the
// state database being written from many goroutines at once.
func TestRunnerPersistsEveryTaskUnderConcurrency(t *testing.T) {
	db := runnerTestDB(t)

	const count = 30

	var tasks []*Task

	for i := range count {
		tasks = append(tasks, &Task{
			Name: fmt.Sprintf("task-%02d", i),
			Run:  func(context.Context) error { return nil },
		})
	}

	results := NewRunner(tasks, state.NewStore(db)).
		WithRetryPolicy(1, time.Millisecond).
		Run(context.Background())

	if len(results) != count {
		t.Fatalf("results = %d, want %d", len(results), count)
	}

	states := persistedStates(t, db)

	if len(states) != count {
		t.Fatalf("persisted %d / %d rows", len(states), count)
	}

	for name, taskState := range states {
		if taskState != "COMPLETED" {
			t.Errorf("%s = %s, want COMPLETED", name, taskState)
		}
	}
}
