package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Nerow75/fastr/internal/platform"
)

// --- streaming ---------------------------------------------------------------

func TestCopyMovesBytesAndHashes(t *testing.T) {
	payload := bytes.Repeat([]byte("fastr"), 10_000)

	var dst bytes.Buffer
	hasher, err := NewHasher()
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}

	//nolint:gosec // a test payload length is never negative
	res, err := Copy(t.Context(), &dst, bytes.NewReader(payload), hasher, uint64(len(payload)), 0, nil)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}

	if res.Written != uint64(len(payload)) {
		t.Errorf("written = %d, want %d", res.Written, len(payload))
	}
	if !bytes.Equal(dst.Bytes(), payload) {
		t.Error("the destination does not match the source")
	}
	if res.Partial {
		t.Error("a complete copy was reported as partial")
	}

	// The digest must match one taken separately over the same bytes.
	reference, _ := NewHasher()
	_, _ = reference.Write(payload)
	if !bytes.Equal(hasher.Sum(nil), reference.Sum(nil)) {
		t.Error("the streamed digest disagrees with a separate pass")
	}
}

// SC-003, and the promise the whole product rests on: memory does not grow with
// file size. The comparison here is between a small copy and one two thousand
// times larger, through the same engine.
func TestCopyMemoryDoesNotGrowWithFileSize(t *testing.T) {
	measure := func(size int) uint64 {
		t.Helper()

		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)

		//nolint:gosec // a test size is never negative
		src := &zeroReader{remaining: uint64(size)}
		hasher, _ := NewHasher()
		//nolint:gosec // a test size is never negative
		if _, err := Copy(t.Context(), io.Discard, src, hasher, uint64(size), 0, nil); err != nil {
			t.Fatalf("Copy: %v", err)
		}

		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}

	small := measure(1 << 20)   // 1 MB
	large := measure(512 << 20) // 512 MB, 512 times more data

	// Allocation should be dominated by the fixed buffer and the hasher, not
	// by the payload. A generous ceiling still catches any per-byte allocation:
	// a naive implementation would allocate 512 MB more here.
	if large > small+(8<<20) {
		t.Errorf("allocation grew with file size: %d bytes for 1 MB, %d bytes for 512 MB", small, large)
	}
}

func TestCopyResumesFromAnOffset(t *testing.T) {
	whole := bytes.Repeat([]byte("abcdefgh"), 1000)
	const cut = 3000

	// First half, hashed.
	hasher, _ := NewHasher()
	var first bytes.Buffer
	//nolint:gosec // test lengths
	if _, err := Copy(t.Context(), &first, bytes.NewReader(whole[:cut]), hasher, uint64(len(whole)), 0, nil); err != nil {
		t.Fatalf("first half: %v", err)
	}

	// Second half, continuing the same hash state, which is what a resume does.
	var second bytes.Buffer
	//nolint:gosec // test lengths
	res, err := Copy(t.Context(), &second, bytes.NewReader(whole[cut:]), hasher, uint64(len(whole)), cut, nil)
	if err != nil {
		t.Fatalf("second half: %v", err)
	}

	if res.Written != uint64(len(whole)-cut) {
		t.Errorf("written = %d, want %d", res.Written, len(whole)-cut)
	}

	joined := append(first.Bytes(), second.Bytes()...)
	if !bytes.Equal(joined, whole) {
		t.Error("the resumed copy does not reconstruct the source")
	}

	reference, _ := NewHasher()
	_, _ = reference.Write(whole)
	if !bytes.Equal(hasher.Sum(nil), reference.Sum(nil)) {
		t.Error("the resumed digest disagrees with a single-pass digest")
	}
}

func TestCopyStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	src := &zeroReader{remaining: 100 << 20}
	res, err := Copy(ctx, io.Discard, src, nil, 100<<20, 0, nil)

	if !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
	if !res.Partial {
		t.Error("a cancelled copy must be reported as partial")
	}
}

func TestCopyReportsProgress(t *testing.T) {
	var updates []Progress
	src := &zeroReader{remaining: 16 << 20}

	//nolint:gosec // test size
	_, err := Copy(t.Context(), io.Discard, src, nil, 16<<20, 0, func(p Progress) {
		updates = append(updates, p)
	})
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}

	if len(updates) == 0 {
		t.Fatal("no progress was reported")
	}

	final := updates[len(updates)-1]
	if final.Transferred != 16<<20 {
		t.Errorf("final transferred = %d, want %d", final.Transferred, 16<<20)
	}
	// Progress must be monotonic, or a progress bar goes backwards.
	for i := 1; i < len(updates); i++ {
		if updates[i].Transferred < updates[i-1].Transferred {
			t.Fatalf("progress went backwards: %d then %d",
				updates[i-1].Transferred, updates[i].Transferred)
		}
	}
}

