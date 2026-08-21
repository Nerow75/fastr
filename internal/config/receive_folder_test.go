package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nerow75/fastr/internal/platform"
)

// FR-023 and its change semantics.
//
// The property worth pinning down is the one users assume wrongly: changing the
// folder does not move anything. Everything else here is refusing paths that
// would quietly widen what a paired device can reach.

// fakePlatform puts every directory under one temporary root, so a test never
// touches the real config or the real receive folder.
type fakePlatform struct {
	root string
}

func (f fakePlatform) OS() platform.OS            { return platform.Current().OS() }
func (f fakePlatform) ConfigDir() (string, error) { return filepath.Join(f.root, "config"), nil }
func (f fakePlatform) DataDir() (string, error)   { return filepath.Join(f.root, "data"), nil }
func (f fakePlatform) DefaultReceiveDir() (string, error) {
	return filepath.Join(f.root, "received"), nil
}
func (f fakePlatform) FreeSpace(string) (uint64, error) { return 1 << 40, nil }
func (f fakePlatform) SetAutostart(bool, string) error  { return nil }
func (f fakePlatform) AutostartEnabled() (bool, error)  { return false, nil }

func newTestStore(t *testing.T) (*Store, fakePlatform) {
	t.Helper()

	p := fakePlatform{root: t.TempDir()}
	st, err := NewStore(p)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	s, err := st.LoadOrInit(p)
	if err != nil {
		t.Fatalf("initialise settings: %v", err)
	}
	if err := s.EnsureFolders(); err != nil {
		t.Fatalf("create folders: %v", err)
	}
	return st, p
}

// The rule users get wrong: previously received files stay where they were.
// FR-025 scenario 5.
func TestChangingTheReceiveFolderLeavesExistingFilesAlone(t *testing.T) {
	st, p := newTestStore(t)

	before := st.Current().ReceiveFolder
	existing := filepath.Join(before, "holiday.mp4")
	if err := os.WriteFile(existing, []byte("already here"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	target := filepath.Join(p.root, "elsewhere")
	change, err := st.ChangeReceiveFolder(target)
	if err != nil {
		t.Fatalf("change: %v", err)
	}

	if !change.Changed() {
		t.Error("the change reported nothing moved")
	}
	if change.PreviousFilesLeftAt != before {
		t.Errorf("previous files reported at %q, want %q", change.PreviousFilesLeftAt, before)
	}
	if !change.Created {
		t.Error("the new folder existed already, which this test did not intend")
	}

	// The file is still where the user last saw it, and was not copied.
	if _, err := os.Stat(existing); err != nil {
		t.Errorf("the existing file moved or vanished: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "holiday.mp4")); !os.IsNotExist(err) {
		t.Error("the existing file was copied into the new folder")
	}

	if got := st.Current().ReceiveFolder; got != filepath.Clean(target) {
		t.Errorf("settings hold %q, want %q", got, target)
	}
}

// Setting it to what it already is succeeds and reports nothing changed, so an
// interface can save unconditionally.
func TestChangingToTheSameFolderIsANoOp(t *testing.T) {
	st, _ := newTestStore(t)

	change, err := st.ChangeReceiveFolder(st.Current().ReceiveFolder)
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if change.Changed() {
		t.Error("setting the folder to itself reported a change")
	}
}

// Staging holds partial and relayed data and must never sit inside the receive
// folder, or a half-arrived file would appear among received ones. FR-055.
//
// A folder that would contain staging is one that contains fastr's own state,
// which means the user named something above what they meant. It is refused
// rather than accommodated by relocating staging: moving partial transfers to a
// path nobody chose, to satisfy a request that was a mistake, is worse than the
// refusal.
func TestAReceiveFolderContainingStagingIsRefused(t *testing.T) {
	st, p := newTestStore(t)

	before := st.Current()
	data, _ := p.DataDir()

	_, err := st.ChangeReceiveFolder(data)
	if err == nil {
		t.Fatalf("%q was accepted, and it contains staging at %q", data, before.StagingFolder)
	}
	if !errors.Is(err, ErrUnsafeReceiveFolder) {
		t.Errorf("refused for the wrong reason: %v", err)
	}

	// A refusal changes nothing.
	if got := st.Current().ReceiveFolder; got != before.ReceiveFolder {
		t.Errorf("the folder moved to %q despite the refusal", got)
	}

	// And the folder the user probably meant, one level in, is accepted.
	inside := filepath.Join(data, "received")
	if _, err := st.ChangeReceiveFolder(inside); err != nil {
		t.Errorf("%q was refused: %v", inside, err)
	}
	if err := st.Current().Validate(); err != nil {
		t.Errorf("the saved settings are invalid: %v", err)
	}
}

func TestUnusableReceiveFoldersAreRefused(t *testing.T) {
	root := t.TempDir()

	file := filepath.Join(root, "not-a-folder.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"relative", "received"},
		{"a file", file},
		{"a parent that does not exist", filepath.Join(root, "no", "such", "parent")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateReceiveFolder(tc.path); err == nil {
				t.Errorf("%q was accepted", tc.path)
			}
		})
	}
}

// A system folder is refused: path confinement holds wherever the folder
// points, but a paired phone writing into one is never what the user meant.
func TestSystemFoldersAreRefused(t *testing.T) {
	for _, path := range systemFolders[platform.Current().OS()] {
		t.Run(path, func(t *testing.T) {
			err := ValidateReceiveFolder(path)
			if err == nil {
				t.Fatalf("%q was accepted as a receive folder", path)
			}
			if !strings.Contains(err.Error(), "system folder") {
				t.Errorf("refused for the wrong reason: %v", err)
			}

			nested := filepath.Join(path, "fastr")
			if err := ValidateReceiveFolder(nested); err == nil {
				t.Errorf("%q was accepted", nested)
			}
		})
	}
}

// A filesystem root would make "only the receive folder is reachable" a promise
// with no content.
func TestFilesystemRootIsRefused(t *testing.T) {
	root := "/"
	if platform.Current().OS() == platform.Windows {
		root = `C:\`
	}
	if err := ValidateReceiveFolder(root); err == nil {
		t.Errorf("%q was accepted as a receive folder", root)
	}
}
