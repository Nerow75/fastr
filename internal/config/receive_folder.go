package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nerow75/fastr/internal/platform"
)

// The receive folder and what changing it means, per FR-023.
//
// The folder is the entire filesystem surface this application exposes. Nothing
// outside it is reachable, by anyone, in any mode (FR-018, Principle V), so
// moving it is not a cosmetic setting: it is the one action that changes what a
// paired device can write to. Three rules follow, and they are the whole file.
//
//  1. **It is never widened implicitly.** A path is accepted only if it is
//     absolute, is a directory this user can write to, and does not sit at or
//     above a place where "the receive folder" would mean "most of the disk".
//  2. **Changing it leaves what is already received alone** (FR-025 scenario 5).
//     Nothing is moved, copied, or deleted. Files received yesterday stay where
//     the user last saw them, because a settings change silently relocating a
//     folder of holiday videos is worse than any inconsistency it fixes.
//  3. **Staging follows, and stays separate.** Partial and relayed data must be
//     structurally incapable of appearing among received files (FR-055), so the
//     two directories may never contain one another. Settings.Validate enforces
//     that; ChangeReceiveFolder is what keeps it true across a change.

// ErrUnsafeReceiveFolder reports a folder that would expose more than the user
// can plausibly have intended.
var ErrUnsafeReceiveFolder = errors.New("unsafe receive folder")

// ReceiveFolderChange is the outcome of moving the receive folder.
type ReceiveFolderChange struct {
	// From and To are the old and new folders. They are equal when the request
	// was a no-op, which is not an error.
	From string
	To   string
	// PreviousFilesLeftAt is where already-received files remain. It is From,
	// stated explicitly because it is the part users get wrong: the interface
	// has to say it rather than let them assume a move happened.
	PreviousFilesLeftAt string
	// Created reports that the new folder did not exist and was made.
	Created bool
}

// Changed reports whether anything actually moved.
func (c ReceiveFolderChange) Changed() bool { return c.From != c.To }

// ValidateReceiveFolder reports why a path cannot be the receive folder, or nil.
//
// It is separate from Settings.Validate because it can be called on a candidate
// the user is still typing, before anything is written, which is what lets the
// interface refuse with a reason rather than accept and fail later.
func ValidateReceiveFolder(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return errors.New("receive folder is empty")
	}
	if !filepath.IsAbs(trimmed) {
		return fmt.Errorf("receive folder must be absolute: %q", trimmed)
	}

	clean := filepath.Clean(trimmed)

	// A root, or a filesystem root, would make "only the receive folder is
	// reachable" a promise with no content.
	if clean == filepath.Dir(clean) {
		return fmt.Errorf("%w: %q is a filesystem root", ErrUnsafeReceiveFolder, clean)
	}
	if err := refuseSystemFolder(clean); err != nil {
		return err
	}

	info, err := os.Stat(clean)
	switch {
	case os.IsNotExist(err):
		// Acceptable: it is created on save. But its parent has to exist, or
		// the user has mistyped a path rather than named a new folder.
		parent := filepath.Dir(clean)
		if _, perr := os.Stat(parent); perr != nil {
			return fmt.Errorf("the folder above %q does not exist", clean)
		}
		return nil
	case err != nil:
		return fmt.Errorf("check receive folder: %w", err)
	case !info.IsDir():
		return fmt.Errorf("%q is a file, not a folder", clean)
	}

	return checkWritable(clean)
}

// ChangeReceiveFolder validates a new folder, moves staging along with it when
// staging was derived from the old one, and saves.
//
// It returns what changed so the interface can tell the user where their
// existing files still are. Nothing on disk is moved: see rule 2 above.
func (st *Store) ChangeReceiveFolder(path string) (ReceiveFolderChange, error) {
	if err := ValidateReceiveFolder(path); err != nil {
		return ReceiveFolderChange{}, err
	}

	current := st.Current()
	target := filepath.Clean(strings.TrimSpace(path))

	change := ReceiveFolderChange{
		From:                current.ReceiveFolder,
		To:                  target,
		PreviousFilesLeftAt: current.ReceiveFolder,
	}
	if !change.Changed() {
		return change, nil
	}

	if _, err := os.Stat(target); os.IsNotExist(err) {
		change.Created = true
	}

	next := current
	next.ReceiveFolder = target

	// Refused rather than resolved by moving staging elsewhere. Staging lives
	// under the data directory, so a receive folder that contains it is one that
	// contains fastr's own state — the user has named their home directory, or
	// something above it, and meant a folder inside it. Relocating staging to
	// make that work would leave partial transfers somewhere they did not
	// choose, to satisfy a request that was a mistake.
	if overlaps(next.ReceiveFolder, next.StagingFolder) {
		return ReceiveFolderChange{}, fmt.Errorf(
			"%w: %q contains the folder holding partial transfers (%s); "+
				"choose a folder inside it instead",
			ErrUnsafeReceiveFolder, target, next.StagingFolder)
	}

	if err := next.EnsureFolders(); err != nil {
		return ReceiveFolderChange{}, err
	}
	if err := st.Save(next); err != nil {
		return ReceiveFolderChange{}, err
	}

	return change, nil
}

// systemFolders are the places a receive folder must not be, per platform.
//
// The list is deliberately short. It is not a security boundary — path
// confinement is, and it holds wherever the folder points — but a user who
// types a system path into a settings field has made a mistake, and accepting
// it would mean a paired phone could write into it. FR-023 asks for a default
// outside system folders; refusing to move it into one is the same rule applied
// to the change rather than to the default.
var systemFolders = map[platform.OS][]string{
	platform.Linux: {
		"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64",
		"/proc", "/root", "/sbin", "/sys", "/usr", "/var",
	},
	platform.Windows: {
		`C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`,
		`C:\ProgramData`, `C:\$Recycle.Bin`,
	},
}

// refuseSystemFolder rejects a path at or below a system location.
func refuseSystemFolder(clean string) error {
	rules := platform.Rules()

	for _, root := range systemFolders[platform.Current().OS()] {
		if platform.EqualNames(clean, root, rules) || within(clean, root) {
			return fmt.Errorf("%w: %q is inside the system folder %q",
				ErrUnsafeReceiveFolder, clean, root)
		}
	}
	return nil
}

// overlaps reports whether either path contains the other, or they are the same.
func overlaps(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if platform.EqualNames(a, b, platform.Rules()) {
		return true
	}
	return within(a, b) || within(b, a)
}

// checkWritable confirms the user can actually write here, by writing.
//
// Permissions are not worth predicting from mode bits: they depend on the user,
// the group, the filesystem, and on Windows on an access control list none of
// that describes. Creating and removing a file is the only answer that is true
// on both platforms, and it is what the first transfer would do anyway. Better
// to fail in a settings dialog than at 90% of a 10 GB transfer.
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".fastr-write-check-*")
	if err != nil {
		return fmt.Errorf("cannot write into %q: %w", dir, err)
	}
	name := f.Name()
	_ = f.Close()

	if err := os.Remove(name); err != nil {
		return fmt.Errorf("cannot manage files in %q: %w", dir, err)
	}
	return nil
}
