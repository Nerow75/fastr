package integration

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/Nerow75/fastr/internal/store"
)

// User Story 5, the queue half: one transfer at a time, visibly, and under the
// user's control.
//
// SC-021 names the number — at most one active at any moment, checked while ten
// are queued — and it is the invariant every other queue rule rests on. It is
// enforced in one place, `store.Activate`, rather than by whoever remembers to
// ask: a caller in a hurry cannot bypass it, and that is the point.
//
// What runs a transfer, though, is worth being clear about, because it is not
// what "queue runner" usually means. Nothing on this computer can *start* one:
// in both directions the bytes come from a browser, either pushed as chunks or
// supplied into a pipe. So the queue does not drive transfers, it decides which
// one is allowed to move when its owner next tries.

// SC-021, with ten transfers queued and every one of them trying at once.
func TestOnlyOneTransferIsEverActive(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	const count = 10
	ids := make([]string, 0, count)
	for i := range count {
		tr := phone.declare(t, h.selfID, fmt.Sprintf("file-%d.bin", i), 64)
		ids = append(ids, tr.ID)
	}

	// Every one of them tries to start at the same moment, which is the only
	// way to test a mutual exclusion rather than a sequence.
	var wg sync.WaitGroup
	accepted := make([]bool, count)
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := phone.upload(t, id, 0, 0, make([]byte, 32))
			accepted[i] = resp.StatusCode == http.StatusOK
			resp.Body.Close()
		}()
	}
	wg.Wait()

	started := 0
	for _, ok := range accepted {
		if ok {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("%d transfers started at once, want exactly 1", started)
	}

	// And the store agrees, which is the record that outlives the request.
	queue, err := h.store.Queue()
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if queue.ActiveID == "" {
		t.Fatal("nothing holds the active slot after a successful upload")
	}
	if len(queue.Entries) != count-1 {
		t.Errorf("%d transfers are waiting, want %d", len(queue.Entries), count-1)
	}
}

// The refusal has to be one the sender can act on: waiting is the right
// response to a busy queue, and it is a different answer from "your transfer is
// broken". FR-038, and what `resume.ts` keys its retry policy on.
func TestABusyQueueSaysSoRatherThanFailing(t *testing.T) {
	h := newHarness(t)
	first := h.pair()
	second := h.pair()

	running := first.declare(t, h.selfID, "running.bin", 64)
	first.uploadOK(t, running.ID, 0, 0, make([]byte, 16))

	blocked := second.declare(t, h.selfID, "blocked.bin", 64)
	resp := second.upload(t, blocked.ID, 0, 0, make([]byte, 16))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, raw)
	}
	if body := errorBody(t, resp); body["error"] != "queue_busy" {
		t.Errorf("error = %v, want queue_busy", body["error"])
	}

	// The blocked transfer is still queued, not failed: it is waiting its turn.
	if got := second.transferState(t, blocked.ID).State; got != "queued" {
		t.Errorf("state = %q, want queued", got)
	}
}

// FR-035d: an interruption yields the slot rather than holding it. One phone
// walking out of range must not stop every other transfer on the machine, and
// the interrupted one must not lose its place in the world either.
func TestAnInterruptedTransferYieldsItsPlaceAndKeepsItsProgress(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()
	other := h.pair()

	stalled := phone.declare(t, h.selfID, "stalled.bin", 32<<10)
	phone.cutUpload(t, stalled.ID, 0, 0, make([]byte, 32<<10), 4<<10)

	// It yielded.
	queue, err := h.store.Queue()
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if queue.ActiveID != "" {
		t.Errorf("the interrupted transfer still holds the slot (%s)", queue.ActiveID)
	}

	// It kept its place in the queue rather than being dropped, because it is
	// still something the user asked for.
	waiting := false
	for _, id := range queue.Entries {
		if id.String() == stalled.ID {
			waiting = true
		}
	}
	if !waiting {
		t.Errorf("the interrupted transfer left the queue entirely: %+v", queue.Entries)
	}

	// And it kept its progress, which is what makes yielding safe.
	if got := phone.itemOffset(t, stalled.ID, 0); got != 4<<10 {
		t.Errorf("committed offset = %d, want %d", got, 4<<10)
	}

	// Meanwhile somebody else gets to work.
	payload := []byte("not blocked behind a phone in a lift")
	moving := other.declare(t, h.selfID, "moving.bin", uint64(len(payload)))
	other.uploadOK(t, moving.ID, 0, 0, payload)
	if got := other.completeOK(t, moving.ID, 0, digestOf(t, payload)).State; got != "completed" {
		t.Errorf("the second transfer reached %q, want completed", got)
	}
}

// FR-035: cancelling reaches both devices. A transfer the sender abandoned but
// the receiver still believes in is the state that produces a page waiting
// forever on something that will never arrive.
func TestCancellingReachesTheOtherDevice(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	tr := phone.declare(t, h.selfID, "abandoned.bin", 4096)
	phone.uploadOK(t, tr.ID, 0, 0, make([]byte, 1024))

	// Cancelled by the computer, which is the side that did not start it.
	if err := h.transfers.Cancel(mustID(t, tr.ID)); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// The sender sees it, and is refused if it tries to continue.
	if got := phone.transferState(t, tr.ID).State; got != "cancelled" {
		t.Errorf("state = %q, want cancelled", got)
	}

	resp := phone.upload(t, tr.ID, 0, 1024, make([]byte, 1024))
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("a cancelled transfer accepted more data")
	}

	// And the slot is free again.
	queue, err := h.store.Queue()
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if queue.ActiveID != "" {
		t.Errorf("a cancelled transfer still holds the slot (%s)", queue.ActiveID)
	}
}

// mustID turns a transfer identifier from the wire back into a store one.
func mustID(t *testing.T, id string) store.ID {
	t.Helper()

	parsed := store.ID(id)
	if err := parsed.Validate(); err != nil {
		t.Fatalf("bad transfer id %q: %v", id, err)
	}
	return parsed
}
