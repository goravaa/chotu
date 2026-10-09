package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// aptTimeout is the budget for a single apt-get invocation. Package installs on
// a cold machine pull a lot of index data, and the daemon runs a cycle every
// minute, so a hung apt must not be able to stall a task indefinitely.
const aptTimeout = 20 * time.Minute

// aptLock serialises package management inside this process. The tasks run as
// concurrent goroutines, and the Docker, NVIDIA, rclone and tooling tasks all
// need apt at the same time.
var aptLock sync.Mutex

// dpkgLockPath is the file apt and dpkg take to serialise against each other
// machine-wide. Locking it explicitly turns "dpkg was interrupted" from a
// recurring failure into a wait.
const dpkgLockPath = "/var/lib/dpkg/lock-frontend"

// withAptLock runs fn while holding both the in-process mutex and the dpkg lock
// file, so concurrent Chotu tasks and anything else on the machine take turns.
func withAptLock(fn func() error) error {
	aptLock.Lock()
	defer aptLock.Unlock()

	if !isRoot() {
		return fn()
	}

	lock, err := os.OpenFile(dpkgLockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		// A missing lock file is not worth failing over: apt will create it
		// itself and the in-process mutex already covers our own tasks.
		slog.Info(fmt.Sprintf("cannot lock %s (%v); relying on apt's own locking", dpkgLockPath, err), "component", "apt")

		return fn()
	}

	// The lock is released by the deferred unlock below; a close failure on a
	// lock file we are about to drop anyway has nothing useful to report.
	defer lock.Close() //nolint:errcheck // releasing a lock file we are dropping

	// Wait rather than fail: another package manager may simply be mid-install.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", dpkgLockPath, err)
	}

	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	return fn()
}

// packageInstalled reports whether dpkg already has the package at the given
// version or any version.
func packageInstalled(pkg string) bool {
	_, err := run(context.Background(), "dpkg-query",
		"-W", "-f=${db:Status-Status}", pkg)
	return err == nil
}

// aptPackagesMissing returns the subset of pkgs that is not installed, with
// their dpkg versions so a pinned toolchain such as python3.14 or go1.22.0
// still resolves.
func aptPackagesMissing(pkgs []string) []string {
	var missing []string

	for _, pkg := range pkgs {
		if !packageInstalled(pkg) {
			missing = append(missing, pkg)
		}
	}

	return missing
}

// aptUpdateLocked refreshes the package index. The caller must already hold
// the apt lock; aptInstall uses this so it does not deadlock on the mutex.
func aptUpdateLocked(ctx context.Context) error {
	runCtx, cancel := context.WithTimeout(ctx, aptTimeout)
	defer cancel()

	out, err := execOutputCtx(runCtx, "apt-get", "update")
	if err != nil {
		return fmt.Errorf("apt-get update: %w: %s", err, out)
	}

	return nil
}

// aptInstall installs packages that are not already present. It is a no-op
// when every package is satisfied, which keeps a steady-state cycle cheap.
func aptInstall(ctx context.Context, pkgs ...string) error {
	missing := aptPackagesMissing(pkgs)
	if len(missing) == 0 {
		return nil
	}

	slog.Info(fmt.Sprintf("installing %s", strings.Join(missing, ", ")), "component", "apt")

	return withAptLock(func() error {
		runCtx, cancel := context.WithTimeout(ctx, aptTimeout)
		defer cancel()

		// The lock may have been released and retaken while we were waiting, so
		// re-check: another task may have installed these already.
		missing = aptPackagesMissing(missing)
		if len(missing) == 0 {
			return nil
		}

		if err := aptUpdateLocked(runCtx); err != nil {
			return err
		}

		args := append([]string{
			"install", "-y", "--no-install-recommends",
			"-o", "Dpkg::Options::=--force-confdef",
			"-o", "Dpkg::Options::=--force-confold",
		}, missing...)

		out, err := execOutputCtx(runCtx, "apt-get", args...)
		if err != nil {
			return fmt.Errorf(
				"apt-get install %s: %w: %s",
				strings.Join(missing, ", "),
				err,
				strings.TrimSpace(out),
			)
		}

		return nil
	})
}

// aptRepo reconciles an APT source and its signing key, repairing existing
// managed files when their contents need updating. Returns true when the
// repository source file was changed.
//
// keyring uses .asc or .gpg according to the format expected by APT.
// ASCII-armored keys must be dearmored before writing to a .gpg keyring;
// binary keyrings can be written directly. Repository entries are updated
// with signed-by so APT verifies the source using the specified keyring.

