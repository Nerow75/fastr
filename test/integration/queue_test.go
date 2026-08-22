package integration

import (
	"io"
	"net/http"
	"testing"
)

// The queue, per FR-035a to FR-035e and contracts/http-api.md.
//
// It carries two jobs. The visible one is the queue itself: what is waiting, in
// which order, and the ability to change that. The quieter one is
// reconciliation — a page reads it on connect to find the transfers whose
// announcing events it was not there to hear, which is the only way a phone
// that reloaded mid-transfer ever sees that transfer again.
//
// Both jobs are scoped to what the caller is a party to, and that is the part
// worth testing hardest: a paired phone holding a valid credential must not be
// able to learn what a different phone is being sent.

type queueBody struct {
	Entries []declaredTransfer `json:"entries"`
	Active  *declaredTransfer  `json:"active"`
}

// queue reads one device's view of the queue.
func (d *device) queue(t *testing.T) queueBody {
	t.Helper()

	const path = "/api/queue"
	resp := d.do("GET", path, nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("queue: status %d: %s", resp.StatusCode, raw)
	}

	var out queueBody
	d.open("GET", path, resp, &out)
	return out
}

// A declared transfer waits, and once bytes start moving it is the active one.
// FR-035a: exactly one runs at a time, and the queue says which.
func TestTheQueueSeparatesWhatWaitsFromWhatRuns(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("something to send")
	tr := phone.declare(t, h.selfID, "queued.bin", uint64(len(payload)))

	waiting := phone.queue(t)
	if len(waiting.Entries) != 1 || waiting.Entries[0].ID != tr.ID {
		t.Fatalf("entries = %+v, want the declared transfer", waiting.Entries)
	}
	if waiting.Active != nil {
		t.Errorf("active = %+v, want nothing running yet", waiting.Active)
	}

	phone.uploadOK(t, tr.ID, 0, 0, payload)

	running := phone.queue(t)
	if running.Active == nil || running.Active.ID != tr.ID {
		t.Fatalf("active = %+v, want the transfer that is moving", running.Active)
	}
	if len(running.Entries) != 0 {
		t.Errorf("entries = %+v, want the active transfer to have left the queue", running.Entries)
	}

	phone.completeOK(t, tr.ID, 0, digestOf(t, payload))

	if done := phone.queue(t); done.Active != nil || len(done.Entries) != 0 {
		t.Errorf("a finished transfer is still in the queue: %+v", done)
	}
}

// T087b, the reason this endpoint exists on the phone at all: a transfer
// declared while a page was not listening is invisible to it forever otherwise.
// Reading the queue is what a reconnecting page does instead of guessing.
func TestAReconnectingDeviceFindsTheTransferItMissed(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := make([]byte, 4096)
	tr := phone.declare(t, h.selfID, "missed.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload[:1024])

	// A reload: same credential, same envelope key, no memory of any event.
	returning := &device{h: h, ID: phone.ID, credential: phone.credential, envelope: phone.envelope}

	view := returning.queue(t)
	if view.Active == nil || view.Active.ID != tr.ID {
		t.Fatalf("a reconnecting device found no active transfer: %+v", view)
	}

	// And enough to resume from, without a single event having been received.
	if got := returning.itemOffset(t, tr.ID, 0); got != 1024 {
		t.Errorf("offset = %d, want 1024", got)
	}
}

// Holding a credential is not the same as being a party to a transfer.
func TestTheQueueShowsOnlyWhatTheCallerIsAPartyTo(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()
	stranger := h.pair()

	tr := phone.declare(t, h.selfID, "private.bin", 32)

	if view := stranger.queue(t); len(view.Entries) != 0 || view.Active != nil {
		t.Fatalf("a third device saw someone else's transfer: %+v", view)
	}

	// And it cannot remove what it cannot see.
	resp := stranger.do("DELETE", "/api/queue/"+tr.ID, nil)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("a third device removed someone else's transfer from the queue")
	}

	// Which left the real owner's queue untouched.
	if view := phone.queue(t); len(view.Entries) != 1 {
		t.Errorf("the owner's queue changed: %+v", view)
	}
}

