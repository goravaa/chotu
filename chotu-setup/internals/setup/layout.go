package setup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/goravaa/chotu/chotu-setup/internals/config"
)

// The instance store is always mounted at the data directory.
//
// In persistent mode it is scratch space.
// In ephemeral mode it is the machine's primary storage.
const defaultDataDir = "/data"

const (
	rootDiskDockerRoot = "/var/lib/docker"
	rootDiskModelCache = "/var/lib/chotu/huggingface"
	rootDiskStateDir   = "/var/lib/chotu"
)

// Layout is the set of paths the machine writes to, resolved once from the
// config so no task has to re-derive the storage topology.
type Layout struct {
	// Mode is "persistent" or "ephemeral".
	Mode string

	// DataDir is the EC2 instance store mount point. It is scratch space when
	// Mode is persistent and the primary storage when it is ephemeral.
	DataDir string

	// RunUser is the unprivileged user the machine is prepared for. Chotu runs
	// as root because it installs system packages, but the workspace belongs
	// to this user.
	RunUser string

	// Home is RunUser's home directory.
	Home string

	// DockerRoot, ModelCache, StateDir and Workspace are the paths that follow
	// the storage mode.
	DockerRoot string
	ModelCache string
	StateDir   string
	LogDir     string
	Workspace  string
}

// IsPersistent reports whether the machine keeps its state on a disk that
// outlives the instance.
func (l Layout) IsPersistent() bool { return l.Mode == "persistent" }

// RequiresDataDir reports whether the instance store must be attached.
//
// An ephemeral machine uses the instance store as its primary storage.
// A persistent machine uses the root disk as primary storage, so the
// instance store is optional scratch space.
func (l Layout) RequiresDataDir() bool { return !l.IsPersistent() }

// ResolveLayout turns the config into the concrete paths the tasks use.
func ResolveLayout(cfg *config.Config) Layout {
	persist := cfg != nil && cfg.Machine.Storage.Persist
	runUser, home := resolveRunUser()

	layout := Layout{
		Mode:    "ephemeral",
		DataDir: envOr("CHOTU_DATA_DIR", defaultDataDir),
		RunUser: runUser,
		Home:    home,
	}

	if persist {
		layout.Mode = "persistent"

		// Root disk is primary storage. /data is scratch.
		layout.DockerRoot = rootDiskDockerRoot
		layout.ModelCache = rootDiskModelCache
		layout.StateDir = rootDiskStateDir
		layout.Workspace = filepath.Join(home, "workspace")
	} else {
		// Instance store is primary storage.
		layout.DockerRoot = filepath.Join(layout.DataDir, "docker")
		layout.ModelCache = filepath.Join(layout.DataDir, "huggingface")
		layout.StateDir = filepath.Join(layout.DataDir, "chotu")
		layout.Workspace = filepath.Join(layout.DataDir, "workspace")
	}

	// The overrides exist so the tasks can be exercised without root, and so an
	// operator can move a single directory without editing the config.
	if dir := os.Getenv("CHOTU_DOCKER_ROOT"); dir != "" {
		layout.DockerRoot = dir
	}

	if dir := os.Getenv("CHOTU_MODEL_CACHE"); dir != "" {
		layout.ModelCache = dir
	}

	if dir := os.Getenv("CHOTU_STATE_DIR"); dir != "" {
		layout.StateDir = dir
	}

	if dir := os.Getenv("CHOTU_WORKSPACE_DIR"); dir != "" {
		layout.Workspace = dir
	}

	layout.LogDir = envOr("CHOTU_LOG_DIR", filepath.Join(layout.StateDir, "log"))

	return layout
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}

// resolveRunUser finds the unprivileged user the machine is being prepared for.
// A Chotu machine is an SSH workstation first, so the workspace must be owned
// by someone who can log in rather than by root.
func resolveRunUser() (string, string) {
	if user := os.Getenv("CHOTU_RUN_USER"); user != "" {
		return user, homeOf(user)
	}

	if user := os.Getenv("SUDO_USER"); user != "" && user != "root" {
		return user, homeOf(user)
	}

	// A login shell on a stock Ubuntu image lands on the first non-system user
	// in /home, which is the account the AMI was created with.
	if entries, err := os.ReadDir("/home"); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				return entry.Name(), filepath.Join("/home", entry.Name())
			}
		}
	}

	return "ubuntu", "/home/ubuntu"
}

func homeOf(user string) string {
	if dir := filepath.Join("/home", user); isDir(dir) {
		return dir
	}

	// Fall back to whatever passwd says before assuming the conventional path.
	if out, err := run(context.Background(), "getent", "passwd", user); err == nil {
		if fields := strings.Split(strings.TrimSpace(out), ":"); len(fields) > 5 && fields[5] != "" {
			return fields[5]
		}
	}

	return filepath.Join("/home", user)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ensureDir creates a directory and reports whether it had to do any work, so
// tasks can log only on a real change instead of every cycle.
func ensureDir(path string, owner string) (bool, error) {
	created := !isDir(path)

	if err := os.MkdirAll(path, 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", path, err)
	}

	if owner != "" && isRoot() {
		if err := chownRecursive(path, owner); err != nil {
			return created, err
		}
	}

	return created, nil
}

func chownRecursive(path, user string) error {
	out, err := execOutput("chown", "-R", user+":"+user, path)
	if err != nil {
		return fmt.Errorf("chown %s to %s: %w: %s", path, user, err, out)
	}

	return nil
}

// isMountpoint reports whether path is a distinct mount rather than a directory
// that happens to exist on the root filesystem.
//
// os.Stat alone cannot tell the two apart, and the difference is the whole
// point of the instance store: on an ephemeral machine an unmounted /data
// silently means the workspace lands on the root disk.
func isMountpoint(path string) (bool, error) {
	var target, parent syscall.Stat_t

	if err := syscall.Stat(path, &target); err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}

	parentPath := filepath.Dir(path)
	if err := syscall.Stat(parentPath, &parent); err != nil {
		return false, fmt.Errorf("stat %s: %w", parentPath, err)
	}

	return target.Dev != parent.Dev, nil
}

// mountInfo returns the filesystem type backing path, for the log line that
// makes the topology visible on the status page.
func mountInfo(path string) string {
	contents, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return ""
	}

	best, bestType := "", ""

	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		mount, fstype := fields[1], fields[2]
		if mount != path && !strings.HasPrefix(mount, strings.TrimSuffix(path, "/")+"/") {
			continue
		}

		// A nested mount is more specific than its parent, so the longest match
		// is the one that actually describes path.
		if len(mount) > len(best) {
			best, bestType = mount, fstype
		}
	}

	return bestType
}

func logLayout(layout Layout) {
	mounted, _ := isMountpoint(layout.DataDir)

	slog.Info(fmt.Sprintf("mode=%s", layout.Mode), "component", "storage")
	slog.Info(fmt.Sprintf("workspace=%s", layout.Workspace), "component", "storage")
	slog.Info(fmt.Sprintf("docker-root=%s", layout.DockerRoot), "component", "storage")
	slog.Info(fmt.Sprintf("model-cache=%s", layout.ModelCache), "component", "storage")
	slog.Info(fmt.Sprintf("state=%s", layout.StateDir), "component", "storage")
	slog.Info(
		fmt.Sprintf(
			"data=%s (fstype=%s mount=%t)",
			layout.DataDir,
			mountInfo(layout.DataDir),
			mounted,
		),
		"component",
		"storage",
	)
}
