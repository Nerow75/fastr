//go:build windows

package integration

import (
	"fmt"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Reading the operating system's own accounting for SC-018, on Windows. See
// `idle_budget_test.go` for what the numbers are for.
//
// **This is a different implementation rather than a port**, which is why the
// budget went unmeasured here for a while. There is no `/proc`: the figures
// come from `GetProcessTimes` and `GetProcessMemoryInfo` against a handle, and
// the handle has to be opened and closed around every read.
//
// The two counters chosen are the ones that mean the same thing as the Linux
// pair. Kernel plus user time is what `/proc/<pid>/stat` reports as system plus
// user ticks; `WorkingSetSize` is the set of pages actually resident, which is
// what `VmRSS` counts. Picking `PagefileUsage` instead would measure something
// the Linux side is not measuring, and the two platforms would then be held to
// numbers that are not comparable — which for a parity criterion is worse than
// having only one of them.

// isolatedEnvironment keeps the measured instance out of the developer's real
// configuration and data directories.
//
// `LOCALAPPDATA` rather than the XDG variables the Linux side sets: it is what
// `platform_windows.go` reads for both the configuration and the data
// directory, and nothing here honours XDG.
func isolatedEnvironment(root string) []string {
	return []string{"LOCALAPPDATA=" + filepath.Join(root, "appdata")}
}

// processorTime reads how much processor time a process has used.
//
// Read from the accounting rather than sampled, for the same reason as on
// Linux: a burst between two samples of instantaneous usage is invisible, and a
// burst is exactly what a badly set timer produces.
func processorTime(pid int) (time.Duration, error) {
	handle, err := openForAccounting(pid)
	if err != nil {
		return 0, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, fmt.Errorf("process times for %d: %w", pid, err)
	}
	return intervalsOf(kernel) + intervalsOf(user), nil
}

// residentMemoryMB reads the working set in megabytes, the closest thing
// Windows has to a resident set size.
func residentMemoryMB(pid int) (int, error) {
	handle, err := openForAccounting(pid)
	if err != nil {
		return 0, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	var counters processMemoryCounters
	counters.CB = uint32(unsafe.Sizeof(counters))

	ok, _, callErr := procGetProcessMemoryInfo.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.CB),
	)
	if ok == 0 {
		return 0, fmt.Errorf("memory counters for %d: %w", pid, callErr)
	}

	const bytesPerMB = 1024 * 1024
	return int(counters.WorkingSetSize / bytesPerMB), nil
}

// The call itself, declared here because `golang.org/x/sys/windows` does not
// export it: it has `GetProcessWorkingSetSizeEx`, which reports the *limits* on
// a process's working set rather than what it is actually using, and those two
// answer different questions. Twenty lines against pulling in a dependency for
// one function, which the dependency budget in research.md would not have.
//
// `K32GetProcessMemoryInfo` from kernel32 rather than `GetProcessMemoryInfo`
// from psapi: the same function, exported where it does not depend on which
// psapi shim the system resolves.
var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetProcessMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
)

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS. Only `CB` and
// `WorkingSetSize` are read; the rest has to be here so the structure is the
// size the call is told it is.
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// openForAccounting opens a process for reading its counters and nothing else.
//
// `PROCESS_VM_READ` is in there because `GetProcessMemoryInfo` requires it;
// `GetProcessTimes` alone would be content with far less. Both are read rights,
// and the handle is closed immediately after each read rather than held for the
// duration of the measurement, so a test that fails does not leave one behind.
func openForAccounting(pid int) (windows.Handle, error) {
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ,
		false,
		uint32(pid), //nolint:gosec // a process identifier, never negative
	)
	if err != nil {
		return 0, fmt.Errorf("open process %d: %w", pid, err)
	}
	return handle, nil
}

// intervalsOf converts a FILETIME used as a duration.
//
// Not `Filetime.Nanoseconds()`, which subtracts the 1601 epoch: correct for a
// point in time, and wrong by three and a half centuries for an amount of one.
// These two fields are amounts.
func intervalsOf(ft windows.Filetime) time.Duration {
	const interval = 100 * time.Nanosecond
	ticks := int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
	return time.Duration(ticks) * interval
}
