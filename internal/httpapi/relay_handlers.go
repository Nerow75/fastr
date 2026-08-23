package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/store"
)

// The downward half of a relayed transfer, per FR-053 and FR-055.
//
// The sending phone has already pushed the file here and it was verified on
// arrival, so this is an ordinary file being served from disk — with one
// difference that matters: the bytes are not this machine's, and the moment the
// last of them leaves, they go.
//
// Range is honoured, like every other content route, so the receiving phone's
// download manager can resume without the application's help. That is the whole
// reason the relay stages rather than pipes: a phone cannot hold one streaming
// request open for the length of a file.

// serveRelayed streams a staged relayed item to the device it is meant for.
func (d Deps) serveRelayed(s *Session, tr store.Transfer, index int, offset uint64, w http.ResponseWriter, r *http.Request) {
	item := tr.Items[index]

	// Only the target collects. A relay that let the sender fetch back what it
	// had just pushed would turn this computer into storage for anyone paired
	// with it, which is the opposite of holding nothing.
	if s.DeviceID != tr.TargetDeviceID {
		d.writeError(w, r, app.New(app.CodeTransferNotFound))
		return
	}

	if item.State != store.StateVerifying && item.State != store.StateCompleted {
		// Still arriving from the other phone. Waiting is the right answer, and
		// it is the same one a busy queue gives.
		d.writeError(w, r, app.New(app.CodeAwaitingRelay))
		return
	}

	path := d.Transfers.RelayItemPath(tr, index)
	file, err := os.Open(path) //nolint:gosec // path is built by this process from a ULID and an index
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Swept, or cleared by a crash recovery. Saying so beats a 500.
			d.writeError(w, r, app.New(app.CodeTransferNotFound))
			return
		}
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}
	defer func() { _ = file.Close() }()

	if offset > 0 {
		if _, err := file.Seek(int64(offset), io.SeekStart); err != nil { //nolint:gosec // bounded by the declared size
			d.writeError(w, r, app.Errorf(app.CodeInternal, err))
			return
		}
	}

	remaining := item.Size - offset
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatUint(remaining, 10))
	// The sanitized name, not the original: the sender does not choose what the
	// receiver's browser writes.
	w.Header().Set("Content-Disposition", contentDisposition(item.StoredName))
	securityHeaders(w)

	status := http.StatusOK
	if offset > 0 {
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", offset, item.Size-1, item.Size))
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)

	written, copyErr := io.Copy(w, file)
	if copyErr != nil {
		// The receiving phone went away mid-download. The bytes are still here
		// and it can come back for them, which is what interrupted means.
		d.Transfers.Interrupt(tr.ID, copyErr)
		return
	}

	//nolint:gosec // a copy count is never negative
	if offset+uint64(written) >= item.Size {
		if err := d.Transfers.Complete(tr.ID, index); err != nil {
			d.Log.Error("complete relayed", "transfer_id", tr.ID.String(), "error", err)
		}
	}
}

// relayView is one transfer passing through this machine, as its user sees it.
type relayView struct {
	TransferID string `json:"transfer_id"`
	FromName   string `json:"from_name"`
	ToName     string `json:"to_name"`
	ItemCount  int    `json:"item_count"`
	TotalBytes uint64 `json:"total_bytes"`
	// Staged is how much of it is on this disk right now, which is the number
	// the relaying user actually cares about.
	Staged uint64 `json:"staged_bytes"`
	State  string `json:"state"`
	Name   string `json:"name"`
}

// handleRelayed lists what is passing through this computer. FR-056.
//
// Loopback only. This is the relaying user's own question about their own
// machine, and the answer names two other people's devices and their files —
// which is exactly the sort of thing a paired phone has no business reading.
func (d Deps) handleRelayed(s *Session, w http.ResponseWriter, r *http.Request) {
	passing, err := d.Transfers.RelayedTransfers()
	if err != nil {
		d.writeError(w, r, err)
		return
	}

	out := make([]relayView, 0, len(passing))
	for _, tr := range passing {
		staged, err := d.Transfers.RelayStagedBytes(tr)
		if err != nil {
			d.Log.Debug("relay size", "transfer_id", tr.ID.String(), "error", err)
		}

		name := ""
		if len(tr.Items) > 0 {
			name = tr.Items[0].OriginalName
		}

		out = append(out, relayView{
			TransferID: tr.ID.String(),
			FromName:   d.deviceName(tr.SourceDeviceID),
			ToName:     d.deviceName(tr.TargetDeviceID),
			ItemCount:  len(tr.Items),
			TotalBytes: tr.TotalBytes,
			Staged:     staged,
			State:      string(tr.State),
			Name:       name,
		})
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"relayed": out})
}

// deviceName is what to call a device on screen, falling back to its identifier
// so a row is never blank.
func (d Deps) deviceName(deviceID string) string {
	if dev, err := d.Store.Device(deviceID); err == nil && dev.Name != "" {
		return dev.Name
	}
	return deviceID
}
