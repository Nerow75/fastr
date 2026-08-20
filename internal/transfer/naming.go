package transfer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nerow75/fastr/internal/platform"
)

// Destination naming, per FR-024 and FR-025.
//
// Two rules, and they are the receiver's alone to apply:
//
//  1. Sanitize against the *destination's* filename rules. A Linux sender has
//     no business deciding what a Windows disk will accept, and vice versa.
//  2. Never overwrite. A collision resolves to a distinct name, and the user
//     is told what changed.

// MaxCollisionAttempts bounds the search for a free name. Reaching it means
// something is wrong with the directory rather than with this transfer.
const MaxCollisionAttempts = 1000

// Resolution is the outcome of placing one incoming file.
type Resolution struct {
	// Path is where the file will finally live.
	Path string
	// StoredName is the base name actually used.
	StoredName string
	// Renamed reports whether the name had to change at all.
	Renamed bool
	// SanitizedFrom is set when the *name* was illegal at the destination, as
	// opposed to merely colliding. The two are reported differently, because
	// they mean different things to the user.
	SanitizedFrom string
	// CollidedWith is set when an existing file forced a distinct name.
	CollidedWith string
}

// Resolve computes where an incoming file goes.
//
// root is the receive folder. relativePath positions the file inside a folder
// send and may be empty. The result is guaranteed to sit inside root: every
// component is sanitized, and the final path is checked against root after
// normalization rather than before, which is the check that actually holds.
func Resolve(root, relativePath, originalName string, rules platform.FilenameRules) (Resolution, error) {
	dir, sanitizedDir, err := resolveDirectory(root, relativePath, rules)
	if err != nil {
		return Resolution{}, err
	}

	stored, renamed := platform.Sanitize(originalName, rules)

	res := Resolution{StoredName: stored, Renamed: renamed || sanitizedDir}
	if renamed {
		res.SanitizedFrom = originalName
	}

	// Find a free name. The directory may not exist yet, in which case nothing
	// can collide and the first candidate wins.
	existing, err := listNames(dir)
	if err != nil {
		return Resolution{}, err
	}

	candidate := stored
	for attempt := 1; attempt <= MaxCollisionAttempts; attempt++ {
		candidate = platform.CollisionName(stored, attempt)
		if !nameTaken(existing, candidate, rules) {
			break
		}
		if attempt == MaxCollisionAttempts {
			return Resolution{}, fmt.Errorf(
				"could not find a free name for %q after %d attempts", stored, MaxCollisionAttempts)
		}
	}

	if candidate != stored {
		res.CollidedWith = stored
		res.Renamed = true
	}
	res.StoredName = candidate
	res.Path = filepath.Join(dir, candidate)

	// The belt-and-braces check. Every component above was sanitized, so this
	// should be unreachable; it is here because "should be unreachable" is
	// exactly the assumption that path traversal bugs are made of.
	if !within(res.Path, root) {
		return Resolution{}, fmt.Errorf("resolved path escapes the receive folder: %q", res.Path)
	}

	return res, nil
}

// resolveDirectory sanitizes each component of a folder-send path.
//
// Components are sanitized individually rather than the path as a whole, so
// "../../etc" becomes a nested directory named after its sanitized parts and
// never a traversal.
func resolveDirectory(root, relativePath string, rules platform.FilenameRules) (dir string, sanitized bool, err error) {
	dir = root
	if relativePath == "" {
		return dir, false, nil
	}

	// Accept either separator: the sender's platform decided which one it used.
	parts := strings.FieldsFunc(relativePath, func(r rune) bool { return r == '/' || r == '\\' })

	for _, part := range parts {
		if part == "." || part == ".." {
			// Not an error: a sender may legitimately produce these through a
			// symlink or a badly built archive. Dropping them is the safe read
			// of the intent, and the file still lands somewhere sensible.
			sanitized = true
			continue
		}
		clean, changed := platform.Sanitize(part, rules)
		if changed {
			sanitized = true
		}
		dir = filepath.Join(dir, clean)
	}

	if !within(dir, root) {
		return "", false, fmt.Errorf("relative path escapes the receive folder: %q", relativePath)
	}
	return dir, sanitized, nil
}

// listNames returns the entries of a directory, or nothing when it does not
// exist yet.
func listNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// nameTaken reports whether a name collides, using the destination's case
// rules. On Windows "Report.txt" and "report.txt" are the same file, and a
// case-sensitive check would pass and then overwrite.
func nameTaken(existing []string, candidate string, rules platform.FilenameRules) bool {
	for _, name := range existing {
		if platform.EqualNames(name, candidate, rules) {
			return true
		}
	}
	return false
}

// within reports whether child is at or below parent, after normalization.
func within(child, parent string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