// FR-035c: removing an entry is a decision, and a decision has to reach the
// other device. A dequeued transfer that stayed `queued` would be invisible
// until the retention sweep took it a week later.
func TestRemovingAnEntryCancelsIt(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	tr := phone.declare(t, h.selfID, "changed-my-mind.bin", 128)

	resp := phone.do("DELETE", "/api/queue/"+tr.ID, nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("remove: status %d: %s", resp.StatusCode, raw)
	}
	resp.Body.Close()

	if got := phone.transferState(t, tr.ID).State; got != "cancelled" {
		t.Errorf("state = %q, want cancelled", got)
	}
	if view := phone.queue(t); len(view.Entries) != 0 {
		t.Errorf("the entry survived its removal: %+v", view)
	}
}

// Clearing takes everything waiting and leaves what is running, which is what
// FR-035c says and the only behaviour that does not surprise someone mid-send.
func TestClearingTheQueueLeavesTheActiveTransferAlone(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("in flight")
	running := phone.declare(t, h.selfID, "running.bin", uint64(len(payload)))
	phone.uploadOK(t, running.ID, 0, 0, payload[:4])

	waiting := phone.declare(t, h.selfID, "waiting.bin", 64)

	resp := phone.do("DELETE", "/api/queue", nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("clear: status %d: %s", resp.StatusCode, raw)
	}
	resp.Body.Close()

	if got := phone.transferState(t, waiting.ID).State; got != "cancelled" {
		t.Errorf("the waiting transfer is %q, want cancelled", got)
	}
	if got := phone.transferState(t, running.ID).State; got != "running" {
		t.Errorf("the active transfer is %q, want it left alone", got)
	}

	// And it can still be finished, which is what "left alone" has to mean.
	phone.uploadOK(t, running.ID, 0, 4, payload[4:])
	if got := phone.completeOK(t, running.ID, 0, digestOf(t, payload)).State; got != "completed" {
		t.Errorf("the active transfer reached %q, want completed", got)
	}
}

// A reordering that adds, drops, or duplicates an entry is refused whole. Two
// pages reordering at once must not be able to interleave into an order neither
// asked for.
func TestReorderRefusesAnOrderingThatIsNotThisQueue(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	first := phone.declare(t, h.selfID, "first.bin", 16)
	second := phone.declare(t, h.selfID, "second.bin", 16)

	for _, order := range [][]string{
		{first.ID},                               // one short
		{first.ID, second.ID, first.ID},          // duplicated
		{first.ID, "01ARZ3NDEKTSV4RRFFQ69G5FAV"}, // a transfer that is not queued
	} {
		resp := phone.do("POST", "/api/queue/reorder", map[string]any{"order": order})
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("reorder accepted %v", order)
		}
	}

	// The real reordering works, and is what the queue reports afterwards.
	resp := phone.do("POST", "/api/queue/reorder", map[string]any{
		"order": []string{second.ID, first.ID},
	})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("reorder: status %d: %s", resp.StatusCode, raw)
	}
	var out queueBody
	phone.open("POST", "/api/queue/reorder", resp, &out)

	if len(out.Entries) != 2 || out.Entries[0].ID != second.ID {
		t.Errorf("order after reordering = %+v, want the second transfer first", out.Entries)
	}
}

// FR-035a: the single active slot is the invariant every other queue rule rests
// on, and it is enforced at the store rather than by whoever remembers to ask.
func TestASecondTransferCannotTakeTheActiveSlot(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()
	other := h.pair()

	payload := []byte("holding the slot")
	held := phone.declare(t, h.selfID, "held.bin", uint64(len(payload)))
	phone.uploadOK(t, held.ID, 0, 0, payload[:4])

	blocked := other.declare(t, h.selfID, "blocked.bin", 16)
	resp := other.upload(t, blocked.ID, 0, 0, []byte("sixteen bytes!!!"))
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatal("two transfers ran at once")
	}
	if body := errorBody(t, resp); body["error"] != "queue_busy" {
		t.Errorf("error = %v, want queue_busy", body["error"])
	}

	// And it is genuinely only waiting: the slot frees and it goes through.
	phone.uploadOK(t, held.ID, 0, 4, payload[4:])
	phone.completeOK(t, held.ID, 0, digestOf(t, payload))

	other.uploadOK(t, blocked.ID, 0, 0, []byte("sixteen bytes!!!"))
	if got := other.transferState(t, blocked.ID).State; got != "running" {
		t.Errorf("state after the slot freed = %q, want running", got)
	}
}
