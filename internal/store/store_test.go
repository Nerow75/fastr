package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "fastr.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestIDsSortByCreationTime(t *testing.T) {
	first := NewID()
	time.Sleep(2 * time.Millisecond)
	second := NewID()

	if first >= second {
		t.Errorf("ids must sort by creation time: %s >= %s", first, second)
	}
	for _, id := range []ID{first, second} {
		if err := id.Validate(); err != nil {
			t.Errorf("Validate(%s): %v", id, err)
		}
	}

	got, err := first.Time()
	if err != nil {
		t.Fatalf("Time: %v", err)
	}
	if d := time.Since(got); d > time.Minute || d < -time.Minute {
		t.Errorf("recovered time is %v off", d)
	}
}

func TestIDValidateRejectsMalformed(t *testing.T) {
	// Ambiguous Crockford characters and wrong lengths must be refused before
	// anything reaches a bucket key.
	for _, bad := range []ID{"", "short", "IIIIIIIIIIIIIIIIIIIIIIIIII", "0123456789012345678901234!"} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Validate(%q) = %v, want ErrInvalidID", bad, err)
		}
	}
}

// FR-035a is the invariant most likely to be broken by a caller in a hurry, so
// it is enforced in the store and tested here rather than trusted to the runner.
func TestOnlyOneTransferCanBeActive(t *testing.T) {
	s := newTestStore(t)
	a, b := NewID(), NewID()

	for _, id := range []ID{a, b} {
		if err := s.Enqueue(id); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	if err := s.Activate(a); err != nil {
		t.Fatalf("Activate(a): %v", err)
	}
	if err := s.Activate(b); !errors.Is(err, ErrQueueBusy) {
		t.Fatalf("Activate(b) = %v, want ErrQueueBusy", err)
	}

	q, err := s.Queue()
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if q.ActiveID != a {
		t.Errorf("active = %s, want %s", q.ActiveID, a)
	}
	if len(q.Entries) != 1 || q.Entries[0] != b {
		t.Errorf("entries = %v, want [%s]", q.Entries, b)
	}
}

// An interrupted transfer must step aside rather than hold the head. FR-035d.
func TestDeactivateRequeuesAtTheBack(t *testing.T) {
	s := newTestStore(t)
	a, b := NewID(), NewID()

	_ = s.Enqueue(a)
	_ = s.Enqueue(b)
	_ = s.Activate(a)

	if err := s.Deactivate(a, true); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	q, _ := s.Queue()
	if q.ActiveID != "" {
		t.Errorf("active = %s, want empty", q.ActiveID)
	}
	if len(q.Entries) != 2 || q.Entries[0] != b || q.Entries[1] != a {
		t.Errorf("entries = %v, want [%s %s]", q.Entries, b, a)
	}
}

func TestReorderRefusesTamperedOrdering(t *testing.T) {
	s := newTestStore(t)
	a, b, c := NewID(), NewID(), NewID()
	for _, id := range []ID{a, b, c} {
		_ = s.Enqueue(id)
	}

	if err := s.Reorder([]ID{c, a, b}); err != nil {
		t.Fatalf("legal reorder: %v", err)
	}
	q, _ := s.Queue()
	if q.Entries[0] != c {
		t.Errorf("reorder did not apply: %v", q.Entries)
	}

	// Dropping, adding, or duplicating an entry must be refused: each would
	// silently lose or double a transfer the user queued.
	for name, order := range map[string][]ID{
		"drops an entry":   {a, b},
		"adds an entry":    {a, b, c, NewID()},
		"duplicates":       {a, a, b},
		"unknown transfer": {a, b, NewID()},
	} {
		if err := s.Reorder(order); err == nil {
			t.Errorf("Reorder that %s was accepted", name)
		}
	}
}

func TestReorderCannotTouchActive(t *testing.T) {
	s := newTestStore(t)
	a, b := NewID(), NewID()
	_ = s.Enqueue(a)
	_ = s.Enqueue(b)
	_ = s.Activate(a)

	// The active transfer is no longer in Entries, so naming it is both a
	// length error and a semantic one. Either way it must fail.
	if err := s.Reorder([]ID{a}); err == nil {
		t.Error("reordering the active transfer was accepted")
	}
}

func TestClearQueueLeavesActiveRunning(t *testing.T) {
	s := newTestStore(t)
	a, b := NewID(), NewID()
	_ = s.Enqueue(a)
	_ = s.Enqueue(b)
	_ = s.Activate(a)

	if err := s.ClearQueue(); err != nil {
		t.Fatalf("ClearQueue: %v", err)
	}
	q, _ := s.Queue()
	if q.ActiveID != a {
		t.Errorf("ClearQueue stopped the active transfer")
	}
	if len(q.Entries) != 0 {
		t.Errorf("entries = %v, want empty", q.Entries)
	}
}

// FR-035e: the queue survives a restart.
func TestQueueSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fastr.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	a, b := NewID(), NewID()
	_ = s.Enqueue(a)
	_ = s.Enqueue(b)
	_ = s.Activate(a)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	q, err := reopened.Queue()
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if q.ActiveID != a || len(q.Entries) != 1 || q.Entries[0] != b {
		t.Errorf("queue did not survive restart: active=%s entries=%v", q.ActiveID, q.Entries)
	}
}

