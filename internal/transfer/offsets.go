package transfer

import (
	"context"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"sync"
)

// The receiving end of an upward transfer, per FR-031 and FR-032.
//
// A phone cannot accept an inbound connection, so it cannot be fetched from the
// way the desktop is: it has to push. It pushes in chunks, and every chunk is a
// separate request, which leaves two problems the pipe never had.
//
//  1. **What is durable.** The answer this endpoint returns is a promise: the
//     phone is entitled to delete nothing, but it *is* entitled to never send
//     those bytes again. So the offset only advances after an fsync. Without
//     that, "committed" would mean the kernel intends to write it eventually,
//     and a power cut would leave a file with a hole the length check cannot
//     see.
//
//  2. **Where the checksum comes from.** BLAKE2b is a streaming hash with no
//     serialisable state, so the digest of a file arriving over forty requests
//     cannot be stored between them. Rehashing the staged prefix on every chunk
//     would turn a linear transfer into a quadratic one. The hash state is
//     therefore kept in memory alongside the open file, for as long as the
//     process lives, and rebuilt by one pass over the staged bytes when it does
//     not — a restart mid-transfer, which is rare and costs one read.
//
// A Sink is that pair: an open staging file and the hash of what is in it.

// ErrCommittedLoss reports a staging file shorter than the offset already
// acknowledged to the sender. It should be unreachable, because an offset is
// only returned after the bytes behind it are on disk.
var ErrCommittedLoss = errors.New("staged data is shorter than the committed offset")

// Sink is one incoming item being written.
//
// Every method holds the lock for the whole operation, including the copy. Two
// concurrent uploads of the same item therefore serialise, and the second finds
// the offset moved and is refused rather than interleaving into the file.
type Sink struct {
	mu     sync.Mutex
	staged *StagedFile
	hasher hash.Hash

	// committed is the durable offset: bytes written, hashed, and fsynced.
	committed uint64
	closed    bool
}

// Sinks holds the open sinks of this process, keyed by item.
type Sinks struct {
	mu    sync.Mutex
	byKey map[string]*Sink
}

// NewSinks returns an empty set.
func NewSinks() *Sinks { return &Sinks{byKey: make(map[string]*Sink)} }

// Open returns the sink for an item, creating it on first use.
//
// committed is what the store believes is durable. The staging file is brought
// into agreement with it: a file longer than the committed offset holds bytes
// written but never acknowledged, most likely by a process that died between
// the write and the fsync, and they are truncated away. Keeping them would put
// the hash out of step with what the sender thinks it still owes.
func (s *Sinks) Open(key Key, stagingDir, itemKey string, committed uint64) (*Sink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sink, ok := s.byKey[key.String()]; ok {
		return sink, nil
	}

	staged, onDisk, err := OpenStaged(stagingDir, "", itemKey)
	if err != nil {
		return nil, err
	}

	if onDisk < committed {
		_ = staged.Close()
		return nil, fmt.Errorf("%w: %d on disk, %d acknowledged", ErrCommittedLoss, onDisk, committed)
	}
	if onDisk > committed {
		if err := staged.Truncate(committed); err != nil {
			_ = staged.Close()
			return nil, err
		}
	}

	hasher, err := NewHasher()
	if err != nil {
		_ = staged.Close()
		return nil, err
	}
	if committed > 0 {
		if err := rehash(hasher, staged.StagingPath, committed); err != nil {
			_ = staged.Close()
			return nil, err
		}
	}

	sink := &Sink{staged: staged, hasher: hasher, committed: committed}
	s.byKey[key.String()] = sink
	return sink, nil
}

// Release forgets a sink, closing its file. Used when a transfer reaches a
// terminal state, so a cancelled transfer does not hold a descriptor open.
func (s *Sinks) Release(key Key) {
	s.mu.Lock()
	sink, ok := s.byKey[key.String()]
	delete(s.byKey, key.String())
	s.mu.Unlock()

	if ok {
		sink.close()
	}
}

// Discard forgets a sink and deletes its staged data.
func (s *Sinks) Discard(key Key) error {
	s.mu.Lock()
	sink, ok := s.byKey[key.String()]
	delete(s.byKey, key.String())
	s.mu.Unlock()

	if !ok {
		return nil
	}
	return sink.discard()
}

// CloseAll releases every open staging file, leaving the partial data on disk
// for a later resume.
//
// Called at shutdown. It matters more than it looks: Windows refuses to delete
// or move a file that is still open, so a handle left behind by a transfer that
// was merely abandoned — the phone closed, nobody cancelled anything — would
// block the retention sweep from ever clearing it. Linux would have let it go
// unnoticed, which is exactly the kind of difference Principle IV exists to
// keep out of the domain logic.
func (s *Sinks) CloseAll() {
	s.mu.Lock()
	held := make([]*Sink, 0, len(s.byKey))
	for key, sink := range s.byKey {
		held = append(held, sink)
		delete(s.byKey, key)
	}
	s.mu.Unlock()

	for _, sink := range held {
		sink.close()
	}
}

