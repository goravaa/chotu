package setup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/config"
)

const (
	TaskStatus    = "status"
	TaskMachine   = "machine"
	TaskStorage   = "storage"
	TaskDocker    = "docker"
	TaskGPU       = "gpu"
	TaskDockerGPU = "docker-gpu"
	TaskRclone    = "rclone"
	TaskS3        = "s3"
	TaskTooling   = "tooling"
	TaskWorkspace = "workspace"
	TaskOpenCode  = "opencode"
	TaskVLLMPull  = "vllm-pull"
	TaskVLLM      = "vllm"
	TaskSync      = "sync"
)

type ErrRebootRequired struct {
	Message string
}

func (e ErrRebootRequired) Error() string {
	return e.Message
}

const (
	commandTimeout = 60 * time.Second

	// defaultNvidiaDriverPackage is pinned by Chotu rather than exposed in the
	// config, because the driver has to match the kernel the image was built with.
	defaultNvidiaDriverPackage = "nvidia-driver-580-server"
)

// nvidiaDriverPackage returns the driver package to install and the matching
// userspace package. The userspace libraries are t  he part that has to line up
// with the kernel module, which is why they are installed together and verified
// separately.
//
// The default is overridable because the right branch moves with the Ubuntu
// release: a machine on a newer release than Chotu was tested against needs a
// driver branch that actually exists for its kernel.
func nvidiaDriverPackage() (driver, utils string) {
	driver = envOr("CHOTU_NVIDIA_DRIVER_PACKAGE", defaultNvidiaDriverPackage)

	// nvidia-driver-580-server -> nvidia-utils-580-server
	return driver, "nvidia-utils-" + strings.TrimPrefix(driver, "nvidia-driver-")
}

// Tasks returns the setup tasks in dependency order. Each task body is
// idempotent: it installs what is missing, repairs what drifted, and verifies
// what it installed. The daemon re-runs every task on every cycle, so a task
// must never fail just because it already ran, and must be cheap when the
// machine is already correct.
func Tasks(cfg *config.Config) []*Task {
	layout := ResolveLayout(cfg)

	return []*Task{
		{
			Name: TaskStatus,
			Run:  setupStatusPage(cfg, layout),
		},
		{
			Name:         TaskMachine,
			Dependencies: []string{TaskStatus},
			Run:          setupMachine(cfg),
		},
		{
			Name:         TaskStorage,
			Dependencies: []string{TaskStatus, TaskMachine},
			Run:          setupStorage(cfg, layout),
		},
		{
			Name:         TaskDocker,
			Dependencies: []string{TaskStatus, TaskMachine, TaskStorage},
			Run:          setupDocker(cfg, layout),
		},
		{
			Name:         TaskGPU,
			Dependencies: []string{TaskStatus, TaskMachine, TaskStorage},
			Run:          setupGPU(cfg, layout),
		},
		{
			Name:         TaskDockerGPU,
			Dependencies: []string{TaskStatus, TaskMachine, TaskDocker, TaskGPU},
			Run:          setupDockerGPU(cfg, layout),
		},
		{
			Name:         TaskRclone,
			Dependencies: []string{TaskStatus, TaskMachine, TaskStorage},
			Run:          setupRclone(cfg, layout),
		},
		{
			Name:         TaskS3,
			Dependencies: []string{TaskStatus, TaskMachine, TaskRclone},
			Run:          setupS3(cfg, layout),
		},
		{
			Name:         TaskTooling,
			Dependencies: []string{TaskStatus, TaskMachine},
			Run:          setupTooling(cfg, layout),
		},
		{
			Name:         TaskWorkspace,
			Dependencies: []string{TaskStatus, TaskMachine, TaskStorage},
			Run:          setupWorkspace(cfg, layout),
		},
		{
			Name:         TaskOpenCode,
			Dependencies: []string{TaskStatus, TaskMachine, TaskWorkspace},
			Run:          setupOpenCode(cfg, layout),
		},
		{
			// The warmup pulls the multi-GB inference image as soon as the
			// Docker daemon is up, while the GPU chain (driver install, CDI,
			// runtime wiring) still runs in parallel. It depends only on the
			// docker chain precisely so it overlaps the GPU chain instead of
			// waiting behind it.
			//
			// Best-effort by design: it always reports success and setupVLLM
			// does the authoritative inspect+pull. A registry outage must warm
			// nothing, not block the vLLM task behind a failed dependency.
			// Retries is 1 because retrying a gigabytes-large pull inside
			// this task only duplicates what setupVLLM already does.
			Name:         TaskVLLMPull,
			Dependencies: []string{TaskStatus, TaskMachine, TaskStorage, TaskDocker},
			Retries:      1,
			Run:          setupVLLMImageWarmup(cfg),
		},
		{
			Name:         TaskVLLM,
			Dependencies: []string{TaskStatus, TaskMachine, TaskDockerGPU, TaskStorage, TaskVLLMPull},
			Run:          setupVLLM(cfg, layout),
		},
		{
			Name:         TaskSync,
			Dependencies: []string{TaskStatus, TaskMachine, TaskWorkspace, TaskS3},
			Run:          setupSync(cfg, layout),
		},
	}
}

// commandExists reports whether a binary is on PATH.
func commandExists(name string) bool {
	_, err := exec.LookPath(name)

	return err == nil
}

