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
	TaskVLLM      = "vllm"
	TaskSync      = "sync"
)

const (
	commandTimeout = 60 * time.Second

	// defaultNvidiaDriverPackage is pinned by Chotu rather than exposed in the
	// config, because the driver has to match the kernel the image was built with.
	defaultNvidiaDriverPackage = "nvidia-driver-580-server"

	dockerContainerName = "chotu-vllm"
)

// nvidiaDriverPackage returns the driver package to install and the matching
// userspace package. The userspace libraries are the part that has to line up
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
			Name: TaskMachine,
			Run:  setupMachine(cfg),
		},
		{
			Name: TaskStorage,
			Run:  setupStorage(cfg, layout),
		},
		{
			Name: TaskDocker,
			Run:  setupDocker(cfg, layout),
		},
		{
			Name: TaskGPU,
			Run:  setupGPU(cfg, layout),
		},
		{
			Name:         TaskDockerGPU,
			Dependencies: []string{TaskDocker, TaskGPU},
			Run:          setupDockerGPU(cfg, layout),
		},
		{
			Name:         TaskRclone,
			Dependencies: []string{TaskStorage},
			Run:          setupRclone(cfg, layout),
		},
		{
			Name:         TaskS3,
			Dependencies: []string{TaskRclone},
			Run:          setupS3(cfg, layout),
		},
		{
			Name: TaskTooling,
			Run:  setupTooling(cfg, layout),
		},
		{
			Name:         TaskWorkspace,
			Dependencies: []string{TaskStorage, TaskTooling},
			Run:          setupWorkspace(cfg, layout),
		},
		{
			Name:         TaskOpenCode,
			Dependencies: []string{TaskWorkspace},
			Run:          setupOpenCode(cfg, layout),
		},
		{
			Name:         TaskVLLM,
			Dependencies: []string{TaskDockerGPU, TaskStorage},
			Run:          setupVLLM(cfg, layout),
		},

		{
			Name:         TaskSync,
			Dependencies: []string{TaskWorkspace, TaskS3},
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

		log.Printf("[machine] %s %s %s", id, version, arch())

		if major, _, _ := strings.Cut(version, "."); major != expectedUbuntuMajor {
			log.Printf(
				"[machine] WARNING: expected Ubuntu %s, found %s. machine.image_id in the "+
					"config has to point at an Ubuntu %s AMI",
				expectedUbuntuMajor,
				version,
				expectedUbuntuMajor,
			)
		}

		if arch() != "x86_64" {
			return fmt.Errorf("unsupported architecture %s: Chotu targets x86_64", arch())
		}

		if _, err := run(ctx, "nvidia-smi", "-L"); err != nil {
			return fmt.Errorf(
				"no NVIDIA GPU is visible: the instance type in config is %q, "+
					"which has no GPU. Provisioning mistake, not something setup can fix: %w",
				instanceType(cfg),
				err,
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

// setupStorage prepares the storage topology the machine runs on: the instance
// store, the state directory, the model cache and the workspace.
//
// The topology follows machine.storage.persist. On a disposable spot machine the
// instance store is the only storage worth having, so the workspace and the
// Docker root go there and the task fails loudly when it is missing. On a
// persistent machine the root disk is the primary storage and the instance
// store is scratch.
func setupStorage(cfg *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		logLayout(layout)

		mounted, mountErr := isMountpoint(layout.DataDir)

		if mountErr != nil {
			if layout.RequiresDataDir() {
				return fmt.Errorf(
					"the instance store is not mounted at %s and machine.storage.persist is "+
						"false, so the workspace would be lost when the instance is reclaimed: %w",
					layout.DataDir,
					mountErr,
				)
			}
		} else if !mounted && layout.RequiresDataDir() {
			return fmt.Errorf(
				"%s is not a mount point: the EC2 instance store must be attached and "+
					"formatted, or set machine.storage.persist to true to keep state on the root disk",
				layout.DataDir,
			)
		} else if !mounted {
			log.Printf(
				"[storage] %s is not a mount point; continuing because the root disk is the "+
					"primary storage in persistent mode",
				layout.DataDir,
			)
		}

		for _, dir := range []string{layout.StateDir, layout.LogDir, layout.ModelCache} {
			if _, err := ensureDir(dir, layout.RunUser); err != nil {
				return err
			}
		}

		usage, err := run(ctx, "df", "-h", layout.StateDir)
		if err != nil {
			return fmt.Errorf("check storage for %s: %w", layout.StateDir, err)
		}

		log.Printf("[storage] state filesystem:\n%s", usage)

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

		if err := writeDockerDaemonConfig(layout); err != nil {
			return err
		}

		if !unitRunning(ctx, "docker") {
			log.Printf("[docker] starting the daemon")

			if _, err := run(ctx, "systemctl", "enable", "--now", "docker"); err != nil {
				return fmt.Errorf("start docker: %w", err)
			}
		}

		version, err := run(ctx, "docker", "version", "--format", "{{.Server.Version}}")
		if err != nil {
			// A daemon that was just reconfigured needs a moment before it
			// answers, so retry briefly rather than failing the cycle.
			if restarted, restartErr := run(ctx, "systemctl", "restart", "docker"); restartErr == nil {
				log.Printf("[docker] restarted the daemon: %s", restarted)

				version, err = run(ctx, "docker", "version", "--format", "{{.Server.Version}}")
			}
		}

		if err != nil {
			return fmt.Errorf("docker daemon unreachable: %w", err)
		}

		if err := addUserToDockerGroup(layout); err != nil {
			return err
		}

		log.Printf("[docker] healthy, server version %s, root %s", version, layout.DockerRoot)

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
// cycle does not bounce the daemon.
func writeDockerDaemonConfig(layout Layout) error {
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
		return fmt.Errorf("render the docker daemon config: %w", err)
	}

	wrote, err := writeFileIfChanged("/etc/docker/daemon.json", string(encoded), 0o644)
	if err != nil {
		return err
	}

	if wrote {
		log.Printf("[docker] wrote /etc/docker/daemon.json (data-root %s)", layout.DockerRoot)
	}

	return nil
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

	log.Printf(
		"[groups] added %s to %s; they apply on the next login",
		layout.RunUser,
		strings.Join(missing, ", "),
	)

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
			return fmt.Errorf(
				"nvidia-smi cannot reach the GPU. A newly installed driver needs a reboot: %w",
				err,
			)
		}

		// The GPU is working, so any earlier reboot request is satisfied.
		clearRebootMarker(layout)

		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				log.Printf("[gpu] %s", line)
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
		log.Printf("[gpu] installing %s", driver)

		// Headers let the driver build its kernel module against the running
		// kernel. Their absence is not fatal, so a failure here is not.
		if _, err := run(ctx, "apt-get", "install", "-y", "--no-install-recommends",
			"linux-headers-"+kernelRelease()); err != nil {
			log.Printf("[gpu] could not install kernel headers: %v", err)
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

		log.Printf(
			"[gpu] %s installed. The GPU will not be usable until the machine reboots; "+
				"reboot it and the next cycle will finish the setup",
			driver,
		)

		return fmt.Errorf("reboot required: %s was just installed and the driver is not loaded yet", driver)
	}

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
		log.Printf("[gpu] could not clear the reboot marker: %v", err)
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
		log.Printf("[gpu] nvidia-cdi-refresh.service: %s", out)
	}

	// A refresh service that was just installed may still be settling, so give
	// the spec a moment to appear before calling it missing.
	for attempt := range 3 {
		out, err := execOutput("nvidia-ctk", "cdi", "list")
		if err == nil && strings.Contains(out, "nvidia.com/gpu") {
			return nil
		}

		log.Printf("[gpu] CDI does not expose a GPU yet (attempt %d)", attempt+1)

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf(
		"NVIDIA CDI did not expose any GPU: the driver userspace and the kernel " +
			"module are out of step, which usually needs a reboot",
	)
}

// setupDockerGPU proves the whole host-to-container path works, not just that
// the pieces are installed. The runtime being registered in the daemon config
// says nothing about whether a container can actually open the device.
func setupDockerGPU(_ *config.Config, _ Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if !commandExists("nvidia-ctk") {
			return fmt.Errorf("nvidia-ctk is not installed: install the NVIDIA container toolkit")
		}

		out, err := run(ctx, "docker", "info", "--format", "{{json .Runtimes}}")
		if err != nil {
			return fmt.Errorf("read docker runtimes: %w", err)
		}

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
		}

		// The smoke test pulls an image, so give it a budget of its own rather
		// than the shared command timeout.
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		if out, err := execOutputCtx(runCtx, "docker", "run", "--rm",
			"--runtime=nvidia", "--gpus", "all", "ubuntu", "nvidia-smi"); err != nil {
			return fmt.Errorf(
				"docker can run containers but not on the GPU: %w: %s",
				err,
				strings.TrimSpace(out),
			)
		}

		log.Printf("[docker-gpu] a container can reach the GPU")

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

		if _, err := writeFileIfChanged(filepath.Join(rcloneDir, "rclone.conf"), remote, 0o600); err != nil {
			return err
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
			log.Printf("[s3] disabled in config, nothing to verify")

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

		log.Printf("[s3] remote healthy: %s", target)

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
			log.Printf("[tooling] no packages requested")

			return nil
		}

		missing := aptPackagesMissing(cfg.Tooling.Packages)
		if len(missing) == 0 {
			log.Printf("[tooling] all %d requested packages are present", len(cfg.Tooling.Packages))

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

		log.Printf("[tooling] installed %s", strings.Join(missing, ", "))

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
			log.Printf("[workspace] created %s", layout.Workspace)
		}

		// Prove the login user can actually write there, not just that the
		// directory exists with the right owner.
		if isRoot() && layout.RunUser != "root" {
			probe := filepath.Join(layout.Workspace, ".chotu-write-probe")

			if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
				return fmt.Errorf("%s is not writable: %w", layout.Workspace, err)
			}

			if err := os.Remove(probe); err != nil {
				return fmt.Errorf("clean up the write probe in %s: %w", layout.Workspace, err)
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
			log.Printf("[opencode] disabled in config, nothing to do")

			return nil
		}

		if !commandExists("opencode") {
			if err := installOpenCode(ctx, layout); err != nil {
				return err
			}
		}

		if !commandExists("opencode") {
			return fmt.Errorf("opencode is not installed after trying to install it")
		}

		if err := writeOpenCodeConfig(cfg, layout); err != nil {
			return err
		}

		version, err := run(ctx, "opencode", "--version")
		if err != nil {
			return fmt.Errorf("opencode --version: %w", err)
		}

		log.Printf("[opencode] %s configured in %s", strings.TrimSpace(version), layout.Workspace)

		return nil
	}
}

