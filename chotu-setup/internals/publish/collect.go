package publish

import (
	"context"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goravaa/chotu/chotu-setup/internals/setup"
	"github.com/goravaa/chotu/status"
)

// Options is everything the collector needs to turn a finished cycle into a
// status document.
type Options struct {
	// Version is the daemon build, so a status can be traced to a binary.
	Version string

	// Cycle is how many cycles this daemon has completed.
	Cycle int

	// MachineState is the coarse state the daemon recorded for the machine.
	MachineState string

	// Results is the outcome of the setup task graph for this cycle.
	Results map[string]setup.TaskResult

	// Layout is the resolved storage topology, used to report the filesystems
	// the machine actually depends on.
	Layout setup.Layout

	// StateDir holds the markers the tasks write.
	StateDir string

	// Duration is how long the cycle took. It is reported because a cycle that
	// suddenly takes minutes is the clearest sign of a stuck task, and a page
	// that always reads 0 hides exactly that.
	Duration time.Duration
}

// Collector builds status documents. It holds the previous CPU reading,
// because utilisation is only meaningful as a delta.
type Collector struct {
	previousCPU *cpuCounters
}

func NewCollector() *Collector { return &Collector{} }

// Collect gathers a status document. Monitoring failures are absorbed: the page
// should show a partial machine rather than nothing at all, and none of this is
// worth failing a setup cycle over.
func (c *Collector) Collect(ctx context.Context, opts Options) status.Status {
	document := status.New()
	document.Version = opts.Version
	document.Cycle = opts.Cycle
	document.State = opts.MachineState
	document.Setup = buildSetup(opts)

	document.Monitoring.CPU, c.previousCPU = collectCPU(c.previousCPU)
	document.Monitoring.RAM = collectRAM()
	document.Monitoring.GPU = collectGPU(ctx)
	document.Monitoring.Disks = collectDisks(opts.Layout)
	document.Monitoring.Sync = collectSync(ctx)

	return *document
}

// buildSetup maps the task graph onto the page. The task names are the keys, so
// a task added to the graph shows up without touching the status package.
func buildSetup(opts Options) status.Setup {
	setupReport := status.Setup{
		Tasks:  make(map[string]status.ServiceStatus, len(opts.Results)),
		Failed: []string{},
	}

	for name, result := range opts.Results {
		message := ""
		if result.Error != nil {
			message = result.Error.Error()
		}

		setupReport.Tasks[name] = status.ServiceStatus{
			State:   string(result.State),
			Message: message,
		}

		// Only root failures belong in Failed. Blocked dependents are still in
		// Tasks with their dependency message, but listing them here would hide
		// the real cause behind a cascade of "did not complete" noise.
		if result.State == setup.Failed {
			setupReport.Failed = append(setupReport.Failed, name)
		}
	}

	// Sorting keeps the page stable between cycles, so a task that fails and
	// recovers does not shuffle the list the user is reading.
	sort.Strings(setupReport.Failed)

	setupReport.LastCycleDurationMS = opts.Duration.Milliseconds()

	return setupReport
}

// collectGPU asks the driver, which is the only thing that knows. A machine
// without a GPU reports zeroes rather than an error: the GPU task already
// reports the real problem.
func collectGPU(ctx context.Context) status.GPU {
	query := "utilization.gpu,memory.used,memory.total,temperature.gpu"

	out, err := runWithTimeout(ctx, "nvidia-smi",
		"--query-gpu="+query, "--format=csv,noheader,nounits")
	if err != nil {
		return status.GPU{}
	}

	// Report the first device. A machine with more than one GPU would need a
	// per-device list here, which the status type does not carry yet.
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	fields := strings.Split(line, ",")
	if len(fields) < 4 {
		return status.GPU{}
	}

	return status.GPU{
		UsagePercent:       parseFloat(fields[0]),
		MemoryUsedBytes:    parseUint(fields[1]) * 1024 * 1024,
		MemoryTotalBytes:   parseUint(fields[2]) * 1024 * 1024,
		TemperatureCelsius: parseFloat(fields[3]),
	}
}

// collectDisks reports the filesystems the machine depends on, which is not the
// same as every mount: the root disk and the instance store are the two the
// user pays for and the two that can fill up.
func collectDisks(layout setup.Layout) []status.Disk {
	paths := []string{"/"}
	if layout.DataDir != "" && layout.DataDir != "/" {
		paths = append(paths, layout.DataDir)
	}

	disks := make([]status.Disk, 0, len(paths))

	for _, path := range paths {
		disk, err := collectDisk(path)
		if err != nil {
			continue
		}

		disks = append(disks, disk)
	}

	return disks
}

// collectSync reports the workspace sync timer. Every part is optional: a
// machine with S3 disabled has no unit, and that is not a sync failure.
func collectSync(ctx context.Context) status.Sync {
	sync := status.Sync{State: "disabled"}

	active, err := runWithTimeout(ctx, "systemctl", "is-active", "chotu-workspace-sync.timer")
	if err != nil {
		return sync
	}

	sync.State = strings.TrimSpace(active)

	properties, err := runWithTimeout(ctx, "systemctl", "show",
		"chotu-workspace-sync.service",
		"--property=ExecMainStatus", "--property=Result",
		"--property=ExecMainExitTimestamp")
	if err == nil {
		for _, line := range strings.Split(properties, "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}

			switch key {
			case "ExecMainStatus":
				if value == "0" {
					sync.State = "healthy"
				} else if value != "" {
					sync.Message = "last run exited " + value
				}
			case "ExecMainExitTimestamp":
				// Exit timestamp is the last attempt. Only a successful run
				// promotes it to LastSuccess; a failed run must not look like
				// a successful sync on the status page.
				sync.LastAttempt = value
				if sync.State == "healthy" {
					sync.LastSuccess = value
				}
			case "Result":
				if value != "" && value != "success" {
					sync.Message = "result: " + value
				}
			}
		}
	}

	return sync
}

func parseFloat(s string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}

	return round(parsed)
}

// runWithTimeout keeps a hung command from stalling the daemon. Monitoring runs
// on the same cycle as setup, so it has to be cheap and bounded.
func runWithTimeout(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	slog.Debug("running command for metrics collection", "component", "status", "command", name, "args", args)

	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()

	if err != nil {
		slog.Debug("metrics collection command failed", "component", "status", "command", name, "error", err, "output", string(out))
	}

	return strings.TrimSpace(string(out)), err
}
