package platform

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// The tray and the single-instance guard.
//
// FR-048 to FR-052: fastr stays reachable with its window closed, the user can
// always tell whether it is listening, and can stop it in one action. With no
// window of its own, the tray is the surface that carries all three.
//
// research.md item 3 flagged that the tray depends on a system library that
// some Linux desktops do not ship. That is why TrayUnavailable exists and why
// the caller is expected to keep running headless rather than refusing to
// start: a missing icon must not cost the user their file transfers.

// ErrTrayUnavailable reports that no system tray could be attached.
var ErrTrayUnavailable = errors.New("no system tray available")

// ErrAlreadyRunning reports that another instance holds the lock.
var ErrAlreadyRunning = errors.New("already running")

// TrayMenu is what the tray offers. Every entry is also reachable from the web
// interface, so a user without a working tray loses convenience, not function.
type TrayMenu struct {
	// OnOpen opens the interface in the user's browser.
	OnOpen func()
	// OnStop stops listening without quitting, per FR-050.
	OnStop func()
	// OnQuit exits the process entirely.
	OnQuit func()
	// Status returns the line shown at the top of the menu.
	Status func() string
}

// InstanceLock prevents a second instance from binding the same port and
// opening the same store.
//
// The store's own file lock would catch a second instance too, but only after
// it had started, printed, and confused the user. Failing early with a clear
// message is better than failing correctly with an obscure one.
type InstanceLock struct {
	path string
	file *os.File
	once sync.Once
}

// AcquireInstanceLock takes the single-instance lock, or reports who holds it.
func AcquireInstanceLock(dataDir string) (*InstanceLock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}

	path := filepath.Join(dataDir, "fastr.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}

	if err := lockFile(file); err != nil {
		file.Close()
		return nil, err
	}

	// Record the process identifier so a stale lock can be explained rather
	// than merely refused.
	_ = file.Truncate(0)
	if _, err := file.WriteAt([]byte(pidString()), 0); err != nil {
		// Not fatal: the lock is held, which is what matters.
		_ = err
	}

	return &InstanceLock{path: path, file: file}, nil
}

// Release drops the lock.
func (l *InstanceLock) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		_ = unlockFile(l.file)
		_ = l.file.Close()
		_ = os.Remove(l.path)
	})
}

func pidString() string {
	pid := os.Getpid()
	if pid < 0 {
		return ""
	}
	digits := make([]byte, 0, 12)
	for pid > 0 {
		digits = append([]byte{byte('0' + pid%10)}, digits...)
		pid /= 10
	}
	return string(digits)
}