// run executes a command under a short deadline and returns its combined
// output. Commands are given a deadline so a hung service cannot stall a cycle
// until the retry budget expires.
func run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	return execOutputCtx(ctx, name, args...)
}

func execOutputCtx(ctx context.Context, name string, args ...string) (string, error) {
	slog.Debug("executing command", "command", name, "args", args)
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		slog.Debug("command failed", "command", name, "error", err, "output", string(out))
	} else {
		slog.Debug("command succeeded", "command", name)
	}

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

// execOutput runs a command with the same short deadline as run, for the
// helpers that do not take a context.
func execOutput(name string, args ...string) (string, error) {
	return run(context.Background(), name, args...)
}

func isRoot() bool { return os.Geteuid() == 0 }

// expectedUbuntuMajor is the release Chotu is built and tested against. A
// mismatch is reported rather than fatal: the tasks that actually depend on the
// OS, the driver and the container runtime, are the real gate, and they fail
// with a specific error if the release is genuinely wrong.
const expectedUbuntuMajor = "26"

// setupMachine checks the things the rest of the setup assumes but cannot fix.
// A wrong OS or architecture is a provisioning mistake, so it is reported
// plainly rather than papered over.
func setupMachine(cfg *config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		release, err := os.ReadFile("/etc/os-release")
		if err != nil {
			return fmt.Errorf("read /etc/os-release: %w", err)
		}

		contents := string(release)
		id := osReleaseValue(contents, "ID")
		version := osReleaseValue(contents, "VERSION_ID")

		if id != "ubuntu" {
			return fmt.Errorf("unsupported operating system %q: Chotu targets Ubuntu", id)
		}

		slog.Info(fmt.Sprintf("%s %s %s", id, version, arch()), "component", "machine")

		if major, _, _ := strings.Cut(version, "."); major != expectedUbuntuMajor {
			slog.Info(fmt.Sprintf("[machine] WARNING: expected Ubuntu %s, found %s. machine.image_id in the "+
				"config has to point at an Ubuntu %s AMI", expectedUbuntuMajor,
				version,
				expectedUbuntuMajor))
		}

		if arch() != "x86_64" {
			return fmt.Errorf("unsupported architecture %s: Chotu targets x86_64", arch())
		}

		if out, err := run(ctx, "lspci", "-nn"); err != nil {
			return fmt.Errorf("check PCI devices: %w", err)
		} else if !strings.Contains(strings.ToLower(out), "nvidia") {
			return fmt.Errorf(
				"no NVIDIA GPU is attached: the instance type in config is %q",
				instanceType(cfg),
			)
		}

		return nil
	}
}

func instanceType(cfg *config.Config) string {
	if cfg == nil || cfg.Machine.InstanceType == "" {
		return "(unset)"
	}

	return cfg.Machine.InstanceType
}

func arch() string {
	if out, err := execOutput("uname", "-m"); err == nil {
		return strings.TrimSpace(out)
	}

	return "unknown"
}

// osReleaseValue pulls a key out of an os-release file without sourcing it.
func osReleaseValue(contents, key string) string {
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, key+"=") {
			continue
		}

		return strings.Trim(strings.TrimPrefix(line, key+"="), `"'`)
	}

	return ""
}

// setupStorage verifies the instance store and creates the layout directories
// that other tasks write into (state, logs, model cache, docker root).
// Workspace creation stays in the workspace task.
//
// /data must always be mounted as the EC2 instance store. In ephemeral mode it
// is the primary storage. In persistent mode it is scratch storage.
func setupStorage(cfg *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		logLayout(layout)

		if err := os.MkdirAll(layout.DataDir, 0755); err != nil {
			return fmt.Errorf("create data directory %s: %w", layout.DataDir, err)
		}

		mounted, mountErr := isMountpoint(layout.DataDir)
		if mountErr != nil {
			return fmt.Errorf("check mountpoint %s: %w", layout.DataDir, mountErr)
		}

		if !mounted {
			if err := mountInstanceStore(layout.DataDir); err != nil {
				return fmt.Errorf("mount instance store: %w", err)
			}
		}

		for _, dir := range []string{
			layout.StateDir,
			layout.LogDir,
			layout.ModelCache,
		} {
			if _, err := ensureDir(dir, layout.RunUser); err != nil {
				return err
			}
		}

		// Docker runs as root and owns its data root. Creating it here means
		// the docker task can point data-root at a real path under /data in
		// ephemeral mode instead of letting dockerd invent one after the fact.
		if _, err := ensureDir(layout.DockerRoot, ""); err != nil {
			return err
		}

		usage, err := run(ctx, "df", "-h", layout.StateDir)
		if err != nil {
			return fmt.Errorf("check storage for %s: %w", layout.StateDir, err)
		}

		slog.Info(
			fmt.Sprintf("state filesystem:\n%s", usage),
			"component",
			"storage",
		)

		if cfg != nil && cfg.Machine.Storage.RootDiskSizeGB > 0 {
			slog.Info(
				fmt.Sprintf(
					"configured root disk: %d GB %s",
					cfg.Machine.Storage.RootDiskSizeGB,
					cfg.Machine.Storage.RootDiskType,
				),
				"component",
				"storage",
			)
		}

		return nil
	}
}

