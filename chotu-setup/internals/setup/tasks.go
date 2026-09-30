package setup

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/config"
)

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

const commandTimeout = 60 * time.Second

// Tasks returns the setup tasks in dependency order. Each task body is
// idempotent: it verifies the machine is in the desired state, repairs what
// it can, and reports what is still wrong. The daemon re-runs every task on
// every cycle, so a task must never fail just because it already ran.
func Tasks(cfg *config.Config) []*Task {
	return []*Task{
		{
			Name: TaskStorage,
			Run:  setupStorage(cfg),
		},
		{
			Name: TaskDocker,
			Run:  setupDocker(cfg),
		},
		{
			Name: TaskGPU,
			Run:  setupGPU(cfg),
		},
		{
			Name:         TaskDockerGPU,
			Dependencies: []string{TaskDocker, TaskGPU},
			Run:          setupDockerGPU(cfg),
		},
		{
			Name:         TaskRclone,
			Dependencies: []string{TaskStorage},
			Run:          setupRclone(cfg),
		},
		{
			Name:         TaskS3,
			Dependencies: []string{TaskRclone},
			Run:          setupS3(cfg),
		},
		{
			Name: TaskTooling,
			Run:  setupTooling(cfg),
		},
		{
			Name:         TaskOpenCode,
			Dependencies: []string{TaskTooling},
			Run:          setupOpenCode(cfg),
		},
		{
			Name:         TaskVLLM,
			Dependencies: []string{TaskDockerGPU},
			Run:          setupVLLM(cfg),
		},
	}
}

// commandExists reports whether a binary is on PATH.
func commandExists(name string) bool {
	_, err := exec.LookPath(name)

	return err == nil
}

// run executes a command and returns its combined output. Commands are given
// a deadline so a hung service cannot stall a cycle until the retry budget
// expires.
func run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()

	trimmed := strings.TrimSpace(string(out))

	if err != nil {
		return trimmed, fmt.Errorf(
			"%s %s: %w: %s",
			name,
			strings.Join(args, " "),
			err,
			trimmed,
		)
	}

	return trimmed, nil
}

// unitIsActive reports whether a systemd unit is running.
func unitIsActive(ctx context.Context, unit string) bool {
	_, err := run(ctx, "systemctl", "is-active", "--quiet", unit)

	return err == nil
}

func isRoot() bool { return os.Geteuid() == 0 }

// State directories default to where the service runs, but stay overridable so
// the task can be exercised without root.
const defaultStateDir = "/var/lib/chotu"

func stateDir() string {
	if dir := os.Getenv("CHOTU_STATE_DIR"); dir != "" {
		return dir
	}

	return defaultStateDir
}

// logDir lives under the state directory so a custom -db stays self-contained
// and does not need a second root-owned path.
// dataDir is where the persistent workspace volume is expected. The config has
// no field for it yet, so it stays overridable by the environment.
func dataDir() string {
	if dir := os.Getenv("CHOTU_DATA_DIR"); dir != "" {
		return dir
	}

	return "/data"
}

func logDir() string {
	if dir := os.Getenv("CHOTU_LOG_DIR"); dir != "" {
		return dir
	}

	return filepath.Join(stateDir(), "log")
}

// setupStorage verifies the machine has usable storage and the directories
// Chotu writes to.
func setupStorage(cfg *config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		for _, dir := range []string{stateDir(), logDir()} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", dir, err)
			}
		}

		// A persistent disk is attached and formatted by the provisioner, so it
		// is verified rather than created: creating the directory would make
		// this task pass on a machine that has no disk at all.
		if cfg != nil && cfg.Machine.Storage.Persist {
			info, err := os.Stat(dataDir())
			if err != nil {
				return fmt.Errorf(
					"machine.storage.persist is set but %s is not present: "+
						"attach the persistent volume, or set persist: false: %w",
					dataDir(),
					err,
				)
			}

			if !info.IsDir() {
				return fmt.Errorf("%s exists but is not a directory", dataDir())
			}

			log.Printf("[storage] persistent volume present at %s", dataDir())
		}

		usage, err := run(ctx, "df", "-h", "/")
		if err != nil {
			return fmt.Errorf("check root disk: %w", err)
		}

		log.Printf("[storage] root filesystem:\n%s", usage)

		if cfg != nil && cfg.Machine.Storage.RootDiskSizeGB > 0 {
			log.Printf(
				"[storage] configured root disk: %d GB %s",
				cfg.Machine.Storage.RootDiskSizeGB,
				cfg.Machine.Storage.RootDiskType,
			)
		}

		return nil
	}
}

