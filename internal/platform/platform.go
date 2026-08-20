// Package platform isolates everything that differs between Linux and Windows.
//
// Principle IV requires identical behavior on both, verified in CI. That is only
// achievable if the differences live in one place instead of leaking into domain
// logic. Two rules follow from that:
//
//  1. Filename rules are data, not build tags. A Linux machine must be able to
//     apply the Windows rule set, because a Linux sender writing to a Windows
//     receiver has to sanitize using the *destination's* rules, and because the
//     parity tests must exercise both rule sets on whichever host CI runs.
//  2. Only things that genuinely need a syscall are guarded by build tags.
package platform

import (
	"fmt"
	"strings"
	"unicode"
)

// OS identifies a destination platform. It is the destination's rules that
// apply to an incoming filename, never the sender's.
type OS string

const (
	Linux   OS = "linux"
	Windows OS = "windows"
)

// FilenameRules describes what a platform will accept as a filename.
type FilenameRules struct {
	// ForbiddenRunes may not appear anywhere in a name.
	ForbiddenRunes string
	// ReservedNames may not be used as a base name, with or without extension.
	ReservedNames []string
	// ForbidTrailingDotOrSpace rejects names ending in '.' or ' '.
	ForbidTrailingDotOrSpace bool
	// MaxNameBytes caps a single path component.
	MaxNameBytes int
	// CaseInsensitive means "Report.txt" and "report.txt" collide.
	CaseInsensitive bool
}

// RulesFor returns the rule set of a destination platform.
func RulesFor(os OS) FilenameRules {
	switch os {
	case Windows:
		return FilenameRules{
			// Windows rejects these outright, plus every control character.
			ForbiddenRunes: `<>:"/\|?*`,
			ReservedNames: []string{
				"CON", "PRN", "AUX", "NUL",
				"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
				"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
			},
			ForbidTrailingDotOrSpace: true,
			MaxNameBytes:             255,
			CaseInsensitive:          true,
		}
	default:
		return FilenameRules{
			// A path separator and NUL are the only hard prohibitions.
			ForbiddenRunes:  "/",
			MaxNameBytes:    255,
			CaseInsensitive: false,
		}
	}
}

// Replacement stands in for a rune the destination will not accept. Chosen
// because it is legal on both platforms and visually signals a substitution.
const Replacement = '_'

// Sanitize returns a name the destination can store, and whether it had to
// change. FR-024 requires preserving the original wherever it is usable, and
// reporting the difference whenever it is not.
//
// It never returns a name containing a path separator, and never returns "",
// "." or "..", so the result cannot escape a directory no matter what arrived.
func Sanitize(name string, rules FilenameRules) (string, bool) {
	original := name

	// Strip any directory component the sender may have smuggled in. A name is
	// a name; paths are carried separately and validated separately.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r == 0 || unicode.IsControl(r):
			b.WriteRune(Replacement)
		case strings.ContainsRune(rules.ForbiddenRunes, r):
			b.WriteRune(Replacement)
		default:
			b.WriteRune(r)
		}
	}
	name = b.String()

	if rules.ForbidTrailingDotOrSpace {
		name = strings.TrimRight(name, ". ")
	}

	if isReserved(name, rules.ReservedNames) {
		name = "_" + name
	}

	name = truncateBytes(name, rules.MaxNameBytes)

	// Everything above can produce an empty or dot-only name from a hostile
	// input such as "..", "/" or "   ".
	if name == "" || name == "." || name == ".." {
		name = "unnamed"
	}

	return name, name != original
}

// isReserved reports whether the base name, ignoring extension and case, is a
// device name the platform reserves.
func isReserved(name string, reserved []string) bool {
	if len(reserved) == 0 {
		return false
	}
	base := name
	if i := strings.IndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	base = strings.ToUpper(strings.TrimSpace(base))
	for _, r := range reserved {
		if base == r {
			return true
		}
	}
	return false
}

// truncateBytes shortens a name to fit a byte budget without splitting a rune,
// preserving the extension so the file still opens with the right application.
func truncateBytes(name string, max int) string {
	if max <= 0 || len(name) <= max {
		return name
	}

	ext := ""
	if i := strings.LastIndexByte(name, '.'); i > 0 && len(name)-i <= 16 {
		ext = name[i:]
	}

	budget := max - len(ext)
	if budget <= 0 {
		ext, budget = "", max
	}

	stem := name[:len(name)-len(ext)]
	for len(stem) > budget {
		_, size := decodeLastRune(stem)
		stem = stem[:len(stem)-size]
	}
	return stem + ext
}

func decodeLastRune(s string) (rune, int) {
	if s == "" {
		return 0, 0
	}
	runes := []rune(s)
	last := runes[len(runes)-1]
	return last, len(string(last))
}

// CollisionName produces the nth alternative for a name that already exists,
// as "report (2).pdf". FR-025 forbids overwriting silently.
func CollisionName(name string, n int) string {
	if n < 2 {
		return name
	}
	ext := ""
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		ext = name[i:]
	}
	stem := name[:len(name)-len(ext)]
	return fmt.Sprintf("%s (%d)%s", stem, n, ext)
}

// EqualNames compares two names using the destination's case sensitivity, so a
// collision check on Windows catches "Report.txt" against "report.txt".
func EqualNames(a, b string, rules FilenameRules) bool {
	if rules.CaseInsensitive {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Platform exposes what genuinely needs a syscall or an OS convention.
// Obtain the running one with Current().
type Platform interface {
	// OS identifies the running platform.
	OS() OS
	// ConfigDir is where settings live. Never the install directory.
	ConfigDir() (string, error)
	// DataDir is where the store and staging live.
	DataDir() (string, error)
	// DefaultReceiveDir is the initial receive folder, outside system folders.
	DefaultReceiveDir() (string, error)
	// FreeSpace reports bytes available to this user at path.
	FreeSpace(path string) (uint64, error)
	// SetAutostart enables or disables starting with the user's session.
	SetAutostart(enabled bool, executable string) error
	// AutostartEnabled reports the current setting.
	AutostartEnabled() (bool, error)
}

// Rules returns the running platform's filename rules.
func Rules() FilenameRules { return RulesFor(Current().OS()) }
