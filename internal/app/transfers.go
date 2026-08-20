package app

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Nerow75/fastr/internal/store"
	"github.com/Nerow75/fastr/internal/transfer"
)

// Transfers orchestrates the store, the pipe, and the event stream.
//
// It is the only place that decides what a transfer's state should become, so
// the rules in data-model.md live in one file rather than being reconstructed
// at each endpoint.

// Notifier publishes an event. The HTTP layer supplies it, so this package does
// not depend on the transport.
type Notifier interface {
	NotifyTransfer(kind string, transferID store.ID, payload map[string]any)
}

// SpaceChecker reports free space at a path.
type SpaceChecker interface {
	FreeSpace(path string) (uint64, error)
}

// Transfers is the transfer service.
type Transfers struct {
	Store    *store.Store
	Pipes    *transfer.Pipes
	Notify   Notifier
	Space    SpaceChecker
	Log      *slog.Logger
	ReceiveD string
	StagingD string
}

// Declaration is what a sender proposes.
type Declaration struct {
	TargetDeviceID string
	Items          []DeclaredItem
}

// DeclaredItem is one file in a proposal.
type DeclaredItem struct {
	Name         string
	RelativePath string
	Size         uint64
}

// ErrNothingToSend reports an empty declaration.
var ErrNothingToSend = errors.New("the transfer has no files")

// Declare validates a proposal, checks space, and queues it.
//
// Space is checked here rather than when the bytes start arriving, per FR-028:
// discovering a full disk at 90% of a 10 GB transfer is the worst possible
// moment and it is entirely avoidable.
func (t *Transfers) Declare(sourceDeviceID string, d Declaration) (store.Transfer, error) {
	if len(d.Items) == 0 {
		return store.Transfer{}, Errorf(CodeInvalidRequest, ErrNothingToSend)
	}
	if d.TargetDeviceID == "" || d.TargetDeviceID == sourceDeviceID {
		return store.Transfer{}, New(CodeInvalidRequest)
	}

	var total uint64
	items := make([]store.TransferItem, 0, len(d.Items))
	for _, item := range d.Items {
		if item.Name == "" {
			return store.Transfer{}, New(CodeInvalidRequest)
		}
		// Overflow matters: the sizes come from a peer.
		if total > ^uint64(0)-item.Size {
			return store.Transfer{}, New(CodeInsufficientSpace)
		}
		total += item.Size

		items = append(items, store.TransferItem{
			OriginalName: item.Name,
			StoredName:   item.Name,
			RelativePath: item.RelativePath,
			Size:         item.Size,
			State:        store.StateQueued,
		})
	}

	if t.Space != nil {
		if err := transfer.CheckSpace(t.Space, t.ReceiveD, total); err != nil {
			var short *transfer.ErrInsufficientSpace
			if errors.As(err, &short) {
				return store.Transfer{}, Errorf(CodeInsufficientSpace, err).
					WithParam("needed", short.Needed).
					WithParam("available", short.Available)
			}
			return store.Transfer{}, Errorf(CodeInternal, err)
		}
	}

	tr := store.Transfer{
		ID:             store.NewID(),
		Direction:      store.DirectionOutgoing,
		SourceDeviceID: sourceDeviceID,
		TargetDeviceID: d.TargetDeviceID,
		ProtectionMode: store.ProtectionSimple,
		Items:          items,
		TotalBytes:     total,
		State:          store.StateQueued,
		QueuedAt:       time.Now(),
	}

	if err := t.Store.PutTransfer(tr); err != nil {
		return store.Transfer{}, Errorf(CodeInternal, err)
	}
	if err := t.Store.Enqueue(tr.ID); err != nil {
		return store.Transfer{}, Errorf(CodeInternal, err)
	}

	t.publish("transfer_queued", tr, nil)
	t.Log.Info("transfer queued",
		"transfer_id", tr.ID.String(), "items", len(items), "bytes", total)

	return tr, nil
}

// Get returns a transfer.
func (t *Transfers) Get(id store.ID) (store.Transfer, error) {
	tr, err := t.Store.Transfer(id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Transfer{}, New(CodeTransferNotFound)
	}
	if err != nil {
		return store.Transfer{}, Errorf(CodeInternal, err)
	}
	return tr, nil
}

// Participant reports whether a device is a party to a transfer.
//
// Every content endpoint asks this. Holding a valid credential is not enough:
// a paired phone must not be able to fetch a file being sent to a different
// paired phone just by guessing an identifier.
func Participant(tr store.Transfer, deviceID string) bool {
	return deviceID == tr.SourceDeviceID ||
		deviceID == tr.TargetDeviceID ||
		(tr.RelayDeviceID != "" && deviceID == tr.RelayDeviceID)
}

// Start moves a queued transfer to running, taking the single active slot.
func (t *Transfers) Start(id store.ID) error {
	if err := t.Store.Activate(id); err != nil {
		if errors.Is(err, store.ErrQueueBusy) {
			return Errorf(CodeQueueBusy, err)
		}
		return Errorf(CodeInternal, err)
	}
	if err := t.Store.SetTransferState(id, store.StateRunning, ""); err != nil {
		return Errorf(CodeInternal, err)
	}

	tr, _ := t.Store.Transfer(id)
	t.publish("transfer_started", tr, nil)
	return nil
}

