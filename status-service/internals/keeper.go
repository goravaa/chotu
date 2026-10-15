package internals

import (
	"strings"
	"sync"
	"time"

	"github.com/goravaa/chotu/status"
)

// Keeper tracks timing in memory: when each setup task was first seen and how
// long it took to reach a terminal state, plus this service's own boot time.
//
// It observes the same documents the store keeps, so no new data source is
// needed and nothing is persisted. A restart resets every clock, which is the
// honest semantic for an in-memory stopwatch.
type Keeper struct {
	mu     sync.RWMutex
	now    func() time.Time
	booted time.Time
	tasks  map[string]*taskClock
}

type taskClock struct {
	state     string
	firstSeen time.Time
	sightings int
	done      bool
	known     bool
	took      time.Duration
}

func NewKeeper() *Keeper {
	booted := time.Now().UTC()
	return NewKeeperWithClock(time.Now, booted)
}

func NewKeeperWithClock(now func() time.Time, booted time.Time) *Keeper {
	return &Keeper{
		now:    now,
		booted: booted,
		tasks:  map[string]*taskClock{},
	}
}

// terminalTaskState reports whether no further progress is expected for a
// task. Blocked is not terminal: the task never ran, it is still waiting.
func terminalTaskState(state string) bool {
	return state == "completed" || state == "failed"
}

// Observe folds one published document into the clocks. It is called on every
// status POST, right after the store update. A nil keeper is a no-op, so
// callers constructed before the keeper existed keep working.
func (k *Keeper) Observe(document status.Status) {
	if k == nil {
		return
	}

	now := k.now()

	k.mu.Lock()
	defer k.mu.Unlock()

	for name, task := range document.Setup.Tasks {
		state := strings.ToLower(task.State)

		clock, ok := k.tasks[name]
		if !ok {
			clock = &taskClock{firstSeen: now}
			k.tasks[name] = clock
		}
		clock.sightings++

		if terminalTaskState(state) {
			if !clock.done {
				clock.done = true
				// First sight already terminal: it finished before this
				// service started watching, so there is no duration to
				// report and the page must show a dash, not 0s.
				clock.known = clock.sightings > 1
				clock.took = now.Sub(clock.firstSeen)
			}
		} else if clock.done {
			// Regressed out of a terminal state (a new cycle re-running
			// it): restart the stopwatch rather than reporting stale time.
			clock.done = false
			clock.known = false
			clock.took = 0
			clock.firstSeen = now
			clock.sightings = 1
		}

		clock.state = state
	}
}

// TaskTiming is one task's stopwatch as served to the page. TookSeconds is
// null when the task has not finished yet, or when it was already finished
// the first time this service saw it.
type TaskTiming struct {
	State       string   `json:"state"`
	TookSeconds *float64 `json:"took_seconds"`
}

// Meta is the keeper's snapshot: service uptime plus every task stopwatch.
type Meta struct {
	BootedAt      string                `json:"service_booted_at"`
	UptimeSeconds int64                 `json:"service_uptime_seconds"`
	Tasks         map[string]TaskTiming `json:"tasks"`
}

func (k *Keeper) Snapshot() Meta {
	now := k.now()

	k.mu.RLock()
	defer k.mu.RUnlock()

	tasks := make(map[string]TaskTiming, len(k.tasks))
	for name, clock := range k.tasks {
		timing := TaskTiming{State: clock.state}
		if clock.done && clock.known {
			took := clock.took.Seconds()
			timing.TookSeconds = &took
		}
		tasks[name] = timing
	}

	return Meta{
		BootedAt:      k.booted.Format(time.RFC3339),
		UptimeSeconds: int64(now.Sub(k.booted).Seconds()),
		Tasks:         tasks,
	}
}
