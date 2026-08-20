package integration

import (
	"strings"
	"testing"

	"github.com/Nerow75/fastr/internal/platform"
)

// Principle IV: identical behavior on both platforms, verified in CI. These
// tests run the *Windows* rule set on whatever host CI happens to use, and the
// Linux rule set likewise, so a Linux-only developer cannot break Windows
// silently. That is the whole reason filename rules are data rather than build
// tags.

func TestSanitizeAppliesDestinationRules(t *testing.T) {
	win := platform.RulesFor(platform.Windows)
	lin := platform.RulesFor(platform.Linux)

	cases := []struct {
		name      string
		input     string
		rules     platform.FilenameRules
		want      string
		changed   bool
		rulesName string
	}{
		// A reserved device name is legal on Linux and refused by Windows.
		{"reserved base", "aux.txt", win, "_aux.txt", true, "windows"},
		{"reserved bare", "CON", win, "_CON", true, "windows"},
		{"reserved lowercase", "nul.log", win, "_nul.log", true, "windows"},
		{"reserved is fine on linux", "aux.txt", lin, "aux.txt", false, "linux"},

		// Trailing dots and spaces are silently stripped by Windows itself,
		// which is worse than refusing: the file ends up under a name nobody
		// asked for. We do it explicitly and report it.
		{"trailing dot", "report.", win, "report", true, "windows"},
		{"trailing space", "report ", win, "report", true, "windows"},
		{"trailing dot ok on linux", "report.", lin, "report.", false, "linux"},

		// Characters Windows forbids outright.
		{"colon", `12:30 notes.txt`, win, "12_30 notes.txt", true, "windows"},
		{"pipe and question", "a|b?c.txt", win, "a_b_c.txt", true, "windows"},
		{"colon ok on linux", "12:30 notes.txt", lin, "12:30 notes.txt", false, "linux"},

		// A name is a name. Any directory component is stripped before use,
		// on both platforms, so a hostile sender cannot smuggle a path in.
		{"unix path", "../../etc/passwd", lin, "passwd", true, "linux"},
		{"windows path", `..\..\Windows\System32\cmd.exe`, win, "cmd.exe", true, "windows"},
		{"bare dotdot", "..", lin, "unnamed", true, "linux"},
		{"bare dot", ".", lin, "unnamed", true, "linux"},
		{"bare slash", "/", lin, "unnamed", true, "linux"},

		// Control characters are never acceptable anywhere.
		{"newline", "a\nb.txt", lin, "a_b.txt", true, "linux"},
		{"nul", "a\x00b.txt", lin, "a_b.txt", true, "linux"},

		// Names that are already fine must survive untouched. FR-024 requires
		// preserving the original wherever it is usable.
		{"plain", "holiday video.mp4", win, "holiday video.mp4", false, "windows"},
		{"accented", "été à Paris.jpg", win, "été à Paris.jpg", false, "windows"},
		{"non latin", "写真.png", lin, "写真.png", false, "linux"},
		{"no extension", "LICENSE", lin, "LICENSE", false, "linux"},
		{"leading dot", ".bashrc", lin, ".bashrc", false, "linux"},
	}

	for _, tc := range cases {
		t.Run(tc.rulesName+"/"+tc.name, func(t *testing.T) {
			got, changed := platform.Sanitize(tc.input, tc.rules)
			if got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if changed != tc.changed {
				t.Errorf("Sanitize(%q) changed = %v, want %v", tc.input, changed, tc.changed)
			}
		})
	}
}