// Progress records durable progress and publishes an update.
//
// The event bus throttles these; the store write is what makes a resume exact,
// so it is not throttled.
func (t *Transfers) Progress(id store.ID, index int, committed uint64) error {
	if err := t.Store.AdvanceItem(id, index, committed); err != nil {
		return Errorf(CodeInternal, err)
	}

	tr, err := t.Store.Transfer(id)
	if err != nil {
		return Errorf(CodeInternal, err)
	}
	t.publish("transfer_progress", tr, map[string]any{
		"item":        index,
		"committed":   committed,
		"transferred": tr.TransferredBytes,
		"total":       tr.TotalBytes,
	})
	return nil
}

// Interrupt marks a transfer as interrupted rather than failed, and releases
// the active slot so the queue keeps moving. FR-035d.
func (t *Transfers) Interrupt(id store.ID, reason error) {
	if err := t.Store.SetTransferState(id, store.StateInterrupted, ""); err != nil {
		t.Log.Debug("interrupt", "transfer_id", id.String(), "error", err)
	}
	if err := t.Store.Deactivate(id, true); err != nil {
		t.Log.Debug("deactivate", "transfer_id", id.String(), "error", err)
	}

	tr, _ := t.Store.Transfer(id)
	t.publish("transfer_interrupted", tr, map[string]any{"reason": fmt.Sprint(reason)})
}

// Complete verifies an item and, when every item is done, finishes the
// transfer and records history.
func (t *Transfers) Complete(id store.ID, index int) error {
	tr, err := t.Get(id)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(tr.Items) {
		return New(CodeInvalidRequest)
	}

	tr.Items[index].State = store.StateCompleted
	if err := t.Store.PutTransfer(tr); err != nil {
		return Errorf(CodeInternal, err)
	}

	for _, item := range tr.Items {
		if item.State != store.StateCompleted {
			return nil // still more to come
		}
	}

	if err := t.Store.SetTransferState(id, store.StateVerifying, ""); err != nil {
		return Errorf(CodeInternal, err)
	}
	if err := t.Store.SetTransferState(id, store.StateCompleted, ""); err != nil {
		return Errorf(CodeInternal, err)
	}
	if err := t.Store.Deactivate(id, false); err != nil {
		t.Log.Debug("deactivate", "transfer_id", id.String(), "error", err)
	}

	finished, _ := t.Store.Transfer(id)
	if err := t.Store.RecordHistory(finished, t.peerName(finished)); err != nil {
		t.Log.Error("record history", "transfer_id", id.String(), "error", err)
	}

	t.publish("transfer_completed", finished, nil)
	t.Log.Info("transfer completed", "transfer_id", id.String(), "bytes", finished.TotalBytes)
	return nil
}

// Fail ends a transfer with a cause, records history, and releases the slot.
func (t *Transfers) Fail(id store.ID, cause store.FailureCause) {
	if err := t.Store.SetTransferState(id, store.StateFailed, cause); err != nil {
		t.Log.Debug("fail", "transfer_id", id.String(), "error", err)
	}
	if err := t.Store.Deactivate(id, false); err != nil {
		t.Log.Debug("deactivate", "transfer_id", id.String(), "error", err)
	}

	tr, _ := t.Store.Transfer(id)
	if tr.ID != "" {
		if err := t.Store.RecordHistory(tr, t.peerName(tr)); err != nil {
			t.Log.Error("record history", "transfer_id", id.String(), "error", err)
		}
	}

	t.publish("transfer_failed", tr, map[string]any{"cause": string(cause)})
	t.Log.Info("transfer failed", "transfer_id", id.String(), "cause", string(cause))
}

// Cancel stops a transfer from either side. FR-035.
func (t *Transfers) Cancel(id store.ID) error {
	tr, err := t.Get(id)
	if err != nil {
		return err
	}
	if tr.State.Terminal() {
		return nil // already over; cancelling again is not an error
	}

	// Release the pipe first, or a waiting fetch would hold its connection
	// until the supply timeout rather than learning immediately.
	for index := range tr.Items {
		t.Pipes.Cancel(transfer.Key{TransferID: id.String(), ItemIndex: index})
	}

	if err := t.Store.SetTransferState(id, store.StateCancelled, ""); err != nil {
		return Errorf(CodeInternal, err)
	}
	if err := t.Store.Deactivate(id, false); err != nil {
		t.Log.Debug("deactivate", "transfer_id", id.String(), "error", err)
	}
	if err := t.Store.Dequeue(id); err != nil {
		t.Log.Debug("dequeue", "transfer_id", id.String(), "error", err)
	}

	cancelled, _ := t.Store.Transfer(id)
	if err := t.Store.RecordHistory(cancelled, t.peerName(cancelled)); err != nil {
		t.Log.Error("record history", "transfer_id", id.String(), "error", err)
	}

	t.publish("transfer_cancelled", cancelled, nil)
	return nil
}

// peerName resolves the other device's name for the history record, falling
// back to the identifier so an entry is never blank.
func (t *Transfers) peerName(tr store.Transfer) string {
	id := tr.TargetDeviceID
	if tr.Direction == store.DirectionIncoming {
		id = tr.SourceDeviceID
	}
	if dev, err := t.Store.Device(id); err == nil && dev.Name != "" {
		return dev.Name
	}
	return id
}

func (t *Transfers) publish(kind string, tr store.Transfer, payload map[string]any) {
	if t.Notify == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if tr.ID != "" {
		payload["state"] = string(tr.State)
		if len(tr.Items) > 0 {
			payload["name"] = tr.Items[0].OriginalName
		}
	}
	t.Notify.NotifyTransfer(kind, tr.ID, payload)
}