// setupDocker installs the Docker Engine if it is missing, points it at the
// storage the layout resolved, wires the NVIDIA runtime into it and starts the
// daemon.
func setupDocker(_ *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if !packageInstalled("docker-ce") || !commandExists("docker") {
			if err := installDockerEngine(ctx); err != nil {
				return err
			}
		}

		wroteConfig, err := writeDockerDaemonConfig(layout)
		if err != nil {
			return err
		}

		if wroteConfig {
			slog.Info("restarting docker to apply daemon.json", "component", "docker")

			if _, err := run(ctx, "systemctl", "restart", "docker"); err != nil {
				return fmt.Errorf("restart docker after daemon.json change: %w", err)
			}
		} else if !unitRunning(ctx, "docker") {
			slog.Info("starting the daemon", "component", "docker")

			if _, err := run(ctx, "systemctl", "enable", "--now", "docker"); err != nil {
				return fmt.Errorf("start docker: %w", err)
			}
		}

		version, err := run(ctx, "docker", "version", "--format", "{{.Server.Version}}")
		if err != nil {
			// A daemon that was just reconfigured needs a moment before it
			// answers, so retry briefly rather than failing the cycle.
			if restarted, restartErr := run(ctx, "systemctl", "restart", "docker"); restartErr == nil {
				slog.Info(fmt.Sprintf("restarted the daemon: %s", restarted), "component", "docker")

				version, err = run(ctx, "docker", "version", "--format", "{{.Server.Version}}")
			}
		}

		if err != nil {
			return fmt.Errorf("docker daemon unreachable: %w", err)
		}

		if err := addUserToDockerGroup(layout); err != nil {
			return err
		}

		slog.Info(fmt.Sprintf("healthy, server version %s, root %s", version, layout.DockerRoot), "component", "docker")

		return nil
	}
}

func installDockerEngine(ctx context.Context) error {
	if !isRoot() {
		return fmt.Errorf("docker is not installed and installing it needs root")
	}

	if err := addDockerAptSource(); err != nil {
		return err
	}

	return aptInstall(ctx,
		"docker-ce", "docker-ce-cli", "containerd.io",
		"docker-buildx-plugin", "docker-compose-plugin",
	)
}

// addDockerAptSource installs Docker's signing key and the source line for this
// machine's Ubuntu suite. Docker does not publish a single source list that
// covers every suite, so the codename is substituted in rather than fetched.
func addDockerAptSource() error {
	const (
		keyURL   = "https://download.docker.com/linux/ubuntu/gpg"
		keyring  = "/etc/apt/keyrings/docker.asc"
		listPath = "/etc/apt/sources.list.d/docker.list"
	)

	if _, err := os.Stat(keyring); err != nil {
		key, err := execOutput("curl", "-fsSL", keyURL)
		if err != nil {
			return fmt.Errorf("fetch the docker signing key: %w", err)
		}

		if _, err := writeFileIfChanged(keyring, key, 0o644); err != nil {
			return err
		}
	}

	codename := osReleaseValue(mustReadFile("/etc/os-release"), "VERSION_CODENAME")
	if codename == "" {
		return fmt.Errorf("cannot determine the Ubuntu codename for the docker repository")
	}

	dpkgArch, err := execOutput("dpkg", "--print-architecture")
	if err != nil {
		return fmt.Errorf("determine the dpkg architecture: %w", err)
	}

	source := fmt.Sprintf(
		"deb [arch=%s signed-by=%s] https://download.docker.com/linux/ubuntu %s stable\n",
		strings.TrimSpace(dpkgArch),
		keyring,
		codename,
	)

	_, err = writeFileIfChanged(listPath, source, 0o644)

	return err
}

func mustReadFile(path string) string {
	contents, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	return string(contents)
}

// writeDockerDaemonConfig points the daemon at the resolved data root and adds
// the NVIDIA runtime. The file is only written when it changes, so a steady
// cycle does not bounce the daemon. Returns whether the file was written.
func writeDockerDaemonConfig(layout Layout) (bool, error) {
	daemon := map[string]any{
		"runtimes": map[string]any{
			"nvidia": map[string]any{
				"args": []string{},
				"path": "nvidia-container-runtime",
			},
		},
	}

	// The data root only needs setting when Docker is not already on the root
	// disk. Writing it in persistent mode would be harmless but would rewrite
	// the config and restart the daemon on the first cycle.
	if layout.DockerRoot != "/var/lib/docker" {
		daemon["data-root"] = layout.DockerRoot
	}

	encoded, err := jsonMarshalIndent(daemon)
	if err != nil {
		return false, fmt.Errorf("render the docker daemon config: %w", err)
	}

	wrote, err := writeFileIfChanged("/etc/docker/daemon.json", string(encoded), 0o644)
	if err != nil {
		return false, err
	}

	if wrote {
		slog.Info(fmt.Sprintf("wrote /etc/docker/daemon.json (data-root %s)", layout.DockerRoot), "component", "docker")
	}

	return wrote, nil
}

// asRunUser runs a command as the user the machine is prepared for, so a
// command that needs the user's own home, PATH or rclone config does not
// silently read root's instead.
func asRunUser(ctx context.Context, layout Layout, name string, args ...string) (string, error) {
	if layout.RunUser == "" || layout.RunUser == "root" {
		return run(ctx, name, args...)
	}

	full := append([]string{"-u", layout.RunUser, name}, args...)

	return run(ctx, "sudo", full...)
}

