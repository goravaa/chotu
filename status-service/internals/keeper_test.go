package internals

import (
	"testing"
	"time"

	"github.com/goravaa/chotu/status"
)

// keeperWithClock returns a keeper driven by a manually advanced clock.
func keeperWithClock(start time.Time) (*Keeper, *time.Time) {
	current := start

	return NewKeeperWithClock(func() time.Time { return current }, start), &current
}

func documentWithTasks(tasks map[string]string) status.Status {
	document := status.New()

	for name, state := range tasks {
		document.Setup.Tasks[name] = status.ServiceStatus{State: state}
	}

	return *document
}

func took(t *testing.T, k *Keeper, name string) *float64 {
	t.Helper()

	return k.Snapshot().Tasks[name].TookSeconds
}

// TestKeeperTracksCompletion is the core stopwatch: first seen pending, then
// completed 75s later, so the page can report "took 1m15s".
func TestKeeperTracksCompletion(t *testing.T) {
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	keeper, current := keeperWithClock(start)

	keeper.Observe(documentWithTasks(map[string]string{"gpu": "PENDING"}))
	*current = start.Add(10 * time.Second)
	keeper.Observe(documentWithTasks(map[string]string{"gpu": "RUNNING"}))
	*current = start.Add(75 * time.Second)
	keeper.Observe(documentWithTasks(map[string]string{"gpu": "COMPLETED"}))

	got := took(t, keeper, "gpu")
	if got == nil {
		t.Fatal("took_seconds is null, want 75")
	}

	if *got != 75 {
		t.Errorf("took = %vs, want 75s", *got)
	}

	if uptime := keeper.Snapshot().UptimeSeconds; uptime != 75 {
		t.Errorf("uptime = %ds, want 75s", uptime)
	}
}

// TestKeeperTracksFailure verifies errored tasks get a duration too: the page
// reports how long a task ran before it failed, not just that it failed.
func TestKeeperTracksFailure(t *testing.T) {
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	keeper, current := keeperWithClock(start)

	keeper.Observe(documentWithTasks(map[string]string{"vllm": "RUNNING"}))
	*current = start.Add(120 * time.Second)
	keeper.Observe(documentWithTasks(map[string]string{"vllm": "FAILED"}))

	got := took(t, keeper, "vllm")
	if got == nil {
		t.Fatal("took_seconds is null, want 120")
	}

	if *got != 120 {
		t.Errorf("took = %vs, want 120s", *got)
	}
}

// TestKeeperUnknownWhenAlreadyTerminal covers a service restart mid-setup: a
// task first seen already completed finished before watching began, so there
// is no duration to report and the page must show a dash rather than 0s.
func TestKeeperUnknownWhenAlreadyTerminal(t *testing.T) {
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	keeper, _ := keeperWithClock(start)

	keeper.Observe(documentWithTasks(map[string]string{"docker": "COMPLETED"}))

	if got := took(t, keeper, "docker"); got != nil {
		t.Errorf("took = %vs, want null for a task already done on first sight", *got)
	}

	if state := keeper.Snapshot().Tasks["docker"].State; state != "completed" {
		t.Errorf("state = %q, want completed (lowercased)", state)
	}
}

// TestKeeperRestartsOnRegression covers a task leaving a terminal state: a new
// cycle re-running it restarts the stopwatch instead of reporting stale time.
func TestKeeperRestartsOnRegression(t *testing.T) {
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	keeper, current := keeperWithClock(start)

	keeper.Observe(documentWithTasks(map[string]string{"gpu": "PENDING"}))
	*current = start.Add(60 * time.Second)
	keeper.Observe(documentWithTasks(map[string]string{"gpu": "COMPLETED"}))

	if got := took(t, keeper, "gpu"); got == nil || *got != 60 {
		t.Fatalf("took = %v, want 60s before the regression", got)
	}

	*current = start.Add(90 * time.Second)
	keeper.Observe(documentWithTasks(map[string]string{"gpu": "RUNNING"}))

	if got := took(t, keeper, "gpu"); got != nil {
		t.Errorf("took = %vs after regression, want null while running again", *got)
	}

	*current = start.Add(100 * time.Second)
	keeper.Observe(documentWithTasks(map[string]string{"gpu": "COMPLETED"}))

	if got := took(t, keeper, "gpu"); got == nil || *got != 10 {
		t.Errorf("took = %v, want 10s measured from the restart", got)
	}
}

// TestKeeperIgnoresBlockedState verifies a blocked task is still waiting, not
// done: no duration is reported for it.
func TestKeeperIgnoresBlockedState(t *testing.T) {
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	keeper, current := keeperWithClock(start)

	keeper.Observe(documentWithTasks(map[string]string{"opencode": "PENDING"}))
	*current = start.Add(30 * time.Second)
	keeper.Observe(documentWithTasks(map[string]string{"opencode": "BLOCKED"}))

	if got := took(t, keeper, "opencode"); got != nil {
		t.Errorf("took = %vs, want null for a blocked task", *got)
	}
}