// A sanitized name must never be usable to escape a directory, whatever
// arrived. This is the invariant the write path depends on.
func TestSanitizeNeverEscapes(t *testing.T) {
	hostile := []string{
		"../secret", "../../../../etc/shadow", `..\..\secret`,
		"..", ".", "", "   ", "...", "./../.", "/etc/passwd",
		`C:\Windows\System32\config\SAM`, "a/../../b",
	}

	for _, os := range []platform.OS{platform.Linux, platform.Windows} {
		rules := platform.RulesFor(os)
		for _, in := range hostile {
			got, _ := platform.Sanitize(in, rules)
			if strings.ContainsAny(got, `/\`) {
				t.Errorf("%s: Sanitize(%q) = %q contains a separator", os, in, got)
			}
			if got == "" || got == "." || got == ".." {
				t.Errorf("%s: Sanitize(%q) = %q is not a usable name", os, in, got)
			}
		}
	}
}

func TestSanitizeTruncatesKeepingExtension(t *testing.T) {
	rules := platform.RulesFor(platform.Windows)
	long := strings.Repeat("a", 400) + ".mp4"

	got, changed := platform.Sanitize(long, rules)
	if !changed {
		t.Fatal("a 404-byte name must be reported as changed")
	}
	if len(got) > rules.MaxNameBytes {
		t.Errorf("len = %d, want <= %d", len(got), rules.MaxNameBytes)
	}
	if !strings.HasSuffix(got, ".mp4") {
		t.Errorf("extension lost: %q", got)
	}
}

// Truncation must not split a multi-byte rune, which would produce an invalid
// name and, on some filesystems, an unopenable file.
func TestSanitizeTruncatesOnRuneBoundary(t *testing.T) {
	rules := platform.RulesFor(platform.Linux)
	long := strings.Repeat("é", 300) + ".txt"

	got, _ := platform.Sanitize(long, rules)
	if len(got) > rules.MaxNameBytes {
		t.Errorf("len = %d, want <= %d", len(got), rules.MaxNameBytes)
	}
	if !isValidUTF8(got) {
		t.Errorf("truncation split a rune: %q", got)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

func TestCollisionNaming(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want string
	}{
		{"report.pdf", 1, "report.pdf"},
		{"report.pdf", 2, "report (2).pdf"},
		{"report.pdf", 10, "report (10).pdf"},
		{"LICENSE", 2, "LICENSE (2)"},
		{"archive.tar.gz", 2, "archive.tar (2).gz"},
	}
	for _, tc := range cases {
		if got := platform.CollisionName(tc.name, tc.n); got != tc.want {
			t.Errorf("CollisionName(%q, %d) = %q, want %q", tc.name, tc.n, got, tc.want)
		}
	}
}

// A Windows destination must treat "Report.txt" and "report.txt" as the same
// file, or a collision check would pass and the write would overwrite.
func TestNameEqualityFollowsDestinationCaseRules(t *testing.T) {
	win := platform.RulesFor(platform.Windows)
	lin := platform.RulesFor(platform.Linux)

	if !platform.EqualNames("Report.txt", "report.txt", win) {
		t.Error("windows: case-differing names must collide")
	}
	if platform.EqualNames("Report.txt", "report.txt", lin) {
		t.Error("linux: case-differing names are distinct files")
	}
}

// The running platform must report rules consistent with its own identity.
func TestCurrentPlatformIsCoherent(t *testing.T) {
	p := platform.Current()
	if p.OS() != platform.Linux && p.OS() != platform.Windows {
		t.Fatalf("unexpected platform %q", p.OS())
	}

	for _, dir := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"ConfigDir", p.ConfigDir},
		{"DataDir", p.DataDir},
		{"DefaultReceiveDir", p.DefaultReceiveDir},
	} {
		got, err := dir.fn()
		if err != nil {
			t.Errorf("%s: %v", dir.name, err)
			continue
		}
		if got == "" {
			t.Errorf("%s returned an empty path", dir.name)
		}
	}
}

// Free space must be reported for a folder that does not exist yet, because it
// is checked before the receive folder is created. FR-028.
func TestFreeSpaceOnMissingDirectory(t *testing.T) {
	p := platform.Current()
	missing := t.TempDir() + "/not/created/yet"

	free, err := p.FreeSpace(missing)
	if err != nil {
		t.Fatalf("FreeSpace on a missing path must resolve an ancestor: %v", err)
	}
	if free == 0 {
		t.Error("FreeSpace returned 0 on a temp directory")
	}
}
