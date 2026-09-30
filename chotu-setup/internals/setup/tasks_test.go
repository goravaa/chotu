package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/state"
)

// TestTasksGraphIsWellFormed checks the wiring in Tasks(): names are unique
// and every dependency refers to a task that actually exists. A typo here
// otherwise only surfaces at runtime, as a failed task.
func TestTasksGraphIsWellFormed(t *testing.T) {
	tasks := Tasks()

	seen := make(map[string]bool, len(tasks))

	for _, task := range tasks {
		if task.Name == "" {
			t.Error("task with an empty name")
			continue
		}

		if seen[task.Name] {
			t.Errorf("duplicate task name %q", task.Name)
		}

		seen[task.Name] = true

		if task.Run == nil {
			t.Errorf("task %q has no Run function", task.Name)
		}
	}

	for _, task := range tasks {
		for _, dependency := range task.Dependencies {
			if !seen[dependency] {
				t.Errorf(
					"task %q depends on unknown task %q",
					task.Name,
					dependency,
				)
			}
		}
	}
}

// TestTasksGraphHasNoCycles guards against a dependency cycle, which the
// runner cannot survive: each task blocks on its dependency's completion
// channel, so two tasks waiting on each other hang Run forever.
func TestTasksGraphHasNoCycles(t *testing.T) {
	tasks := Tasks()

	byName := make(map[string]*Task, len(tasks))

	for _, task := range tasks {
		byName[task.Name] = task
	}

	const (
		visiting = 1
		done     = 2
	)

	state := make(map[string]int, len(tasks))
	var stack []string

	var visit func(name string)

	visit = func(name string) {
		switch state[name] {
		case done:
			return
		case visiting:
			cycle := append([]string{}, stack...)
			cycle = append(cycle, name)

			t.Errorf("dependency cycle: %v", cycle)

			return
		}

		state[name] = visiting
		stack = append(stack, name)

		for _, dependency := range byName[name].Dependencies {
			if _, exists := byName[dependency]; !exists {
				continue
			}

			visit(dependency)
		}

		stack = stack[:len(stack)-1]
		state[name] = done
	}

	for _, task := range tasks {
		visit(task.Name)
	}
}

// TestTasksRunToCompletion runs the real graph. The timeout guard turns a
// deadlock into a test failure instead of a hung test binary.
func TestTasksRunToCompletion(t *testing.T) {
	tasks := Tasks()

	finished := make(chan map[string]TaskResult, 1)

	go func() {
		finished <- NewRunner(tasks, nil).
			WithRetryPolicy(1, time.Millisecond).
			Run(context.Background())
	}()

	var results map[string]TaskResult

	select {
	case results = <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("runner did not finish: the task graph deadlocked")
	}

	if len(results) != len(tasks) {
		t.Fatalf("results = %d, want %d", len(results), len(tasks))
	}

	for name, result := range results {
		if result.State != Completed {
			t.Errorf("%s = %s (%v)", name, result.State, result.Error)
		}
	}
}

// The tests below use tasks with real bodies, shaped like the setup tasks in
// tasks.go, so the engine is exercised against work that actually happens:
// running commands, writing state on disk, and re-checking a service on
// every pass. That is the contract the daemon depends on, and it cannot be
// verified with stub functions that return nil.

type fixture struct {
	dir     string
	healthy bool
	execs   atomic.Int32
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{
		dir:     t.TempDir(),
		healthy: true,
	}

	f.setServiceRunning(true)

	return f
}

func (f *fixture) path(name string) string {
	return filepath.Join(f.dir, name)
}

func (f *fixture) setServiceRunning(running bool) {
	f.healthy = running

	name := f.path("service.stopped")
	if running {
		name = f.path("service.ok")
	}

	_ = os.Remove(f.path("service.ok"))
	_ = os.Remove(f.path("service.stopped"))

	if err := os.WriteFile(name, []byte("upstream\n"), 0o600); err != nil {
		panic(err)
	}
}

// probe runs real commands and records what it found.
func (f *fixture) probe(ctx context.Context) error {
	f.execs.Add(1)

	out, err := exec.CommandContext(ctx, "uname", "-s").Output()
	if err != nil {
		return fmt.Errorf("run uname: %w", err)
	}

	kernel := strings.TrimSpace(string(out))
	if kernel == "" {
		return errors.New("uname returned nothing")
	}

	return os.WriteFile(
		f.path("probe.txt"),
		[]byte("kernel="+kernel+"\n"),
		0o600,
	)
}

// verify consumes what probe wrote, so it only succeeds if probe ran first.
func (f *fixture) verify(context.Context) error {
	data, err := os.ReadFile(f.path("probe.txt"))
	if err != nil {
		return fmt.Errorf("read probe output: %w", err)
	}

	if !strings.HasPrefix(string(data), "kernel=") {
		return fmt.Errorf("unexpected probe output %q", string(data))
	}

	return os.WriteFile(
		f.path("verify.txt"),
		[]byte("verified\n"),
		0o600,
	)
}

// health is the re-check a real setup task performs: the service must be up
// every cycle, not just the first time it was installed.
func (f *fixture) health(context.Context) error {
	if !f.healthy {
		return errors.New("fixture service is not running")
	}

	if _, err := os.Stat(f.path("service.ok")); err != nil {
		return fmt.Errorf("fixture service check failed: %w", err)
	}

	return nil
}

// report depends on both branches, so it can only run when they succeeded.
func (f *fixture) report(context.Context) error {
	for _, name := range []string{"probe.txt", "verify.txt"} {
		if _, err := os.Stat(f.path(name)); err != nil {
			return fmt.Errorf("report: %s missing: %w", name, err)
		}
	}

	return os.WriteFile(
		f.path("report.txt"),
		[]byte("complete\n"),
		0o600,
	)
}