// ensureUserInGroups adds the run user to groups it needs, and does nothing
// when it is already a member or the user has no home to log into.
func ensureUserInGroups(layout Layout, groups ...string) error {
	if !isRoot() || layout.RunUser == "" || layout.RunUser == "root" {
		return nil
	}

	if !isDir(filepath.Join("/home", layout.RunUser)) {
		return nil
	}

	current, err := run(context.Background(), "id", "-nG", layout.RunUser)
	if err != nil {
		return fmt.Errorf("read the groups of %s: %w", layout.RunUser, err)
	}

	have := make(map[string]bool)
	for _, group := range strings.Fields(current) {
		have[group] = true
	}

	var missing []string

	for _, group := range groups {
		if !have[group] {
			missing = append(missing, group)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	if _, err := run(context.Background(), "usermod",
		"-aG", strings.Join(missing, ","), layout.RunUser); err != nil {
		return fmt.Errorf("add %s to the %s group(s): %w", layout.RunUser, strings.Join(missing, ", "), err)
	}

	slog.Info(fmt.Sprintf("added %s to %s; they apply on the next login", layout.RunUser,
		strings.Join(missing, ", ")), "component", "groups")

	return nil
}

func addUserToDockerGroup(layout Layout) error {
	return ensureUserInGroups(layout, "docker")
}

func addUserToVideoGroup(layout Layout) error {
	return ensureUserInGroups(layout, "video", "render")
}

// setupGPU installs the NVIDIA driver and container toolkit when they are
// missing, then verifies the GPU is actually usable.
//
// Installation alone does not mean the GPU is ready: a freshly installed driver
// needs a reboot, and the container toolkit is useless until the userspace
// libraries match the kernel module. Both are reported as the specific
// condition they are, rather than as a generic failure.
func setupGPU(_ *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if !isRoot() {
			if !commandExists("nvidia-smi") {
				return fmt.Errorf("nvidia-smi is not installed and installing the driver needs root")
			}
		} else {
			if err := installNvidiaStack(ctx, layout); err != nil {
				return err
			}
		}

		if !commandExists("nvidia-smi") {
			return fmt.Errorf("nvidia-smi is not installed: no NVIDIA driver")
		}

		out, err := run(ctx, "nvidia-smi",
			"--query-gpu=name,driver_version,memory.total",
			"--format=csv,noheader",
		)
		if err != nil {
			return classifyNvidiaSmiFailure(layout, err)
		}

		// The GPU is working, so any earlier reboot request is satisfied.
		clearRebootMarker(layout)

		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				slog.Info(line, "component", "gpu")
			}
		}

		if err := configureCdi(); err != nil {
			return err
		}

		if err := addUserToVideoGroup(layout); err != nil {
			return err
		}

		return nil
	}
}

// rebootPending reports whether a task previously recorded that the machine
// has to restart before setup can continue.
func rebootPending(layout Layout) bool {
	_, err := os.Stat(rebootMarker(layout))

	return err == nil
}

// classifyNvidiaSmiFailure turns "nvidia-smi failed" into the specific
// condition it is. The old message always blamed a pending reboot, which sent
// a machine with a genuinely broken driver (or a toolkit/userspace skew after
// the marker was cleared) down the reboot-and-hope path instead of the fix it
// needed.
func classifyNvidiaSmiFailure(layout Layout, err error) error {
	if rebootPending(layout) {
		return fmt.Errorf(
			"nvidia-smi cannot reach the GPU and a reboot is still pending: the driver "+
				"was installed but its kernel module is not loaded yet; reboot the machine "+
				"and the next cycle will finish the setup: %w",
			err,
		)
	}

	return fmt.Errorf(
		"nvidia-smi cannot reach the GPU and no reboot is pending: the driver userspace "+
			"and the kernel module are out of step (wrong driver branch for this kernel, "+
			"or headers were missing at install time). Check `dkms status` and "+
			"`modinfo nvidia`, and set CHOTU_NVIDIA_DRIVER_PACKAGE to the branch built "+
			"for the running kernel: %w",
		err,
	)
}

