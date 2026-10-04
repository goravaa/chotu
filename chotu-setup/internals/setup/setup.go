package setup

import "context"

type TaskState string

const (
	Pending   TaskState = "PENDING"
	Running   TaskState = "RUNNING"
	Completed TaskState = "COMPLETED"
	Failed    TaskState = "FAILED"
	// Blocked means the task never ran because a dependency did not complete.
	// It is distinct from Failed so the status page can lead with the root
	// cause rather than a cascade of dependency messages.
	Blocked TaskState = "BLOCKED"
)

type Task struct {
	Name         string
	Dependencies []string
	// Retries overrides the runner's attempt count for this task when > 0.
	Retries int
	Run     func(context.Context) error
}
