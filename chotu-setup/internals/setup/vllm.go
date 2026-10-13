package setup

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/config"
)

// This file owns everything the vLLM tasks need: the inference image and its
// warmup, the container lifecycle, and the readiness wait. Tasks() in tasks.go
// only wires the task names and dependencies; the components live here so the
// whole inference path can be reasoned about in one place.

const dockerContainerName = "chotu-vllm"

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

// vllmImages lists every container image the vLLM path needs, deduped and in
// pull order. Both the warmup task and the container reconcile pull through
// this list, so supporting another image later (a sidecar, a new smoke test)
// is one entry here and the bytes are already local when they are needed.
func vllmImages(cfg *config.Config) []string {
	seen := make(map[string]bool)

	var images []string

	for _, image := range []string{inferenceImage(cfg)} {
		image = strings.TrimSpace(image)
		if image == "" || seen[image] {
			continue
		}
		seen[image] = true
		images = append(images, image)
	}

	return images
}

// ensureImagePulled makes sure one image is present locally. It is idempotent:
// a cached image is an inspect hit and no network happens, so calling it on
// every cycle is cheap and a registry outage cannot fail a machine that
// already has the bytes. Pulling a multi-gigabyte image needs more than the
// command timeout, so the pull gets a budget of its own.
func ensureImagePulled(ctx context.Context, image, component string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return fmt.Errorf("empty image name")
	}

	if _, err := run(ctx, "docker", "image", "inspect", image); err == nil {
		slog.Info(fmt.Sprintf("image %s already present, skipping pull", image), "component", component)

		return nil
	}

	slog.Info(fmt.Sprintf("pulling %s (not cached locally)", image), "component", component)

	pullCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	if out, err := execOutputCtx(pullCtx, "docker", "pull", image); err != nil {
		return fmt.Errorf("pull %s (network/registry): %w: %s", image, err, strings.TrimSpace(out))
	}

	return nil
}

// setupVLLMImageWarmup pulls the inference images early, as soon as the Docker
// daemon is up, so gigabytes of download overlap the GPU chain instead of
// serialising behind it. setupVLLM still does its own inspect+pull, which is
// what makes this safe to run ahead: on success the vLLM task's inspect hits
// and skips its pull; on failure nothing is lost because the vLLM task pulls
// authoritatively anyway.
//
// Best-effort on purpose: it always returns nil. A warmup must never turn the
// vLLM task BLOCKED via a failed dependency; the vLLM task owns the real error.
func setupVLLMImageWarmup(cfg *config.Config) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil {
			return nil
		}

		for _, image := range vllmImages(cfg) {
			if err := ensureImagePulled(ctx, image, "vllm-pull"); err != nil {
				// Not a failure: setupVLLM retries the pull authoritatively and
				// reports the real error there. Logging at info keeps a transient
				// registry flake from reading as a broken machine.
				slog.Info(fmt.Sprintf("warmup pull of %s did not complete, vllm will pull: %v",
					image, err), "component", "vllm-pull")
			} else {
				slog.Info(fmt.Sprintf("warmed %s", image), "component", "vllm-pull")
			}
		}

		return nil
	}
}

// setupVLLM runs the inference container and waits for it to actually serve the
// configured model. A container that Docker reports as running is not ready: it
// still has to load the model, and a failed download must not be reported as
// success.
func setupVLLM(cfg *config.Config, layout Layout) func(context.Context) error {
	return func(ctx context.Context) error {
		if cfg == nil {
			slog.Info("no config, nothing to do", "component", "vllm")

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
				slog.Info(fmt.Sprintf("last %d log lines:\n%s", 200, logs), "component", "vllm")
			}

			return err
		}

		slog.Info(fmt.Sprintf("serving %s on port %d", model, port), "component", "vllm")
		slog.Info(fmt.Sprintf("/v1/models: %s", out), "component", "vllm")

		return nil
	}
}

func reconcileVLLMContainer(
	ctx context.Context,
	cfg *config.Config,
	layout Layout,
	image string,
	port int,
	model string,
) error {
	fingerprint := vllmFingerprint(cfg, image, port, model)

	// The container is recreated when the configuration it was created from
	// changes, and left alone when it matches, so a steady cycle does not
	// restart a model that is already serving.
	if existing, err := run(ctx, "docker", "inspect", "--format",
		`{{index .Config.Labels "chotu.fingerprint"}} {{.State.Running}}`, dockerContainerName); err == nil {
		fields := strings.Fields(existing)

		if len(fields) == 2 && fields[0] == fingerprint && fields[1] == "true" {
			if ready, _ := inferenceReady(ctx, port, model); ready {
				return nil
			}
		}

		slog.Info(fmt.Sprintf("replacing the existing %s container", dockerContainerName), "component", "vllm")

		if _, err := run(ctx, "docker", "rm", "-f", dockerContainerName); err != nil {
			return fmt.Errorf("remove the old vllm container: %w", err)
		}
	}

	// The warmup task usually pulled this already; the inspect inside hits and
	// no second download happens.
	if err := ensureImagePulled(ctx, image, "vllm"); err != nil {
		return err
	}

	runCtx, cancelRun := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelRun()

	args := []string{
		"run", "-d",
		"--name", dockerContainerName,
		"--restart", "unless-stopped",
		"--device", "nvidia.com/gpu=all",
		"--ipc=host",
		"--label", "chotu.fingerprint=" + fingerprint,
		"-p", fmt.Sprintf("%d:8000", port),
		"-v", layout.ModelCache + ":/root/.cache/huggingface",
		// Parallel chunked downloads for multi-GB safetensors. Harmless when
		// the image predates hf_transfer (huggingface_hub falls back with a
		// warning); several times faster when it is present.
		"-e", "HF_HUB_ENABLE_HF_TRANSFER=1",
		image,
		"--model", model,
		"--max-model-len", fmt.Sprint(maxModelLen(cfg)),
		"--gpu-memory-utilization", fmt.Sprintf("%g", gpuMemoryUtilization(cfg)),
	}

	if parser := toolCallParser(model); parser != "" {
		args = append(args,
			"--enable-auto-tool-choice",
			"--tool-call-parser", parser,
		)
	}

	if out, err := execOutputCtx(runCtx, "docker", args...); err != nil {
		return fmt.Errorf("start the vllm container: %w: %s", err, strings.TrimSpace(out))
	}

	return nil
}