func installOpenCode(ctx context.Context, layout Layout) error {
	if !isRoot() {
		return fmt.Errorf("opencode is not installed and installing it needs root")
	}

	log.Printf("[opencode] installing opencode")

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

func inferencePort(cfg *config.Config) int {
	if cfg == nil || cfg.Inference.Port == 0 {
		return 8000
	}

	return cfg.Inference.Port
}

func inferenceModel(cfg *config.Config) string {
	if cfg == nil || cfg.Inference.Model == "" {
		return "local-model"
	}

	return cfg.Inference.Model
}

func maxModelLen(cfg *config.Config) int {
	if cfg == nil || cfg.Inference.Resources.MaxModelLen == 0 {
		return 14336
	}

	return cfg.Inference.Resources.MaxModelLen
}

func maxOutputTokens(cfg *config.Config) int {
	if cfg == nil || cfg.Inference.Resources.MaxOutputTokens == 0 {
		return 4096
	}

	return cfg.Inference.Resources.MaxOutputTokens
}

// setupVLLM runs the inference container and waits for it to actually serve the
// configured model. A container that Docker reports as running is not ready: it
// still has to load the model, and a failed download must not be reported as
// success.
func setupVLLM(cfg *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil {
			log.Printf("[vllm] no config, nothing to do")

			return nil
		}

		image := inferenceImage(cfg)
		port := inferencePort(cfg)
		model := inferenceModel(cfg)

		if err := reconcileVLLMContainer(ctx, cfg, layout, image, port, model); err != nil {
			return err
		}

		endpoint := fmt.Sprintf("http://127.0.0.1:%d/v1/models", port)

		out, err := waitForInference(ctx, endpoint, model, 30*time.Minute)
		if err != nil {
			if logs := containerLogs(ctx, dockerContainerName, 200); logs != "" {
				log.Printf("[vllm] last %d log lines:\n%s", 200, logs)
			}

			return err
		}

		log.Printf("[vllm] serving %s on port %d", model, port)
		log.Printf("[vllm] /v1/models: %s", out)

		return nil
	}
}

