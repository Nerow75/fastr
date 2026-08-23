package integration

import (
	"bytes"
	"crypto/rand"
	"net/http"
	"os"
	"testing"

	"github.com/Nerow75/fastr/internal/store"
)

// User Story 6: two phones paired to the same computer send to each other
// through it.
//
// This is the one case where a machine holds data that is not its own, and the
// story's independent test is about what is left behind: send a file between
// two phones, then verify staging is empty and nothing appeared in the receive
// folder. SC-019 puts a number on it — zero bytes, whichever way it ended.
//
// It works in two halves, because a phone cannot hold one streaming request
// open for the length of a file. The sender pushes chunks the way it would to
// the computer itself; the bytes wait on disk, verified; the receiver fetches
// them the way it would fetch anything else. The transfer is not finished until
// the second half is.

// relayTo sends a file from one phone to another and returns the transfer.
func relayTo(t *testing.T, from, to *device, name string, payload []byte) declaredTransfer {
	t.Helper()

	tr := from.declare(t, to.ID, name, uint64(len(payload)))
	from.uploadInChunks(t, tr.ID, 0, payload, 64<<10)
	from.completeOK(t, tr.ID, 0, digestOf(t, payload))

	return tr
}

// The story's own acceptance test.
func TestAFileGoesFromOnePhoneToAnotherAndNothingIsLeftBehind(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	payload := make([]byte, 256<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	tr := relayTo(t, sender, receiver, "from-the-visit.jpg", payload)

	// Halfway: the bytes are here and verified, and the transfer is not
	// finished, because the phone they are for has not seen them.
	midway := sender.transferState(t, tr.ID)
	if midway.State == "completed" {
		t.Fatal("the transfer reported completion before the receiver had the file")
	}

	got := receiver.fetch(t, tr.ID, 0, "")
	if got.Status != http.StatusOK {
		t.Fatalf("fetch: status %d", got.Status)
	}
	if !bytes.Equal(got.Body, payload) {
		t.Fatalf("the relayed file differs (%d bytes vs %d)", len(got.Body), len(payload))
	}

	if final := receiver.transferState(t, tr.ID).State; final != "completed" {
		t.Errorf("state = %q, want completed", final)
	}

	assertNoRelayResidue(t, h)
}

// SC-019 again, the other three ways a transfer can end. The requirement is not
// "cleans up on success"; it is that nothing remains whichever way it went.
func TestNothingRemainsWhicheverWayARelayEnds(t *testing.T) {
	for _, ending := range []struct {
		name string
		end  func(t *testing.T, h *harness, sender, receiver *device, id string)
	}{
		{"cancelled", func(t *testing.T, h *harness, sender, _ *device, id string) {
			resp := sender.do("POST", "/api/transfers/"+id+"/cancel", map[string]any{})
			resp.Body.Close()
		}},
		{"failed", func(t *testing.T, h *harness, _, _ *device, id string) {
			h.transfers.Fail(mustID(t, id), store.CauseNetworkLost)
		}},
		{"swept", func(t *testing.T, h *harness, _, _ *device, id string) {
			h.transfers.Fail(mustID(t, id), store.CauseAbandoned)
			h.transfers.SweepRelayed()
		}},
	} {
		t.Run(ending.name, func(t *testing.T) {
			h := newHarness(t)
			sender := h.pair()
			receiver := h.pair()

			payload := bytes.Repeat([]byte("r"), 32<<10)
			tr := sender.declare(t, receiver.ID, "half-way.bin", uint64(len(payload)))
			sender.uploadOK(t, tr.ID, 0, 0, payload[:8<<10])

			ending.end(t, h, sender, receiver, tr.ID)
			assertNoRelayResidue(t, h)
		})
	}
}

// FR-055: relayed data must never appear as a file this computer received.
// Structural, not a matter of remembering to clean up.
func TestRelayedDataNeverEntersTheReceiveFolder(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	payload := bytes.Repeat([]byte("v"), 16<<10)
	tr := relayTo(t, sender, receiver, "not-mine.jpg", payload)

	// Staged and verified, before the receiver has collected it: the moment
	// when the bytes are most present on this machine.
	entries, err := os.ReadDir(h.receiveDir)
	if err != nil {
		t.Fatalf("read receive folder: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("relayed data appeared in the receive folder: %v", names)
	}

	// And it is genuinely here, waiting, rather than the folder being empty
	// because nothing was ever staged.
	relayed, err := h.transfers.RelayResidue()
	if err != nil {
		t.Fatalf("residue: %v", err)
	}
	if len(relayed) != 1 || relayed[0] != tr.ID {
		t.Errorf("staged relay directories = %v, want just %s", relayed, tr.ID)
	}
}

// FR-056: the relaying user can see what is passing through their machine, and
// stop it. A computer holding someone else's files with no way to look is not
// something to ask a person to run.
func TestTheRelayingUserCanSeeAndStopWhatPassesThrough(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	payload := bytes.Repeat([]byte("p"), 8<<10)
	tr := sender.declare(t, receiver.ID, "passing-through.bin", uint64(len(payload)))
	sender.uploadOK(t, tr.ID, 0, 0, payload[:2<<10])

	passing, err := h.transfers.RelayedTransfers()
	if err != nil {
		t.Fatalf("relayed: %v", err)
	}
	if len(passing) != 1 || passing[0].ID.String() != tr.ID {
		t.Fatalf("relayed transfers = %+v, want the one in flight", passing)
	}
	if passing[0].Items[0].OriginalName != "passing-through.bin" {
		t.Errorf("the entry does not say what is passing through: %+v", passing[0].Items[0])
	}

	// Stopped by the relaying computer, which is neither end of the transfer.
	if err := h.transfers.Cancel(mustID(t, tr.ID)); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// It stops on the sending phone, with a state it can explain.
	if got := sender.transferState(t, tr.ID).State; got != "cancelled" {
		t.Errorf("state = %q, want cancelled", got)
	}
	assertNoRelayResidue(t, h)
}

// The receiving phone cannot collect what has not arrived yet, and is told to
// wait rather than told it failed.
func TestTheReceiverWaitsUntilTheFileHasFullyArrived(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	payload := bytes.Repeat([]byte("w"), 8<<10)
	tr := sender.declare(t, receiver.ID, "still-coming.bin", uint64(len(payload)))
	sender.uploadOK(t, tr.ID, 0, 0, payload[:1<<10])

	got := receiver.fetch(t, tr.ID, 0, "")
	if got.Status == http.StatusOK {
		t.Fatal("a partially arrived file was served to the receiver")
	}
	if got.Status != http.StatusConflict {
		t.Errorf("status = %d, want 409", got.Status)
	}
}

// Only the phone it is for collects it. A relay that let the sender fetch back
// what it had pushed would be storage for anyone paired with this computer,
// which is the opposite of holding nothing.
func TestOnlyTheTargetPhoneCanCollectARelayedFile(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()
	stranger := h.pair()

	payload := bytes.Repeat([]byte("s"), 4<<10)
	tr := relayTo(t, sender, receiver, "private.jpg", payload)

	for _, who := range []struct {
		name string
		dev  *device
	}{{"the sender", sender}, {"a third phone", stranger}} {
		got := who.dev.fetch(t, tr.ID, 0, "")
		if got.Status == http.StatusOK {
			t.Errorf("%s collected a file it was not sent", who.name)
		}
	}

	// And the intended phone still can, so the refusals above are about who is
	// asking rather than about the file.
	if got := receiver.fetch(t, tr.ID, 0, ""); got.Status != http.StatusOK {
		t.Errorf("the intended receiver was refused: status %d", got.Status)
	}
}

// assertNoRelayResidue holds SC-019: zero bytes of relayed content remain.
func assertNoRelayResidue(t *testing.T, h *harness) {
	t.Helper()

	residue, err := h.transfers.RelayResidue()
	if err != nil {
		t.Fatalf("residue: %v", err)
	}
	if len(residue) != 0 {
		t.Errorf("relayed data survived: %v", residue)
	}

	entries, err := os.ReadDir(h.receiveDir)
	if err != nil {
		t.Fatalf("read receive folder: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the receive folder is not empty after a relayed transfer")
	}
}
