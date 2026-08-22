package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/store"
)

// The queue, per contracts/http-api.md and FR-035b to FR-035e.
//
// It does two jobs, and the second is the one that made it urgent. The first is
// the visible queue the contract describes: what is waiting, in which order.
//
// The second is **reconciliation**. A page learns about a transfer from the
// event announcing it, and an event announced while nothing was listening is
// gone: a phone that reloads mid-transfer, or that is sent a file in the
// seconds after pairing, sees nothing at all — no progress, no Save button, no
// way back. Reading this endpoint on connect is the fix, and no new route was
// needed for it, because a transfer that is neither terminal nor forgotten is
// by definition either the active one or one of the waiting entries.
//
// Everything here is scoped to what the caller is a party to. A paired phone
// must not learn what another phone is being sent, and enumerating identifiers
// is exactly how it would try.

type queueView struct {
	// Entries are the waiting transfers, in order.
	Entries []transferView `json:"entries"`
	// Active is the one running, or absent when nothing is.
	Active *transferView `json:"active,omitempty"`
}

// handleQueue lists what this device is waiting on and what is running.
func (d Deps) handleQueue(s *Session, w http.ResponseWriter, r *http.Request) {
	view, err := d.queueFor(s.DeviceID)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, view)
}

// queueFor builds one device's view of the queue.
func (d Deps) queueFor(deviceID string) (queueView, error) {
	q, err := d.Store.Queue()
	if err != nil {
		return queueView{}, app.Errorf(app.CodeInternal, err)
	}

	view := queueView{Entries: make([]transferView, 0, len(q.Entries))}
	for _, id := range q.Entries {
		tr, err := d.Store.Transfer(id)
		if err != nil {
			// A queue entry naming a transfer that is gone is a stale pointer,
			// not a reason to fail the request. The sweep clears them.
			continue
		}
		if !app.Participant(tr, deviceID) {
			continue
		}
		view.Entries = append(view.Entries, viewOf(tr))
	}

	if q.ActiveID != "" {
		if tr, err := d.Store.Transfer(q.ActiveID); err == nil && app.Participant(tr, deviceID) {
			active := viewOf(tr)
			view.Active = &active
		}
	}
	return view, nil
}

type reorderRequest struct {
	Order []string `json:"order"`
}

// handleReorderQueue replaces the waiting order. FR-035c.
//
// The full ordering rather than a move, because two pages reordering at once
// with relative moves would interleave into an order neither asked for. A list
// that does not match what is queued is refused whole.
func (d Deps) handleReorderQueue(s *Session, w http.ResponseWriter, r *http.Request) {
	var req reorderRequest
	if err := json.Unmarshal(s.Body, &req); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return
	}

	order := make([]store.ID, 0, len(req.Order))
	for _, id := range req.Order {
		order = append(order, store.ID(id))
	}

	if err := d.Store.Reorder(order); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return
	}
	d.publishQueueChanged()

	view, err := d.queueFor(s.DeviceID)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, view)
}

// handleRemoveFromQueue drops one waiting entry.
//
// Cancelled rather than merely dequeued: an entry removed from the queue is one
// the user decided against, and leaving the transfer record behind in `queued`
// with nothing to run it would make it invisible until the sweep took it a week
// later. Cancelling is also what tells the other device.
func (d Deps) handleRemoveFromQueue(s *Session, w http.ResponseWriter, r *http.Request) {
	id := store.ID(r.PathValue("id"))

	tr, err := d.Transfers.Get(id)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	if !app.Participant(tr, s.DeviceID) {
		// Invisible rather than refused, like every other transfer lookup.
		d.writeError(w, r, app.New(app.CodeTransferNotFound))
		return
	}

	if err := d.Transfers.Cancel(id); err != nil {
		d.writeError(w, r, err)
		return
	}
	d.publishQueueChanged()

	s.writeJSON(w, r, http.StatusOK, map[string]any{"removed": id.String()})
}

// handleClearQueue empties the waiting list. The active transfer keeps running:
// FR-035c is about what is waiting, and stopping what is in flight is what
// cancel is for.
func (d Deps) handleClearQueue(s *Session, w http.ResponseWriter, r *http.Request) {
	q, err := d.Store.Queue()
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	// Cancelled one by one rather than by emptying the list, for the same
	// reason as above: a dropped entry has to become a terminal transfer, or it
	// survives as a record nothing will ever run.
	cleared := 0
	for _, id := range q.Entries {
		tr, err := d.Store.Transfer(id)
		if err != nil || !app.Participant(tr, s.DeviceID) {
			continue
		}
		if err := d.Transfers.Cancel(id); err != nil {
			d.Log.Warn("clear queue", "transfer_id", id.String(), "error", err)
			continue
		}
		cleared++
	}
	if cleared > 0 {
		d.publishQueueChanged()
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"cleared": cleared})
}

// publishQueueChanged tells every page the order moved. The event carries no
// payload: what changed is the whole list, and each page reads it back scoped
// to itself.
func (d Deps) publishQueueChanged() {
	if d.Events == nil {
		return
	}
	d.Events.Publish(Event{Type: EventQueueChanged})
}
