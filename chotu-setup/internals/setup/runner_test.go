package setup

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
