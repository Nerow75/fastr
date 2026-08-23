package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/Nerow75/fastr/internal/app"
)

// Item completion, per contracts/http-api.md.
//
// The sender says "that is all of it, and here is what I hashed as I read it".
// What happens next depends on who holds the file:
//
//   - When this machine received it, the digest is verified against what was
//     actually written, and only then does the file move out of staging to the
//     name the user will see. A mismatch deletes the partial and fails the
//     transfer. Nothing reaches the final path before that succeeds (FR-032,
//     FR-033).
//   - When this machine is relaying between two phones, the same verification
//     runs, but the file is left in the relay directory for the phone it was
//     meant for rather than placed in this user's receive folder (FR-055).
//   - When the bytes were piped to another device, this machine never had them.
//     The digest is recorded so it appears in history and so the receiver can
//     check its own copy, and the item is marked done.

type completeRequest struct {
	// Checksum is base64 BLAKE2b of the whole item, computed by the sender.
	Checksum string `json:"checksum"`
}

// handleCompleteItem finishes one item of a transfer.
func (d Deps) handleCompleteItem(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.participantTransfer(s, w, r)
	if !ok {
		return
	}

	index, _, ok := d.itemOf(tr, w, r)
	if !ok {
		return
	}

	var req completeRequest
	if len(s.Body) > 0 {
		if err := json.Unmarshal(s.Body, &req); err != nil {
			d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
			return
		}
	}

	var digest []byte
	if req.Checksum != "" {
		var err error
		digest, err = base64.StdEncoding.DecodeString(req.Checksum)
		if err != nil {
			d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
			return
		}
	}

	if digest != nil {
		tr.Items[index].Checksum = digest
		if err := d.Store.PutTransfer(tr); err != nil {
			d.writeError(w, r, app.Errorf(app.CodeInternal, err))
			return
		}
	}

	if !d.Transfers.Writes(tr) {
		// Piped to another device: this machine never held the bytes and has
		// nothing to verify. The receiving browser checks its own copy.
		s.writeJSON(w, r, http.StatusOK, map[string]any{"item": index})
		return
	}

	// Only the source may declare an item finished. A third party doing so
	// would commit a file it never sent, under a checksum it chose.
	if s.DeviceID != tr.SourceDeviceID {
		d.writeError(w, r, app.New(app.CodeUnauthorized))
		return
	}

	updated, err := d.Transfers.CommitItem(tr, index, digest)
	if err != nil {
		d.writeError(w, r, err)
		return
	}

	// The full view, not just an acknowledgement: stored_name may differ from
	// what the sender asked for, and FR-024 requires the change to be reported
	// rather than discovered later in a folder listing.
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"item":     index,
		"transfer": viewOf(updated),
	})
}
