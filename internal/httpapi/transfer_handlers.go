package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/pairing"
	"github.com/Nerow75/fastr/internal/store"
	"github.com/Nerow75/fastr/internal/transfer"
)

// Transfer endpoints, per contracts/http-api.md.
//
// The shape follows from the pipe: the receiver fetches content, the sender
// supplies it, and the server joins the two without touching the disk. See
// internal/transfer/pipe.go for why.

type declareRequest struct {
	TargetDeviceID string `json:"target_device_id"`
	Items          []struct {
		Name         string `json:"name"`
		RelativePath string `json:"relative_path,omitempty"`
		Size         uint64 `json:"size"`
	} `json:"items"`
}

type transferView struct {
	ID               string     `json:"id"`
	Direction        string     `json:"direction"`
	SourceDeviceID   string     `json:"source_device_id"`
	TargetDeviceID   string     `json:"target_device_id"`
	Protection       string     `json:"protection"`
	State            string     `json:"state"`
	FailureCause     string     `json:"failure_cause,omitempty"`
	TotalBytes       uint64     `json:"total_bytes"`
	TransferredBytes uint64     `json:"transferred_bytes"`
	Items            []itemView `json:"items"`
}

type itemView struct {
	Index           int    `json:"index"`
	Name            string `json:"name"`
	StoredName      string `json:"stored_name"`
	RelativePath    string `json:"relative_path,omitempty"`
	Size            uint64 `json:"size"`
	CommittedOffset uint64 `json:"committed_offset"`
	State           string `json:"state"`
}

func viewOf(tr store.Transfer) transferView {
	items := make([]itemView, 0, len(tr.Items))
	for i, item := range tr.Items {
		items = append(items, itemView{
			Index: i, Name: item.OriginalName, StoredName: item.StoredName,
			RelativePath: item.RelativePath, Size: item.Size,
			CommittedOffset: item.CommittedOffset, State: string(item.State),
		})
	}
	return transferView{
		ID: tr.ID.String(), Direction: string(tr.Direction),
		SourceDeviceID: tr.SourceDeviceID, TargetDeviceID: tr.TargetDeviceID,
		Protection: string(tr.ProtectionMode), State: string(tr.State),
		FailureCause: string(tr.FailureCause), TotalBytes: tr.TotalBytes,
		TransferredBytes: tr.TransferredBytes, Items: items,
	}
}

// handleDeclareTransfer proposes a transfer. FR-028: free space is checked here,
// before a single byte moves.
func (d Deps) handleDeclareTransfer(s *Session, w http.ResponseWriter, r *http.Request) {
	var req declareRequest
	if err := json.Unmarshal(s.Body, &req); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return
	}

	decl := app.Declaration{TargetDeviceID: req.TargetDeviceID}
	for _, item := range req.Items {
		decl.Items = append(decl.Items, app.DeclaredItem{
			Name: item.Name, RelativePath: item.RelativePath, Size: item.Size,
		})
	}

	tr, err := d.Transfers.Declare(s.DeviceID, decl)
	if err != nil {
		d.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusCreated, viewOf(tr))
}

// handleGetTransfer reports state and per-item committed offsets, which is what
// a resuming client reads before deciding where to continue.
func (d Deps) handleGetTransfer(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.participantTransfer(s, w, r)
	if !ok {
		return
	}
	s.writeJSON(w, r, http.StatusOK, viewOf(tr))
}

// handleCancelTransfer stops a transfer from either side. FR-035.
func (d Deps) handleCancelTransfer(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.participantTransfer(s, w, r)
	if !ok {
		return
	}
	if err := d.Transfers.Cancel(tr.ID); err != nil {
		d.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"cancelled": tr.ID.String()})
}

