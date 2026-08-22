package integration

import (
	"crypto/rand"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nerow75/fastr/internal/store"
)

// Corruption, per FR-032, FR-033 and SC-007.
//
// The one outcome this project cannot afford is a file reported as received
// that is not the file that was sent. Everything else — a refusal, a failure, a
// transfer that has to start again — is recoverable by trying again; a silently
// wrong file is not, because nobody re-checks a transfer that said it worked.
//
// The end-to-end digest is what makes that impossible, and these tests are
// about the aftermath: what the user is told, what is left on disk, and whether
// a failed transfer can be talked back into life.

// A resumed transfer whose bytes changed in the middle still fails. The digest
// is built across every chunk and every connection, so there is no seam a wrong
// byte can hide in.
func TestCorruptionAcrossAResumeIsStillCaught(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := make([]byte, 96<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	tr := phone.declare(t, h.selfID, "flipped.bin", uint64(len(payload)))

	const boundary = 32 << 10
	phone.uploadOK(t, tr.ID, 0, 0, payload[:boundary])

	// One byte, in the middle of the second chunk, of a file arriving over
	// three requests: the smallest corruption this design has to catch.
	damaged := append([]byte(nil), payload[boundary:]...)
	damaged[len(damaged)/2] ^= 0x01
	phone.uploadOK(t, tr.ID, 0, boundary, damaged)

	resp := phone.completeItem(t, tr.ID, 0, digestOf(t, payload))
	if resp.StatusCode != http.StatusUnprocessableEntity {
		resp.Body.Close()
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	if body := errorBody(t, resp); body["error"] != "checksum_mismatch" {
		t.Errorf("error = %v, want checksum_mismatch", body["error"])
	}

	assertNothingLeftBehind(t, h, "flipped.bin")
}

// FR-037 and FR-038: the failure is recorded with a cause, so the history says
// what went wrong rather than that something did.
func TestAChecksumFailureIsRecordedWithItsCause(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("what was actually sent")
	tr := phone.declare(t, h.selfID, "recorded.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload)

	resp := phone.completeItem(t, tr.ID, 0, digestOf(t, []byte("something else")))
	resp.Body.Close()

	entries, err := h.store.History(10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	var found *store.HistoryEntry
	for i := range entries {
		if entries[i].TransferID.String() == tr.ID {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("the failed transfer left no history entry; %d entries exist", len(entries))
	}

	if found.Outcome != store.OutcomeFailed {
		t.Errorf("outcome = %q, want failed", found.Outcome)
	}
	if found.FailureCause != store.CauseChecksumMismatch {
		t.Errorf("cause = %q, want checksum_mismatch", found.FailureCause)
	}
	// The device it came from, not just an identifier: the entry is something a
	// person reads.
	if found.PeerName == "" {
		t.Error("the history entry names no device")
	}
}

// A failed transfer is over. Without this, a sender could keep pushing after a
// mismatch and eventually place a file the server has already judged corrupt.
func TestAFailedTransferCannotBeRevived(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("the original bytes")
	tr := phone.declare(t, h.selfID, "revived.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload)

	resp := phone.completeItem(t, tr.ID, 0, digestOf(t, []byte("a different file")))
	resp.Body.Close()

	if got := phone.transferState(t, tr.ID).State; got != "failed" {
		t.Fatalf("state = %q, want failed", got)
	}

	// Appending again must not restart it.
	again := phone.upload(t, tr.ID, 0, 0, payload)
	again.Body.Close()
	if again.StatusCode == http.StatusOK {
		t.Error("a failed transfer accepted more data")
	}

	// And neither must completing it a second time, this time honestly.
	honest := phone.completeItem(t, tr.ID, 0, digestOf(t, payload))
	honest.Body.Close()
	if honest.StatusCode == http.StatusOK {
		t.Error("a failed transfer was completed on a second attempt")
	}

	assertNothingLeftBehind(t, h, "revived.bin")
}

// SC-007: a transfer reported as failed leaves nothing that could be mistaken
// for a complete file — not at the destination, and not in staging either.
func assertNothingLeftBehind(t *testing.T, h *harness, name string) {
	t.Helper()

	if _, err := os.Stat(filepath.Join(h.receiveDir, name)); !os.IsNotExist(err) {
		t.Errorf("%s exists in the receive folder after a failure: %v", name, err)
	}

	entries, err := os.ReadDir(h.receiveDir)
	if err != nil {
		t.Fatalf("read receive folder: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the receive folder holds %d entries after a failure", len(entries))
	}

	staged, err := os.ReadDir(h.stagingDir)
	if err != nil {
		t.Fatalf("read staging folder: %v", err)
	}
	for _, e := range staged {
		t.Errorf("partial data survived a failure: %s", e.Name())
	}
}
