package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
		log.Printf("[apt] cannot lock %s (%v); relying on apt's own locking", dpkgLockPath, err)

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

// aptUpdate refreshes the package index.
func aptUpdate(ctx context.Context) error {
	return withAptLock(func() error {
		runCtx, cancel := context.WithTimeout(ctx, aptTimeout)
		defer cancel()

		out, err := execOutputCtx(runCtx, "apt-get", "update")
		if err != nil {
			return fmt.Errorf("apt-get update: %w: %s", err, out)
		}

		return nil
	})
}

// aptInstall installs packages that are not already present. It is a no-op
// when every package is satisfied, which keeps a steady-state cycle cheap.
func aptInstall(ctx context.Context, pkgs ...string) error {
	missing := aptPackagesMissing(pkgs)
	if len(missing) == 0 {
		return nil
	}

	log.Printf("[apt] installing %s", strings.Join(missing, ", "))

	return withAptLock(func() error {
		runCtx, cancel := context.WithTimeout(ctx, aptTimeout)
		defer cancel()

		// The lock may have been released and retaken while we were waiting, so
		// re-check: another task may have installed these already.
		missing = aptPackagesMissing(missing)
		if len(missing) == 0 {
			return nil
		}

		if err := aptUpdate(runCtx); err != nil {
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

// aptRepo adds an apt source with its signing key, unless the source is already
// present. Returns true when the source was added.
//
// keyring must carry the extension matching how the key is published: .asc for
// an ASCII-armoured key (Docker) and .gpg for a binary one (NVIDIA). apt
// infers the format from the extension, so writing the fetched bytes verbatim is
// correct for both.
func aptRepo(ctx context.Context, name, keyURL, keyring, listURL string) (bool, error) {
	listPath := filepath.Join("/etc/apt/sources.list.d", name+".list")

	if _, err := os.Stat(listPath); err == nil {
		return false, nil
	}

	if !isRoot() {
		return false, fmt.Errorf("adding the %s apt repository needs root", name)
	}

	log.Printf("[apt] adding the %s repository", name)

	added := false

	err := withAptLock(func() error {
		runCtx, cancel := context.WithTimeout(ctx, aptTimeout)
		defer cancel()

		key, err := execOutputCtx(runCtx, "curl", "-fsSL", keyURL)
		if err != nil {
			return fmt.Errorf("fetch %s signing key: %w", name, err)
		}

		if _, err := writeFileIfChanged(keyring, key, 0o644); err != nil {
			return err
		}

		list, err := execOutputCtx(runCtx, "curl", "-fsSL", listURL)
		if err != nil {
			return fmt.Errorf("fetch %s source list: %w", name, err)
		}

		if _, err := writeFileIfChanged(listPath, withSignedBy(list, keyring), 0o644); err != nil {
			return err
		}

		added = true

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
		case strings.Contains(trimmed, "signed-by="):
			out = append(out, trimmed)
		case strings.HasPrefix(trimmed, "deb ["):
			out = append(out, insertOption(trimmed, "signed-by="+keyring))
		default:
			out = append(out, trimmed+" signed-by="+keyring)
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

	log.Printf("[systemd] started %s", unit)

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
		log.Printf("[systemd] daemon-reload failed: %v", err)
	}
}
