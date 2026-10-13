// Package status defines the machine status document that the setup daemon
// produces and the status service serves.
//
// It lives in its own module because two deployables need it: chotu-setup
// writes it, status-service reads it, and neither should own a copy. A
// duplicated struct is a contract that silently drifts.
package status

import "time"

type Status struct {
	// Timestamp is when this document was produced, in UTC.
	Timestamp string `json:"timestamp"`

	// Version is the chotu-setup build that produced it.
	Version string `json:"version"`

	// State is the machine state: INITIALIZING, READY or FAILED.
	State string `json:"state"`

	// Cycle counts the setup cycles completed by this daemon.
	Cycle int `json:"cycle"`

	Setup      Setup      `json:"setup"`
	Monitoring Monitoring `json:"monitoring"`
}

// Setup reports the outcome of the setup task graph.
type Setup struct {
	// Tasks is keyed by task name, so a task added to the graph appears here
	// without a change to this package.
	Tasks map[string]ServiceStatus `json:"tasks"`

	// Failed lists the tasks that did not complete, sorted, so the page can lead
	// with what is broken.
	Failed []string `json:"failed"`

	// LastCycleDurationMS is how long the most recent cycle took. It is the
	// number a user watches when the machine seems stuck.
	LastCycleDurationMS int64 `json:"last_cycle_duration_ms"`
}

type ServiceStatus struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

type Monitoring struct {
	CPU   CPU     `json:"cpu"`
	RAM   RAM     `json:"ram"`
	GPU   GPU     `json:"gpu"`
	Disks []Disk  `json:"disks"`
	Sync  Sync    `json:"sync"`
}

type CPU struct {
	UsagePercent float64 `json:"usage_percent"`
}

type RAM struct {
	UsedBytes    uint64  `json:"used_bytes"`
	TotalBytes   uint64  `json:"total_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

type GPU struct {
	UsagePercent       float64 `json:"usage_percent"`
	MemoryUsedBytes    uint64  `json:"memory_used_bytes"`
	MemoryTotalBytes   uint64  `json:"memory_total_bytes"`
	TemperatureCelsius float64 `json:"temperature_celsius"`
}

type Disk struct {
	Mount        string  `json:"mount"`
	UsedBytes    uint64  `json:"used_bytes"`
	TotalBytes   uint64  `json:"total_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

type Sync struct {
	State       string `json:"state"`
	LastSuccess string `json:"last_success"`
	LastAttempt string `json:"last_attempt"`
	Message     string `json:"message"`
}

func New() *Status {
	return &Status{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Setup: Setup{
			Tasks:  map[string]ServiceStatus{},
			Failed: []string{},
		},
		Monitoring: Monitoring{
			Disks: []Disk{},
		},
	}
}
