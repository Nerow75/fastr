// Package transfer moves bytes: the streaming engine, integrity, naming, and
// the staged write path.
package transfer

import (
	"fmt"

	"github.com/Nerow75/fastr/internal/platform"
)

// Free space checking, per FR-028.
//
// The check happens before a single byte moves, and it is refused with a clear
// message naming both numbers. Discovering a full disk at 90% of a 10 GB
// transfer is the worst possible moment, and it is entirely avoidable.

// SpaceHeadroom is kept free beyond the transfer itself.
//
// A destination filled to the last byte is not a working system: the operating
// system, the store, and the staging directory all need room to breathe. 64 MB
// is small enough not to refuse a legitimate transfer and large enough that
// filling the disk is a deliberate act rather than an accident.
const SpaceHeadroom = 64 << 20

// ErrInsufficientSpace reports that a transfer cannot fit.
type ErrInsufficientSpace struct {
	Needed    uint64
	Available uint64
}

func (e *ErrInsufficientSpace) Error() string {
	return fmt.Sprintf("insufficient space: need %d bytes, %d available", e.Needed, e.Available)
}

// SpaceChecker reports free space at a path.
type SpaceChecker interface {
	FreeSpace(path string) (uint64, error)
}

// CheckSpace verifies a transfer will fit at path.
//
// It reports the shortfall through a typed error so the caller can put both
// numbers into the message the user reads, which is what makes the corrective
// action ("free some space, or send fewer files") actionable rather than
// rhetorical.
func CheckSpace(checker SpaceChecker, path string, needed uint64) error {
	available, err := checker.FreeSpace(path)
	if err != nil {
		return fmt.Errorf("check free space at %s: %w", path, err)
	}

	// Overflow matters here: needed comes from a peer, and a hostile or broken
	// sender could declare a size close to the maximum. Adding headroom to it
	// without checking would wrap and pass.
	if needed > ^uint64(0)-SpaceHeadroom {
		return &ErrInsufficientSpace{Needed: needed, Available: available}
	}

	if available < needed+SpaceHeadroom {
		return &ErrInsufficientSpace{Needed: needed, Available: available}
	}
	return nil
}

// PlatformChecker adapts the platform to the SpaceChecker interface.
type PlatformChecker struct{ Platform platform.Platform }

func (p PlatformChecker) FreeSpace(path string) (uint64, error) {
	return p.Platform.FreeSpace(path)
}
