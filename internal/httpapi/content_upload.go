package httpapi

import (
	"net/http"
	"strconv"

	"github.com/Nerow75/fastr/internal/app"
)

// The upward content path, per contracts/http-api.md.
//
// This is the phone-to-computer direction, and it is shaped differently from
// the other two on purpose:
//
//   - A download is a plain GET the browser's own manager performs, because
//     nothing else writes a multi-gigabyte file to a phone.
//   - A desktop-to-phone send is piped between two browsers, because a browser
//     never reveals a file's path and a phone cannot accept a connection.
//   - An upload is chunked POSTs, because a phone that locks its screen loses
//     whatever request was open, and a single 10 GB POST would lose everything
//     with it. Each chunk that returns is a promise the sender may forget those
//     bytes, which is what makes resume exact rather than approximate.
//
// The chunk size is the client's to choose; the server bounds nothing but the
// declared item size, which it will not write past.

// handleUploadContent appends a chunk to an incoming item.
//
// The reply is written in the clear, like every other bulk content route.
// Sealing it would buy nothing: the only value it carries is a byte count an
// observer already has from the length of the request that produced it, and the
// envelope's counter is a control-plane sequence that concurrent streaming
// requests have no ordering guarantee against.
func (d Deps) handleUploadContent(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.participantTransfer(s, w, r)
	if !ok {
		return
	}

	// Only the source writes. Without this, a paired phone could append to a
	// file a different phone is sending, and the checksum failure would be
	// reported against the innocent party.
	if s.DeviceID != tr.SourceDeviceID {
		d.writeError(w, r, app.New(app.CodeUnauthorized))
		return
	}

	index, _, ok := d.itemOf(tr, w, r)
	if !ok {
		return
	}

	offset, err := strconv.ParseUint(r.URL.Query().Get("offset"), 10, 64)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return
	}

	committed, err := d.Transfers.Receive(r.Context(), tr, index, offset, r.Body)
	if err != nil {
		// An offset mismatch carries the server's value, so the client corrects
		// rather than corrupts. It is the one error whose body the sender acts
		// on rather than shows.
		d.writeError(w, r, err)
		return
	}

	d.writePlainJSON(w, http.StatusOK, map[string]any{"committed_offset": committed})
}

// handleItemOffset reports where a resuming sender should continue from.
//
// Control plane, so it is sealed like the rest of /api: the answer is a fact
// about a file being moved between two named devices, and the contract wraps
// control payloads in both protection modes.
func (d Deps) handleItemOffset(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.participantTransfer(s, w, r)
	if !ok {
		return
	}

	index, _, ok := d.itemOf(tr, w, r)
	if !ok {
		return
	}

	committed, err := d.Transfers.CommittedOffset(tr, index)
	if err != nil {
		d.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"item":             index,
		"committed_offset": committed,
		"size":             tr.Items[index].Size,
	})
}
