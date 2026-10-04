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
		if result.State != Completed && result.State != Failed && result.State != Blocked {
			t.Errorf("%s = %s, want COMPLETED, FAILED, or BLOCKED", name, result.State)
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

// TestRealBodiesRespectDependencies proves the real graph never runs a body
// whose dependency failed. On a developer laptop many tasks fail for hardware
// reasons; what must always hold is that their dependents are BLOCKED, not
// COMPLETED, and that a COMPLETED task only appears when every dependency did.
func TestRealBodiesRespectDependencies(t *testing.T) {
	t.Setenv("CHOTU_STATE_DIR", t.TempDir())
	t.Setenv("CHOTU_LOG_DIR", t.TempDir())

	tasks := Tasks(&config.Config{})
	byName := make(map[string]*Task, len(tasks))

	for _, task := range tasks {
		byName[task.Name] = task
	}

	results := NewRunner(tasks, nil).
		WithRetryPolicy(1, time.Millisecond).
		Run(context.Background())

	for name, result := range results {
		task := byName[name]

		for _, dep := range task.Dependencies {
			depResult := results[dep]

			if depResult.State != Completed {
				if result.State != Blocked {
					t.Errorf(
						"%s = %s, want BLOCKED because dependency %s is %s",
						name,
						result.State,
						dep,
						depResult.State,
					)
				}

				if result.Error == nil ||
					!strings.Contains(result.Error.Error(), "did not complete") {
					t.Errorf(
						"%s error = %v, want it to name the failed dependency",
						name,
						result.Error,
					)
				}

				break
			}
		}

		if result.State == Completed {
			for _, dep := range task.Dependencies {
				if results[dep].State != Completed {
					t.Errorf(
						"%s completed while dependency %s is %s",
						name,
						dep,
						results[dep].State,
					)
				}
			}
		}
	}
}

// TestRealGraphReportsActionableErrors pins that a task which cannot finish
// says why. An empty error would leave the status page showing a bare failure
// with nothing to act on.
func TestRealGraphReportsActionableErrors(t *testing.T) {
	results := runRealGraph(t)

	for name, result := range results {
		if result.State != Failed && result.State != Blocked {
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

// testLayout points the layout at temporary directories so the storage bodies
// can run without root or a real disk.
func testLayout(t *testing.T) Layout {
	t.Helper()

	root := t.TempDir()

	layout := Layout{
		Mode:       "persistent",
		DataDir:    filepath.Join(root, "data"),
		RunUser:    "",
		Home:       filepath.Join(root, "home"),
		DockerRoot: filepath.Join(root, "docker"),
		ModelCache: filepath.Join(root, "huggingface"),
		StateDir:   filepath.Join(root, "state"),
		Workspace:  filepath.Join(root, "workspace"),
	}
	layout.LogDir = filepath.Join(layout.StateDir, "log")

	return layout
}

// TestStorageTaskCreatesDirs proves the storage body does its work rather than
// only reporting failure. The task requires a real mount for DataDir, so the
// test uses a writable mount point when one is available.
func TestStorageTaskCreatesDirs(t *testing.T) {
	layout := testLayout(t)

	mount := findTestMount(t)
	if mount == "" {
		t.Skip("no writable mount point available to stand in for the instance store")
	}

	layout.DataDir = mount

	if err := setupStorage(&config.Config{}, layout)(context.Background()); err != nil {
		t.Fatalf("setupStorage: %v", err)
	}

	for _, want := range []string{layout.StateDir, layout.LogDir, layout.ModelCache} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("storage task did not create %s: %v", want, err)
		}
	}
}

// TestStorageTaskRequiresInstanceStoreMount covers the storage topology: every
// machine must have the instance store mounted at DataDir. Persistent mode uses
// it as scratch under /data; ephemeral mode puts the workspace there. An
// unmounted path would silently put state on the small root disk.
func TestStorageTaskRequiresInstanceStoreMount(t *testing.T) {
	t.Run("ephemeral without the instance store fails", func(t *testing.T) {
		layout := testLayout(t)
		layout.Mode = "ephemeral"
		layout.DataDir = filepath.Join(t.TempDir(), "not-attached")

		// The directory existing but not being a mount is the dangerous case.
		if err := os.MkdirAll(layout.DataDir, 0o755); err != nil {
			t.Fatalf("create the data directory: %v", err)
		}

		err := setupStorage(&config.Config{}, layout)(context.Background())
		if err == nil {
			t.Fatal("ephemeral mode with no instance store: want an error")
		}

		if !strings.Contains(err.Error(), "mount point") &&
			!strings.Contains(err.Error(), "instance store") {
			t.Errorf("error = %q, want it to name the missing mount", err)
		}
	})

	t.Run("persistent without the instance store fails", func(t *testing.T) {
		layout := testLayout(t)
		layout.Mode = "persistent"
		layout.DataDir = filepath.Join(t.TempDir(), "not-attached")

		if err := os.MkdirAll(layout.DataDir, 0o755); err != nil {
			t.Fatalf("create the data directory: %v", err)
		}

		err := setupStorage(&config.Config{}, layout)(context.Background())
		if err == nil {
			t.Fatal("persistent mode with no instance store: want an error")
		}

		if !strings.Contains(err.Error(), "mount point") &&
			!strings.Contains(err.Error(), "instance store") {
			t.Errorf("error = %q, want it to name the missing mount", err)
		}
	})

	t.Run("ephemeral with the instance store succeeds", func(t *testing.T) {
		layout := testLayout(t)
		layout.Mode = "ephemeral"

		// The check is on a real mount, so a plain temporary directory will not
		// do. Use an actual mount point if the test host has one.
		mount := findTestMount(t)
		if mount == "" {
			t.Skip("no writable mount point available to stand in for the instance store")
		}

		layout.DataDir = mount

		if err := setupStorage(&config.Config{}, layout)(context.Background()); err != nil {
			t.Errorf("ephemeral mode with the instance store attached: %v", err)
		}
	})

	t.Run("persistent with the instance store succeeds", func(t *testing.T) {
		layout := testLayout(t)
		layout.Mode = "persistent"

		mount := findTestMount(t)
		if mount == "" {
			t.Skip("no writable mount point available to stand in for the instance store")
		}

		layout.DataDir = mount

		if err := setupStorage(&config.Config{}, layout)(context.Background()); err != nil {
			t.Errorf("persistent mode with the instance store attached: %v", err)
		}
	})
}

// findTestMount returns a writable mount point to stand in for the instance
// store, or "" if the test host has none. The storage task only stats the path,
// so the mount point itself is used rather than a directory inside it.
func findTestMount(t *testing.T) string {
	t.Helper()

	for _, candidate := range []string{"/dev/shm", "/run"} {
		mounted, err := isMountpoint(candidate)
		if err != nil || !mounted {
			continue
		}

		return candidate
	}

	return ""
}

// TestResolveLayoutFollowsPersistence pins the storage topology to the config,
// which is the whole point of machine.storage.persist: the same machine
// topology has to be derived without any task hardcoding a path.
func TestResolveLayoutFollowsPersistence(t *testing.T) {
	t.Run("persistent keeps state on the root disk", func(t *testing.T) {
		t.Setenv("CHOTU_DATA_DIR", "/mnt/instance-store")
		t.Setenv("CHOTU_WORKSPACE_DIR", "")
		t.Setenv("CHOTU_DOCKER_ROOT", "")
		t.Setenv("CHOTU_MODEL_CACHE", "")
		t.Setenv("CHOTU_STATE_DIR", "")
		t.Setenv("CHOTU_LOG_DIR", "")

		layout := ResolveLayout(&config.Config{
			Machine: config.MachineConfig{
				Storage: config.StorageConfig{Persist: true},
			},
		})

		if layout.DataDir != "/mnt/instance-store" {
			t.Errorf("DataDir = %q, want the instance store at /mnt/instance-store", layout.DataDir)
		}

		if layout.DockerRoot != "/var/lib/docker" {
			t.Errorf("DockerRoot = %q, want the root disk at /var/lib/docker", layout.DockerRoot)
		}

		if !strings.HasPrefix(layout.ModelCache, "/var/lib/chotu") {
			t.Errorf("ModelCache = %q, want the root disk under /var/lib/chotu", layout.ModelCache)
		}

		if !strings.HasSuffix(layout.Workspace, "/workspace") {
			t.Errorf("Workspace = %q, want a workspace under the user's home", layout.Workspace)
		}

		if strings.HasPrefix(layout.Workspace, layout.DataDir) {
			t.Errorf(
				"Workspace = %q must not be on the instance store in persistent mode",
				layout.Workspace,
			)
		}
	})

	t.Run("ephemeral puts everything on the instance store", func(t *testing.T) {
		t.Setenv("CHOTU_DATA_DIR", "/mnt/instance-store")
		t.Setenv("CHOTU_WORKSPACE_DIR", "")
		t.Setenv("CHOTU_DOCKER_ROOT", "")
		t.Setenv("CHOTU_MODEL_CACHE", "")
		t.Setenv("CHOTU_STATE_DIR", "")
		t.Setenv("CHOTU_LOG_DIR", "")

		layout := ResolveLayout(&config.Config{})

		for name, path := range map[string]string{
			"Workspace":  layout.Workspace,
			"DockerRoot": layout.DockerRoot,
			"ModelCache": layout.ModelCache,
			"StateDir":   layout.StateDir,
		} {
			if !strings.HasPrefix(path, "/mnt/instance-store") {
				t.Errorf("%s = %q, want it on the instance store", name, path)
			}
		}
	})
}

// TestDisabledFeaturesSucceed checks that features switched off in the config
// are a no-op rather than a failure, since every task runs on every cycle.
func TestDisabledFeaturesSucceed(t *testing.T) {
	ctx := context.Background()
	layout := testLayout(t)

	if err := setupS3(&config.Config{}, layout)(ctx); err != nil {
		t.Errorf("s3 with enabled=false: %v", err)
	}

	if err := setupS3(&config.Config{S3: config.S3Config{Enabled: true}}, layout)(ctx); err == nil {
		t.Error("s3 enabled with no bucket: want an error")
	}

	if err := setupOpenCode(&config.Config{}, layout)(ctx); err != nil {
		t.Errorf("opencode disabled: %v", err)
	}

	if err := setupTooling(&config.Config{}, layout)(ctx); err != nil {
		t.Errorf("tooling with no packages: %v", err)
	}

	if err := setupSync(&config.Config{}, layout)(ctx); err != nil {
		t.Errorf("sync with s3 disabled: %v", err)
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

// assertAllCompleted fails unless every task in the map completed. The
// expected state is part of the name rather than a parameter because every
// caller wants COMPLETED: a caller that wanted anything else should be
// asserting on that one task directly, where a failure is readable.
func assertAllCompleted(t *testing.T, results map[string]TaskResult) {
	t.Helper()

	for name, result := range results {
		if result.State != Completed {
			t.Errorf("%s = %s, want %s (%v)", name, result.State, Completed, result.Error)
		}
	}
}

func TestBodiedTasksRunInDependencyOrder(t *testing.T) {
	f := newFixture(t)

	results := runTasks(t, f.tasks())

	assertAllCompleted(t, results)

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

		assertAllCompleted(t, results)

		if f.execs.Load() != int32(cycle+1) {
			t.Fatalf("cycle %d: uname ran %d times", cycle+1, f.execs.Load())
		}
	}

	states := persistedStates(t, db)
	if len(states) != 4 {
		t.Fatalf("persisted %v, want 4 rows after 3 cycles", states)
	}

	assertAllCompleted(t, resultsOf(states))

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

	assertAllCompleted(t, recovered)
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

	assertAllCompleted(t, runTasks(t, tasks))

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
	if drifted["report"].State != Blocked {
		t.Errorf("report = %s, want BLOCKED", drifted["report"].State)
	}

	// The service comes back and the next cycle is healthy again.
	f.setServiceRunning(true)

	assertAllCompleted(t, runTasks(t, tasks))
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

	assertAllCompleted(t, runTasks(t, tasks))

	if arrived.Load() != 2 {
		t.Errorf("arrivals = %d, want 2", arrived.Load())
	}
}
