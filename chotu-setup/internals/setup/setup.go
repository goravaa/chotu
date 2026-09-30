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
	Run          func(context.Context) error
}
