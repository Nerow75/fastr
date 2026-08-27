package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
// rather than sampled, so a burst between two samples cannot hide in it. Which
// accounting that is differs per platform and lives in the files beside this
// one: `/proc` on Linux, `GetProcessTimes` and `GetProcessMemoryInfo` on
// Windows. **Principle IV is why both exist.** The budget was always meant to
// apply on both systems; for a while only one of them was ever checked, which
// made the criterion true by measurement on Linux and true by assertion on
// Windows.
//
// There is no third implementation and no fallback. fastr ships for two
// operating systems, `internal/platform` has files for exactly those two, and a
// package that cannot build for a third would not reach a stub here anyway. If
// a third is ever added, the missing file is the compile error that says so.

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
	if testing.Short() {
		t.Skip("takes twelve seconds of wall clock")
	}

	binary := builtBinary(t)

	root := t.TempDir()
	cmd := exec.CommandContext(t.Context(), binary, "--port", "0", "--state-dir", root) //nolint:gosec // the path is this test's own build output
	cmd.Env = append(os.Environ(), isolatedEnvironment(root)...)

	// Kept rather than discarded, because the only thing worth saying when this
	// test fails is what the instance said before it stopped.
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	// **The instance has to still be running, or this measures nothing.** A
	// process that exited a second after starting reports no processor time and
	// an empty working set, which is a perfect score against both budgets. The
	// reader below is proved against a busy loop for exactly this reason; the
	// subject needed the same care and did not have it. Found on Windows, where
	// the binary refuses to start without the web bundle and the test passed
	// anyway.
	stillRunning := func() error {
		select {
		case err := <-exited:
			exited <- err // hand it back, so the cleanup above does not block
			return fmt.Errorf("the instance stopped during the measurement (%v). It said: %s",
				err, strings.TrimSpace(output.String()))
		default:
			return nil
		}
	}

	pid := cmd.Process.Pid

	// Let it finish starting: binding, opening the store, the first sweep and
	// the first discovery query all happen at once and none of them is idle.
	time.Sleep(3 * time.Second)

	if err := stillRunning(); err != nil {
		t.Fatalf("nothing to measure: %v", err)
	}

	before, err := processorTime(pid)
	if err != nil {
		t.Fatalf("read processor time: %v", err)
	}

	start := time.Now()

	time.Sleep(idleWindow)

	if err := stillRunning(); err != nil {
		t.Fatalf("the window measured a process that was not there: %v", err)
	}

	after, err := processorTime(pid)
	if err != nil {
		t.Fatalf("read processor time: %v", err)
	}
	elapsed := time.Since(start)

	used := after - before
	percent := used.Seconds() / elapsed.Seconds() * 100

	// Measured at 0 ms and 11 MB on a Linux developer machine in August 2026,
	// against budgets of 1% and 100 MB — the process does not accumulate a
	// single 10 ms tick across twelve idle seconds. The margin is the point: a
	// timer set too tight later will show up here as a number that is no longer
	// zero.
	t.Logf("idle for %s on %s: %.1f ms of processor time, %.3f%%",
		elapsed.Round(time.Second), runtime.GOOS, used.Seconds()*1000, percent)

	if percent > maxIdleCPUPercent {
		t.Errorf("idle processor use is %.2f%%, over the %.0f%% budget", percent, maxIdleCPUPercent)
	}

	residentMB, err := residentMemoryMB(pid)
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	t.Logf("resident memory: %d MB", residentMB)

	// A floor as well as a ceiling. Nothing running the Go runtime has a
	// resident set under a megabyte, so a reader that reports one is broken
	// rather than reporting a very frugal program — and a broken reader passes
	// the budget with room to spare, which is the worst way for this test to
	// fail.
	if residentMB < 1 {
		t.Errorf("the memory reader reports %d MB, which no running Go program uses", residentMB)
	}

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
	if runtime.GOOS == "windows" {
		built += ".exe"
	}
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
// kernel's accounting tick. That holds on both systems, and Windows counts more
// coarsely still.
func TestTheProcessorReaderMeasuresSomething(t *testing.T) {
	before, err := processorTime(os.Getpid())
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Enough arithmetic to cross several accounting ticks, and nothing the
	// compiler can discard. Windows updates these counters on a scheduler tick
	// of about 15.6 ms, so the window has to be comfortably wider than one.
	sum := 0
	deadline := time.Now().Add(250 * time.Millisecond)
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
		t.Fatalf("processor time did not move across 250ms of arithmetic: %v to %v", before, after)
	}
}

// The reader has to measure the *right* process, not merely some process. One
// that returned this test's own figures would sail past every assertion above
// while saying nothing at all about the binary.
func TestTheReadersLookAtTheProcessTheyAreGiven(t *testing.T) {
	if _, err := processorTime(unusedPID); err == nil {
		t.Errorf("processor time was read for pid %d, which does not exist", unusedPID)
	}
	if _, err := residentMemoryMB(unusedPID); err == nil {
		t.Errorf("memory was read for pid %d, which does not exist", unusedPID)
	}
}

// unusedPID is above the default maximum on both systems, so nothing holds it.
const unusedPID = 0x7FFF0000