// handleFetchContent streams one item to the receiver.
//
// Bulk content is not sealed in simple mode: constitution v2.0.1 is explicit
// that file content travels in the clear there, and the interface says so.
// Range is honoured so a browser download manager can resume without help.
func (d Deps) handleFetchContent(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.participantTransfer(s, w, r)
	if !ok {
		return
	}

	index, item, ok := d.itemOf(tr, w, r)
	if !ok {
		return
	}

	offset, err := parseRangeStart(r.Header.Get("Range"), item.Size)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return
	}

	if tr.State == store.StateQueued || tr.State == store.StateInterrupted {
		if err := d.Transfers.Start(tr.ID); err != nil {
			d.writeError(w, r, err)
			return
		}
	}

	key := transfer.Key{TransferID: tr.ID.String(), ItemIndex: index}
	reader, finish, err := d.Transfers.Pipes.Await(key, offset, r.Context().Done())
	if err != nil {
		d.Transfers.Interrupt(tr.ID, err)
		d.writeError(w, r, pipeError(err))
		return
	}

	remaining := item.Size - offset
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatUint(remaining, 10))
	// The sanitized name, not the original: the sender does not get to choose
	// what the receiver's browser writes.
	w.Header().Set("Content-Disposition", contentDisposition(item.StoredName))
	securityHeaders(w)

	status := http.StatusOK
	if offset > 0 {
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", offset, item.Size-1, item.Size))
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)

	written, copyErr := io.Copy(w, reader)
	finish(copyErr)

	//nolint:gosec // a copy count is never negative
	if err := d.Transfers.Progress(tr.ID, index, offset+uint64(written)); err != nil {
		d.Log.Debug("progress", "transfer_id", tr.ID.String(), "error", err)
	}

	if copyErr != nil {
		d.Transfers.Interrupt(tr.ID, copyErr)
		return
	}
	//nolint:gosec // a copy count is never negative
	if offset+uint64(written) >= item.Size {
		if err := d.Transfers.Complete(tr.ID, index); err != nil {
			d.Log.Error("complete", "transfer_id", tr.ID.String(), "error", err)
		}
	}
}

// handleSupplyContent attaches the sender's body to a waiting receiver.
//
// It returns only once the bytes have reached the other side, so the sending
// page never reports a file as sent while it is still in flight.
func (d Deps) handleSupplyContent(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.participantTransfer(s, w, r)
	if !ok {
		return
	}
	// Only the source supplies. Without this, a paired phone could inject
	// content into a transfer between two other devices.
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

	key := transfer.Key{TransferID: tr.ID.String(), ItemIndex: index}
	if err := d.Transfers.Pipes.Supply(key, offset, r.Body); err != nil {
		d.writeError(w, r, pipeError(err))
		return
	}

	d.writePlainJSON(w, http.StatusOK, map[string]any{"supplied": true})
}

// handlePendingSupply tells the sender where a receiver is waiting, so a page
// that reconnects can resume without being told by an event it missed.
func (d Deps) handlePendingSupply(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.participantTransfer(s, w, r)
	if !ok {
		return
	}

	out := make([]map[string]any, 0, len(tr.Items))
	for index := range tr.Items {
		key := transfer.Key{TransferID: tr.ID.String(), ItemIndex: index}
		if offset, waiting := d.Transfers.Pipes.PendingOffset(key); waiting {
			out = append(out, map[string]any{"item": index, "offset": offset})
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"waiting": out})
}

// handleCompleteItem records the sender's checksum and finishes the item.
func (d Deps) handleCompleteItem(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.participantTransfer(s, w, r)
	if !ok {
		return
	}

	index, _, ok := d.itemOf(tr, w, r)
	if !ok {
		return
	}

	var req struct {
		Checksum string `json:"checksum"`
	}
	if len(s.Body) > 0 {
		if err := json.Unmarshal(s.Body, &req); err != nil {
			d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
			return
		}
	}

	if req.Checksum != "" {
		digest, err := base64.StdEncoding.DecodeString(req.Checksum)
		if err != nil {
			d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
			return
		}
		tr.Items[index].Checksum = digest
		if err := d.Store.PutTransfer(tr); err != nil {
			d.writeError(w, r, app.Errorf(app.CodeInternal, err))
			return
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"item": index})
}

// participantTransfer loads the transfer and checks the caller is a party to it.
//
// Holding a valid credential is not enough: a paired phone must not be able to
// fetch a file being sent to a different paired phone by guessing an identifier.
func (d Deps) participantTransfer(s *Session, w http.ResponseWriter, r *http.Request) (store.Transfer, bool) {
	id := store.ID(r.PathValue("id"))
	if err := id.Validate(); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return store.Transfer{}, false
	}

	tr, err := d.Transfers.Get(id)
	if err != nil {
		d.writeError(w, r, err)
		return store.Transfer{}, false
	}
	if !app.Participant(tr, s.DeviceID) {
		// Deliberately the same answer as an unknown transfer: distinguishing
		// them would let a caller enumerate other people's transfers.
		d.writeError(w, r, app.New(app.CodeTransferNotFound))
		return store.Transfer{}, false
	}
	return tr, true
}

// itemOf resolves the item index from the path.
func (d Deps) itemOf(tr store.Transfer, w http.ResponseWriter, r *http.Request) (int, store.TransferItem, bool) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 || index >= len(tr.Items) {
		d.writeError(w, r, app.New(app.CodeInvalidRequest))
		return 0, store.TransferItem{}, false
	}
	return index, tr.Items[index], true
}