// defaultInferenceImage is the fallback when config.yaml does not name an
// image. It is pinned to a version and a digest for the same reason the config
// is: `latest` moves several times a week, and vLLM's command line changes with
// it, so a machine can start failing without anything in Chotu changing.
const defaultInferenceImage = "vllm/vllm-openai:v0.30.0-x86_64" +
	"@sha256:5f5e535216848d0c52159c8c13a0af04be5f6fe1a84e79914300610796f76d40"

func inferenceImage(cfg *config.Config) string {
	if cfg == nil || cfg.Inference.Image == "" {
		return defaultInferenceImage
	}

	return cfg.Inference.Image
}

func reconcileVLLMContainer(
	ctx context.Context,
	cfg *config.Config,
	layout Layout,
	image string,
	port int,
	model string,
) error {
	// The container is recreated when the configuration it was created from
	// changes, and left alone when it matches, so a steady cycle does not
	// restart a model that is already serving.
	if existing, err := run(ctx, "docker", "inspect", "--format",
		"{{.Config.Image}} {{.State.Running}}", dockerContainerName); err == nil {
		fields := strings.Fields(existing)

		if len(fields) == 2 && fields[0] == image && fields[1] == "true" {
			if ready, _ := inferenceReady(ctx, port, model); ready {
				return nil
			}
		}

		log.Printf("[vllm] replacing the existing %s container", dockerContainerName)

		if _, err := run(ctx, "docker", "rm", "-f", dockerContainerName); err != nil {
			return fmt.Errorf("remove the old vllm container: %w", err)
		}
	}

	// Pulling a multi-gigabyte image needs more than the command timeout, and
	// it gets a budget of its own so it cannot eat into starting the container.
	pullCtx, cancelPull := context.WithTimeout(ctx, 30*time.Minute)
	defer cancelPull()

	if out, err := execOutputCtx(pullCtx, "docker", "pull", image); err != nil {
		return fmt.Errorf("pull %s: %w: %s", image, err, strings.TrimSpace(out))
	}

	cancelPull()

	runCtx, cancelRun := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelRun()

	args := []string{
		"run", "-d",
		"--name", dockerContainerName,
		"--restart", "unless-stopped",
		"--gpus", "all",
		"--ipc=host",
		"-p", fmt.Sprintf("%d:8000", port),
		"-v", layout.ModelCache + ":/root/.cache/huggingface",
		image,
		"--model", model,
		"--enable-auto-tool-choice",
		"--tool-call-parser", toolCallParser(model),
		"--max-model-len", fmt.Sprint(maxModelLen(cfg)),
		"--gpu-memory-utilization", fmt.Sprintf("%g", gpuMemoryUtilization(cfg)),
	}

	if out, err := execOutputCtx(runCtx, "docker", args...); err != nil {
		return fmt.Errorf("start the vllm container: %w: %s", err, strings.TrimSpace(out))
	}

	return nil
}