// --- the staged write path ---------------------------------------------------

func TestStagedFileOnlyAppearsAfterVerification(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	final := filepath.Join(dir, "received", "video.mp4")

	staged, err := CreateStaged(staging, final, "item-1")
	if err != nil {
		t.Fatalf("CreateStaged: %v", err)
	}

	payload := []byte("the whole file")
	if _, err := staged.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Mid-transfer, nothing may exist at the final path.
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatal("the file appeared at its final path before verification")
	}

	hasher, _ := NewHasher()
	_, _ = hasher.Write(payload)
	digest := hasher.Sum(nil)

	if err := staged.Commit(digest, digest); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("the committed file does not match what was written")
	}
	if _, err := os.Stat(staged.StagingPath); !os.IsNotExist(err) {
		t.Error("the staging file survived a successful commit")
	}
}

// FR-032 and FR-033: a corrupted file is reported as failed and never presented
// as usable.
func TestChecksumMismatchDiscardsTheFile(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	final := filepath.Join(dir, "received", "video.mp4")

	staged, err := CreateStaged(staging, final, "item-1")
	if err != nil {
		t.Fatalf("CreateStaged: %v", err)
	}
	if _, err := staged.Write([]byte("corrupted in transit")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	hasher, _ := NewHasher()
	_, _ = hasher.Write([]byte("what the sender actually had"))
	expected := hasher.Sum(nil)

	computed, _ := NewHasher()
	_, _ = computed.Write([]byte("corrupted in transit"))

	err = staged.Commit(computed.Sum(nil), expected)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Commit = %v, want ErrChecksumMismatch", err)
	}

	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Error("a corrupted file reached its final path")
	}
	if _, err := os.Stat(staged.StagingPath); !os.IsNotExist(err) {
		t.Error("the corrupted staging file was left behind")
	}
}

