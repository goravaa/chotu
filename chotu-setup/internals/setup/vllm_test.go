package setup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/config"
)

// vllmImages is the single list both the warmup and the reconcile pull
// through: adding an image here is enough to have it warm before it is needed.
func TestVLLMImagesCoversInferenceImage(t *testing.T) {
	cfg := &config.Config{}
	cfg.Inference.Image = "example.com/custom:v1"

	images := vllmImages(cfg)

	if len(images) != 1 || images[0] != "example.com/custom:v1" {
		t.Fatalf("images = %v, want the configured inference image exactly once", images)
	}

	// The default (empty config image) must still resolve to something
	// pullable, never to an empty entry.
	def := vllmImages(&config.Config{})
	if len(def) != 1 || strings.TrimSpace(def[0]) == "" {
		t.Fatalf("default images = %v, want one concrete image", def)
	}
}

// ensureImagePulled is idempotent in both directions: a bogus image errors
// (authoritative path for setupVLLM), while the warmup over the same list
// stays best-effort. The invalid registry fails fast with or without a daemon.
func TestEnsureImagePulledFailsFastOnBogusImage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	err := ensureImagePulled(ctx, "invalid.invalid/chotu-test:latest", "test")
	if err == nil {
		t.Fatal("expected an error for an unpullable image")
	}

	if !strings.Contains(err.Error(), "pull") {
		t.Errorf("error = %q, want it to name the pull", err)
	}

	if err := ensureImagePulled(ctx, "  ", "test"); err == nil {
		t.Error("expected an error for an empty image name")
	}
}
