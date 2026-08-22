package transfer

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
)

// Telling an interruption from a failure, per FR-031 and FR-035d.
//
// These two outcomes look identical at the point they happen — a copy returned
// an error partway through a file — and they could not be more different
// afterwards:
//
//   - An **interruption** is the network going away. The partial data is exactly
//     right as far as it goes, the resume point stands, and the sender should
//     come back and continue. Nothing is lost.
//   - A **failure** is the destination refusing to hold the file: the disk is
//     full, the folder is read-only, the path stopped existing. Retrying cannot
//     help, and pretending otherwise leaves the user with a transfer that says
//     "interrupted" forever while quietly failing every attempt.
//
// The default is interruption, deliberately. A transfer wrongly marked
// interrupted keeps its bytes and is swept after seven days; one wrongly marked
// failed throws them away immediately. When the classification is uncertain, the
// recoverable answer is the one that costs less to be wrong about.

// Fault is why a transfer stopped.
type Fault int

const (
	// FaultInterrupted means the connection ended. The resume point stands.
	FaultInterrupted Fault = iota
	// FaultDestinationFull means there is no room for the rest of the file.
	FaultDestinationFull
	// FaultDestinationUnwritable means the destination cannot be written at
	// all: read-only, permission denied, or gone.
	FaultDestinationUnwritable
)

// Recoverable reports whether the transfer can be continued later.
func (f Fault) Recoverable() bool { return f == FaultInterrupted }

// Classify decides what an error that stopped a copy actually was.
//
// Only errors that name a condition retrying cannot fix are treated as failures.
// Everything else — a reset connection, a closed body, a cancelled request, a
// timeout, a plain unexpected EOF — is the network, which is the overwhelmingly
// common case on a phone that locked its screen or walked out of range.
func Classify(err error) Fault {
	switch {
	case err == nil:
		return FaultInterrupted
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return FaultDestinationFull
	case errors.Is(err, syscall.EROFS),
		errors.Is(err, os.ErrPermission),
		errors.Is(err, syscall.EACCES),
		errors.Is(err, syscall.EPERM):
		return FaultDestinationUnwritable
	default:
		return FaultInterrupted
	}
}

// IsConnectionLoss reports the errors a dropped connection actually produces.
//
// Not used to decide the fault — Classify's default already covers them — but
// to keep them out of the log at warning level. A phone leaving Wi-Fi is not an
// incident, and a log that says it is trains its reader to ignore it.
func IsConnectionLoss(err error) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, io.EOF),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.EPIPE),
		errors.Is(err, net.ErrClosed):
		return true
	case errors.As(err, &netErr):
		return true
	default:
		return false
	}
}
