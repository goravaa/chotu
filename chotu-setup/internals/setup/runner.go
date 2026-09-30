package setup

import (
	"context"
	"fmt"
	"log"
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

type Runner struct {
	tasks map[string]*Task
	store TaskStateStore

	attempts   int
	retryDelay time.Duration
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

func (r *Runner) Run(ctx context.Context) map[string]TaskResult {
	results := make(map[string]TaskResult)

	var mu sync.Mutex
	var wg sync.WaitGroup

	done := make(map[string]chan struct{})

	for name := range r.tasks {
		done[name] = make(chan struct{})
	}

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
			log.Printf(
				"task %s: failed to persist state %s: %v",
				task.Name,
				state,
				storeErr,
			)
		}
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

		log.Printf(
			"task %s: panic recovered: %v\n%s",
			task.Name,
			rec,
			stack,
		)

		record(Failed, fmt.Errorf("panic: %v", rec))
	}()

	if err := r.waitForDependencies(ctx, task, done, results, mu); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			log.Printf("task %s: interrupted while waiting for dependencies", task.Name)
		} else {
			log.Printf("task %s: %v", task.Name, err)
		}

		record(Failed, err)

		return
	}

	record(Running, nil)

	err := r.runWithRetries(ctx, task)

	if err != nil {
		record(Failed, err)

		return
	}

	record(Completed, nil)
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
				log.Printf("task %s: succeeded on attempt %d/%d", task.Name, attempt, attempts)
			}

			return nil
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%w (last error: %w)", ctxErr, err)
		}

		if attempt >= attempts {
			log.Printf(
				"task %s: failed after %d attempt(s): %v",
				task.Name,
				attempt,
				err,
			)

			return err
		}

		log.Printf(
			"task %s: attempt %d/%d failed: %v; retrying in %s",
			task.Name,
			attempt,
			attempts,
			err,
			delay,
		)

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

		log.Printf(
			"task %s: panic recovered: %v\n%s",
			task.Name,
			rec,
			debug.Stack(),
		)
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