func aptRepo(ctx context.Context, name, keyURL, keyring, listURL string) (bool, error) {
	listPath := filepath.Join("/etc/apt/sources.list.d", name+".list")

	if !isRoot() {
		return false, fmt.Errorf("adding the %s apt repository needs root", name)
	}

	slog.Info(fmt.Sprintf("configuring the %s repository", name), "component", "apt")

	added := false

	err := withAptLock(func() error {
		runCtx, cancel := context.WithTimeout(ctx, aptTimeout)
		defer cancel()

		// Fetch the ASCII-armored public signing key.
		key, err := execOutputCtx(runCtx, "curl", "-fsSL", keyURL)
		if err != nil {
			return fmt.Errorf("fetch %s signing key: %w", name, err)
		}
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("fetch %s signing key: empty response", name)
		}

		// Write the downloaded key to a temporary file.
		keyInput, err := os.CreateTemp("", name+"-key-*.asc")
		if err != nil {
			return fmt.Errorf("create temporary signing key: %w", err)
		}
		keyInputPath := keyInput.Name()
		defer os.Remove(keyInputPath)

		if _, err := keyInput.WriteString(key); err != nil {
			keyInput.Close()
			return fmt.Errorf("write temporary signing key: %w", err)
		}
		if err := keyInput.Close(); err != nil {
			return fmt.Errorf("close temporary signing key: %w", err)
		}

		// Convert the ASCII-armored key into a binary OpenPGP keyring.
		keyOutput, err := os.CreateTemp("", name+"-key-*.gpg")
		if err != nil {
			return fmt.Errorf("create temporary keyring: %w", err)
		}
		keyOutputPath := keyOutput.Name()
		if err := keyOutput.Close(); err != nil {
			os.Remove(keyOutputPath)
			return fmt.Errorf("close temporary keyring: %w", err)
		}
		defer os.Remove(keyOutputPath)

		if _, err := execOutputCtx(
			runCtx,
			"gpg", "--batch", "--yes", "--dearmor",
			"--output", keyOutputPath,
			keyInputPath,
		); err != nil {
			return fmt.Errorf("dearmor %s signing key: %w", name, err)
		}

		binaryKey, err := os.ReadFile(keyOutputPath)
		if err != nil {
			return fmt.Errorf("read dearmored %s keyring: %w", name, err)
		}
		if len(binaryKey) == 0 {
			return fmt.Errorf("dearmored %s keyring is empty", name)
		}

		// Install the binary keyring only after conversion succeeds.
		if _, err := writeFileIfChanged(keyring, string(binaryKey), 0o644); err != nil {
			return fmt.Errorf("install %s keyring: %w", name, err)
		}

		// Fetch the upstream repository definition.
		list, err := execOutputCtx(runCtx, "curl", "-fsSL", listURL)
		if err != nil {
			return fmt.Errorf("fetch %s source list: %w", name, err)
		}
		if strings.TrimSpace(list) == "" {
			return fmt.Errorf("fetch %s source list: empty response", name)
		}

		// Add the signing-key restriction to the repository entries.
		source := withSignedBy(list, keyring)

		// Validate basic content before writing the managed source file.
		if strings.TrimSpace(source) == "" {
			return fmt.Errorf("%s source list is empty after processing", name)
		}
		hasEntry := false
		for _, line := range strings.Split(source, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "deb ") || strings.HasPrefix(line, "deb-src ") {
				hasEntry = true
				break
			}
		}
		if !hasEntry {
			return fmt.Errorf("%s source list contains no supported deb entries", name)
		}

		changed, err := writeFileIfChanged(listPath, source, 0o644)
		if err != nil {
			return fmt.Errorf("write %s source list: %w", name, err)
		}

		added = changed
		return nil
	})

	return added, err
}

// withSignedBy points an apt source at the keyring we fetched, so the source is
// verified no matter how upstream published the options.
func withSignedBy(list, keyring string) string {
	var out []string

	for _, line := range strings.Split(strings.TrimSpace(list), "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
			out = append(out, trimmed)

		case strings.HasPrefix(trimmed, "deb ["):
			if strings.Contains(trimmed, "signed-by=") {
				out = append(out, trimmed)
			} else {
				out = append(out, insertOption(trimmed, "signed-by="+keyring))
			}

		case strings.HasPrefix(trimmed, "deb "):
			rest := strings.TrimPrefix(trimmed, "deb ")
			out = append(out, "deb [signed-by="+keyring+"] "+rest)

		default:
			out = append(out, trimmed)
		}
	}

	return strings.Join(out, "\n") + "\n"
}

// insertOption adds an option to the bracket group of an apt source line.
func insertOption(line, option string) string {
	start := strings.Index(line, "[")
	end := strings.Index(line, "]")

	if start < 0 || end < start {
		return line + " [" + option + "]"
	}

	inner := line[start+1 : end]

	return line[:start+1] + strings.TrimSpace(inner) + " " + option + line[end:]
}

// writeFileIfChanged writes content only when the file is absent or different,
// and reports whether it wrote. Every task runs on every cycle, so rewriting
// identical unit files would trigger a daemon-reload loop forever.
func writeFileIfChanged(path, content string, mode os.FileMode) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) == content {
			info, statErr := os.Stat(path)
			if statErr == nil && info.Mode().Perm() == mode.Perm() {
				return false, nil
			}
		}
	}

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return false, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}

	return true, nil
}

// unitRunning reports whether a systemd unit is active.
func unitRunning(ctx context.Context, unit string) bool {
	_, err := run(ctx, "systemctl", "is-active", "--quiet", unit)
	return err == nil
}

// enableUnit installs the changes and starts a unit if it is not already
// running. It is idempotent, so it is safe to call on every cycle.
func enableUnit(ctx context.Context, unit string) error {
	if unitRunning(ctx, unit) {
		return nil
	}

	if _, err := run(ctx, "systemctl", "enable", "--now", unit); err != nil {
		return fmt.Errorf("enable %s: %w", unit, err)
	}

	slog.Info(fmt.Sprintf("started %s", unit), "component", "systemd")

	return nil
}

// jsonMarshalIndent renders a config document with a trailing newline, so the
// files Chotu writes stay readable and diff cleanly.
func jsonMarshalIndent(value any) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(encoded, '\n'), nil
}

// daemonReload tells systemd about unit files that were written this cycle.
func daemonReload(ctx context.Context) {
	if _, err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		slog.Info(fmt.Sprintf("daemon-reload failed: %v", err), "component", "systemd")
	}
}