func TestStagedFileResumesFromWhatIsOnDisk(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	final := filepath.Join(dir, "received", "big.bin")

	staged, err := CreateStaged(staging, final, "item-1")
	if err != nil {
		t.Fatalf("CreateStaged: %v", err)
	}
	if _, err := staged.Write([]byte("first half ")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := staged.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A restart: the offset must come from the file, not from anything
	// remembered in memory.
	reopened, offset, err := OpenStaged(staging, final, "item-1")
	if err != nil {
		t.Fatalf("OpenStaged: %v", err)
	}
	if offset != uint64(len("first half ")) {
		t.Fatalf("offset = %d, want %d", offset, len("first half "))
	}

	if _, err := reopened.Write([]byte("second half")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	whole := []byte("first half second half")
	hasher, _ := NewHasher()
	_, _ = hasher.Write(whole)
	digest := hasher.Sum(nil)

	if err := reopened.Commit(digest, digest); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, _ := os.ReadFile(final)
	if !bytes.Equal(got, whole) {
		t.Errorf("resumed file is %q, want %q", got, whole)
	}
}

func TestDiscardRemovesStagedData(t *testing.T) {
	dir := t.TempDir()
	staged, err := CreateStaged(filepath.Join(dir, "staging"), filepath.Join(dir, "f"), "item-1")
	if err != nil {
		t.Fatalf("CreateStaged: %v", err)
	}
	_, _ = staged.Write([]byte("partial"))

	if err := staged.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(staged.StagingPath); !os.IsNotExist(err) {
		t.Error("Discard left the staging file behind")
	}
	// Discarding twice must not fail: cleanup paths run more than once.
	if err := staged.Discard(); err != nil {
		t.Errorf("second Discard: %v", err)
	}
}

// --- naming ------------------------------------------------------------------

// FR-018: nothing a sender says can place a file outside the receive folder.
func TestResolveNeverEscapesTheReceiveFolder(t *testing.T) {
	root := t.TempDir()

	hostile := []struct{ relPath, name string }{
		{"", "../escape.txt"},
		{"..", "file.txt"},
		{"../..", "file.txt"},
		{"a/../../..", "file.txt"},
		{"/etc", "passwd"},
		{`..\..\Windows`, "cmd.exe"},
		{"a/b/../../../..", "file.txt"},
		{"", "/etc/passwd"},
		{"", `C:\Windows\System32\SAM`},
	}

	for _, os := range []platform.OS{platform.Linux, platform.Windows} {
		rules := platform.RulesFor(os)
		for _, tc := range hostile {
			res, err := Resolve(root, tc.relPath, tc.name, rules)
			if err != nil {
				continue // refusing outright is also a correct answer
			}
			if !within(res.Path, root) {
				t.Errorf("%s: Resolve(%q, %q) escaped to %q", os, tc.relPath, tc.name, res.Path)
			}
		}
	}
}

// FR-025: an existing file is never overwritten.
func TestResolveNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	rules := platform.RulesFor(platform.Linux)

	if err := os.WriteFile(filepath.Join(root, "report.pdf"), []byte("original"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := Resolve(root, "", "report.pdf", rules)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.StoredName == "report.pdf" {
		t.Fatal("the resolution would overwrite the existing file")
	}
	if res.StoredName != "report (2).pdf" {
		t.Errorf("stored name = %q, want %q", res.StoredName, "report (2).pdf")
	}
	if !res.Renamed || res.CollidedWith != "report.pdf" {
		t.Errorf("the collision was not reported: %+v", res)
	}

	// The original must be untouched.
	original, _ := os.ReadFile(filepath.Join(root, "report.pdf"))
	if string(original) != "original" {
		t.Error("the existing file was modified")
	}
}

// On Windows a case-differing name is the same file, so the collision check
// must follow the destination's rules rather than the sender's.
func TestResolveHonoursDestinationCaseRules(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Report.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	win, err := Resolve(root, "", "report.txt", platform.RulesFor(platform.Windows))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if win.StoredName == "report.txt" {
		t.Error("windows rules allowed a case-only collision to overwrite")
	}

	lin, err := Resolve(root, "", "report.txt", platform.RulesFor(platform.Linux))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if lin.StoredName != "report.txt" {
		t.Errorf("linux rules renamed a distinct file: %q", lin.StoredName)
	}
}

// FR-024: a name illegal at the destination is stored sanitized, and the change
// is reported so the user can be told.
func TestResolveReportsSanitization(t *testing.T) {
	root := t.TempDir()

	res, err := Resolve(root, "", "aux.txt", platform.RulesFor(platform.Windows))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.StoredName != "_aux.txt" {
		t.Errorf("stored name = %q, want %q", res.StoredName, "_aux.txt")
	}
	if res.SanitizedFrom != "aux.txt" {
		t.Errorf("the sanitization was not reported: %+v", res)
	}
	if !res.Renamed {
		t.Error("Renamed is false after sanitizing")
	}
}

func TestResolvePreservesFolderStructure(t *testing.T) {
	root := t.TempDir()
	rules := platform.RulesFor(platform.Linux)

	res, err := Resolve(root, "holiday/2026/beach", "photo.jpg", rules)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := filepath.Join(root, "holiday", "2026", "beach", "photo.jpg")
	if res.Path != want {
		t.Errorf("path = %q, want %q", res.Path, want)
	}
	if res.Renamed {
		t.Error("a legal path was reported as renamed")
	}
}

// --- free space --------------------------------------------------------------

type fakeSpace uint64

func (f fakeSpace) FreeSpace(string) (uint64, error) { return uint64(f), nil }

func TestCheckSpaceRefusesBeforeAnyBytesMove(t *testing.T) {
	// Enough room for the file but not the headroom: still refused, because a
	// destination filled to the last byte is not a working system.
	err := CheckSpace(fakeSpace(1<<30), "/anywhere", 1<<30)
	var short *ErrInsufficientSpace
	if !errors.As(err, &short) {
		t.Fatalf("err = %v, want ErrInsufficientSpace", err)
	}
	if short.Needed != 1<<30 || short.Available != 1<<30 {
		t.Errorf("the error does not carry both numbers: %+v", short)
	}

	// Comfortably enough.
	if err := CheckSpace(fakeSpace(10<<30), "/anywhere", 1<<30); err != nil {
		t.Errorf("a transfer that fits was refused: %v", err)
	}
}

// A peer declares the size. A hostile or broken one could declare a value near
// the maximum, and adding headroom to it must not wrap and pass.
func TestCheckSpaceHandlesOverflow(t *testing.T) {
	err := CheckSpace(fakeSpace(1<<30), "/anywhere", ^uint64(0))
	var short *ErrInsufficientSpace
	if !errors.As(err, &short) {
		t.Errorf("an overflowing size was accepted: %v", err)
	}
}

func TestCheckSpaceAllowsAnEmptyTransfer(t *testing.T) {
	if err := CheckSpace(fakeSpace(1<<30), "/anywhere", 0); err != nil {
		t.Errorf("a zero-byte transfer was refused: %v", err)
	}
}

// --- helpers -----------------------------------------------------------------

// zeroReader produces a bounded stream without allocating it.
type zeroReader struct{ remaining uint64 }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.remaining == 0 {
		return 0, io.EOF
	}
	n := uint64(len(p))
	if n > z.remaining {
		n = z.remaining
	}
	z.remaining -= n
	return int(n), nil
}