func gpuMemoryUtilization(cfg *config.Config) float64 {
	if cfg == nil || cfg.Inference.Resources.GPUMemoryUtilization == 0 {
		return 0.97
	}

	return cfg.Inference.Resources.GPUMemoryUtilization
}

// toolCallParser picks the vLLM tool-call parser for the configured model.
// vLLM matches on the model name, so a model with no known parser is served
// without one rather than with a wrong one.
func toolCallParser(model string) string {
	lower := strings.ToLower(model)

	switch {
	case strings.Contains(lower, "qwen3-coder"):
		return "qwen3_coder"
	case strings.Contains(lower, "qwen3"):
		return "hermes"
	case strings.Contains(lower, "llama-3.1"), strings.Contains(lower, "llama-3.2"):
		return "llama3_json"
	case strings.Contains(lower, "mistral"), strings.Contains(lower, "mixtral"):
		return "mistral"
	default:
		return ""
	}
}

func inferenceReady(ctx context.Context, port int, model string) (bool, error) {
	out, err := run(ctx, "curl", "-fsS", "--max-time", "10",
		fmt.Sprintf("http://127.0.0.1:%d/v1/models", port))
	if err != nil {
		return false, err
	}

	return model == "" || strings.Contains(out, model), nil
}

func waitForInference(ctx context.Context, endpoint, model string, budget time.Duration) (string, error) {
	deadline := time.Now().Add(budget)

	var last string

	for {
		out, err := execOutput("curl", "-fsS", "--max-time", "10", endpoint)
		if err == nil {
			if model == "" || strings.Contains(out, model) {
				return out, nil
			}

			last = "the endpoint is up but is not serving " + model
		} else {
			last = err.Error()
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("the inference endpoint %s did not become ready in %s: %s", endpoint, budget, last)
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func containerLogs(ctx context.Context, container string, lines int) string {
	out, err := run(ctx, "docker", "logs", "--tail", fmt.Sprint(lines), container)
	if err != nil && out == "" {
		return ""
	}

	return out
}

func statusPageResponds(ctx context.Context, port int) bool {
	_, err := run(ctx, "curl", "-fsS", "--max-time", "5",
		fmt.Sprintf("http://127.0.0.1:%d/", port))

	return err == nil
}

// setupSync installs the timer that pushes the workspace to the bucket, and the
// exclusion filter it syncs through.
func setupSync(cfg *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil || !cfg.S3.Enabled {
			log.Printf("[sync] s3 is disabled, no workspace sync")

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

		log.Printf("[sync] workspace %s -> %s every %s", layout.Workspace, s3Target(cfg), interval)

		return nil
	}
}

// defaultSyncExclusions are applied on top of whatever the user configured.
// Regenerating a dependency tree is expensive and the synced workspace is
// source code, not a build artefact.
var defaultSyncExclusions = []string{
	".git/**",
	"node_modules/**",
	".venv/**",
	"__pycache__/**",
	".cache/**",
	".chotu-write-probe",
}

func writeWorkspaceFilter(cfg *config.Config, layout Layout) (string, error) {
	dir := filepath.Join(layout.Home, ".config", "chotu")

	if _, err := ensureDir(dir, layout.RunUser); err != nil {
		return "", err
	}

	// Union the defaults with the user's, keeping the defaults even if the user
	// replaces the list: a user who syncs node_modules has usually made a
	// mistake rather than intended a 400 MB upload every minute.
	seen := make(map[string]bool, len(defaultSyncExclusions))
	lines := make([]string, 0, len(defaultSyncExclusions)+len(cfg.S3.Sync.Exclusions))

	for _, pattern := range append(append([]string{}, defaultSyncExclusions...), cfg.S3.Sync.Exclusions...) {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || seen[pattern] {
			continue
		}

		seen[pattern] = true

		lines = append(lines, pattern)
	}

	path := filepath.Join(dir, "workspace-filter")

	if _, err := writeFileIfChanged(path, strings.Join(lines, "\n")+"\n", 0o644); err != nil {
		return "", err
	}

	return path, nil
}
