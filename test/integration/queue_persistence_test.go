package integration

import (
	"path/filepath"
	"testing"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/httpapi"
	"github.com/Nerow75/fastr/internal/platform"
	"github.com/Nerow75/fastr/internal/store"
	"github.com/Nerow75/fastr/internal/transfer"
)

// FR-035e: a queue survives a restart, or its entries are reported as abandoned
// with a reason.
//
// This is the requirement that chose bbolt over a JSON file (research.md item
// 9): progress is written on every committed chunk while the queue has to
// remain readable, and a file rewritten atomically handles the first badly
// under the second.
//
// The tests here restart the *store and the service*, not the HTTP server. What
// a restart means for a queue is that the process holding it ended and another
// opened the same file, and that is exactly what this does — with the added
// benefit that the second process sees precisely the bytes the first left
// behind, rather than anything held in memory between them.

// restartable is a transfer service over a store at a known path, so it can be
// closed and opened again.
type restartable struct {
	store     *store.Store
	transfers *app.Transfers
}

// pairWith records a trust relationship the way the pairing endpoints do.
//
// Needed because a transfer aimed at this machine is refused outright when its
// sender has no pairing here (FR-016a): being able to reach the service is not
// the same as being allowed to write to the disk, and Declare checks the second
// as well as the first.
func (r *restartable) pairWith(t *testing.T, deviceID string, mode store.TrustMode) {
	t.Helper()

	if _, err := r.store.CreatePairing(deviceID, []byte("hash"), []byte("key"), mode); err != nil {
		t.Fatalf("pair %s: %v", deviceID, err)
	}
}

func openInstance(t *testing.T, dataDir, receiveDir, stagingDir, selfID string) *restartable {
	t.Helper()

	st, err := store.Open(filepath.Join(dataDir, "fastr.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sinks := transfer.NewSinks()
	t.Cleanup(sinks.CloseAll)

	return &restartable{
		store: st,
		transfers: &app.Transfers{
			Store:    st,
			Pipes:    transfer.NewPipes(),
			Sinks:    sinks,
			Notify:   httpapi.NewNotifier(httpapi.NewEvents()),
			Space:    fakeSpace(1 << 40),
			Log:      discardLogger(),
			SelfID:   selfID,
			Rules:    platform.Rules(),
			ReceiveD: receiveDir,
			StagingD: stagingDir,
		},
	}
}

// What was waiting is still waiting, in the same order, after a restart.
func TestAQueueSurvivesARestart(t *testing.T) {
	dataDir, receiveDir, stagingDir := t.TempDir(), t.TempDir(), t.TempDir()

	first := openInstance(t, dataDir, receiveDir, stagingDir, "01COMPUTERDEVICEID0000000")
	const phone = "01PHONEDEVICEIDENTIFIER00"
	first.pairWith(t, phone, store.TrustAuto)

	var declared []store.ID
	for _, name := range []string{"one.bin", "two.bin", "three.bin"} {
		tr, err := first.transfers.Declare(phone, app.Declaration{
			TargetDeviceID: "01COMPUTERDEVICEID0000000",
			Items:          []app.DeclaredItem{{Name: name, Size: 128}},
		})
		if err != nil {
			t.Fatalf("declare %s: %v", name, err)
		}
		declared = append(declared, tr.ID)
	}

	// The process ends.
	if err := first.store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// And another opens the same file.
	second := openInstance(t, dataDir, receiveDir, stagingDir, "01COMPUTERDEVICEID0000000")

	queue, err := second.store.Queue()
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(queue.Entries) != len(declared) {
		t.Fatalf("%d entries survived, want %d", len(queue.Entries), len(declared))
	}
	for i, id := range declared {
		if queue.Entries[i] != id {
			t.Errorf("entry %d is %s, want %s", i, queue.Entries[i], id)
		}
	}

	// And the transfers themselves are still there to run, with their names.
	tr, err := second.transfers.Get(declared[0])
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if tr.Items[0].OriginalName != "one.bin" || tr.State != store.StateQueued {
		t.Errorf("first transfer came back as %+v", tr)
	}
}

// A transfer the previous process died holding is a lie: nothing is running
// when a process starts. Left alone it sits in the single active slot with
// nobody driving it, and FR-035a then blocks every real transfer behind a
// ghost.
func TestATransferLeftRunningByACrashIsRecovered(t *testing.T) {
	dataDir, receiveDir, stagingDir := t.TempDir(), t.TempDir(), t.TempDir()
	const self = "01COMPUTERDEVICEID0000000"
	const phone = "01PHONEDEVICEIDENTIFIER00"

	first := openInstance(t, dataDir, receiveDir, stagingDir, self)
	first.pairWith(t, phone, store.TrustAuto)

	crashed, err := first.transfers.Declare(phone, app.Declaration{
		TargetDeviceID: self,
		Items:          []app.DeclaredItem{{Name: "half-sent.bin", Size: 4096}},
	})
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	waiting, err := first.transfers.Declare(phone, app.Declaration{
		TargetDeviceID: self,
		Items:          []app.DeclaredItem{{Name: "behind-it.bin", Size: 128}},
	})
	if err != nil {
		t.Fatalf("declare: %v", err)
	}

	// It starts, makes progress, and the process dies with it running.
	if err := first.transfers.Start(crashed.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := first.transfers.Progress(crashed.ID, 0, 2048); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if err := first.store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := openInstance(t, dataDir, receiveDir, stagingDir, self)

	recovered, err := second.transfers.Recover()
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 1 {
		t.Errorf("recovered %d transfers, want 1", recovered)
	}

	// Interrupted rather than failed: the committed offset is exact and the
	// bytes are on disk, so this is precisely what resume was built for.
	tr, err := second.transfers.Get(crashed.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if tr.State != store.StateInterrupted {
		t.Errorf("state = %q, want interrupted", tr.State)
	}
	if tr.Items[0].CommittedOffset != 2048 {
		t.Errorf("committed offset = %d, want the 2048 that had arrived", tr.Items[0].CommittedOffset)
	}

	// The slot is free, and what was behind the ghost can now run.
	queue, err := second.store.Queue()
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if queue.ActiveID != "" {
		t.Fatalf("the slot is still held by %s", queue.ActiveID)
	}
	if err := second.transfers.Start(waiting.ID); err != nil {
		t.Errorf("a transfer behind the ghost could not start: %v", err)
	}
}

// Recovery is idempotent. A restart loop must not turn one interrupted transfer
// into a different state each time round.
func TestRecoveringTwiceChangesNothingTheSecondTime(t *testing.T) {
	dataDir, receiveDir, stagingDir := t.TempDir(), t.TempDir(), t.TempDir()
	const self = "01COMPUTERDEVICEID0000000"

	instance := openInstance(t, dataDir, receiveDir, stagingDir, self)
	instance.pairWith(t, "01PHONEDEVICEIDENTIFIER00", store.TrustAuto)

	tr, err := instance.transfers.Declare("01PHONEDEVICEIDENTIFIER00", app.Declaration{
		TargetDeviceID: self,
		Items:          []app.DeclaredItem{{Name: "once.bin", Size: 64}},
	})
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if err := instance.transfers.Start(tr.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	if recovered, err := instance.transfers.Recover(); err != nil || recovered != 1 {
		t.Fatalf("first recovery: %d, %v", recovered, err)
	}
	if recovered, err := instance.transfers.Recover(); err != nil || recovered != 0 {
		t.Fatalf("second recovery touched %d transfers, want 0 (%v)", recovered, err)
	}
}
