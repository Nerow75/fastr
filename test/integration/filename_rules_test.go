package integration

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Nerow75/fastr/internal/platform"
)

// Destination naming at the HTTP level, per FR-024 and FR-025, and the path
// confinement of FR-018.
//
// internal/transfer/transfer_test.go already proves these rules at the
// resolution level. What is proved here is different and is the half that
// matters to a user: that a name arriving over the network, from a device whose
// operating system disagrees with this one, reaches the same outcome after
// passing through the endpoints — and that nothing lands outside the receive
// folder on the way.
//
// The rule sets are applied as data rather than by build tag, so a Linux CI
// runner exercises the Windows rules and vice versa. That is deliberate: a
// Linux sender writing to a Windows receiver has to sanitize with the
// destination's rules, so both machines must be able to apply both.

// sendNamed pushes one small file under a given name and returns the stored
// name the server chose.
func (d *device) sendNamed(t *testing.T, target, name string, payload []byte) string {
	t.Helper()

	tr := d.declare(t, target, name, uint64(len(payload)))
	d.uploadOK(t, tr.ID, 0, 0, payload)
	final := d.completeOK(t, tr.ID, 0, digestOf(t, payload))

	return final.Items[0].StoredName
}

// FR-025: an existing file is preserved and the new one takes a distinct name.
// Story 2, scenario 4.
func TestCollisionPreservesTheExistingFile(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	original := []byte("the file that was already there")
	if err := os.WriteFile(filepath.Join(h.receiveDir, "report.pdf"), original, 0o644); err != nil {
		t.Fatalf("seed the receive folder: %v", err)
	}

	incoming := []byte("the file that arrived second")
	stored := phone.sendNamed(t, h.selfID, "report.pdf", incoming)

	if stored == "report.pdf" {
		t.Fatal("the incoming file took the existing name")
	}

	// The original is untouched.
	got, err := os.ReadFile(filepath.Join(h.receiveDir, "report.pdf"))
	if err != nil {
		t.Fatalf("read the original: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Error("the existing file was overwritten")
	}

	// And the new one is there under its distinct name.
	got, err = os.ReadFile(filepath.Join(h.receiveDir, stored))
	if err != nil {
		t.Fatalf("read the incoming file, stored as %q: %v", stored, err)
	}
	if !bytes.Equal(got, incoming) {
		t.Errorf("%q does not hold what was sent", stored)
	}
}

// Repeated collisions keep resolving rather than piling onto one name.
func TestRepeatedCollisionsEachGetTheirOwnName(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	seen := map[string]bool{}
	for i := range 4 {
		payload := []byte(fmt.Sprintf("copy %d", i))
		stored := phone.sendNamed(t, h.selfID, "photo.jpg", payload)

		if seen[stored] {
			t.Fatalf("%q was chosen twice", stored)
		}
		seen[stored] = true

		got, err := os.ReadFile(filepath.Join(h.receiveDir, stored))
		if err != nil {
			t.Fatalf("read %s: %v", stored, err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("%s holds %q, want %q", stored, got, payload)
		}
	}

	entries, err := os.ReadDir(h.receiveDir)
	if err != nil {
		t.Fatalf("read receive folder: %v", err)
	}
	if len(entries) != 4 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		t.Errorf("got %d files, want 4: %v", len(entries), names)
	}
}

// FR-024: a name the destination cannot store is sanitized, and the change is
// reported rather than left for the user to discover in a folder listing.
// Story 2, scenario 5.
func TestWindowsReservedNamesAreSanitizedAndReported(t *testing.T) {
	h := newHarness(t)
	// This machine answers as a Windows destination. The rules are data, so a
	// Linux runner exercises them exactly as a Windows one would.
	h.transfers.Rules = platform.RulesFor(platform.Windows)

	phone := h.pair()

	cases := []struct {
		name   string
		reason string
	}{
		{"CON", "a reserved device name"},
		{"con.txt", "a reserved device name with an extension"},
		{"NUL.log", "a reserved device name"},
		{"report:final.txt", "a colon, which Windows reads as a stream separator"},
		{"what?.txt", "a wildcard character"},
		{`back\slash.txt`, "a path separator"},
		{"trailing.", "a trailing dot, which Windows silently strips"},
		{"trailing space ", "a trailing space, likewise"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte("content of " + tc.name)
			stored := phone.sendNamed(t, h.selfID, tc.name, payload)

			if stored == tc.name {
				t.Fatalf("%q was stored unchanged, despite %s", tc.name, tc.reason)
			}
			if stored == "" {
				t.Fatal("sanitizing produced an empty name")
			}

			path := filepath.Join(h.receiveDir, stored)
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %q: %v", stored, err)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("%q holds the wrong content", stored)
			}

			// However it was renamed, it landed inside the receive folder.
			assertInside(t, h.receiveDir, path)
		})
	}
}

