package transfer

import (
	"fmt"
	"os"
	"path/filepath"
)

// Relaying, per FR-053 to FR-058.
//
// Two phones paired to the same computer send to each other through it. That
// is the one case where this machine ends up holding data that is not its own,
// and every rule here follows from taking that seriously.
//
// **Why it is staged rather than piped.** The desktop-to-phone path joins a
// fetching receiver to a supplying sender and never touches the disk. That
// cannot work here: the supplying side is a phone, and a phone holding one
// streaming request open for the length of a file is a phone that loses the
// whole transfer the moment its screen locks. So the sender pushes chunks the
// way it does to a computer, the bytes wait on disk, and the receiver fetches
// them. It costs a round trip through the disk and buys a transfer that
// survives a locked screen on either side.
//
// **Where it is kept, and what that guarantees.** Relayed bytes live in a
// directory of their own, inside the staging area and never inside the receive
// folder. That is structural rather than a matter of remembering to clean up:
// FR-055 says relayed data must not appear as a file this computer received,
// and a file that is never written there cannot. `config.Settings.Validate`
// already refuses a configuration where staging and the receive folder overlap.
//
// **It is deleted on every ending.** Completed, failed, cancelled, abandoned:
// each of those calls Discard, and the retention sweep catches whatever a crash
// left behind. SC-019 puts it plainly — zero bytes remain afterwards, whichever
// way it ended.

// relayFolder is the subdirectory of staging that holds relayed content.
//
// A separate directory rather than a naming convention, so "delete everything
// relayed" is one operation that cannot miss a file, and so an operator looking
// at the staging area can see at a glance what is theirs and what is passing
// through.
const relayFolder = "relay"

// RelayDir is where relayed content for one transfer lives.
//
// Per transfer rather than per item: the whole transfer ends at once, so the
// whole directory goes at once.
func RelayDir(stagingDir, transferID string) string {
	return filepath.Join(stagingDir, relayFolder, transferID)
}

// PrepareRelay creates the directory a relayed transfer will use.
func PrepareRelay(stagingDir, transferID string) (string, error) {
	dir := RelayDir(stagingDir, transferID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create relay directory: %w", err)
	}
	return dir, nil
}

// DiscardRelay removes everything staged for one transfer.
//
// Safe to call on a transfer that never relayed anything, and safe to call
// twice: both are the ordinary case, because every terminal path calls it and
// the sweep calls it again for whatever a crash left behind.
func DiscardRelay(stagingDir, transferID string) error {
	if err := os.RemoveAll(RelayDir(stagingDir, transferID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove relay directory: %w", err)
	}
	return nil
}

// RelayBytes reports how much is currently staged for one transfer, which is
// what the relaying user is shown and what SC-019 is checked against.
func RelayBytes(stagingDir, transferID string) (uint64, error) {
	dir := RelayDir(stagingDir, transferID)

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	var total uint64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue // vanished between the listing and the stat; it is not there
		}
		if info.Size() > 0 {
			total += uint64(info.Size()) //nolint:gosec // a file size is never negative
		}
	}
	return total, nil
}

// RelayResidue reports every transfer identifier still holding staged bytes.
//
// Used by the retention sweep and by the test that holds SC-019: after a
// relayed transfer ends, this must not name it.
func RelayResidue(stagingDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(stagingDir, relayFolder))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, entry.Name())
		}
	}
	return out, nil
}