func installNvidiaStack(ctx context.Context, layout Layout) error {
	driver, utils := nvidiaDriverPackage()

	needsDriver := !packageInstalled(driver)

	if !needsDriver && packageInstalled("nvidia-container-toolkit") {
		return nil
	}

	if !isRoot() {
		return fmt.Errorf(
			"the NVIDIA stack is incomplete (driver=%t toolkit=%t) and installing it needs root",
			needsDriver,
			!packageInstalled("nvidia-container-toolkit"),
		)
	}

	if needsDriver {
		slog.Info(fmt.Sprintf("installing %s", driver), "component", "gpu")

		// Headers let the driver build its kernel module against the running
		// kernel. Their absence is not fatal, so a failure here is not.
		if err := aptInstall(ctx, "linux-headers-"+kernelRelease()); err != nil {
			slog.Info(fmt.Sprintf("could not install kernel headers: %v", err), "component", "gpu")
		}

		if err := aptInstall(ctx, driver, utils); err != nil {
			return fmt.Errorf(
				"the NVIDIA driver %s is not installable on this machine: %w. The driver "+
					"branch has to exist for the running kernel, and moves with the Ubuntu "+
					"release. Set CHOTU_NVIDIA_DRIVER_PACKAGE to the branch that does",
				driver,
				err,
			)
		}

		// Install the container toolkit in the same cycle. Its packages do not
		// need a loaded driver, only the repository and the binaries, so doing
		// it now saves a whole second cycle (plus another apt-get update)
		// after the reboot. A failure here is not fatal: the reboot is still
		// required for the driver, and the toolkit install is retried
		// post-reboot when setupGPU runs again.
		if err := installToolkit(ctx); err != nil {
			slog.Info(fmt.Sprintf("container toolkit install deferred until after the reboot: %v", err), "component", "gpu")
		}

		// The driver will not load until the machine reboots, and the daemon
		// cannot usefully verify the GPU before that. Record the need for a
		// reboot where the bootstrap step can see it, and say so explicitly:
		// the alternative is an error that looks like a broken GPU.
		if _, err := writeFileIfChanged(
			rebootMarker(layout),
			time.Now().UTC().Format(time.RFC3339)+"\n",
			0o644,
		); err != nil {
			return err
		}

		slog.Info(fmt.Sprintf("[gpu] %s installed. The GPU will not be usable until the machine reboots; "+
			"reboot it and the next cycle will finish the setup", driver))

		return ErrRebootRequired{Message: fmt.Sprintf("reboot required: %s was just installed and the driver is not loaded yet", driver)}
	}

	return installToolkit(ctx)
}

// installToolkit adds the NVIDIA container-toolkit repository and installs the
// package. It is used both pre-reboot (best-effort, inside the driver-install
// cycle so a fresh machine pays one apt transaction instead of two) and
// post-reboot (as the hard requirement before CDI verification).
func installToolkit(ctx context.Context) error {
	const (
		keyURL  = "https://nvidia.github.io/libnvidia-container/gpgkey"
		keyring = "/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg"
	)

	if _, err := aptRepo(ctx, "nvidia-container-toolkit", keyURL, keyring,
		"https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list"); err != nil {
		return err
	}

	return aptInstall(ctx, "nvidia-container-toolkit")
}

// rebootMarker is where a task records that the machine has to restart before
// setup can continue. The daemon cannot reboot a machine a user may be working
// on, so the decision is left to the bootstrap step, which runs at boot before
// anyone has logged in.
func rebootMarker(layout Layout) string {
	return filepath.Join(layout.StateDir, "reboot-required")
}

// clearRebootMarker drops the marker once the condition that created it is gone.
func clearRebootMarker(layout Layout) {
	if err := os.Remove(rebootMarker(layout)); err != nil && !os.IsNotExist(err) {
		slog.Info(fmt.Sprintf("could not clear the reboot marker: %v", err), "component", "gpu")
	}
}

func kernelRelease() string {
	if out, err := execOutput("uname", "-r"); err == nil {
		return strings.TrimSpace(out)
	}

	return ""
}

// configureCdi refreshes the CDI specification, which is what makes the GPU
// injectable into a container independently of the Docker runtime config.
func configureCdi() error {
	if !commandExists("nvidia-ctk") {
		return fmt.Errorf("nvidia-ctk is not installed: install the NVIDIA container toolkit")
	}

	if out, err := execOutput("systemctl", "start", "nvidia-cdi-refresh.service"); err != nil {
		slog.Info(fmt.Sprintf("nvidia-cdi-refresh.service: %s", out), "component", "gpu")
	}

	// A refresh service that was just installed may still be settling, so give
	// the spec a moment to appear before calling it missing.
	last := ""
	for attempt := range 3 {
		out, err := execOutput("nvidia-ctk", "cdi", "list")
		if err == nil && strings.Contains(out, "nvidia.com/gpu") {
			return nil
		}

		if err != nil {
			last = strings.TrimSpace(out + ": " + err.Error())
		} else {
			last = strings.TrimSpace(out)
		}

		slog.Info(fmt.Sprintf("CDI does not expose a GPU yet (attempt %d)", attempt+1), "component", "gpu")

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf(
		"NVIDIA CDI did not expose any GPU after 3 attempts: the driver userspace and the "+
			"kernel module are out of step, which usually needs a reboot. "+
			"Check `systemctl status nvidia-cdi-refresh.service` and `nvidia-ctk cdi list`; "+
			"last output: %s",
		last,
	)
}

// gpuSmokeImage is the image the docker-gpu smoke test runs nvidia-smi in.
// It is pinned to an LTS tag for the same reason the vLLM image is pinned: a
// floating `ubuntu` tag moves and re-pulls unpredictably, which makes a steady
// cycle slow and a failure hard to reproduce. The toolkit injects the driver
// libraries and nvidia-smi into the container, so any small Linux image works;
// this one is chosen because it is already the most likely to be cached.
const gpuSmokeImage = "ubuntu:22.04"

// setupDockerGPU proves the whole host-to-container path works, not just that
// the pieces are installed. The runtime being registered in the daemon config
// says nothing about whether a container can actually open the device.
func setupDockerGPU(_ *config.Config, _ Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if !commandExists("nvidia-ctk") {
			return fmt.Errorf("nvidia-ctk is not installed: install the NVIDIA container toolkit (gpu task)")
		}

		out, err := run(ctx, "docker", "info", "--format", "{{json .Runtimes}}")
		if err != nil {
			return fmt.Errorf("docker daemon unreachable, cannot verify the nvidia runtime (docker task): %w", err)
		}

		justConfigured := false

		if !strings.Contains(out, "nvidia") {
			if _, err := run(ctx, "nvidia-ctk", "runtime", "configure", "--runtime=docker"); err != nil {
				return fmt.Errorf("docker has no nvidia runtime and it could not be added: %w", err)
			}

			if _, err := run(ctx, "systemctl", "restart", "docker"); err != nil {
				return fmt.Errorf("restart docker after adding the nvidia runtime: %w", err)
			}

			out, err = run(ctx, "docker", "info", "--format", "{{json .Runtimes}}")
			if err != nil || !strings.Contains(out, "nvidia") {
				return fmt.Errorf("docker has no nvidia runtime configured: %s", out)
			}

			justConfigured = true
		}

		// Steady cycles already have the runtime and the gpu task verified
		// nvidia-smi on the host. Re-pulling ubuntu and running nvidia-smi in a
		// container every minute is correct but not cheap, so only smoke-test
		// right after we configure the runtime.
		if !justConfigured {
			slog.Info("nvidia runtime already configured", "component", "docker-gpu")

			return nil
		}

		runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		// The smoke image is tiny but still a network fetch on a cold machine.
		// Skip the pull when it is already cached so a steady cycle never pays
		// for it, and so a registry outage cannot fail a machine whose image
		// is already there.
		if _, err := execOutputCtx(runCtx, "docker", "image", "inspect", gpuSmokeImage); err != nil {
			if out, err := execOutputCtx(runCtx, "docker", "pull", gpuSmokeImage); err != nil {
				return fmt.Errorf(
					"pull the gpu smoke-test image %s (network/registry): %w: %s",
					gpuSmokeImage,
					err,
					strings.TrimSpace(out),
				)
			}
		}

		if out, err := execOutputCtx(runCtx, "docker", "run", "--rm",
			"--device", "nvidia.com/gpu=all", gpuSmokeImage, "nvidia-smi"); err != nil {
			return fmt.Errorf(
				"docker can run containers but not on the GPU (CDI device injection): %w: %s",
				err,
				strings.TrimSpace(out),
			)
		}

		slog.Info("a container can reach the GPU", "component", "docker-gpu")

		return nil
	}
}