// parseRangeStart reads the start offset from a Range header.
//
// Only a single open-ended range is supported: `bytes=N-`. Multipart ranges
// exist for media seeking and have no place in a file transfer, so they are
// refused rather than silently answered with the whole file.
func parseRangeStart(header string, size uint64) (uint64, error) {
	if header == "" {
		return 0, nil
	}

	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !ok {
		return 0, errors.New("unsupported range unit")
	}
	if strings.Contains(spec, ",") {
		return 0, errors.New("multiple ranges are not supported")
	}

	start, end, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, errors.New("malformed range")
	}
	if start == "" {
		return 0, errors.New("suffix ranges are not supported")
	}
	// An explicit end is accepted only when it is the end of the file: the
	// pipe streams forward and cannot stop early without desynchronising the
	// sender.
	offset, err := strconv.ParseUint(start, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("malformed range start: %w", err)
	}
	if end != "" {
		last, err := strconv.ParseUint(end, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("malformed range end: %w", err)
		}
		if size > 0 && last != size-1 {
			return 0, errors.New("partial ranges that stop early are not supported")
		}
	}
	if offset > size {
		return 0, fmt.Errorf("range start %d is beyond the file", offset)
	}
	return offset, nil
}

// contentDisposition builds a header that survives a non-ASCII filename.
//
// The plain `filename` is stripped to ASCII for old clients, and `filename*`
// carries the real name, which is what every current browser reads.
func contentDisposition(name string) string {
	var ascii strings.Builder
	for _, r := range name {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			ascii.WriteByte('_')
			continue
		}
		ascii.WriteRune(r)
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		ascii.String(), urlEscape(name))
}

// urlEscape percent-encodes everything outside the unreserved set, which is
// what RFC 5987 requires for filename*.
func urlEscape(s string) string {
	const unreserved = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~"
	var out strings.Builder
	for _, b := range []byte(s) {
		if strings.IndexByte(unreserved, b) >= 0 {
			out.WriteByte(b)
			continue
		}
		fmt.Fprintf(&out, "%%%02X", b)
	}
	return out.String()
}

// pipeError maps a rendezvous failure to its catalogue code.
func pipeError(err error) error {
	switch {
	case errors.Is(err, transfer.ErrNoSupplier):
		return app.Errorf(app.CodeRelayUnavailable, err)
	case errors.Is(err, transfer.ErrOffsetMismatch):
		return app.Errorf(app.CodeOffsetMismatch, err)
	case errors.Is(err, transfer.ErrNoDemand):
		return app.Errorf(app.CodeTransferNotFound, err)
	case errors.Is(err, transfer.ErrCancelled):
		return app.Errorf(app.CodeInvalidRequest, err)
	default:
		return app.Errorf(app.CodeInternal, err)
	}
}

// handleContentTicket mints a scoped ticket for one item.
//
// The caller must already be a party to the transfer, so the ticket grants
// nothing it did not already have. It exists only because a download cannot
// carry a header.
func (d Deps) handleContentTicket(s *Session, w http.ResponseWriter, r *http.Request) {
	tr, ok := d.participantTransfer(s, w, r)
	if !ok {
		return
	}

	index, _, ok := d.itemOf(tr, w, r)
	if !ok {
		return
	}

	scope := ContentScope(tr.ID.String(), index)
	value, err := d.Tickets.IssueScoped(s.DeviceID, scope)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"ticket":     value,
		"expires_in": int(contentTicketTTL.Seconds()),
	})
}

// handleFetchContentTicketed authorizes a download by header or by scoped
// ticket, then hands over to the streaming path.
//
// Both are accepted because both are legitimate: a client fetching
// programmatically sets a header, and the browser's download manager cannot.
func (d Deps) handleFetchContentTicketed(w http.ResponseWriter, r *http.Request) {
	trusted := d.Trusted != nil && d.Trusted(r)

	if ps, err := d.Sessions.Resolve(r, trusted); err == nil {
		d.handleFetchContent(&Session{Session: ps, deps: d}, w, r)
		return
	}

	id := store.ID(r.PathValue("id"))
	index, convErr := strconv.Atoi(r.PathValue("index"))
	if convErr != nil || id.Validate() != nil {
		d.writeError(w, r, app.New(app.CodeInvalidRequest))
		return
	}

	deviceID, err := d.Tickets.RedeemScoped(
		r.URL.Query().Get("ticket"), ContentScope(id.String(), index))
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeUnauthorized, err))
		return
	}

	// The pairing may have been revoked since the ticket was minted, and
	// revocation means immediately.
	p, err := d.Store.ActivePairing(deviceID)
	if err != nil {
		d.writeError(w, r, authError(err))
		return
	}

	d.handleFetchContent(&Session{
		Session: &pairing.Session{DeviceID: deviceID, Pairing: p, Trusted: trusted},
		deps:    d,
	}, w, r)
}
