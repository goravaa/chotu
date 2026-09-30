package setup

import (
	"context"
	"errors"
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

	runner := NewRunner(tasks)

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

	runner := NewRunner(tasks)

	results := runner.Run(context.Background())

	if results["A"].State != Failed {
		t.Fatalf("A = %s", results["A"].State)
	}

	if results["B"].State != Failed {
		t.Fatalf("B = %s", results["B"].State)
	}
}