// vllmFingerprint captures the knobs that must recreate the container when
// they change. Image alone is not enough: max-model-len and gpu util drift
// would otherwise leave an already-running container forever.
func vllmFingerprint(cfg *config.Config, image string, port int, model string) string {
	return fmt.Sprintf("%s|%s|%d|%d|%g|%s",
		image,
		model,
		port,
		maxModelLen(cfg),
		gpuMemoryUtilization(cfg),
		toolCallParser(model),
	)
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
	started := time.Now()
	lastLog := time.Now()

	var last string

	for {
		out, err := execOutput("curl", "-fsS", "--max-time", "10", endpoint)
		if err == nil {
			if model == "" || strings.Contains(out, model) {
				return out, nil
			}

			last = "the endpoint is up but is not serving " + model +
				" (wrong model or stale container): " + summarizeModels(out)
		} else {
			last = err.Error()

			// Fail fast on a dead container instead of burning the whole 30m
			// budget curling a port nothing will ever answer. The container
			// exiting is a different condition from the model still
			// downloading, and retrying the wait cannot fix it.
			if stateErr := checkVLLMContainerState(ctx, model); stateErr != nil {
				return "", stateErr
			}
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("the inference endpoint %s did not become ready in %s: %s. "+
				"The model may still be downloading into the model cache on first boot; "+
				"`docker logs %s` shows the download/load progress",
				endpoint, budget, last, dockerContainerName)
		}

		// A healthy first boot downloads gigabytes and then loads the model,
		// both silent at the default log level. A heartbeat every minute keeps
		// the operator from killing a working download that only looks stuck.
		if time.Since(lastLog) >= time.Minute {
			slog.Info(fmt.Sprintf("still waiting for inference (%s elapsed): container running, last check: %s",
				time.Since(started).Round(time.Second), last), "component", "vllm")
			lastLog = time.Now()
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

// checkVLLMContainerState returns a specific error when the vLLM container is
// not running. It returns nil when the container is running (still starting)
// or when the state cannot be determined (in which case the wait continues and
// curl remains the source of truth).
func checkVLLMContainerState(ctx context.Context, model string) error {
	out, err := run(ctx, "docker", "inspect", "--format",
		"{{.State.Running}} {{.State.ExitCode}} {{.State.OOMKilled}} {{.State.Error}}",
		dockerContainerName)
	if err != nil {
		// Cannot determine state (docker hiccup, container being replaced):
		// keep waiting rather than fail on an inspection flake.
		return nil
	}

	fields := strings.Fields(out)
	if len(fields) == 0 {
		return nil
	}

	if fields[0] == "true" {
		return nil
	}

	exitCode := ""
	oomKilled := false
	if len(fields) >= 2 {
		exitCode = fields[1]
	}
	if len(fields) >= 3 {
		oomKilled = fields[2] == "true"
	}

	logs := containerLogs(ctx, dockerContainerName, 50)

	if oomKilled || exitCode == "137" {
		return fmt.Errorf(
			"the vllm container was OOM-killed (exit %s): the model does not fit in GPU "+
				"memory with the current settings; lower inference.resources.max_model_len or "+
				"inference.resources.gpu_memory_utilization in config.yaml. Last logs:\n%s",
			exitCode, logs,
		)
	}

	detail := ""
	if len(fields) >= 4 {
		detail = strings.Join(fields[3:], " ")
	}

	return fmt.Errorf(
		"the vllm container exited (exit %s%s) instead of serving %q: `docker logs %s` ends with:\n%s",
		exitCode, withDetailSuffix(detail), model, dockerContainerName, logs,
	)
}

func withDetailSuffix(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" || detail == "<no value>" {
		return ""
	}

	return ", " + detail
}

// summarizeModels keeps the "wrong model" error to one line. /v1/models can be
// kilobytes of JSON; the operator needs the model IDs, not the full document.
func summarizeModels(body string) string {
	const maxLen = 300

	body = strings.TrimSpace(body)
	if body == "" {
		return "(empty response)"
	}

	// Prefer the model IDs when the body is a JSON list.
	ids := []string{}
	for _, line := range strings.Split(body, `"id"`) {
		if len(ids) >= 5 {
			break
		}
		rest := strings.TrimLeft(line, ` :"`)
		end := strings.Index(rest, `"`)
		if end > 0 && end < 200 {
			ids = append(ids, rest[:end])
		}
	}

	if len(ids) > 0 {
		return "serving [" + strings.Join(ids, ", ") + "]"
	}

	if len(body) > maxLen {
		return body[:maxLen] + "..."
	}

	return body
}

func containerLogs(ctx context.Context, container string, lines int) string {
	out, err := run(ctx, "docker", "logs", "--tail", fmt.Sprint(lines), container)
	if err != nil && out == "" {
		return ""
	}

	return out
}
