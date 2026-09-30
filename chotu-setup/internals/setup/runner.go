package setup

import (
	"context"
	"fmt"
	"sync"
)

type TaskResult struct {
	State TaskState
	Error error
}

type TaskStateStore interface {
	SetTaskState(name string, state TaskState, err error) error
}

type Runner struct {
	tasks map[string]*Task
	store TaskStateStore
}

func NewRunner(tasks []*Task, store TaskStateStore) *Runner {
	taskMap := make(map[string]*Task, len(tasks))

	for _, task := range tasks {
		taskMap[task.Name] = task
	}

	return &Runner{
		tasks: taskMap,
		store: store,
	}
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
		task := task

		wg.Add(1)

		go func() {
			defer wg.Done()

			if err := r.waitForDependencies(ctx, task, done, results, &mu); err != nil {
				result := TaskResult{
					State: Failed,
					Error: err,
				}

				mu.Lock()
				results[task.Name] = result
				mu.Unlock()

				_ = r.updateTaskState(task.Name, Failed, err)

				close(done[task.Name])
				return
			}

			mu.Lock()
			results[task.Name] = TaskResult{
				State: Running,
			}
			mu.Unlock()

			_ = r.updateTaskState(task.Name, Running, nil)

			err := task.Run(ctx)

			state := Completed
			if err != nil {
				state = Failed
			}

			mu.Lock()
			results[task.Name] = TaskResult{
				State: state,
				Error: err,
			}
			mu.Unlock()

			_ = r.updateTaskState(task.Name, state, err)

			close(done[task.Name])
		}()
	}

	wg.Wait()

	return results
}

func (r *Runner) updateTaskState(
	name string,
	state TaskState,
	err error,
) error {
	if r.store == nil {
		return nil
	}

	return r.store.SetTaskState(name, state, err)
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