// setupRclone installs rclone and writes the S3 remote. The remote uses
// env_auth, so credentials come from the EC2 instance role and no access key
// is ever written to disk.
func setupRclone(_ *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if !packageInstalled("rclone") && !commandExists("rclone") {
			if err := aptInstall(ctx, "rclone"); err != nil {
				return fmt.Errorf("rclone is not installed: %w", err)
			}
		}

		if !commandExists("rclone") {
			return fmt.Errorf("rclone is not installed and installing it needs root")
		}

		rcloneDir := filepath.Join(layout.Home, ".config", "rclone")

		if _, err := ensureDir(rcloneDir, layout.RunUser); err != nil {
			return err
		}

		remote := "[s3]\ntype = s3\nprovider = AWS\nenv_auth = true\n"

		configPath := filepath.Join(rcloneDir, "rclone.conf")
		if _, err := writeFileIfChanged(configPath, remote, 0o600); err != nil {
			return err
		}

		// Ensure the actual RunUser can read the file since writeFileIfChanged writes it as root
		if layout.RunUser != "" && layout.RunUser != "root" && isRoot() {
			if err := chownRecursive(configPath, layout.RunUser); err != nil {
				return fmt.Errorf("set rclone.conf ownership: %w", err)
			}
		}

		version, err := run(ctx, "rclone", "version")
		if err != nil {
			return fmt.Errorf("rclone version: %w", err)
		}

		for _, line := range strings.Split(version, "\n") {
			if strings.HasPrefix(line, "rclone v") {
				slog.Info(fmt.Sprintf("%s", strings.TrimSpace(line)), "component", "rclone")

				break
			}
		}

		// Confirm the remote is readable as the user the sync will run as. A
		// root-only config would pass the file checks and fail silently later.
		out, err := asRunUser(ctx, layout, "rclone", "listremotes")
		if err != nil {
			return fmt.Errorf("rclone cannot read its config as %s: %w", layout.RunUser, err)
		}

		if !strings.Contains(out, "s3:") {
			return fmt.Errorf("the s3 remote was not written to %s", rcloneDir)
		}

		return nil
	}
}

// setupS3 verifies the workspace bucket is reachable before anything starts
// writing to it. This is the check that catches a missing IAM policy, which
// would otherwise surface as a workspace that silently stops syncing.
func setupS3(cfg *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil || !cfg.S3.Enabled {
			slog.Info("disabled in config, nothing to verify", "component", "s3")

			return nil
		}

		if cfg.S3.Bucket == "" {
			return fmt.Errorf("s3 is enabled but s3.bucket_name is empty")
		}

		if !commandExists("rclone") {
			return fmt.Errorf("rclone is not installed, cannot verify the s3 remote")
		}

		target := s3Target(cfg)

		if out, err := asRunUser(ctx, layout, "rclone", "lsd", target); err != nil {
			return fmt.Errorf(
				"the s3 target %s is unreachable. Check that the instance role can reach the "+
					"bucket: %w: %s",
				target,
				err,
				strings.TrimSpace(out),
			)
		}

		slog.Info(fmt.Sprintf("remote healthy: %s", target), "component", "s3")

		return nil
	}
}

