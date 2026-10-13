package publish

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/setup"
	"github.com/goravaa/chotu/chotu-setup/internals/config"
)

// DefaultMonitorInterval is how often the daemon publishes metrics while setup
// runs. One second keeps CPU/GPU/RAM visibly live on the page without
// hammering the machine: collection is a few /proc reads plus nvidia-smi and
// two systemctl calls.
const DefaultMonitorInterval = time.Second

// Monitor publishes metrics on its own ticker, independent of setup progress.
//
// The stall it fixes: collection and publishing used to run synchronously on
// the task goroutine that reported progress (runner.WithProgress). A cycle
// with no state changes — vllm pulling a multi-GB image, tooling apt-getting,
// gpu waiting on a reboot — published nothing for minutes, and every publish
// that did fire blocked a setup task behind Collect+POST (each with up to 10s
// of subprocess/HTTP timeouts).
//
// The monitor splits the two halves:
//   - setup goroutines only call Update, which copies the task snapshot under
//     a lock and returns immediately (no I/O, never blocks setup).
//   - a single ticker goroutine owns the Collect+Publish cadence and reads the
//     latest snapshot. If a tick is still in flight (slow nvidia-smi, status
//     service down with a 10s HTTP timeout), the next tick is skipped instead
//     of piling up.
type Monitor struct {
	collector *Collector
	version   string
	interval  time.Duration

	mu       sync.RWMutex
	cycle    int
	cfg      *config.Config
	layout   setup.Layout
	results  map[string]setup.TaskResult
	state    string
	started  time.Time
	hasCycle bool

	inflight atomic.Bool
}

// NewMonitor builds a monitor that publishes with the given daemon version on
// the given interval. A non-positive interval falls back to
// DefaultMonitorInterval.
func NewMonitor(collector *Collector, version string, interval time.Duration) *Monitor {
	if interval <= 0 {
		interval = DefaultMonitorInterval
	}

	return &Monitor{
		collector: collector,
		version:   version,
		interval:  interval,
	}
}

// Update records the latest setup snapshot. It is called from runner progress
// callbacks, so it must be cheap and never do I/O: copy the map and return.
func (m *Monitor) Update(
	cfg *config.Config,
	layout setup.Layout,
	results map[string]setup.TaskResult,
	cycle int,
	machineState string,
	started time.Time,
) {
	if m == nil {
		return
	}

	snapshot := make(map[string]setup.TaskResult, len(results))
	for name, result := range results {
		snapshot[name] = result
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg = cfg
	m.layout = layout
	m.results = snapshot
	m.cycle = cycle
	m.state = machineState
	m.started = started
	m.hasCycle = true
}

// Run ticks until ctx is done, publishing one document per interval from the
// latest snapshot. It returns when ctx is cancelled. An immediate first tick
// means the page gets live metrics within one interval of boot, not after the
// first task finishes.
func (m *Monitor) Run(ctx context.Context) {
	if m == nil {
		return
	}

	m.tick(ctx)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

// tick publishes one document. If the previous tick is still running its
// Collect+POST, this tick is skipped: a single in-flight document is enough to
// keep the page honest, and overlapping collects would only stack up slow
// subprocess calls and skew the CPU delta.
func (m *Monitor) tick(ctx context.Context) {
	m.mu.RLock()
	if !m.hasCycle || m.cfg == nil {
		m.mu.RUnlock()
		return
	}

	cfg := m.cfg
	layout := m.layout
	results := m.results
	cycle := m.cycle
	machineState := m.state
	started := m.started
	m.mu.RUnlock()

	if !m.inflight.CompareAndSwap(false, true) {
		slog.Debug(
			"skipping monitor tick: previous collect/publish still in flight",
			"component", "status",
			"cycle", cycle,
		)

		return
	}
	defer m.inflight.Store(false)

	var elapsed time.Duration
	if !started.IsZero() {
		elapsed = time.Since(started)
	}

	publisher := NewPublisher("", cfg.StatusPage.Port, cfg.StatusPage.Enabled)

	document := m.collector.Collect(ctx, Options{
		Version:      m.version,
		Cycle:        cycle,
		MachineState: machineState,
		Results:      results,
		Layout:       layout,
		StateDir:     layout.StateDir,
		Duration:     elapsed,
	})

	publisher.Publish(ctx, document)
}
