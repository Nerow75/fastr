package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/store"
)

// The transfer history, per FR-037 and FR-039.
//
// **This machine's history, not a per-device view.** A history entry names the
// other device, so scoping the list to the caller would be the wrong shape:
// the user asking is the person sitting at this computer, and what they want to
// know is what happened here — including with the phone that is no longer in
// the house. Reaching it needs a session, which is what stops a stranger on the
// network from reading it.
//
// It is also the one thing in the product a user can erase completely, and that
// is deliberate: a record of someone's files that they cannot clear is a record
// they did not ask to keep.

type historyEntryView struct {
	TransferID string `json:"transfer_id"`
	Direction  string `json:"direction"`

	PeerName     string `json:"peer_name"`
	PeerDeviceID string `json:"peer_device_id"`

	ItemCount  int    `json:"item_count"`
	TotalBytes uint64 `json:"total_bytes"`

	Outcome string `json:"outcome"`
	// FailureCause maps to a message carrying a corrective action, never shown
	// as a bare code. FR-038.
	FailureCause string `json:"failure_cause,omitempty"`
	// Protection lets the user see afterwards which transfers were encrypted
	// and which were not, which Principle V's honesty duty makes more than a
	// curiosity.
	Protection string    `json:"protection"`
	EndedAt    time.Time `json:"ended_at"`
}

func historyViewOf(e store.HistoryEntry) historyEntryView {
	return historyEntryView{
		TransferID: e.TransferID.String(), Direction: string(e.Direction),
		PeerName: e.PeerName, PeerDeviceID: e.PeerDeviceID,
		ItemCount: e.ItemCount, TotalBytes: e.TotalBytes,
		Outcome: string(e.Outcome), FailureCause: string(e.FailureCause),
		Protection: string(e.ProtectionMode), EndedAt: e.EndedAt,
	}
}

// handleHistory lists what has finished, newest first.
func (d Deps) handleHistory(s *Session, w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
			return
		}
		limit = parsed
	}

	entries, err := d.Transfers.History(limit)
	if err != nil {
		d.writeError(w, r, err)
		return
	}

	out := make([]historyEntryView, 0, len(entries))
	for _, e := range entries {
		out = append(out, historyViewOf(e))
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"entries": out})
}

// handleClearHistory erases every entry. FR-039.
//
// Loopback only, like every other action that changes what this machine keeps.
// A paired phone deleting the record of what it sent is exactly backwards.
func (d Deps) handleClearHistory(s *Session, w http.ResponseWriter, r *http.Request) {
	if err := d.Transfers.ClearHistory(); err != nil {
		d.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"cleared": true})
}