// The same names are legal on Linux and must survive unchanged there, or
// sanitizing would be corrupting names for no reason on the platform that
// accepts them. Principle IV: the difference is the destination's, not a
// behavioural divergence.
//
// This one direction cannot be exercised on both hosts. The rule sets are data,
// so a Linux runner can apply the Windows rules and watch a name be sanitized
// into something both platforms accept. The reverse is not symmetrical: applying
// the Linux rules on Windows produces a name the actual filesystem refuses to
// create, and the test would be measuring NTFS rather than this code. So it runs
// where the disk agrees with the rules being applied, and the Windows rules are
// covered above from either host.
func TestNamesLegalOnLinuxAreStoredUnchanged(t *testing.T) {
	if platform.Current().OS() != platform.Linux {
		t.Skip("the Linux rule set can only be stored on a filesystem that accepts it")
	}

	h := newHarness(t)
	h.transfers.Rules = platform.RulesFor(platform.Linux)

	phone := h.pair()

	for _, name := range []string{"CON", "report:final.txt", "what?.txt", "café ☕.txt"} {
		t.Run(name, func(t *testing.T) {
			payload := []byte("content")
			if stored := phone.sendNamed(t, h.selfID, name, payload); stored != name {
				t.Errorf("stored as %q, want %q unchanged", stored, name)
			}
		})
	}
}

// FR-018: a name or a relative path built to escape the receive folder never
// does. The resolution level already proves this; what is proved here is that
// nothing between the endpoint and the disk reintroduces the possibility.
func TestHostileNamesNeverEscapeTheReceiveFolder(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	// A sibling directory, so an escape lands somewhere observable rather than
	// somewhere the test cannot see.
	outside := filepath.Join(filepath.Dir(h.receiveDir), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("create the sibling: %v", err)
	}

	cases := []struct{ name, relative string }{
		{"../escaped.txt", ""},
		{"../../escaped.txt", ""},
		{"escaped.txt", "../outside"},
		{"escaped.txt", "../../outside"},
		{"escaped.txt", "..\\..\\outside"},
		{"escaped.txt", "trip/../../outside"},
		{"/etc/passwd", ""},
		{`..\..\escaped.txt`, ""},
		{"....//escaped.txt", ""},
	}

	payload := []byte("this must not leave the receive folder")

	for _, tc := range cases {
		t.Run(tc.name+"|"+tc.relative, func(t *testing.T) {
			resp := phone.do("POST", "/api/transfers", map[string]any{
				"target_device_id": h.selfID,
				"items": []map[string]any{{
					"name":          tc.name,
					"relative_path": tc.relative,
					"size":          len(payload),
				}},
			})

			// Either the declaration is refused outright, or it is accepted and
			// the file lands inside. Both are correct; escaping is not.
			if resp.StatusCode != http.StatusCreated {
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				t.Logf("declaration refused, which is a valid answer: %s", raw)
				return
			}

			var tr declaredTransfer
			phone.open("POST", "/api/transfers", resp, &tr)

			phone.uploadOK(t, tr.ID, 0, 0, payload)
			phone.completeOK(t, tr.ID, 0, digestOf(t, payload))

			assertNothingUnder(t, outside)
			assertOnlyUnder(t, h.receiveDir, payload)
		})
	}
}

// assertInside fails when path is not at or below root, after normalization.
func assertInside(t *testing.T, root, path string) {
	t.Helper()

	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == ".." || filepath.IsAbs(rel) ||
		(len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator)) {
		t.Fatalf("%q is not inside %q (relative: %q, %v)", path, root, rel, err)
	}
}

// assertNothingUnder fails when anything was written into a directory that
// should have stayed empty.
func assertNothingUnder(t *testing.T, dir string) {
	t.Helper()

	var found []string
	_ = filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil //nolint:nilerr // a missing directory is the passing case
		}
		found = append(found, path)
		return nil
	})

	if len(found) > 0 {
		t.Fatalf("a file escaped the receive folder: %v", found)
	}
}

// assertOnlyUnder fails when the payload landed anywhere but inside root.
func assertOnlyUnder(t *testing.T, root string, payload []byte) {
	t.Helper()

	var landed bool
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is not what is being tested
		}
		got, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(got, payload) {
			landed = true
			assertInside(t, root, path)
		}
		return nil
	})

	if !landed {
		t.Error("the file was accepted but is nowhere inside the receive folder")
	}
}
