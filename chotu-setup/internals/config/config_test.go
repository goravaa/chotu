package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}

// TestLoadRejectsSpotWithPersistence is the contradiction that would otherwise
// ship silently. Persistence keeps the workspace and the Docker root on a root
// disk that outlives the instance; a spot instance does not outlive itself, so
// the two together would lose the user's work on the first reclamation.
func TestLoadRejectsSpotWithPersistence(t *testing.T) {
	path := write(t, `
machine:
  use_spot: true
  storage:
    persist: true
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("spot with persist=true: want an error")
	}

	for _, want := range []string{"use_spot", "persist"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %s", err, want)
		}
	}

	// The message has to say what to do, not just what is wrong.
	if !strings.Contains(err.Error(), "on-demand") {
		t.Errorf("error = %q, want it to point at the fix", err)
	}
}

func TestLoadAcceptsBothWorkingCombinations(t *testing.T) {
	for name, body := range map[string]string{
		"spot with everything on the instance store": `
machine:
  use_spot: true
  storage:
    persist: false
`,
		"on-demand with a persistent root disk": `
machine:
  use_spot: false
  storage:
    persist: true
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err != nil {
				t.Errorf("Load: %v", err)
			}
		})
	}
}

// TestLoadRejectsIncompleteEnabledFeatures stops a config that switches a
// feature on without saying what to switch on, which would otherwise fail much
// later inside a task with a less obvious message.
func TestLoadRejectsIncompleteEnabledFeatures(t *testing.T) {
	t.Run("s3 enabled with no bucket", func(t *testing.T) {
		_, err := Load(write(t, "s3:\n  enabled: true\n"))
		if err == nil || !strings.Contains(err.Error(), "bucket_name") {
			t.Errorf("err = %v, want it to name s3.bucket_name", err)
		}
	})
}

// TestImagesArePinned stops a floating tag from creeping back in. `latest` moves
// several times a week, and the vLLM command line changes with it, so an
// unpinned image means a machine that worked this morning can fail this
// afternoon with nothing in Chotu having changed.
func TestImagesArePinned(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "..", "dev.config.yaml"))
	if err != nil {
		t.Fatalf("load the shipped config: %v", err)
	}

	image := cfg.Inference.Image

	if strings.HasSuffix(image, ":latest") || !strings.Contains(image, ":v") {
		t.Errorf("inference.image = %q, want a version tag", image)
	}

	if !strings.Contains(image, "@sha256:") {
		t.Errorf(
			"inference.image = %q, want it pinned by digest. A version tag alone "+
			"leaves the exact bytes unpinned; see the upgrade note in dev.config.yaml "+
			"for how to resolve a digest",
			image,
		)
	}
}

// TestLoadReadsTheShippedConfig keeps the dev config loadable. It is the
// local-run default and the reference terraform defaults are mirrored from,
// so a field the loader does not recognise is a silent behaviour change.
func TestLoadReadsTheShippedConfig(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "..", "dev.config.yaml"))
	if err != nil {
		t.Fatalf("load the shipped config: %v", err)
	}

	if !cfg.StatusPage.Enabled {
		t.Error("status_page.enabled = false, want the shipped config to serve the page")
	}

	if cfg.StatusPage.Port != 8081 {
		t.Errorf("status_page.port = %d, want 8081", cfg.StatusPage.Port)
	}

	if !cfg.Machine.Storage.Persist {
		t.Error("machine.storage.persist = false, want the shipped default to be persistent")
	}

	if cfg.Machine.UseSpot {
		t.Error("machine.use_spot = true with persist=true: the shipped config must not " +
			"ship the combination the loader rejects")
	}
}