func (f *fixture) tasks() []*Task {
	return []*Task{
		{Name: "probe", Run: f.probe},
		{Name: "verify", Dependencies: []string{"probe"}, Run: f.verify},
		{Name: "health", Run: f.health},
		{
			Name:         "report",
			Dependencies: []string{"verify", "health"},
			Run:          f.report,
		},
	}
}

func runTasks(t *testing.T, tasks []*Task) map[string]TaskResult {
	t.Helper()

	finished := make(chan map[string]TaskResult, 1)

	go func() {
		finished <- NewRunner(tasks, nil).
			WithRetryPolicy(1, time.Millisecond).
			Run(context.Background())
	}()

	select {
	case results := <-finished:
		return results
	case <-time.After(30 * time.Second):
		t.Fatal("runner did not finish: the task graph deadlocked")
	}

	return nil
}

func assertAll(t *testing.T, results map[string]TaskResult, want TaskState) {
	t.Helper()

	for name, result := range results {
		if result.State != want {
			t.Errorf("%s = %s, want %s (%v)", name, result.State, want, result.Error)
		}
	}
}

func TestBodiedTasksRunInDependencyOrder(t *testing.T) {
	f := newFixture(t)

	results := runTasks(t, f.tasks())

	assertAll(t, results, Completed)

	// Ordering is proven by data flow, not timestamps: report can only be
	// written after verify consumed probe's output.
	report, err := os.ReadFile(f.path("report.txt"))
	if err != nil {
		t.Fatalf("report.txt: %v", err)
	}

	if strings.TrimSpace(string(report)) != "complete" {
		t.Errorf("report = %q", string(report))
	}

	probe, err := os.ReadFile(f.path("probe.txt"))
	if err != nil {
		t.Fatalf("probe.txt: %v", err)
	}

	if !strings.HasPrefix(string(probe), "kernel=") {
		t.Errorf("probe = %q, want the uname output", string(probe))
	}

	if f.execs.Load() != 1 {
		t.Errorf("uname ran %d times, want 1", f.execs.Load())
	}
}

// The daemon re-runs every cycle and relies on task bodies being idempotent.
func TestBodiedTasksAreIdempotent(t *testing.T) {
	db := runnerTestDB(t)

	f := newFixture(t)
	store := state.NewStore(db)

	for cycle := range 3 {
		results := NewRunner(f.tasks(), store).
			WithRetryPolicy(1, time.Millisecond).
			Run(context.Background())

		assertAll(t, results, Completed)

		if f.execs.Load() != int32(cycle+1) {
			t.Fatalf("cycle %d: uname ran %d times", cycle+1, f.execs.Load())
		}
	}

	states := persistedStates(t, db)
	if len(states) != 4 {
		t.Fatalf("persisted %v, want 4 rows after 3 cycles", states)
	}

	assertAll(t, resultsOf(states), Completed)

	// A failed cycle must heal on the next one.
	f.healthy = false

	failed := NewRunner(f.tasks(), store).
		WithRetryPolicy(1, time.Millisecond).
		Run(context.Background())

	if failed["health"].State != Failed {
		t.Fatalf("health = %s, want FAILED", failed["health"].State)
	}

	f.healthy = true

	recovered := NewRunner(f.tasks(), store).
		WithRetryPolicy(1, time.Millisecond).
		Run(context.Background())

	assertAll(t, recovered, Completed)
}

func resultsOf(states map[string]string) map[string]TaskResult {
	results := make(map[string]TaskResult, len(states))

	for name, state := range states {
		results[name] = TaskResult{State: TaskState(state)}
	}

	return results
}

// The daemon only notices that a service died because a task body re-checks
// it on every pass. This is the behaviour that turns a healthy machine into a
// FAILED one without any restart.
func TestBodiedTaskDetectsServiceDrift(t *testing.T) {
	f := newFixture(t)
	tasks := f.tasks()

	assertAll(t, runTasks(t, tasks), Completed)

	// The service dies after setup completed.
	f.setServiceRunning(false)

	drifted := runTasks(t, tasks)

	if drifted["health"].State != Failed {
		t.Fatalf("health = %s, want FAILED after drift", drifted["health"].State)
	}

	if !strings.Contains(
		drifted["health"].Error.Error(),
		"not running",
	) {
		t.Errorf("error = %v, want it to mention the service", drifted["health"].Error)
	}

	// Unrelated work still completes.
	if drifted["probe"].State != Completed {
		t.Errorf("probe = %s, want COMPLETED", drifted["probe"].State)
	}

	// Anything depending on the failed check must be skipped.
	if drifted["report"].State != Failed {
		t.Errorf("report = %s, want FAILED", drifted["report"].State)
	}

	// The service comes back and the next cycle is healthy again.
	f.setServiceRunning(true)

	assertAll(t, runTasks(t, tasks), Completed)
}

// Two independent tasks must overlap in wall-clock time. The rendezvous makes
// this deterministic: if the runner serialised them, the first task would
// never see the second arrive and would fail on its own timeout.
func TestIndependentBodiedTasksRunInParallel(t *testing.T) {
	release := make(chan struct{})

	var arrived atomic.Int32

	body := func(context.Context) error {
		if arrived.Add(1) == 2 {
			close(release)
		}

		select {
		case <-release:
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("the other task never ran in parallel")
		}
	}

	tasks := []*Task{
		{Name: "first", Run: body},
		{Name: "second", Run: body},
	}

	assertAll(t, runTasks(t, tasks), Completed)

	if arrived.Load() != 2 {
		t.Errorf("arrivals = %d, want 2", arrived.Load())
	}
}
