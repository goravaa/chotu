package setup

import "context"

type TaskState string

const (
	Pending   TaskState = "PENDING"
	Running   TaskState = "RUNNING"
	Completed TaskState = "COMPLETED"
	Failed    TaskState = "FAILED"
)

type Task struct {
	Name         string
	Dependencies []string
	// Retries overrides the runner's attempt count for this task when > 0.
	Retries int
	Run     func(context.Context) error
}
