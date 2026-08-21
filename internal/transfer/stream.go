package transfer

import (
	"context"
	"errors"
	"fmt"
	"hash"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/blake2b"
)

// The streaming engine, per FR-029 and FR-032.
//
// Two properties matter and both are structural rather than incidental:
//
//   - Memory does not grow with file size. Every copy goes through one fixed
//     buffer, so a 10 GB transfer costs the same as a 10 MB one. SC-003 checks
//     it within 10%.
//   - Integrity is computed *during* the copy, not by a second pass. Reading a
//     10 GB file twice to hash it would halve the throughput for nothing.

// BufferSize is the copy buffer. 256 KB is large enough to keep a gigabit link
// busy without the syscall overhead of small reads, and small enough that a
// few concurrent copies do not add up to anything noticeable.
const BufferSize = 256 << 10

// bufferPool reuses copy buffers. The queue runs one transfer at a time, so
// this is mostly about not allocating a fresh 256 KB on every resumed chunk.
var bufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, BufferSize)
		return &buf
	},
}

// ErrCancelled reports that a transfer was stopped deliberately.
var ErrCancelled = errors.New("transfer cancelled")

// Progress is reported as a copy runs.
type Progress struct {
	// Transferred is the total bytes moved for this item so far, including
	// anything already committed before a resume.
	Transferred uint64
	// Rate is bytes per second over the recent window.
	Rate float64
	// Remaining is an estimate in seconds, or -1 when it cannot be computed.
	Remaining float64
}

// ProgressFunc receives progress updates. It must not block: it is called from
// the copy loop, and a slow callback becomes a slow transfer.
type ProgressFunc func(Progress)

// CopyResult is what a completed copy produced.
type CopyResult struct {
	// Written is how many bytes this call moved.
	Written uint64
	// Checksum is the BLAKE2b digest of everything hashed, which is the whole
	// file when the copy started at offset zero.
	Checksum []byte
	// Partial reports that the copy stopped before the source was exhausted.
	Partial bool
}

// Copy streams from src to dst, hashing as it goes.
//
// startOffset is how many bytes were already transferred before this call, used
// only to report progress correctly on a resume. When resuming, hasher must
// already carry the digest of those bytes, or the final checksum will be wrong.
//
// The context is checked between buffers rather than mid-write, so cancelling
// never leaves a half-written buffer.
func Copy(
	ctx context.Context,
	dst io.Writer,
	src io.Reader,
	hasher io.Writer,
	total uint64,
	startOffset uint64,
	onProgress ProgressFunc,
) (CopyResult, error) {
	bufPtr := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(bufPtr)
	buf := *bufPtr

	var written uint64
	transferred := startOffset

	reporter := newRateReporter(startOffset, total, onProgress)

	for {
		select {
		case <-ctx.Done():
			return CopyResult{Written: written, Partial: true}, ErrCancelled
		default:
		}

		n, readErr := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]

			// Hash before writing. If the write fails, the hash state is ahead
			// of the file, but the transfer has failed anyway and the partial
			// file is discarded. The reverse order would risk hashing bytes
			// that never reached disk on a successful-looking path.
			if hasher != nil {
				if _, err := hasher.Write(chunk); err != nil {
					return CopyResult{Written: written, Partial: true}, fmt.Errorf("hash: %w", err)
				}
			}

			if _, err := dst.Write(chunk); err != nil {
				return CopyResult{Written: written, Partial: true}, fmt.Errorf("write: %w", err)
			}

			//nolint:gosec // n is a read count from a fixed-size buffer
			written += uint64(n)
			transferred += uint64(n)
			reporter.report(transferred)
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				reporter.flush(transferred)
				return CopyResult{Written: written}, nil
			}
			return CopyResult{Written: written, Partial: true}, fmt.Errorf("read: %w", readErr)
		}
	}
}

// NewHasher returns the digest used for integrity, per research.md section 6.
//
// BLAKE2b rather than SHA-256: it is faster without hardware acceleration,
// which is exactly the phone case, and it is in golang.org/x/crypto, which the
// project already depends on.
func NewHasher() (hash.Hash, error) {
	h, err := blake2b.New256(nil)
	if err != nil {
		return nil, fmt.Errorf("build hasher: %w", err)
	}
	return h, nil
}

// rateReporter turns a byte count into a rate and an estimate, without
// flooding the caller.
//
// Progress is emitted at most every 200ms. The event bus throttles again at
// four per second, but doing it here as well means the callback, and everything
// behind it, is not woken thousands of times a second for a fast local copy.
type rateReporter struct {
	onProgress ProgressFunc
	total      uint64

	start      time.Time
	startBytes uint64

	lastReport time.Time
	lastBytes  uint64
	now        func() time.Time
}

const reportInterval = 200 * time.Millisecond

func newRateReporter(startBytes, total uint64, onProgress ProgressFunc) *rateReporter {
	now := time.Now()
	return &rateReporter{
		onProgress: onProgress,
		total:      total,
		start:      now,
		startBytes: startBytes,
		lastReport: now,
		lastBytes:  startBytes,
		now:        time.Now,
	}
}

func (r *rateReporter) report(transferred uint64) {
	if r.onProgress == nil {
		return
	}
	now := r.now()
	if now.Sub(r.lastReport) < reportInterval {
		return
	}
	r.emit(now, transferred)
}

// flush emits a final update, so the interface does not sit at 99% because the
// last partial buffer fell inside the throttle window.
func (r *rateReporter) flush(transferred uint64) {
	if r.onProgress == nil {
		return
	}
	r.emit(r.now(), transferred)
}

func (r *rateReporter) emit(now time.Time, transferred uint64) {
	elapsed := now.Sub(r.lastReport).Seconds()
	rate := 0.0
	if elapsed > 0 {
		rate = float64(transferred-r.lastBytes) / elapsed
	}

	remaining := -1.0
	if rate > 0 && r.total > transferred {
		remaining = float64(r.total-transferred) / rate
	}

	r.onProgress(Progress{Transferred: transferred, Rate: rate, Remaining: remaining})

	r.lastReport = now
	r.lastBytes = transferred
}
