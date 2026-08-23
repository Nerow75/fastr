package httpapi

import (
	"net/http"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/store"
)

// Accepting and declining an incoming transfer, per FR-016a and FR-016d.
//
// Only the **target** may answer. That is not a formality: the whole meaning of
// "ask every time" is that the person whose disk is about to be written to
// decides, and a sender that could accept on their behalf would make the
// setting decorative. Being a participant is not enough either — the sender is
// a participant too.

// handleAcceptTransfer lets a waiting transfer run.
func (d Deps) handleAcceptTransfer(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.answerableTransfer(s, w, r)
	if !ok {
		return
	}

	accepted, err := d.Transfers.Accept(tr.ID)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, viewOf(accepted))
}

// handleDeclineTransfer refuses one.
func (d Deps) handleDeclineTransfer(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.answerableTransfer(s, w, r)
	if !ok {
		return
	}

	if err := d.Transfers.Decline(tr.ID); err != nil {
		d.writeError(w, r, err)
		return
	}

	declined, err := d.Transfers.Get(tr.ID)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, viewOf(declined))
}

// answerableTransfer resolves a transfer the caller is entitled to answer for.
//
// Invisible rather than refused when they are not, like every other transfer
// lookup: a device that is not the target has no business learning that this
// identifier exists.
func (d Deps) answerableTransfer(s *Session, w http.ResponseWriter, r *http.Request) (store.Transfer, bool) {
	tr, ok := d.participantTransfer(s, w, r)
	if !ok {
		return store.Transfer{}, false
	}
	if s.DeviceID != tr.TargetDeviceID {
		d.writeError(w, r, app.New(app.CodeTransferNotFound))
		return store.Transfer{}, false
	}
	return tr, true
}
