package setup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

const (
	// DefaultAttempts is the total number of attempts per task, so a task is
	// tried once and then retried DefaultAttempts-1 times.
	DefaultAttempts = 3

	// DefaultRetryDelay is the delay before the first retry. It doubles on
	// every subsequent retry.
	DefaultRetryDelay = 2 * time.Second

	// MaxRetryDelay caps the exponential backoff.
	MaxRetryDelay = 30 * time.Second
)

type TaskResult struct {
	State TaskState
	Error error
}

type TaskStateStore interface {
	SetTaskState(name string, state string, err error) error
}

// ProgressFunc receives a snapshot of every task's current result whenever the
// graph advances. Callers use it to publish live status during a long cycle.
type ProgressFunc func(results map[string]TaskResult)

type Runner struct {
	tasks map[string]*Task
	store TaskStateStore

	attempts   int
	retryDelay time.Duration
	onProgress ProgressFunc
}

func NewRunner(tasks []*Task, store TaskStateStore) *Runner {
	taskMap := make(map[string]*Task, len(tasks))

	for _, task := range tasks {
		taskMap[task.Name] = task
	}

	return &Runner{
		tasks:      taskMap,
		store:      store,
		attempts:   DefaultAttempts,
		retryDelay: DefaultRetryDelay,
	}
}

// WithRetryPolicy overrides the attempt count and the initial retry delay.
// Values below 1 attempt or a non-positive delay are ignored.
func (r *Runner) WithRetryPolicy(attempts int, delay time.Duration) *Runner {
	if attempts >= 1 {
		r.attempts = attempts
	}

	if delay > 0 {
		r.retryDelay = delay
	}

	return r
}

// WithProgress registers a callback invoked after each task state change,
// including the initial PENDING seed. The callback must not block for long:
// it runs on the task goroutine that just recorded the change.
func (r *Runner) WithProgress(fn ProgressFunc) *Runner {
	r.onProgress = fn
	return r
}

func (r *Runner) Run(ctx context.Context) map[string]TaskResult {
	results := make(map[string]TaskResult, len(r.tasks))

	var mu sync.Mutex
	var wg sync.WaitGroup

	done := make(map[string]chan struct{}, len(r.tasks))

	for name := range r.tasks {
		done[name] = make(chan struct{})
		results[name] = TaskResult{State: Pending}

		if storeErr := r.updateTaskState(name, Pending, nil); storeErr != nil {
			slog.Error("failed to persist state", "task", name, "state", Pending, "error", storeErr)
		}
	}

	r.notifyProgress(results, &mu)

	for _, task := range r.tasks {
		wg.Add(1)

		go func() {
			// Done must be deferred: a task that fails or panics still has
			// to release its dependents, otherwise they wait forever.
			defer wg.Done()
			defer close(done[task.Name])

			r.runTask(ctx, task, done, results, &mu)
		}()
	}

	wg.Wait()

	return results
}

func (r *Runner) runTask(
	ctx context.Context,
	task *Task,
	done map[string]chan struct{},
	results map[string]TaskResult,
	mu *sync.Mutex,
) {
	record := func(state TaskState, err error) {
		mu.Lock()
		results[task.Name] = TaskResult{
			State: state,
			Error: err,
		}
		mu.Unlock()

		if storeErr := r.updateTaskState(task.Name, state, err); storeErr != nil {
			slog.Error("failed to persist state", "task", task.Name, "state", state, "error", storeErr)
		}

		r.notifyProgress(results, mu)
	}

	// A panic in a task would otherwise terminate the whole process, since Go
	// has no per-goroutine recovery. Turn it into a normal failure so the
	// retry loop and the dependent tasks see a normal error.
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}

		stack := string(debug.Stack())

		slog.Error("task panic recovered", "task", task.Name, "panic", rec, "stack", stack)

		record(Failed, fmt.Errorf("panic: %v", rec))
	}()

	if err := r.waitForDependencies(ctx, task, done, results, mu); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			slog.Error("interrupted while waiting for dependencies", "task", task.Name)
		} else {
			slog.Error("dependency wait failed", "task", task.Name, "error", err)
		}

		record(Blocked, err)

		return
	}

	slog.Info("task started", "task", task.Name, "state", "running")
	record(Running, nil)

	err := r.runWithRetries(ctx, task)

	if err != nil {
		var rebootErr ErrRebootRequired
		if errors.As(err, &rebootErr) {
			slog.Info("task requires reboot", "task", task.Name, "state", "reboot_required")
			record(RebootRequired, err)
		} else {
			record(Failed, err)
		}

		return
	}

	slog.Info("task completed", "task", task.Name, "state", "completed")
	record(Completed, nil)
}

func (r *Runner) notifyProgress(results map[string]TaskResult, mu *sync.Mutex) {
	if r.onProgress == nil {
		return
	}

	mu.Lock()
	snapshot := make(map[string]TaskResult, len(results))
	for name, result := range results {
		snapshot[name] = result
	}
	mu.Unlock()

	r.onProgress(snapshot)
}

// runWithRetries runs the task, retrying transient failures and panics with
// exponential backoff until the attempt budget is exhausted.
func (r *Runner) runWithRetries(ctx context.Context, task *Task) error {
	attempts := r.attempts
	if task.Retries > 0 {
		attempts = task.Retries
	}

	delay := r.retryDelay

	var err error

	for attempt := 1; ; attempt++ {
		err = r.runGuarded(ctx, task)

		if err == nil {
			if attempt > 1 {
				slog.Info("task succeeded on retry", "task", task.Name, "attempt", attempt, "max_attempts", attempts)
			}

			return nil
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%w (last error: %w)", ctxErr, err)
		}

		if attempt >= attempts {
			slog.Error("task failed after all attempts", "task", task.Name, "attempts", attempt, "error", err)

			return err
		}

		slog.Warn("task attempt failed", "task", task.Name, "attempt", attempt, "max_attempts", attempts, "error", err, "retry_delay", delay)

		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (last error: %w)", ctx.Err(), err)
		case <-time.After(delay):
		}

		delay *= 2

		if delay > MaxRetryDelay {
			delay = MaxRetryDelay
		}
	}
}

// runGuarded executes a single attempt, converting a panic into an error and
// logging the stack so the failure is diagnosable.
func (r *Runner) runGuarded(ctx context.Context, task *Task) (err error) {
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}

		err = fmt.Errorf("panic: %v", rec)

		slog.Error("task attempt panic recovered", "task", task.Name, "panic", rec, "stack", string(debug.Stack()))
	}()

	return task.Run(ctx)
}

func (r *Runner) updateTaskState(
	name string,
	state TaskState,
	err error,
) error {
	if r.store == nil {
		return nil
	}

	return r.store.SetTaskState(name, string(state), err)
}

func (r *Runner) waitForDependencies(
	ctx context.Context,
	task *Task,
	done map[string]chan struct{},
	results map[string]TaskResult,
	mu *sync.Mutex,
) error {
	for _, dependency := range task.Dependencies {
		_, exists := r.tasks[dependency]

		if !exists {
			return fmt.Errorf(
				"task %q depends on unknown task %q",
				task.Name,
				dependency,
			)
		}

		select {
		case <-done[dependency]:
			mu.Lock()
			result := results[dependency]
			mu.Unlock()

			if result.State != Completed {
				return fmt.Errorf(
					"dependency %q for task %q did not complete",
					dependency,
					task.Name,
				)
			}

		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}
