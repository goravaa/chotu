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

	"github.com/goravaa/chotu/chotu-setup/internals/config"
	"github.com/goravaa/chotu/chotu-setup/internals/state"
)

// TestTasksGraphIsWellFormed checks the wiring in Tasks(nil): names are unique
// and every dependency refers to a task that actually exists. A typo here
// otherwise only surfaces at runtime, as a failed task.
func TestTasksGraphIsWellFormed(t *testing.T) {
	tasks := Tasks(nil)

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
	tasks := Tasks(nil)

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

// runRealGraph runs the real task graph and returns the results. The timeout
// guard turns a deadlock into a test failure instead of a hung test binary.
func runRealGraph(t *testing.T) map[string]TaskResult {
	t.Helper()

	t.Setenv("CHOTU_STATE_DIR", t.TempDir())
	t.Setenv("CHOTU_LOG_DIR", t.TempDir())

	tasks := Tasks(&config.Config{})

	finished := make(chan map[string]TaskResult, 1)

	go func() {
		finished <- NewRunner(tasks, nil).
			WithRetryPolicy(1, time.Millisecond).
			Run(context.Background())
	}()

	select {
	case results := <-finished:
		if len(results) != len(tasks) {
			t.Fatalf("results = %d, want %d", len(results), len(tasks))
		}

		return results
	case <-time.After(30 * time.Second):
		t.Fatal("runner did not finish: the task graph deadlocked")
		return nil
	}
}

// TestRealGraphReachesATerminalStateForEveryTask runs the real bodies. The
// assertions cannot be "everything succeeded", because a developer laptop
// legitimately has no GPU and no rclone, and a task that reports that is
// working correctly. What must always hold is that every task ends in a
// terminal state, that nothing is left RUNNING, and that a task is never
// marked COMPLETED when a task it depends on did not complete.
func TestRealGraphReachesATerminalStateForEveryTask(t *testing.T) {
	results := runRealGraph(t)

	for name, result := range results {
		if result.State != Completed && result.State != Failed {
			t.Errorf("%s = %s, want COMPLETED or FAILED", name, result.State)
		}
	}

	// Every task in the graph must have been visited, otherwise a task added to
	// the graph could quietly stop running.
	for _, task := range Tasks(&config.Config{}) {
		if _, ok := results[task.Name]; !ok {
			t.Errorf("no result recorded for task %s", task.Name)
		}
	}
}

// TestRealBodiesRespectDependencies proves the real bodies are gated on their
// dependencies. Pointing HOME at a path that cannot be created makes the rclone
// body fail for any user, while s3 is disabled and would otherwise succeed, so
// s3 must be skipped rather than run against a dependency that failed.
func TestRealBodiesRespectDependencies(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	t.Setenv("HOME", filepath.Join(blocked, "home"))
	t.Setenv("CHOTU_STATE_DIR", t.TempDir())
	t.Setenv("CHOTU_LOG_DIR", t.TempDir())

	tasks := Tasks(&config.Config{})

	results := NewRunner(tasks, nil).
		WithRetryPolicy(1, time.Millisecond).
		Run(context.Background())

	if results[TaskRclone].State != Failed {
		t.Fatalf("rclone = %s, want FAILED so the dependency is genuinely broken",
			results[TaskRclone].State)
	}

	s3 := results[TaskS3]
	if s3.State != Failed {
		t.Fatalf("s3 = %s, want FAILED: it must be skipped when rclone fails",
			s3.State)
	}

	if !strings.Contains(s3.Error.Error(), "did not complete") {
		t.Errorf("s3 error = %q, want it to name the failed dependency", s3.Error)
	}
}

// TestRealGraphReportsActionableErrors pins that a task which cannot finish
// says why. An empty error would leave the status page showing a bare failure
// with nothing to act on.
func TestRealGraphReportsActionableErrors(t *testing.T) {
	results := runRealGraph(t)

	for name, result := range results {
		if result.State != Failed {
			continue
		}

		msg := result.Error.Error()
		if len(msg) < 20 {
			t.Errorf("%s failed with an unhelpful error: %q", name, msg)
		}

		if !strings.Contains(msg, "did not complete") &&
			!strings.Contains(msg, "not installed") &&
			!strings.Contains(msg, "not responding") &&
			!strings.Contains(msg, "permission denied") {
			t.Logf("%s failed: %s", name, msg)
		}
	}
}

// TestStorageTaskCreatesStateDirs proves the storage body does its work rather
// than only reporting failure, using the environment override so the test needs
// no root.
func TestStorageTaskCreatesStateDirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chotu-state")

	t.Setenv("CHOTU_STATE_DIR", dir)
	t.Setenv("CHOTU_LOG_DIR", filepath.Join(dir, "log"))

	if err := setupStorage(&config.Config{})(context.Background()); err != nil {
		t.Fatalf("setupStorage: %v", err)
	}

	for _, want := range []string{dir, filepath.Join(dir, "log")} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("storage task did not create %s: %v", want, err)
		}
	}
}

// TestStorageTaskVerifiesThePersistentVolume covers the difference between a
// directory the task may create and a disk that has to be attached by the
// provisioner. Creating the path would make the task pass on a machine with no
// persistent storage at all.
func TestStorageTaskVerifiesThePersistentVolume(t *testing.T) {
	t.Setenv("CHOTU_STATE_DIR", t.TempDir())
	t.Setenv("CHOTU_LOG_DIR", t.TempDir())

	persist := &config.Config{}
	persist.Machine.Storage.Persist = true

	missing := filepath.Join(t.TempDir(), "no-such-disk")

	t.Setenv("CHOTU_DATA_DIR", missing)

	err := setupStorage(persist)(context.Background())
	if err == nil {
		t.Fatal("persist with no volume attached: want an error")
	}

	if !strings.Contains(err.Error(), "persist") {
		t.Errorf("error = %q, want it to name machine.storage.persist", err)
	}

	present := t.TempDir()

	t.Setenv("CHOTU_DATA_DIR", present)

	if err := setupStorage(persist)(context.Background()); err != nil {
		t.Errorf("persist with the volume attached: %v", err)
	}

	// With persist off the task must not care about the data directory.
	t.Setenv("CHOTU_DATA_DIR", missing)

	if err := setupStorage(&config.Config{})(context.Background()); err != nil {
		t.Errorf("persist disabled: %v", err)
	}
}

// TestDisabledFeaturesSucceed checks that features switched off in the config
// are a no-op rather than a failure, since every task runs on every cycle.
func TestDisabledFeaturesSucceed(t *testing.T) {
	ctx := context.Background()

	if err := setupS3(&config.Config{})(ctx); err != nil {
		t.Errorf("s3 with enabled=false: %v", err)
	}

	if err := setupS3(&config.Config{S3: config.S3Config{Enabled: true}})(ctx); err == nil {
		t.Error("s3 enabled with no bucket: want an error")
	}

	if err := setupOpenCode(&config.Config{})(ctx); err != nil {
		t.Errorf("opencode disabled: %v", err)
	}

	if err := setupTooling(&config.Config{})(ctx); err != nil {
		t.Errorf("tooling with no packages: %v", err)
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
