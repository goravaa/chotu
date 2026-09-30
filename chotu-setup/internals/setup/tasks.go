package setup

import "context"

const (
	TaskStorage   = "storage"
	TaskDocker    = "docker"
	TaskGPU       = "gpu"
	TaskDockerGPU = "docker-gpu"
	TaskRclone    = "rclone"
	TaskS3        = "s3"
	TaskTooling   = "tooling"
	TaskOpenCode  = "opencode"
	TaskVLLM      = "vllm"
)

func Tasks() []*Task {
	return []*Task{
		{
			Name: TaskStorage,
			Run:  setupStorage,
		},
		{
			Name: TaskDocker,
			Run:  setupDocker,
		},
		{
			Name: TaskGPU,
			Run:  setupGPU,
		},
		{
			Name:         TaskDockerGPU,
			Dependencies: []string{TaskDocker, TaskGPU},
			Run:          setupDockerGPU,
		},
		{
			Name:         TaskRclone,
			Dependencies: []string{TaskStorage},
			Run:          setupRclone,
		},
		{
			Name:         TaskS3,
			Dependencies: []string{TaskRclone},
			Run:          setupS3,
		},
		{
			Name: TaskTooling,
			Run:  setupTooling,
		},
		{
			Name:         TaskOpenCode,
			Dependencies: []string{TaskTooling},
			Run:          setupOpenCode,
		},
		{
			Name:         TaskVLLM,
			Dependencies: []string{TaskDockerGPU},
			Run:          setupVLLM,
		},
	}
}

func setupStorage(context.Context) error   { return nil }
func setupDocker(context.Context) error    { return nil }
func setupGPU(context.Context) error       { return nil }
func setupDockerGPU(context.Context) error { return nil }
func setupRclone(context.Context) error    { return nil }
func setupS3(context.Context) error        { return nil }
func setupTooling(context.Context) error   { return nil }
func setupOpenCode(context.Context) error  { return nil }
func setupVLLM(context.Context) error      { return nil }