func s3Target(cfg *config.Config) string {
	target := "s3:" + cfg.S3.Bucket
	if prefix := strings.Trim(cfg.S3.Sync.Prefix, "/"); prefix != "" {
		target += "/" + prefix
	}

	return target
}

// setupTooling installs the packages the user asked for.
func setupTooling(cfg *config.Config, _ Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil || len(cfg.Tooling.Packages) == 0 {
			slog.Info("no packages requested", "component", "tooling")

			return nil
		}

		missing := aptPackagesMissing(cfg.Tooling.Packages)
		if len(missing) == 0 {
			slog.Info(fmt.Sprintf("all %d requested packages are present", len(cfg.Tooling.Packages)), "component", "tooling")

			return nil
		}

		if err := aptInstall(ctx, missing...); err != nil {
			return fmt.Errorf(
				"could not install the requested tooling %s. A package name that is not in "+
					"the Ubuntu repositories has to be installed another way: %w",
				strings.Join(missing, ", "),
				err,
			)
		}

		slog.Info(fmt.Sprintf("installed %s", strings.Join(missing, ", ")), "component", "tooling")

		return nil
	}
}

// setupWorkspace creates the workspace directory the user works in and owns it
// as the login user. This is the directory rclone syncs to the bucket, so it
// has to exist and be writable by that user.
func setupWorkspace(_ *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		created, err := ensureDir(layout.Workspace, layout.RunUser)
		if err != nil {
			return err
		}

		if created {
			slog.Info(fmt.Sprintf("created %s", layout.Workspace), "component", "workspace")
		}

		// Prove the login user can actually write there, not just that root can.
		if layout.RunUser != "" && layout.RunUser != "root" {
			probe := filepath.Join(layout.Workspace, ".chotu-write-probe")

			if _, err := asRunUser(ctx, layout, "bash", "-lc",
				fmt.Sprintf("echo ok > %q && rm -f %q", probe, probe)); err != nil {
				return fmt.Errorf("%s is not writable by %s: %w", layout.Workspace, layout.RunUser, err)
			}
		}

		return nil
	}
}

// setupOpenCode installs OpenCode when the user asked for it and writes the
// workspace-level configuration that points it at the local inference endpoint.
func setupOpenCode(cfg *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil || !cfg.Coding.OpenCode {
			slog.Info("disabled in config, nothing to do", "component", "opencode")

			return nil
		}

		openCodePath := filepath.Join(layout.Home, ".opencode", "bin", "opencode")

		if _, err := os.Stat(openCodePath); err != nil {
			if err := installOpenCode(ctx, layout); err != nil {
				return err
			}
		}

		if _, err := os.Stat(openCodePath); err != nil {
			return fmt.Errorf("opencode executable not found at %s after trying to install it", openCodePath)
		}

		if err := writeOpenCodeConfig(cfg, layout); err != nil {
			return err
		}

		version, err := asRunUser(ctx, layout, openCodePath, "--version")
		if err != nil {
			return fmt.Errorf("verify opencode execution at %s: %w", openCodePath, err)
		}

		slog.Info(fmt.Sprintf("%s configured in %s", strings.TrimSpace(version), layout.Workspace), "component", "opencode")

		return nil
	}
}

func installOpenCode(ctx context.Context, layout Layout) error {
	if !isRoot() {
		return fmt.Errorf("opencode is not installed and installing it needs root")
	}

	slog.Info("installing opencode", "component", "opencode")

	// OpenCode's own installer writes to the invoking user's home, so it has to
	// run as that user or the binary ends up somewhere the user cannot reach.
	if _, err := asRunUser(ctx, layout, "bash", "-lc",
		"curl -fsSL https://opencode.ai/install | bash"); err != nil {
		return fmt.Errorf("install opencode: %w", err)
	}

	return nil
}

func writeOpenCodeConfig(cfg *config.Config, layout Layout) error {
	port := inferencePort(cfg)
	model := inferenceModel(cfg)

	providers := map[string]any{
		"vllm": map[string]any{
			"name": "Local vLLM",
			"npm":  "@ai-sdk/openai-compatible",
			"options": map[string]any{
				"baseURL": fmt.Sprintf("http://127.0.0.1:%d/v1", port),
			},
			"models": map[string]any{
				model: map[string]any{
					"name": model,
					"limit": map[string]any{
						"context": maxModelLen(cfg),
						"output":  maxOutputTokens(cfg),
					},
				},
			},
		},
	}

	document := map[string]any{
		"$schema":  "https://opencode.ai/config.json",
		"model":    "vllm/" + model,
		"provider": providers,
	}

	encoded, err := jsonMarshalIndent(document)
	if err != nil {
		return fmt.Errorf("render the opencode config: %w", err)
	}

	path := filepath.Join(layout.Workspace, "opencode.json")

	if _, err := writeFileIfChanged(path, string(encoded), 0o644); err != nil {
		return err
	}

	if isRoot() && layout.RunUser != "root" {
		if err := chownRecursive(path, layout.RunUser); err != nil {
			return err
		}
	}

	return nil
}