func TestIllegalStateTransitionsRefused(t *testing.T) {
	s := newTestStore(t)
	tr := sampleTransfer()
	if err := s.PutTransfer(tr); err != nil {
		t.Fatalf("PutTransfer: %v", err)
	}

	// A queued transfer cannot jump straight to completed: that is exactly how
	// a partial file gets presented as a whole one.
	if err := s.SetTransferState(tr.ID, StateCompleted, ""); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("queued -> completed = %v, want ErrIllegalTransition", err)
	}

	for _, to := range []State{StateRunning, StateVerifying, StateCompleted} {
		if err := s.SetTransferState(tr.ID, to, ""); err != nil {
			t.Fatalf("legal transition to %s: %v", to, err)
		}
	}

	// Terminal states are terminal.
	if err := s.SetTransferState(tr.ID, StateRunning, ""); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("completed -> running = %v, want ErrIllegalTransition", err)
	}

	got, _ := s.Transfer(tr.ID)
	if got.StartedAt == nil {
		t.Error("StartedAt was not stamped on running")
	}
	if got.EndedAt == nil {
		t.Error("EndedAt was not stamped on completed")
	}
}

func TestCommittedOffsetOnlyMovesForward(t *testing.T) {
	s := newTestStore(t)
	tr := sampleTransfer()
	_ = s.PutTransfer(tr)

	if err := s.AdvanceItem(tr.ID, 0, 500); err != nil {
		t.Fatalf("AdvanceItem: %v", err)
	}
	// Rewinding means the caller is resuming from stale state. Honouring it
	// would corrupt the file.
	if err := s.AdvanceItem(tr.ID, 0, 100); err == nil {
		t.Error("a backwards offset was accepted")
	}
	if err := s.AdvanceItem(tr.ID, 0, 5000); err == nil {
		t.Error("an offset beyond the file size was accepted")
	}

	got, _ := s.Transfer(tr.ID)
	if got.TransferredBytes != 500 {
		t.Errorf("TransferredBytes = %d, want 500", got.TransferredBytes)
	}
}

func TestTransferValidationCatchesInconsistency(t *testing.T) {
	s := newTestStore(t)

	bad := sampleTransfer()
	bad.TotalBytes = 999 // does not match the items
	if err := s.PutTransfer(bad); err == nil {
		t.Error("a transfer whose total disagrees with its items was accepted")
	}

	same := sampleTransfer()
	same.TargetDeviceID = same.SourceDeviceID
	if err := s.PutTransfer(same); err == nil {
		t.Error("a transfer to itself was accepted")
	}

	relay := sampleTransfer()
	relay.Direction = DirectionRelayed
	if err := s.PutTransfer(relay); err == nil {
		t.Error("a relayed transfer with no relay device was accepted")
	}
}

// FR-016: the expiry window follows the trust mode, and changing the mode
// recomputes from last activity rather than from now.
func TestTrustModeGovernsExpiry(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return base })

	p, err := s.CreatePairing("dev-1", []byte("hash"), []byte("key"), TrustAuto)
	if err != nil {
		t.Fatalf("CreatePairing: %v", err)
	}
	if want := base.Add(ExpiryAuto); !p.ExpiresAt.Equal(want) {
		t.Errorf("auto expiry = %v, want %v", p.ExpiresAt, want)
	}

	// Time passes without the device being used, then the mode changes. The
	// new window must run from last activity, so toggling a switch cannot
	// resurrect a stale pairing.
	s.SetClock(func() time.Time { return base.Add(60 * 24 * time.Hour) })
	if err := s.SetTrustMode("dev-1", TrustAsk); err != nil {
		t.Fatalf("SetTrustMode: %v", err)
	}

	got, _ := s.Pairing("dev-1")
	if want := base.Add(ExpiryAsk); !got.ExpiresAt.Equal(want) {
		t.Errorf("ask expiry = %v, want %v (recomputed from last activity)", got.ExpiresAt, want)
	}
	// 30 days from a last activity 60 days ago means it is already expired.
	if _, err := s.ActivePairing("dev-1"); !errors.Is(err, ErrPairingExpired) {
		t.Errorf("ActivePairing = %v, want ErrPairingExpired", err)
	}
}

