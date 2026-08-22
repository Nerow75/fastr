package integration

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nerow75/fastr/internal/store"
)

// The retention sweep, per FR-034 and data-model.md.
//
// Everything here is about time, and none of it waits: the store takes its clock
// from a function, so a year of inactivity is a value rather than a year. What
// the tests hold it to is the pair of promises FR-034 makes — that abandoned
// data does go, and that the user is told it went. The second is the one worth
// testing hardest, because a sweep that removes a transfer silently is
// indistinguishable, from where the user sits, from losing one.

// at moves the harness's clock, for both the store and the sweep.
func (h *harness) at(when time.Time) {
	h.store.SetClock(func() time.Time { return when })
}

// A transfer nobody has touched for more than seven days goes, and its partial
// data goes with it.
func TestPartialDataIsSweptAfterSevenDays(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := make([]byte, 8<<10)
	tr := phone.declare(t, h.selfID, "forgotten.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload[:2048])

	staged := stagedFiles(t, h)
	if len(staged) != 1 {
		t.Fatalf("expected one staging file before the sweep, found %v", staged)
	}

	// The sink still holds the file open. On Windows nothing can delete it in
	// that state, which is the whole reason app.Sweep releases them first.
	removals := sweepAt(t, h, time.Now().Add(store.PartialRetention+time.Hour))

	if len(removals) != 1 {
		t.Fatalf("the sweep removed %d things, want 1: %+v", len(removals), removals)
	}
	if removals[0].Kind != store.RemovedTransfer {
		t.Errorf("kind = %q, want transfer", removals[0].Kind)
	}
	// FR-034 wants the user told what went. A ULID is not what went.
	if removals[0].Name != "forgotten.bin" {
		t.Errorf("name = %q, want the file name", removals[0].Name)
	}
	if removals[0].Bytes != 2048 {
		t.Errorf("bytes = %d, want the 2048 that had arrived", removals[0].Bytes)
	}

	if left := stagedFiles(t, h); len(left) != 0 {
		t.Errorf("partial data survived the sweep: %v", left)
	}

	// And it is in the history with a cause, rather than having simply vanished.
	entries, err := h.store.History(10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.TransferID.String() == tr.ID {
			found = true
			if e.FailureCause != store.CauseAbandoned {
				t.Errorf("cause = %q, want abandoned", e.FailureCause)
			}
		}
	}
	if !found {
		t.Error("the swept transfer left no history entry")
	}
}

// A transfer that is merely slow is not abandoned. Without the progress stamp
// this is the case that would be swept out from under a 10 GB file crossing a
// bad link, which is precisely the case User Story 3 exists for.
func TestATransferStillMakingProgressIsNotSwept(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	// Declared eight days ago, and started eight days ago: both of the
	// timestamps a transfer used to carry are far outside the window.
	h.at(time.Now().Add(-8 * 24 * time.Hour))

	payload := make([]byte, 4<<10)
	tr := phone.declare(t, h.selfID, "slow.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload[:1024])

	// And still moving as of a minute ago. Only last_progress_at says so; judged
	// by queued_at or started_at this transfer looks a week dead.
	h.at(time.Now().Add(-time.Minute))
	phone.uploadOK(t, tr.ID, 0, 1024, payload[1024:2048])

	if removals := sweepAt(t, h, time.Now()); len(removals) != 0 {
		t.Fatalf("a transfer that moved a minute ago was swept: %+v", removals)
	}

	// And it is still resumable, which is the point of not sweeping it.
	if got := phone.itemOffset(t, tr.ID, 0); got != 2048 {
		t.Errorf("offset after the sweep = %d, want 2048", got)
	}
}

// A completed transfer is not swept: it has no partial data, and its record is
// what the interface lists.
func TestACompletedTransferSurvivesTheSweep(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("delivered long ago")
	tr := phone.declare(t, h.selfID, "kept.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload)
	phone.completeOK(t, tr.ID, 0, digestOf(t, payload))

	if removals := sweepAt(t, h, time.Now().Add(365*24*time.Hour)); len(removals) != 0 {
		for _, r := range removals {
			if r.Kind == store.RemovedTransfer {
				t.Errorf("a completed transfer was swept: %+v", r)
			}
		}
	}

	if _, err := os.Stat(filepath.Join(h.receiveDir, "kept.bin")); err != nil {
		t.Errorf("the received file did not survive: %v", err)
	}
}

// FR-016: trust expires through inactivity, and the sweep is what enforces it.
// The device record stays, so the user still recognises the name when it comes
// back rather than meeting an unknown device.
func TestAnInactivePairingIsSweptButTheDeviceIsRemembered(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	// Just past the automatic-trust window.
	removals := sweepAt(t, h, time.Now().Add(store.ExpiryAuto+time.Hour))

	var swept *store.Removal
	for i := range removals {
		if removals[i].Kind == store.RemovedPairing && removals[i].ID == phone.ID {
			swept = &removals[i]
		}
	}
	if swept == nil {
		t.Fatalf("the lapsed pairing was not swept: %+v", removals)
	}
	if swept.Name != "Test Phone" {
		t.Errorf("name = %q, want the device name", swept.Name)
	}

	if _, err := h.store.Pairing(phone.ID); err == nil {
		t.Error("the pairing survived its expiry")
	}
	if dev, err := h.store.Device(phone.ID); err != nil || dev.Name == "" {
		t.Errorf("the device record went with the pairing: %v", err)
	}

	// And the credential it was holding no longer opens anything.
	resp := phone.do("GET", "/api/devices", nil)
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Error("a swept pairing still authorized a request")
	}
}

// A sweep that removes nothing says nothing. Otherwise the daily sweep would
// raise a notification every day, and the one that mattered would be the one
// nobody read.
func TestASweepThatRemovesNothingIsSilent(t *testing.T) {
	h := newHarness(t)
	h.pair()

	if removals := sweepAt(t, h, time.Now()); len(removals) != 0 {
		t.Errorf("a fresh instance swept %+v", removals)
	}
}

// sweepAt runs the sweep at a chosen instant and returns what it took.
func sweepAt(t *testing.T, h *harness, when time.Time) []store.Removal {
	t.Helper()

	// The store's own clock moves too, so the history entry the sweep writes is
	// stamped at the same instant rather than at the wall clock.
	h.at(when)

	removals, err := h.transfers.Sweep(when)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	return removals
}

// stagedFiles lists what is left in the staging directory.
func stagedFiles(t *testing.T, h *harness) []string {
	t.Helper()

	entries, err := os.ReadDir(h.stagingDir)
	if err != nil {
		t.Fatalf("read staging folder: %v", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
