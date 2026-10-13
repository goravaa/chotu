package publish

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/config"
	"github.com/goravaa/chotu/chotu-setup/internals/setup"
	"github.com/goravaa/chotu/status"
)

// The monitor must publish on its ticker even when setup reports no progress.
// This is the stall: a cycle with a long task (vllm pull, apt install) fired
// no progress events, so the old Collect+Publish-on-progress path published
// nothing for minutes.
func TestMonitorPublishesWithoutProgressEvents(t *testing.T) {
	var received atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var document status.Status
		if err := json.NewDecoder(r.Body).Decode(&document); err != nil {
			t.Errorf("decode: %v", err)
		}
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	collector := NewCollector()
	monitor := NewMonitor(collector, "test", 10*time.Millisecond)

	cfg := &config.Config{}
	cfg.StatusPage.Enabled = true
	cfg.StatusPage.Port = portOf(t, server.URL)

	// One snapshot, then silence: no further Updates, mimicking a long task
	// that never changes state.
	monitor.Update(cfg, setup.Layout{DataDir: "/"}, map[string]setup.TaskResult{
		"vllm": {State: setup.Running},
	}, 1, "INITIALIZING", time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go monitor.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for received.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := received.Load(); got < 3 {
		t.Fatalf("published = %d, want at least 3 docs with no progress events", got)
	}
}

// A tick that is still in flight must not pile up overlapping collects.
// The second tick arriving while the first POST hangs is skipped.
func TestMonitorSkipsOverlappingTicks(t *testing.T) {
	release := make(chan struct{})
	var received atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		<-release // hang the first POST
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer close(release)

	collector := NewCollector()
	monitor := NewMonitor(collector, "test", 10*time.Millisecond)

	cfg := &config.Config{}
	cfg.StatusPage.Enabled = true
	cfg.StatusPage.Port = portOf(t, server.URL)

	monitor.Update(cfg, setup.Layout{DataDir: "/"}, map[string]setup.TaskResult{}, 1, "INITIALIZING", time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go monitor.Run(ctx)

	time.Sleep(100 * time.Millisecond)

	// The first POST is still hung, so at most one document may have arrived.
	// Without the skip, every 10ms tick would stack another blocked collect.
	if got := received.Load(); got > 1 {
		t.Fatalf("published = %d during a hung POST, want at most 1 (ticks must skip, not pile up)", got)
	}
}

// Concurrent Collects must not race on the CPU delta. Run with -race.
func TestCollectorIsConcurrentSafe(t *testing.T) {
	collector := NewCollector()
	opts := testOptions()

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			collector.Collect(context.Background(), opts)
			select {
			case done <- struct{}{}:
			default:
			}
		}()
	}

	timeout := time.After(10 * time.Second)
	for i := 0; i < 8; i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("concurrent Collect hung")
		}
	}
}
