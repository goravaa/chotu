package setup

import (
	"context"
	"testing"
	"time"
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
