//go:build linux

package integration

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Reading the kernel's own accounting for SC-018, on Linux. See
// `idle_budget_test.go` for what the numbers are for.

// isolatedEnvironment keeps the measured instance out of the developer's real
// configuration and data directories.
func isolatedEnvironment(root string) []string {
	return []string{
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"),
	}
}

// processorTime reads how much processor time a process has used, from the
// kernel's own accounting.
//
// Fields 14 and 15 of /proc/<pid>/stat are user and system ticks. Read rather
// than sampled: a burst between two samples of instantaneous usage is invisible,
// and a burst is exactly what a badly set timer produces.
func processorTime(pid int) (time.Duration, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}

	// The second field is the executable name in parentheses and may contain
	// spaces, so the fields after it are counted from the closing bracket.
	text := string(raw)
	shut := strings.LastIndex(text, ")")
	if shut < 0 {
		return 0, os.ErrInvalid
	}
	fields := strings.Fields(text[shut+1:])
	if len(fields) < 13 {
		return 0, os.ErrInvalid
	}

	user, err := strconv.ParseInt(fields[11], 10, 64)
	if err != nil {
		return 0, err
	}
	system, err := strconv.ParseInt(fields[12], 10, 64)
	if err != nil {
		return 0, err
	}

	// The kernel counts in clock ticks, conventionally 100 per second on Linux.
	const ticksPerSecond = 100
	return time.Duration(user+system) * time.Second / ticksPerSecond, nil
}

// residentMemoryMB reads the resident set size in megabytes.
func residentMemoryMB(pid int) (int, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, err
	}

	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, os.ErrInvalid
		}
		kb, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, err
		}
		return kb / 1024, nil
	}
	return 0, os.ErrNotExist
}