// FR-015: revocation takes effect immediately, and must not leave usable key
// material behind.
func TestRevocationIsImmediateAndZeroesTheSessionKey(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreatePairing("dev-1", []byte("hash"), []byte("key"), TrustAuto); err != nil {
		t.Fatalf("CreatePairing: %v", err)
	}
	if _, err := s.ActivePairing("dev-1"); err != nil {
		t.Fatalf("pairing should be active: %v", err)
	}

	if err := s.RevokePairing("dev-1"); err != nil {
		t.Fatalf("RevokePairing: %v", err)
	}
	if _, err := s.ActivePairing("dev-1"); !errors.Is(err, ErrPairingRevoked) {
		t.Errorf("ActivePairing = %v, want ErrPairingRevoked", err)
	}

	got, _ := s.Pairing("dev-1")
	if len(got.SessionKey) != 0 {
		t.Error("revocation left live key material in the store")
	}
	// The token hash is deliberately kept. It cannot be turned back into a
	// credential, and it is what lets the revoked device be told "access was
	// removed" rather than "this device is not paired", which are different
	// corrective actions under FR-038.
	if len(got.TokenHash) == 0 {
		t.Error("revocation dropped the token hash, so the device cannot be told why it failed")
	}
	// The record itself survives, because FR-016 requires the user to see that
	// a pairing lapsed rather than have it vanish.
	if got.RevokedAt == nil {
		t.Error("revocation was not recorded")
	}
}

// Deleting a device must not leave an authorization no interface lists.
func TestDeletingADeviceRemovesItsPairing(t *testing.T) {
	s := newTestStore(t)
	dev := Device{ID: "dev-1", Name: "Phone", Kind: KindPhone}
	if err := s.PutDevice(dev); err != nil {
		t.Fatalf("PutDevice: %v", err)
	}
	if _, err := s.CreatePairing("dev-1", []byte("hash"), []byte("key"), TrustAuto); err != nil {
		t.Fatalf("CreatePairing: %v", err)
	}

	if err := s.DeleteDevice("dev-1"); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	if _, err := s.Pairing("dev-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("pairing survived device deletion: %v", err)
	}
}

func TestHistoryIsNewestFirstAndClearable(t *testing.T) {
	s := newTestStore(t)

	for i := 0; i < 3; i++ {
		tr := sampleTransfer()
		tr.State = StateCompleted
		ended := time.Now()
		tr.EndedAt = &ended
		if err := s.RecordHistory(tr, "Phone"); err != nil {
			t.Fatalf("RecordHistory: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	entries, err := s.History(10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].TransferID < entries[i].TransferID {
			t.Error("history is not newest first")
		}
	}

	if err := s.ClearHistory(); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}
	entries, _ = s.History(10)
	if len(entries) != 0 {
		t.Errorf("history survived clearing: %d entries", len(entries))
	}
}

func TestRecordHistoryRefusesNonTerminalTransfer(t *testing.T) {
	s := newTestStore(t)
	tr := sampleTransfer() // still queued
	if err := s.RecordHistory(tr, "Phone"); err == nil {
		t.Error("a running transfer was recorded in history")
	}
}

func TestSchemaVersionIsRecorded(t *testing.T) {
	s := newTestStore(t)
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != schemaVersion {
		t.Errorf("schema version = %d, want %d", v, schemaVersion)
	}
}

func sampleTransfer() Transfer {
	return Transfer{
		ID:             NewID(),
		Direction:      DirectionOutgoing,
		SourceDeviceID: "computer-1",
		TargetDeviceID: "phone-1",
		ProtectionMode: ProtectionSimple,
		Items: []TransferItem{{
			OriginalName: "video.mp4",
			StoredName:   "video.mp4",
			Size:         1000,
			State:        StateQueued,
		}},
		TotalBytes: 1000,
		State:      StateQueued,
		QueuedAt:   time.Now(),
	}
}