// setupDocker verifies Docker is installed and its daemon is responding.
func setupDocker(*config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		if !commandExists("docker") {
			return fmt.Errorf("docker is not installed")
		}

		if _, err := run(ctx, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
			// The daemon may simply be stopped, which we can repair.
			if !isRoot() {
				return fmt.Errorf("docker daemon unreachable: %w", err)
			}

			log.Printf("[docker] daemon not responding, attempting to start it")

			if _, startErr := run(ctx, "systemctl", "start", "docker"); startErr != nil {
				return fmt.Errorf("start docker: %w", startErr)
			}

			if _, err := run(ctx, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
				return fmt.Errorf("docker daemon still unreachable: %w", err)
			}
		}

		version, err := run(ctx, "docker", "version", "--format", "{{.Server.Version}}")
		if err != nil {
			return fmt.Errorf("docker version: %w", err)
		}

		log.Printf("[docker] daemon healthy, server version %s", version)

		return nil
	}
}

// setupGPU verifies the NVIDIA driver is loaded and the GPU is visible.
func setupGPU(*config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		if !commandExists("nvidia-smi") {
			return fmt.Errorf("nvidia-smi is not installed: no NVIDIA driver")
		}

		out, err := run(ctx, "nvidia-smi",
			"--query-gpu=name,driver_version,memory.total",
			"--format=csv,noheader",
		)
		if err != nil {
			return fmt.Errorf("nvidia-smi: %w", err)
		}

		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				log.Printf("[gpu] %s", line)
			}
		}

		return nil
	}
}

// setupDockerGPU verifies Docker can schedule work on the GPU.
func setupDockerGPU(*config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		if !commandExists("nvidia-ctk") {
			return fmt.Errorf(
				"nvidia-ctk is not installed: install the NVIDIA container toolkit",
			)
		}

		out, err := run(ctx, "docker", "info", "--format", "{{json .Runtimes}}")
		if err != nil {
			return fmt.Errorf("read docker runtimes: %w", err)
		}

		if !strings.Contains(out, "nvidia") {
			return fmt.Errorf(
				"docker has no nvidia runtime configured: %s",
				out,
			)
		}

		log.Printf("[docker-gpu] runtimes: %s", out)

		return nil
	}
}

// setupRclone verifies rclone is installed and has a config directory.
func setupRclone(*config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		if !commandExists("rclone") {
			return fmt.Errorf("rclone is not installed")
		}

		configHome, err := os.UserHomeDir()
		if err != nil {
			configHome = "/root"
		}

		dir := filepath.Join(configHome, ".config", "rclone")

		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}

		version, err := run(ctx, "rclone", "version")
		if err != nil {
			return fmt.Errorf("rclone version: %w", err)
		}

		for _, line := range strings.Split(version, "\n") {
			if strings.HasPrefix(line, "rclone v") {
				log.Printf("[rclone] %s", strings.TrimSpace(line))

				break
			}
		}

		return nil
	}
}

