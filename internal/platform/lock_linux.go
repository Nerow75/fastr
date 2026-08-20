//go:build linux

package platform

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive advisory lock without blocking.
//
// flock rather than a lock file whose existence is the signal: a process that
// is killed releases a flock, whereas a stale lock file would strand the user
// until they found and deleted it.
func lockFile(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return fmt.Errorf("%w: another instance is already running", ErrAlreadyRunning)
		}
		return err
	}
	return nil
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
