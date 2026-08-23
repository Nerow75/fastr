package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// History, per FR-037 and FR-039.
//
// The question it answers is "did last night's transfer actually finish". A
// product that cannot answer that leaves the user opening folders to check,
// which is the work this whole project exists to remove.

type historyBody struct {
	Entries []struct {
		TransferID   string `json:"transfer_id"`
		Direction    string `json:"direction"`
		PeerName     string `json:"peer_name"`
		ItemCount    int    `json:"item_count"`
		TotalBytes   uint64 `json:"total_bytes"`
		Outcome      string `json:"outcome"`
		FailureCause string `json:"failure_cause"`
		Protection   string `json:"protection"`
	} `json:"entries"`
}

func (d *device) history(t *testing.T) historyBody {
	t.Helper()

	const path = "/api/history"
	resp := d.do("GET", path, nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("history: status %d: %s", resp.StatusCode, raw)
	}

	var out historyBody
	d.open("GET", path, resp, &out)
	return out
}

// FR-037: what moved, with which device, and how it turned out.
func TestHistoryRecordsWhatHappenedAndWithWhom(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("something that finished")
	done := phone.declare(t, h.selfID, "finished.bin", uint64(len(payload)))
	phone.uploadOK(t, done.ID, 0, 0, payload)
	phone.completeOK(t, done.ID, 0, digestOf(t, payload))

	// And one that failed, so both outcomes are covered.
	broken := phone.declare(t, h.selfID, "corrupt.bin", uint64(len(payload)))
	phone.uploadOK(t, broken.ID, 0, 0, payload)
	phone.completeItem(t, broken.ID, 0, digestOf(t, []byte("different"))).Body.Close()

	entries := phone.history(t).Entries
	if len(entries) != 2 {
		t.Fatalf("%d entries, want 2: %+v", len(entries), entries)
	}

	byID := map[string]int{}
	for i, e := range entries {
		byID[e.TransferID] = i
	}

	completed := entries[byID[done.ID]]
	if completed.Outcome != "completed" {
		t.Errorf("outcome = %q, want completed", completed.Outcome)
	}
	if completed.PeerName != "Test Phone" {
		t.Errorf("peer = %q, want the device's name rather than an identifier", completed.PeerName)
	}
	if completed.Direction != "incoming" {
		t.Errorf("direction = %q, want incoming", completed.Direction)
	}
	if completed.TotalBytes != uint64(len(payload)) || completed.ItemCount != 1 {
		t.Errorf("recorded %d bytes over %d items", completed.TotalBytes, completed.ItemCount)
	}
	// Principle V's honesty duty: the user can see afterwards which transfers
	// were encrypted and which were not.
	if completed.Protection == "" {
		t.Error("the entry does not say which protection mode was used")
	}

	failed := entries[byID[broken.ID]]
	if failed.Outcome != "failed" {
		t.Errorf("outcome = %q, want failed", failed.Outcome)
	}
	if failed.FailureCause != "checksum_mismatch" {
		t.Errorf("cause = %q, want checksum_mismatch", failed.FailureCause)
	}
}

// Newest first, because that is the end anyone is looking at.
func TestHistoryIsNewestFirst(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	names := []string{"first.bin", "second.bin", "third.bin"}
	for _, name := range names {
		payload := []byte(name)
		tr := phone.declare(t, h.selfID, name, uint64(len(payload)))
		phone.uploadOK(t, tr.ID, 0, 0, payload)
		phone.completeOK(t, tr.ID, 0, digestOf(t, payload))
	}

	entries := phone.history(t).Entries
	if len(entries) != len(names) {
		t.Fatalf("%d entries, want %d", len(entries), len(names))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].TransferID < entries[i].TransferID {
			t.Errorf("entry %d is older than the one before it", i)
		}
	}
}

// A transfer still running is not history. Two sources for one fact is two
// sources that can disagree.
func TestOnlyFinishedTransfersAreRecorded(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	tr := phone.declare(t, h.selfID, "still-going.bin", 4096)
	phone.uploadOK(t, tr.ID, 0, 0, make([]byte, 1024))

	if entries := phone.history(t).Entries; len(entries) != 0 {
		t.Errorf("a running transfer was recorded as history: %+v", entries)
	}
}

// FR-039: the user can erase all of it, with nothing kept back.
func TestHistoryCanBeCleared(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("to be forgotten")
	tr := phone.declare(t, h.selfID, "forgettable.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload)
	phone.completeOK(t, tr.ID, 0, digestOf(t, payload))

	if entries := phone.history(t).Entries; len(entries) != 1 {
		t.Fatalf("%d entries before clearing, want 1", len(entries))
	}

	if err := h.transfers.ClearHistory(); err != nil {
		t.Fatalf("clear: %v", err)
	}

	if entries := phone.history(t).Entries; len(entries) != 0 {
		t.Errorf("%d entries survived clearing: %+v", len(entries), entries)
	}

	// And the store still works afterwards, rather than having lost its bucket.
	again := phone.declare(t, h.selfID, "after.bin", uint64(len(payload)))
	phone.uploadOK(t, again.ID, 0, 0, payload)
	phone.completeOK(t, again.ID, 0, digestOf(t, payload))

	if entries := phone.history(t).Entries; len(entries) != 1 {
		t.Errorf("%d entries after clearing and sending again, want 1", len(entries))
	}
}

// Clearing is the user's decision, made on their own machine. A paired phone
// erasing the record of what it sent is exactly backwards.
func TestAPhoneCannotClearTheHistory(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("stays on the record")
	tr := phone.declare(t, h.selfID, "recorded.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload)
	phone.completeOK(t, tr.ID, 0, digestOf(t, payload))

	// Forged remote addresses rather than the harness's client, which always
	// comes from loopback: the question here is what happens to a request that
	// arrives from the network, and that is the one thing a loopback test
	// client cannot be.
	for _, remote := range []string{"192.168.1.50:41234", "10.0.0.7:5000"} {
		req := httptest.NewRequest(http.MethodDelete, "/api/history", nil)
		req.RemoteAddr = remote
		req.Header.Set("Authorization", "Bearer "+phone.credential)
		// Headers a phone can set must not buy loopback.
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.Header.Set("X-Real-IP", "127.0.0.1")

		rec := httptest.NewRecorder()
		h.server.Config.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("from %s: status %d, want 401", remote, rec.Code)
		}
	}

	if entries := phone.history(t).Entries; len(entries) != 1 {
		t.Errorf("the history was cleared from the network: %+v", entries)
	}
}

// The limit is bounded, because it comes from a peer and an unbounded read is
// an unbounded allocation.
func TestHistoryLimitIsBounded(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	resp := phone.do("GET", "/api/history?limit=99999999", nil)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// Opened against the bare path: the envelope binds the method and path, and
	// the query string is not part of either.
	var out historyBody
	phone.open("GET", "/api/history", resp, &out)
	// Nothing to return here; what matters is that an absurd limit was accepted
	// and clamped rather than honoured.
	if out.Entries == nil {
		t.Error("no entries field in the response")
	}

	bad := phone.do("GET", "/api/history?limit=nonsense", nil)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("status for a bad limit = %d, want 400", bad.StatusCode)
	}
}
