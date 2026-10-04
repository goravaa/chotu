package publish

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/setup"
	"github.com/goravaa/chotu/status"
)

type testError string

func (e testError) Error() string { return string(e) }

func testOptions() Options {
	return Options{
		Version:      "test",
		Cycle:        3,
		MachineState: "FAILED",
		Results: map[string]setup.TaskResult{
			setup.TaskStorage: {State: setup.Completed},
			setup.TaskDocker:  {State: setup.Failed, Error: testError("no GPU")},
			setup.TaskVLLM:    {State: setup.Completed},
		},
		Layout: setup.Layout{
			Mode:       "persistent",
			DataDir:    "/data",
			StateDir:   "/var/lib/chotu",
			DockerRoot: "/var/lib/docker",
			Workspace:  "/home/ubuntu/workspace",
		},
	}
}

// TestPublishSendsTheDocument proves the daemon actually reaches the status
// service with a decodable body, which is the whole contract between the two
// deployables.
func TestPublishSendsTheDocument(t *testing.T) {
	received := make(chan status.Status, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}

		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q, want application/json", got)
		}

		var document status.Status
		if err := json.NewDecoder(r.Body).Decode(&document); err != nil {
			t.Errorf("decode: %v", err)
		}

		received <- document
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	document := NewCollector().Collect(context.Background(), testOptions())

	NewPublisher("", portOf(t, server.URL), true).Publish(context.Background(), document)

	select {
	case got := <-received:
		if got.State != "FAILED" {
			t.Errorf("state = %q, want FAILED", got.State)
		}

		if got.Version != "test" {
			t.Errorf("version = %q, want test", got.Version)
		}

		if got.Cycle != 3 {
			t.Errorf("cycle = %d, want 3", got.Cycle)
		}

		// The task names are the keys, so the page shows the graph as it is
		// rather than as a hand-maintained list.
		if _, ok := got.Setup.Tasks[setup.TaskDocker]; !ok {
			t.Errorf("tasks = %v, want an entry for %s", got.Setup.Tasks, setup.TaskDocker)
		}

		if message := got.Setup.Tasks[setup.TaskDocker].Message; message != "no GPU" {
			t.Errorf("docker message = %q, want the task error", message)
		}

	case <-time.After(5 * time.Second):
		t.Fatal("the status service never received a document")
	}
}

// TestPublishToleratesAnUnreachableService is the important one. The status
// service is started by one of the setup tasks, so on a first boot it is often
// not running yet. Reporting that must never fail or stall a cycle.
func TestPublishToleratesAnUnreachableService(t *testing.T) {
	// Port 1 is reserved and nothing listens on it, so the dial fails fast.
	publisher := NewPublisher("", 1, true)

	done := make(chan struct{})

	go func() {
		defer close(done)

		// A panic here would be the failure this test exists to prevent.
		publisher.Publish(context.Background(), *status.New())
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Publish blocked on an unreachable service")
	}
}

// TestPublishToleratesARejection covers the other half: a service that answers
// with an error is still not a reason to fail setup.
func TestPublishToleratesARejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	NewPublisher("", portOf(t, server.URL), true).
		Publish(context.Background(), *status.New())
}

// TestPublishIsInertWhenDisabled keeps a machine with the status page switched
// off from paying for an HTTP client it will never use.
func TestPublishIsInertWhenDisabled(t *testing.T) {
	publisher := NewPublisher("", 0, false)

	if publisher.Enabled() {
		t.Fatal("publisher is enabled with status_page.enabled false")
	}

	done := make(chan struct{})

	go func() {
		defer close(done)
		publisher.Publish(context.Background(), *status.New())
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish did something with the status page disabled")
	}
}

// TestCollectReportsFailedTasks pins what the page leads with: the failures,
// sorted, with the reason attached.
func TestCollectReportsFailedTasks(t *testing.T) {
	options := testOptions()
	options.Results = map[string]setup.TaskResult{
		setup.TaskVLLM:    {State: setup.Failed, Error: testError("model download failed")},
		setup.TaskDocker:  {State: setup.Failed, Error: testError("daemon unreachable")},
		setup.TaskStorage: {State: setup.Completed},
	}

	document := NewCollector().Collect(context.Background(), options)

	want := []string{setup.TaskDocker, setup.TaskVLLM}

	if len(document.Setup.Failed) != len(want) {
		t.Fatalf("failed = %v, want %v", document.Setup.Failed, want)
	}

	for i, name := range want {
		if document.Setup.Failed[i] != name {
			t.Errorf("failed[%d] = %s, want %s (sorted)", i, document.Setup.Failed[i], name)
		}
	}
}

