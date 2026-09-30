package publish

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/goravaa/chotu/status"
)

// collectCPU reads the aggregate line out of /proc/stat and works out the
// utilisation since the previous call. The counters are cumulative, so
// utilisation only means anything as a delta against the last reading.
func collectCPU(previous *cpuCounters) (status.CPU, *cpuCounters) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return status.CPU{}, previous
	}
	defer file.Close() //nolint:errcheck // read-only handle, nothing to report

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "cpu ") {
			continue
		}

		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			return status.CPU{}, previous
		}

		now, err := parseCPUFields(fields[1:])
		if err != nil {
			return status.CPU{}, previous
		}

		usage := 0.0

		if previous != nil {
			usage = now.usageSince(*previous)
		}

		return status.CPU{UsagePercent: usage}, &now
	}

	return status.CPU{}, previous
}

type cpuCounters struct {
	total float64
	idle  float64
}

func parseCPUFields(fields []string) (cpuCounters, error) {
	// user nice system idle iowait irq softirq steal ...
	var total, idle float64

	for index, field := range fields {
		value, err := strconv.ParseFloat(field, 64)
		if err != nil {
			return cpuCounters{}, fmt.Errorf("parse cpu counter %q: %w", field, err)
		}

		total += value

		// Fields 3 and 4 are idle and iowait: time the CPU was not doing work.
		if index == 3 || index == 4 {
			idle += value
		}
	}

	return cpuCounters{total: total, idle: idle}, nil
}

func (c cpuCounters) usageSince(previous cpuCounters) float64 {
	totalDelta := c.total - previous.total
	if totalDelta <= 0 {
		// The counters went backwards, which happens when the host is
		// restored from a snapshot. Report nothing rather than a huge number.
		return 0
	}

	usage := (totalDelta - (c.idle - previous.idle)) / totalDelta * 100
	if usage < 0 {
		return 0
	}

	return round(usage)
}

func collectRAM() status.RAM {
	fields, err := readMeminfo()
	if err != nil {
		return status.RAM{}
	}

	total := fields["MemTotal"] * 1024

	// MemAvailable is what the kernel can hand out right now without swapping.
	// Free alone understates the picture badly on a machine running a model.
	available := fields["MemAvailable"] * 1024
	if available == 0 {
		available = fields["MemFree"] * 1024
	}

	if total == 0 {
		return status.RAM{}
	}

	// Compare before subtracting. On an unsigned type an underflow wraps to
	// roughly 1.8e19, which would publish a memory usage figure in the
	// quintillions, so the guard has to be on the operands rather than on the
	// result.
	if available >= total {
		available = total
	}

	used := total - available

	return status.RAM{
		UsedBytes:    used,
		TotalBytes:   total,
		UsagePercent: round(float64(used) / float64(total) * 100),
	}
}

// readMeminfo returns the values in /proc/meminfo in kibibytes.
func readMeminfo() (map[string]uint64, error) {
	contents, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, err
	}

	fields := make(map[string]uint64)

	for _, line := range strings.Split(string(contents), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		parts := strings.Fields(value)
		if len(parts) == 0 {
			continue
		}

		parsed, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			continue
		}

		fields[key] = parsed
	}

	return fields, nil
}

// collectDisk reports usage of one filesystem. syscall.Statfs is used rather
// than shelling out to df so the numbers come from the same view of the mount
// the kernel has, with no parsing.
func collectDisk(path string) (status.Disk, error) {
	var stat syscall.Statfs_t

	if err := syscall.Statfs(path, &stat); err != nil {
		return status.Disk{}, fmt.Errorf("statfs %s: %w", path, err)
	}

	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)

	used := total - free
	if total == 0 {
		return status.Disk{Mount: path}, nil
	}

	return status.Disk{
		Mount:        path,
		UsedBytes:    used,
		TotalBytes:   total,
		UsagePercent: round(float64(used) / float64(total) * 100),
	}, nil
}

func round(value float64) float64 {
	return float64(int(value*10+0.5)) / 10
}

func parseUint(s string) uint64 {
	parsed, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}

	return parsed
}