// setupS3 verifies the workspace sync remote, when S3 is enabled.
func setupS3(cfg *config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil || !cfg.S3.Enabled {
			log.Printf("[s3] disabled in config, nothing to verify")

			return nil
		}

		if cfg.S3.Bucket == "" {
			return fmt.Errorf("s3 is enabled but s3.bucket_name is empty")
		}

		if !commandExists("rclone") {
			return fmt.Errorf("rclone is not installed, cannot verify the s3 remote")
		}

		remotes, err := run(ctx, "rclone", "listremotes")
		if err != nil {
			return fmt.Errorf("list rclone remotes: %w", err)
		}

		if !strings.Contains(remotes, "s3:") {
			return fmt.Errorf(
				"no s3 remote configured in rclone: run rclone config",
			)
		}

		target := "s3:" + cfg.S3.Bucket
		if cfg.S3.Sync.Prefix != "" {
			target += "/" + cfg.S3.Sync.Prefix
		}

		if _, err := run(ctx, "rclone", "lsd", target); err != nil {
			return fmt.Errorf("s3 target %s unreachable: %w", target, err)
		}

		log.Printf("[s3] remote healthy: %s", target)

		return nil
	}
}

// setupTooling verifies the user requested build tools are installed,
// installing any that are missing.
func setupTooling(cfg *config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil || len(cfg.Tooling.Packages) == 0 {
			log.Printf("[tooling] no packages requested")

			return nil
		}

		var missing []string

		for _, pkg := range cfg.Tooling.Packages {
			if commandExists(pkg) {
				continue
			}

			missing = append(missing, pkg)
		}

		if len(missing) == 0 {
			log.Printf(
				"[tooling] all %d requested packages are present",
				len(cfg.Tooling.Packages),
			)

			return nil
		}

		if !isRoot() {
			return fmt.Errorf(
				"missing packages %s: installing them needs root",
				strings.Join(missing, ", "),
			)
		}

		log.Printf("[tooling] installing %s", strings.Join(missing, ", "))

		env := append(
			os.Environ(),
			"DEBIAN_FRONTEND=noninteractive",
		)

		if _, err := run(ctx, "apt-get", "update"); err != nil {
			return fmt.Errorf("apt-get update: %w", err)
		}

		args := append([]string{"install", "-y", "--no-install-recommends"}, missing...)

		cmd := exec.CommandContext(ctx, "apt-get", args...)
		cmd.Env = env

		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf(
				"install %s: %w: %s",
				strings.Join(missing, ", "),
				err,
				strings.TrimSpace(string(out)),
			)
		}

		for _, pkg := range missing {
			if !commandExists(pkg) {
				return fmt.Errorf(
					"%s still missing after install",
					pkg,
				)
			}
		}

		log.Printf("[tooling] installed %s", strings.Join(missing, ", "))

		return nil
	}
}

// setupOpenCode verifies OpenCode is installed, when the user asked for it.
func setupOpenCode(cfg *config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil || !cfg.Coding.OpenCode {
			log.Printf("[opencode] disabled in config, nothing to verify")

			return nil
		}

		if !commandExists("opencode") {
			return fmt.Errorf(
				"opencode is not installed: install it with `snap install opencode`",
			)
		}

		version, err := run(ctx, "opencode", "--version")
		if err != nil {
			return fmt.Errorf("opencode --version: %w", err)
		}

		log.Printf("[opencode] %s", strings.TrimSpace(version))

		return nil
	}
}

// setupVLLM verifies the inference endpoint is serving the configured model.
func setupVLLM(cfg *config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil {
			log.Printf("[vllm] no config, nothing to verify")

			return nil
		}

		port := cfg.Inference.Port
		if port == 0 {
			port = 8000
		}

		endpoint := fmt.Sprintf("http://127.0.0.1:%d/v1/models", port)

		out, err := run(ctx, "curl", "-fsS", "--max-time", "10", endpoint)
		if err != nil {
			return fmt.Errorf(
				"inference endpoint %s is not responding: %w",
				endpoint,
				err,
			)
		}

		if cfg.Inference.Model != "" && !strings.Contains(out, cfg.Inference.Model) {
			return fmt.Errorf(
				"inference endpoint is not serving %s",
				cfg.Inference.Model,
			)
		}

		log.Printf("[vllm] serving on port %d", port)

		return nil
	}
}