// TestCollectOmitsBlockedDependentsFromFailed keeps the page's failed list
// focused on root causes. Cascaded BLOCKED tasks stay in Tasks with their
// dependency message, but must not crowd the lead-in list.
func TestCollectOmitsBlockedDependentsFromFailed(t *testing.T) {
	options := testOptions()
	options.Results = map[string]setup.TaskResult{
		setup.TaskGPU: {
			State: setup.Failed,
			Error: testError("reboot required: nvidia driver is not loaded yet"),
		},
		setup.TaskDockerGPU: {
			State: setup.Blocked,
			Error: testError(`dependency "gpu" for task "docker-gpu" did not complete`),
		},
		setup.TaskVLLM: {
			State: setup.Blocked,
			Error: testError(`dependency "docker-gpu" for task "vllm" did not complete`),
		},
	}

	document := NewCollector().Collect(context.Background(), options)

	if got := document.Setup.Failed; len(got) != 1 || got[0] != setup.TaskGPU {
		t.Fatalf("failed = %v, want only [%s]", got, setup.TaskGPU)
	}

	if document.Setup.Tasks[setup.TaskDockerGPU].State != string(setup.Blocked) {
		t.Errorf("docker-gpu state = %q, want BLOCKED on the task map",
			document.Setup.Tasks[setup.TaskDockerGPU].State)
	}
}

// TestCollectReportsHostMetrics checks the monitoring block is populated from
// the running machine. A status page showing zeroes is indistinguishable from a
// broken one, so the collectors have to actually read something.
func TestCollectReportsHostMetrics(t *testing.T) {
	document := NewCollector().Collect(context.Background(), testOptions())

	if document.Monitoring.RAM.TotalBytes == 0 {
		t.Error("ram total is zero: /proc/meminfo was not read")
	}

	if len(document.Monitoring.Disks) == 0 {
		t.Fatal("no disks reported: the root filesystem was not measured")
	}

	if document.Monitoring.Disks[0].Mount != "/" {
		t.Errorf("disks[0].Mount = %s, want the root filesystem", document.Monitoring.Disks[0].Mount)
	}

	if document.Monitoring.Disks[0].TotalBytes == 0 {
		t.Error("root filesystem reported zero bytes")
	}

	// A machine with no GPU reports zeroes rather than a failure, but the sync
	// block must always be a valid value for the page to render.
	if document.Monitoring.Sync.State == "" {
		t.Error("sync state is empty: the page would render a blank field")
	}
}

// TestCollectReportsCPUUsageAcrossCycles covers the delta: the first reading has
// nothing to compare against, the second one does.
func TestCollectReportsCPUUsageAcrossCycles(t *testing.T) {
	collector := NewCollector()

	first := collector.Collect(context.Background(), testOptions())
	if first.Monitoring.CPU.UsagePercent != 0 {
		t.Errorf(
			"first reading = %v, want 0: there is nothing to compare against yet",
			first.Monitoring.CPU.UsagePercent,
		)
	}

	// Burn a little CPU so the counters move.
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = strings.Repeat("x", 1000)
	}

	second := collector.Collect(context.Background(), testOptions())

	usage := second.Monitoring.CPU.UsagePercent
	if usage < 0 || usage > 100 {
		t.Errorf("second reading = %v, want a percentage between 0 and 100", usage)
	}
}

// TestCollectReportsTheCycleDuration guards a bug worth guarding: the duration
// was measured in the daemon and never carried into the document, so a cycle
// that took six seconds published as 0. A page reading 0 forever hides exactly
// the stuck task the user is trying to spot.
func TestCollectReportsTheCycleDuration(t *testing.T) {
	options := testOptions()
	options.Duration = 6*time.Second + 400*time.Millisecond

	document := NewCollector().Collect(context.Background(), options)

	if got := document.Setup.LastCycleDurationMS; got != 6400 {
		t.Errorf("last cycle duration = %dms, want 6400ms", got)
	}
}

// portOf extracts the port from an httptest URL.
func portOf(t *testing.T, rawURL string) int {
	t.Helper()

	trimmed := strings.TrimPrefix(rawURL, "http://127.0.0.1:")

	port, err := strconv.Atoi(trimmed)
	if err != nil {
		t.Fatalf("could not parse a port out of %s: %v", rawURL, err)
	}

	return port
}
