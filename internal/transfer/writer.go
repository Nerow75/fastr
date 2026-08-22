package transfer

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// The staged write path, per FR-032 and FR-033.
//
// Nothing reaches its final name until its checksum verifies. A file that is
// still arriving, or that arrived corrupted, must never be visible at the path
// the user will open, because the one thing worse than a failed transfer is a
// silently truncated file that looks fine.
//
// The sequence is: write into staging, verify, then rename into place. The
// rename is atomic within a filesystem, so an interrupted move cannot leave a
// half-file at the destination.

// ErrChecksumMismatch reports that a file did not arrive intact.
var ErrChecksumMismatch = errors.New("checksum mismatch")

// StagedFile is a file being received.
type StagedFile struct {
	// StagingPath is where bytes land while the transfer runs.
	StagingPath string
	// FinalPath is where the file goes once it verifies.
	FinalPath string

	file   *os.File
	closed bool
}

// CreateStaged opens a staging file for a transfer item.
//
// The staging directory is separate from the receive folder, which is what
// makes "a partial file never appears among received files" structural rather
// than a matter of remembering to clean up. config.Settings.Validate refuses a
// configuration where one contains the other.
func CreateStaged(stagingDir, finalPath, itemKey string) (*StagedFile, error) {
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("create staging directory: %w", err)
	}

	stagingPath := filepath.Join(stagingDir, itemKey+".part")
	f, err := os.OpenFile(stagingPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open staging file: %w", err)
	}

	return &StagedFile{StagingPath: stagingPath, FinalPath: finalPath, file: f}, nil
}

// OpenStaged reopens an existing staging file to resume into it, returning the
// offset already on disk.
//
// The offset comes from the file itself rather than from the store, because
// the file is the thing that will be read back: trusting a recorded offset that
// disagrees with the disk is how a resumed transfer produces a file with a hole
// in it.
func OpenStaged(stagingDir, finalPath, itemKey string) (*StagedFile, uint64, error) {
	stagingPath := filepath.Join(stagingDir, itemKey+".part")

	info, err := os.Stat(stagingPath)
	if os.IsNotExist(err) {
		staged, err := CreateStaged(stagingDir, finalPath, itemKey)
		return staged, 0, err
	}
	if err != nil {
		return nil, 0, fmt.Errorf("stat staging file: %w", err)
	}

	f, err := os.OpenFile(stagingPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, 0, fmt.Errorf("reopen staging file: %w", err)
	}

	//nolint:gosec // a file size is never negative
	return &StagedFile{StagingPath: stagingPath, FinalPath: finalPath, file: f}, uint64(info.Size()), nil
}

// Write appends to the staging file.
func (s *StagedFile) Write(p []byte) (int, error) { return s.file.Write(p) }

// Sync forces buffered data to disk.
//
// Called before a committed offset advances: the whole meaning of "committed"
// is that the bytes behind it survive a power cut, and without this it means
// only that the kernel intends to write them eventually.
func (s *StagedFile) Sync() error { return s.file.Sync() }

// Truncate cuts the staging file back to n bytes.
//
// Used when the file holds more than was ever acknowledged to the sender, which
// happens when a process dies between a write and its fsync. Those bytes were
// never promised, so dropping them is what keeps the file and the resume point
// describing the same thing.
func (s *StagedFile) Truncate(n uint64) error {
	//nolint:gosec // a staged length is bounded by the declared file size
	if err := s.file.Truncate(int64(n)); err != nil {
		return fmt.Errorf("truncate staging file: %w", err)
	}
	if _, err := s.file.Seek(int64(n), io.SeekStart); err != nil { //nolint:gosec // same bound
		return fmt.Errorf("seek staging file: %w", err)
	}
	return nil
}

// Close releases the file without moving it. Safe to call twice.
func (s *StagedFile) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.file.Close()
}

// Commit verifies the checksum and moves the file into place.
//
// A mismatch deletes the staged data and returns ErrChecksumMismatch. It does
// not leave the file for inspection: FR-033 forbids presenting an incomplete or
// failed file as if it were complete, and a stray .part is a smaller problem
// than a corrupt file the user might open.
func (s *StagedFile) Commit(computed, expected []byte) error {
	if err := s.Sync(); err != nil {
		return fmt.Errorf("sync before commit: %w", err)
	}
	if err := s.Close(); err != nil {
		return fmt.Errorf("close before commit: %w", err)
	}

	// Constant time is not strictly required for a checksum, but it costs
	// nothing and removes the question.
	if len(expected) > 0 && subtle.ConstantTimeCompare(computed, expected) != 1 {
		_ = os.Remove(s.StagingPath)
		return ErrChecksumMismatch
	}

	if err := os.MkdirAll(filepath.Dir(s.FinalPath), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	if err := os.Rename(s.StagingPath, s.FinalPath); err != nil {
		// Staging and the receive folder may sit on different filesystems, in
		// which case rename cannot work and the bytes have to be copied. This
		// loses atomicity, so the copy goes to a temporary name in the
		// destination directory and is renamed from there.
		if copyErr := crossDeviceMove(s.StagingPath, s.FinalPath); copyErr != nil {
			return fmt.Errorf("move into place: %w", copyErr)
		}
	}

	// Received files are the user's, and should look like every other file
	// they own rather than inheriting the staging directory's private mode.
	if err := os.Chmod(s.FinalPath, 0o644); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}
	return nil
}

// Discard removes the staged data, for a cancelled or abandoned transfer.
func (s *StagedFile) Discard() error {
	_ = s.Close()
	if err := os.Remove(s.StagingPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DiscardStaged removes an item's staging file without opening it.
//
// The sink-based path only reaches files this process is currently holding, and
// most partial data is not: a transfer abandoned before a restart, or one whose
// sink was already released, leaves a .part behind with nothing in memory
// pointing at it. The retention sweep and every terminal transition need to
// reach those, and the name is derived the same way CreateStaged derives it.
func DiscardStaged(stagingDir, itemKey string) error {
	if err := os.Remove(filepath.Join(stagingDir, itemKey+".part")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// crossDeviceMove copies then renames, for the case where staging and the
// receive folder are on different filesystems.
func crossDeviceMove(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()

	tmp, err := os.CreateTemp(filepath.Dir(to), ".fastr-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if err := streamFile(tmp, src); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	if err := os.Rename(tmpName, to); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Remove(from)
}

// streamFile copies through the shared fixed buffer, so even this fallback path
// does not grow with file size.
func streamFile(dst *os.File, src *os.File) error {
	bufPtr := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(bufPtr)
	buf := *bufPtr

	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