func setupStatusPage(cfg *config.Config, _ Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil || !cfg.StatusPage.Enabled {
			slog.Info("status page disabled", "component", "status")
			return nil
		}

		port := cfg.StatusPage.Port
		if port == 0 {
			port = 8081
		}

		const service = "chotu-status.service"
		url := fmt.Sprintf("http://127.0.0.1:%d/", port)

		slog.Info(
			"checking status page health",
			"component", "status",
			"service", service,
			"url", url,
		)

		// The HTTP response is the source of truth for health.
		if statusPageResponds(ctx, port) {
			slog.Info("status page healthy", "component", "status", "url", url)
			return nil
		}

		slog.Warn(
			"status page is not responding; attempting service recovery",
			"component", "status",
			"service", service,
			"url", url,
		)

		// Recover an existing systemd-managed service.
		// A missing unit must be installed by the setup/bootstrap layer.
		if out, err := execOutputCtx(ctx, "systemctl", "restart", service); err != nil {
			output := strings.TrimSpace(out)

			slog.Error(
				"could not restart status service",
				"component", "status",
				"service", service,
				"error", err,
				"output", output,
			)

			return fmt.Errorf(
				"status page is unhealthy and service recovery failed; install/configure %s before retrying: %w",
				service,
				err,
			)
		}

		// Wait for the service to become healthy after restart.
		if !statusPageResponds(ctx, port) {
			slog.Error(
				"status page remains unhealthy after restart",
				"component", "status",
				"service", service,
				"url", url,
			)

			return fmt.Errorf(
				"status page at %s is not responding after restarting %s",
				url,
				service,
			)
		}

		slog.Info(
			"status page healthy after recovery",
			"component", "status",
			"service", service,
			"url", url,
		)

		return nil
	}
}

func statusPageResponds(ctx context.Context, port int) bool {
	const (
		timeout  = 30 * time.Second
		interval = 1 * time.Second
	)

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	check := func() bool {
		_, err := run(
			ctx,
			"curl",
			"-fsS",
			"--max-time", "1",
			url,
		)
		return err == nil
	}

	// Check immediately before waiting for the next tick.
	if check() {
		return true
	}

	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
			if check() {
				return true
			}
		}
	}
}

// setupSync installs the timer that pushes the workspace to the bucket, and the
// exclusion filter it syncs through.
func setupSync(cfg *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil || !cfg.S3.Enabled {
			slog.Info("s3 is disabled, no workspace sync", "component", "sync")

			return nil
		}

		if cfg.S3.Bucket == "" {
			return fmt.Errorf("s3 is enabled but s3.bucket_name is empty")
		}

		interval := strings.TrimSpace(cfg.S3.Sync.Interval)
		if interval == "" {
			interval = "1m"
		}

		filter, err := writeWorkspaceFilter(cfg, layout)
		if err != nil {
			return err
		}

		unit := fmt.Sprintf(`[Unit]
Description=Sync the Chotu workspace to S3
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=%s
Environment=HOME=%s
ExecStart=/usr/bin/rclone sync %s %s --filter-from %s --create-empty-src-dirs
`, layout.RunUser, layout.Home, layout.Workspace, s3Target(cfg), filter)

		timer := fmt.Sprintf(`[Unit]
Description=Sync the Chotu workspace to S3 every %s

[Timer]
OnBootSec=%s
OnUnitActiveSec=%s
Unit=chotu-workspace-sync.service

[Install]
WantedBy=timers.target
`, interval, interval, interval)

		wrote := false

		for path, content := range map[string]string{
			"/etc/systemd/system/chotu-workspace-sync.service": unit,
			"/etc/systemd/system/chotu-workspace-sync.timer":   timer,
		} {
			changed, err := writeFileIfChanged(path, content, 0o644)
			if err != nil {
				return err
			}

			wrote = wrote || changed
		}

		if wrote {
			daemonReload(ctx)
		}

		if err := enableUnit(ctx, "chotu-workspace-sync.timer"); err != nil {
			return err
		}

		slog.Info(fmt.Sprintf("workspace %s -> %s every %s", layout.Workspace, s3Target(cfg), interval), "component", "sync")

		return nil
	}
}

// defaultSyncExclusions are applied on top of whatever the user configured.
// Regenerating a dependency tree is expensive and the synced workspace is
// source code, not a build artefact.
var defaultSyncExclusions = []string{
	"- node_modules/**",
	"- .venv/**",
	"- __pycache__/**",
	"- .cache/**",
	"- .chotu-write-probe",
}

func writeWorkspaceFilter(cfg *config.Config, layout Layout) (string, error) {
	dir := filepath.Join(layout.Home, ".config", "chotu")

	if _, err := ensureDir(dir, layout.RunUser); err != nil {
		return "", err
	}

	seen := make(map[string]bool)
	lines := make([]string, 0, len(defaultSyncExclusions)+len(cfg.S3.Sync.Exclusions))

	patterns := append(
		append([]string{}, defaultSyncExclusions...),
		cfg.S3.Sync.Exclusions...,
	)

	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		// Normalize existing exclusion rules.
		pattern = strings.TrimPrefix(pattern, "- ")
		pattern = strings.TrimSpace(pattern)

		if pattern == "" || seen[pattern] {
			continue
		}
		seen[pattern] = true

		lines = append(lines, "- "+pattern)
	}

	path := filepath.Join(dir, "workspace-filter")

	if _, err := writeFileIfChanged(
		path,
		strings.Join(lines, "\n")+"\n",
		0o644,
	); err != nil {
		return "", err
	}

	return path, nil
}