// Len reports how many sinks are held. Tests use it to check they do not leak.
func (s *Sinks) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byKey)
}

// Offset is the durable offset: what a resuming sender should continue from.
func (sk *Sink) Offset() uint64 {
	sk.mu.Lock()
	defer sk.mu.Unlock()
	return sk.committed
}

// Append writes a chunk starting at offset and returns the new committed
// offset.
//
// A chunk that does not start exactly where the file ends is refused with
// ErrOffsetMismatch rather than seeked to. A gap would produce a file that is
// the right length and the wrong content, which is the one failure this whole
// design exists to prevent, and the sender is told the real offset so it can
// correct rather than corrupt. FR-031.
func (sk *Sink) Append(
	ctx context.Context,
	offset uint64,
	body io.Reader,
	limit uint64,
	onProgress ProgressFunc,
) (uint64, error) {
	sk.mu.Lock()
	defer sk.mu.Unlock()

	if sk.closed {
		return 0, os.ErrClosed
	}
	if offset != sk.committed {
		return sk.committed, fmt.Errorf("%w: the file holds %d bytes, the chunk starts at %d",
			ErrOffsetMismatch, sk.committed, offset)
	}
	if offset > limit {
		return sk.committed, fmt.Errorf("offset %d is beyond the declared size %d", offset, limit)
	}

	// The declared size is the contract, and the sender is a peer: a body longer
	// than what remains is refused rather than written. Without the bound, a
	// declaration of one byte could fill the disk.
	remaining := limit - offset
	result, err := Copy(ctx, sk.staged, io.LimitReader(body, int64(remaining)), //nolint:gosec // bounded by the declared size
		sk.hasher, limit, offset, onProgress)

	// A short or failed copy still wrote what it wrote, and those bytes are
	// hashed and on the way to disk. Committing them is what makes the next
	// attempt resume rather than restart.
	if result.Written > 0 {
		if syncErr := sk.staged.Sync(); syncErr != nil {
			return sk.committed, fmt.Errorf("commit offset: %w", syncErr)
		}
		sk.committed += result.Written
	}
	if err != nil {
		return sk.committed, err
	}

	return sk.committed, nil
}

// Commit verifies the digest of everything received and moves the file to
// finalPath.
//
// The expected digest comes from the sender, which computed it over the same
// bytes as it read them. A mismatch deletes the staged data and reports failure:
// FR-032 forbids reporting a corrupted transfer as a success, and FR-033 forbids
// the file appearing at its final path at all.
func (sk *Sink) Commit(expected []byte, finalPath string) error {
	sk.mu.Lock()
	defer sk.mu.Unlock()

	if sk.closed {
		return os.ErrClosed
	}
	sk.closed = true

	sk.staged.FinalPath = finalPath
	return sk.staged.Commit(sk.hasher.Sum(nil), expected)
}

// Digest is what the receiver computed. Used to report a mismatch, and by tests.
func (sk *Sink) Digest() []byte {
	sk.mu.Lock()
	defer sk.mu.Unlock()
	return sk.hasher.Sum(nil)
}

// StagingPath is where the partial file lives, for the retention sweep.
func (sk *Sink) StagingPath() string {
	sk.mu.Lock()
	defer sk.mu.Unlock()
	return sk.staged.StagingPath
}

func (sk *Sink) close() {
	sk.mu.Lock()
	defer sk.mu.Unlock()

	if sk.closed {
		return
	}
	sk.closed = true
	_ = sk.staged.Close()
}

func (sk *Sink) discard() error {
	sk.mu.Lock()
	defer sk.mu.Unlock()

	if sk.closed {
		return nil
	}
	sk.closed = true
	return sk.staged.Discard()
}

// rehash rebuilds the hash state over the first n bytes of a staging file.
//
// Only reached when the process restarted mid-transfer: the state is otherwise
// carried forward in memory from chunk to chunk. It reads through the shared
// fixed buffer, so recovering a 10 GB partial costs one pass and no memory.
func rehash(h hash.Hash, path string, n uint64) error {
	// The path is built from the configured staging directory and an item key
	// made of a ULID and an integer, so nothing here comes from a peer.
	f, err := os.Open(path) //nolint:gosec // path is constructed, never supplied
	if err != nil {
		return fmt.Errorf("reopen staged data: %w", err)
	}
	// Read-only, so a failed close loses nothing; the caller is told about a
	// read failure instead, which is the one that would matter.
	defer func() { _ = f.Close() }()

	bufPtr := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(bufPtr)
	buf := *bufPtr

	var read uint64
	for read < n {
		want := uint64(len(buf))
		if remaining := n - read; remaining < want {
			want = remaining
		}

		got, err := f.Read(buf[:want])
		if got > 0 {
			if _, werr := h.Write(buf[:got]); werr != nil {
				return werr
			}
			read += uint64(got) //nolint:gosec // a read count is never negative
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("read staged data: %w", err)
		}
	}

	if read != n {
		return fmt.Errorf("%w: rehashed %d of %d", ErrCommittedLoss, read, n)
	}
	return nil
}
