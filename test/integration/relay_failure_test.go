package integration

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"net/http"
	"testing"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/store"
)

// FR-057: a relayed transfer says clearly when it stopped because the relaying
// computer became unavailable, and resumes once one is back.
//
// "Unavailable" is the ordinary case here, not an exotic one. The relay is
// somebody's laptop: it sleeps, it gets carried to another room, it is closed
// while two visitors are still sending photos to each other. What must not
// happen is that either phone is left with a bar that stopped, or that the work
// already done is thrown away.
//
// Both halves have to survive it, and they survive it differently. The upload
// resumes from the committed offset, exactly as an upload to the computer
// itself does. The download resumes with a Range request, exactly as any other
// download does. Neither needed anything of its own, which is the argument for
// staging the relay rather than inventing a third mechanism.

// The sending half: bytes already handed over are not sent again.
func TestARelayedUploadResumesFromWhatArrived(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	payload := make([]byte, 128<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	tr := sender.declare(t, receiver.ID, "interrupted.jpg", uint64(len(payload)))

	// The connection dies partway through, the way it does when a laptop lid
	// closes mid-chunk.
	const cut = 40 << 10
	sender.cutUpload(t, tr.ID, 0, 0, payload, cut)

	if got := sender.itemOffset(t, tr.ID, 0); got != cut {
		t.Fatalf("committed offset after the cut = %d, want %d", got, cut)
	}
	if got := sender.transferState(t, tr.ID).State; got != "interrupted" {
		t.Errorf("state = %q, want interrupted", got)
	}

	// It comes back and continues rather than starting again.
	sender.uploadOK(t, tr.ID, 0, cut, payload[cut:])
	sender.completeOK(t, tr.ID, 0, digestOf(t, payload))

	// And what the receiving phone collects is the whole file, assembled across
	// the interruption.
	got := receiver.fetch(t, tr.ID, 0, "")
	if got.Status != http.StatusOK {
		t.Fatalf("fetch: status %d", got.Status)
	}
	if !bytes.Equal(got.Body, payload) {
		t.Error("the file collected after a resumed relay is not the file that was sent")
	}

	// Waited for rather than assumed: the client's read finishes when the last
	// byte arrives, and the server is still finishing the transfer at that
	// moment. SC-019 is about what remains once it has *ended*.
	receiver.waitForState(t, tr.ID, "completed")
	assertNoRelayResidue(t, h)
}

// The collecting half: a download that stopped picks up where it left off, and
// the bytes are still there to pick up.
//
// A resumed download is a Range request, which is what a phone's download
// manager sends without being asked. It is served from the same staged file,
// which is the reason the relay stages rather than piping: there is something
// to come back to.
func TestARelayedDownloadResumesWithARange(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	payload := make([]byte, 96<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	tr := relayTo(t, sender, receiver, "collected-twice.jpg", payload)

	// What a download manager sends when it comes back for the rest.
	const already = 48 << 10
	got := receiver.fetch(t, tr.ID, 0, fmt.Sprintf("bytes=%d-", already))

	if got.Status != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", got.Status)
	}
	want := fmt.Sprintf("bytes %d-%d/%d", already, len(payload)-1, len(payload))
	if rangeHeader := got.Header.Get("Content-Range"); rangeHeader != want {
		t.Errorf("Content-Range = %q, want %q", rangeHeader, want)
	}
	if !bytes.Equal(got.Body, payload[already:]) {
		t.Errorf("resumed body is %d bytes, want %d", len(got.Body), len(payload)-already)
	}

	// Reaching the end finishes it, whichever part of the file the request
	// asked for, and the staged bytes go with it.
	receiver.waitForState(t, tr.ID, "completed")
	assertNoRelayResidue(t, h)
}

// A restart in the middle leaves the staged bytes intact and the transfer
// resumable, rather than throwing away work both phones have already done.
func TestARelayedTransferSurvivesTheRelayRestarting(t *testing.T) {
	dataDir, receiveDir, stagingDir := t.TempDir(), t.TempDir(), t.TempDir()
	const self = "01RELAYCOMPUTERID00000000"
	const sender = "01SENDINGPHONEID000000000"
	const target = "01RECEIVINGPHONEID0000000"

	first := openInstance(t, dataDir, receiveDir, stagingDir, self)
	first.pairWith(t, sender, store.TrustAuto)
	first.pairWith(t, target, store.TrustAuto)

	tr, err := first.transfers.Declare(sender, app.Declaration{
		TargetDeviceID: target,
		Items:          []app.DeclaredItem{{Name: "mid-flight.jpg", Size: 4096}},
	})
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if tr.Direction != store.DirectionRelayed {
		t.Fatalf("direction = %q, want relayed", tr.Direction)
	}

	// Half of it arrives, and then the process ends.
	if err := first.transfers.Start(tr.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := first.transfers.Receive(t.Context(), tr, 0, 0, bytes.NewReader(make([]byte, 2048))); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if err := first.store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := openInstance(t, dataDir, receiveDir, stagingDir, self)
	if _, err := second.transfers.Recover(); err != nil {
		t.Fatalf("recover: %v", err)
	}

	// Interrupted rather than failed, with its resume point, which is what
	// FR-057 means by resumable once a relay is back.
	recovered, err := second.transfers.Get(tr.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if recovered.State != store.StateInterrupted {
		t.Errorf("state = %q, want interrupted", recovered.State)
	}
	if recovered.Items[0].CommittedOffset != 2048 {
		t.Errorf("committed offset = %d, want the 2048 that arrived", recovered.Items[0].CommittedOffset)
	}

	// And the bytes are still on disk, so continuing costs only the rest.
	residue, err := second.transfers.RelayResidue()
	if err != nil {
		t.Fatalf("residue: %v", err)
	}
	if len(residue) != 1 || residue[0] != tr.ID.String() {
		t.Fatalf("staged relay data = %v, want the interrupted transfer", residue)
	}

	if _, err := second.transfers.Receive(t.Context(), recovered, 0, 2048, bytes.NewReader(make([]byte, 2048))); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got, _ := second.transfers.Get(tr.ID); got.Items[0].CommittedOffset != 4096 {
		t.Errorf("offset after resuming = %d, want 4096", got.Items[0].CommittedOffset)
	}
}

// A relay with no room says so before anything moves. FR-058: discovering it at
// ninety percent of a transfer between two other people's phones is the worst
// possible moment and it is entirely avoidable.
func TestARelayWithNoRoomRefusesBeforeAnythingMoves(t *testing.T) {
	h := newHarnessWithSpace(t, 1<<20)
	sender := h.pair()
	receiver := h.pair()

	resp := sender.do("POST", "/api/transfers", map[string]any{
		"target_device_id": receiver.ID,
		"items":            []map[string]any{{"name": "too-big.mov", "size": 8 << 20}},
	})
	if resp.StatusCode != http.StatusConflict {
		resp.Body.Close()
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}

	body := errorBody(t, resp)
	if body["error"] != "insufficient_space" {
		t.Errorf("error = %v, want insufficient_space", body["error"])
	}
	// FR-038: the message says what to do, which needs both numbers.
	params, ok := body["params"].(map[string]any)
	if !ok || params["needed"] == nil || params["available"] == nil {
		t.Errorf("params = %v, want needed and available", body["params"])
	}

	assertNoRelayResidue(t, h)
}
