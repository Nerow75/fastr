package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// SC-018: while idle in the background, the application stays under 1% average
// processor use and under 100 MB of memory.
//
// The budget exists because of what this program is: something a person leaves
// running so their phone can reach it, on a laptop, all day. A background
// service that costs a percent of a core is a background service somebody
// eventually quits, and then the product is one that never works when they need
// it.
//
// **It measures the real binary, not the test process.** A benchmark of the
// packages would miss exactly what the budget is about: the mDNS listener, the
// event bus, the retention sweeper, the acceptance ticker, and the reachability
// prober all run only in the assembled program, and all of them are timers that
// somebody could set too tight without noticing.
//
// The processor figure is read from the operating system's own accounting
// rather than sampled, so a burst between two samples cannot hide in it.

// idleWindow is how long the instance is watched. Long enough to cross the
// three-second discovery re-query and several event-bus ticks, short enough not
// to dominate the suite.
const idleWindow = 12 * time.Second

// Budgets, from SC-018.
const (
	maxIdleCPUPercent = 1.0
	maxIdleMemoryMB   = 100
)

func TestAnIdleInstanceStaysWithinItsBudget(t *testing.T) {
	if runtime.GOOS != "linux" {
		// The accounting below is read from /proc. The budget applies on both
		// platforms; only this way of measuring it is Linux-only, and a
		// Windows equivalent would be a different piece of work rather than a
		// tweak. Recorded rather than silently skipped everywhere.
		t.Skip("idle accounting is read from /proc; a Windows measurement needs its own implementation")
	}
	if testing.Short() {
		t.Skip("takes twelve seconds of wall clock")
	}

	binary := builtBinary(t)

	root := t.TempDir()
	cmd := exec.CommandContext(t.Context(), binary, "--port", "0", "--state-dir", root) //nolint:gosec // the path is this test's own build output
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
	)
	cmd.Stdout, cmd.Stderr = nil, nil

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	pid := cmd.Process.Pid

	// Let it finish starting: binding, opening the store, the first sweep and
	// the first discovery query all happen at once and none of them is idle.
	time.Sleep(3 * time.Second)

	before, err := processorTime(pid)
	if err != nil {
		t.Fatalf("read processor time: %v", err)
	}

	start := time.Now()

	time.Sleep(idleWindow)

	after, err := processorTime(pid)
	if err != nil {
		t.Fatalf("read processor time: %v", err)
	}
	elapsed := time.Since(start)

	used := after - before
	percent := used.Seconds() / elapsed.Seconds() * 100

	// Measured at 0 ms and 11 MB on a developer machine in August 2026, against
	// budgets of 1% and 100 MB — the process does not accumulate a single 10 ms
	// tick across twelve idle seconds. The margin is the point: a timer set too
	// tight later will show up here as a number that is no longer zero.
	t.Logf("idle for %s: %.1f ms of processor time, %.3f%%",
		elapsed.Round(time.Second), used.Seconds()*1000, percent)

	if percent > maxIdleCPUPercent {
		t.Errorf("idle processor use is %.2f%%, over the %.0f%% budget", percent, maxIdleCPUPercent)
	}

	residentMB, err := residentMemoryMB(pid)
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	t.Logf("resident memory: %d MB", residentMB)

	if residentMB > maxIdleMemoryMB {
		t.Errorf("idle memory is %d MB, over the %d MB budget", residentMB, maxIdleMemoryMB)
	}
}

// builtBinary returns the path to the compiled program, building it if the
// checked-in one is missing.
//
// The real binary rather than a test harness, because the timers under test
// only exist in `main`.
func builtBinary(t *testing.T) string {
	t.Helper()

	root := repoRoot(t)
	binary := filepath.Join(root, "bin", "fastr")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}

	if _, err := os.Stat(binary); err == nil {
		return binary
	}

	built := filepath.Join(t.TempDir(), "fastr")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	build := exec.CommandContext(ctx, "go", "build", "-o", built, "./cmd/fastr")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return built
}

// A reader that always returned zero would report a perfect idle score for a
// program pegging a core, and the test above would be worse than none. This
// proves the reader against a process that has demonstrably used processor
// time: this one, having just spent some on purpose.
//
// The obvious check — "the program must have used something during startup" —
// was tried and does not work, and the reason is the finding rather than the
// problem: fastr's whole startup, binding sockets and opening the store and
// loading catalogues and issuing the first discovery query, costs less than the
// kernel's 10 ms accounting tick.
func TestTheProcessorReaderMeasuresSomething(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc only")
	}

	before, err := processorTime(os.Getpid())
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Enough arithmetic to cross several accounting ticks, and nothing the
	// compiler can discard.
	sum := 0
	deadline := time.Now().Add(120 * time.Millisecond)
	for time.Now().Before(deadline) {
		for i := range 100_000 {
			sum += i % 7
		}
	}
	if sum == 0 {
		t.Fatal("the busy loop was optimised away")
	}

	after, err := processorTime(os.Getpid())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if after <= before {
		t.Fatalf("processor time did not move across 120ms of arithmetic: %v to %v", before, after)
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
	close := strings.LastIndex(text, ")")
	if close < 0 {
		return 0, os.ErrInvalid
	}
	fields := strings.Fields(text[close+1:])
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
