package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/Nerow75/fastr/internal/pairing"
	"github.com/Nerow75/fastr/internal/platform"
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

// Announcer shows a native notification on this machine, for the moments the
// browser cannot reach: a transfer finishing while no page is open. Optional.
type Announcer interface {
	NotifyReceived(itemCount int, firstName, folder string)
	// NotifySwept says that the retention sweep removed something. FR-034
	// requires the user to be told, and a sweep runs at startup, which is
	// exactly when no page is open to hear about it.
	NotifySwept(count int)
}

// Transfers is the transfer service.
type Transfers struct {
	Store *store.Store
	Pipes *transfer.Pipes
	// Sinks holds the open staging files of incoming transfers. Only an upward
	// transfer uses them; the pipe never touches disk.
	Sinks  *transfer.Sinks
	Notify Notifier
	Space  SpaceChecker
	Log    *slog.Logger

	// SelfID is this instance's device identifier. A transfer aimed at it is
	// incoming and is written here, rather than piped to a third device.
	SelfID string
	// Rules is the destination's filename rule set. It is this machine's,
	// because FR-024 makes sanitization the receiver's decision: a phone has no
	// business deciding what a Windows disk will accept.
	Rules platform.FilenameRules

	// ReceiveD and StagingD are the folders at construction. After that they are
	// read through folders() and changed through SetFolders, because the user
	// can move the receive folder while the process runs.
	ReceiveD string
	StagingD string

	Announce Announcer

	foldersMu sync.RWMutex
	receiveD  string
	stagingD  string
	folderSet bool
}

// SetFolders changes where incoming files are written and staged.
//
// It affects transfers declared after it. FR-023 scenario 5: files already
// received stay where they are, and a transfer already running keeps writing to
// the path it resolved when it started, because moving a partial file out from
// under an open descriptor is how a resume ends up writing to nowhere.
func (t *Transfers) SetFolders(receive, staging string) {
	t.foldersMu.Lock()
	defer t.foldersMu.Unlock()

	t.receiveD, t.stagingD, t.folderSet = receive, staging, true
}

// folders returns the current destination and staging directories.
func (t *Transfers) folders() (receive, staging string) {
	t.foldersMu.RLock()
	defer t.foldersMu.RUnlock()

	if t.folderSet {
		return t.receiveD, t.stagingD
	}
	return t.ReceiveD, t.StagingD
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

	// A transfer aimed at this machine is written here. One from this machine to
	// a phone is piped between two browsers and never touches this disk. And one
	// between two *other* devices is relayed: it is written here too, but into a
	// directory of its own that the receive folder can never reach (FR-055).
	incoming := t.SelfID != "" && d.TargetDeviceID == t.SelfID
	relayed := t.SelfID != "" && !incoming && sourceDeviceID != t.SelfID
	receive, staging := t.folders()

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

		stored := store.TransferItem{
			OriginalName: item.Name,
			StoredName:   item.Name,
			RelativePath: item.RelativePath,
			Size:         item.Size,
			State:        store.StateQueued,
		}

		if incoming {
			// Resolved now so the sender learns straight away that its name had
			// to change, per FR-024, and so a name that cannot be placed at all
			// is refused before any byte moves. It is resolved again at
			// completion, because a collision can appear in between.
			res, err := transfer.Resolve(receive, item.RelativePath, item.Name, t.Rules)
			if err != nil {
				return store.Transfer{}, Errorf(CodeInvalidPath, err)
			}
			stored.StoredName = res.StoredName
		}

		items = append(items, stored)
	}

	// FR-058: a relay needs room for the whole thing in its own staging area,
	// and the answer has to come before any data moves rather than at ninety
	// percent of it.
	space := receive
	if relayed {
		space = staging
	}

	if t.Space != nil {
		if err := transfer.CheckSpace(t.Space, space, total); err != nil {
			var short *transfer.ErrInsufficientSpace
			if errors.As(err, &short) {
				return store.Transfer{}, Errorf(CodeInsufficientSpace, err).
					WithParam("needed", short.Needed).
					WithParam("available", short.Available)
			}
			return store.Transfer{}, Errorf(CodeInternal, err)
		}
	}

	direction := store.DirectionOutgoing
	relayID := ""
	switch {
	case incoming:
		direction = store.DirectionIncoming
	case relayed:
		direction = store.DirectionRelayed
		relayID = t.SelfID
	}

	// Whether these bytes may land on this disk unasked. Only for a transfer
	// this machine writes: one it merely pipes between two browsers never
	// touches the disk, so there is nothing for a human here to consent to.
	state := store.StateQueued
	if incoming || relayed {
		now := t.Store.Now()
		pair, err := t.Store.Pairing(sourceDeviceID)

		switch pairing.Decide(pair, err, now) {
		case pairing.Refuse:
			return store.Transfer{}, New(CodeUnauthorized)
		case pairing.Ask:
			// FR-016a: the user set this device to ask every time, so it waits
			// for a human rather than for the queue.
			state = store.StateAwaitingAcceptance
		case pairing.Start:
		}
	}

	// FR-054: trust is never transitive. Relaying between two phones needs
	// *both* of them paired with this computer — being paired with the sender
	// says nothing about the machine it wants to reach, and a relay that
	// assumed otherwise would let one visitor's phone push files to another's
	// simply by naming it.
	if relayed {
		target, err := t.Store.Pairing(d.TargetDeviceID)
		if pairing.Decide(target, err, t.Store.Now()) == pairing.Refuse {
			return store.Transfer{}, New(CodeUnauthorized)
		}
	}

	tr := store.Transfer{
		ID:             store.NewID(),
		Direction:      direction,
		SourceDeviceID: sourceDeviceID,
		TargetDeviceID: d.TargetDeviceID,
		RelayDeviceID:  relayID,
		ProtectionMode: store.ProtectionSimple,
		Items:          items,
		TotalBytes:     total,
		State:          state,
		// The store's clock, not the wall clock: every other timestamp on this
		// record comes from there, and a transfer whose queued_at disagreed with
		// its started_at would confuse the retention sweep more than it would
		// help anyone.
		QueuedAt: t.Store.Now(),
	}

	if err := t.Store.PutTransfer(tr); err != nil {
		return store.Transfer{}, Errorf(CodeInternal, err)
	}
	if err := t.Store.Enqueue(tr.ID); err != nil {
		return store.Transfer{}, Errorf(CodeInternal, err)
	}

	t.publish("transfer_queued", tr, nil)
	t.Log.Info("transfer queued", "transfer_id", tr.ID.String(),
		"items", len(items), "bytes", total, "state", string(state))

	return tr, nil
}

// Accept lets an incoming transfer from an ask-every-time device run. FR-016a.
//
// Only the receiving device may accept, which is the whole point of the mode:
// a sender that could accept on the recipient's behalf would make the setting
// decorative.
func (t *Transfers) Accept(id store.ID) (store.Transfer, error) {
	tr, err := t.Get(id)
	if err != nil {
		return store.Transfer{}, err
	}
	// A transfer that ended cannot be accepted, and saying "fine" would tell the
	// caller the opposite of the truth. The cause is carried through, so the
	// interface can say whether nobody answered in time or somebody said no.
	if tr.State.Terminal() {
		switch tr.FailureCause {
		case store.CauseAcceptanceTimeout:
			return store.Transfer{}, New(CodeAcceptanceTimeout)
		case store.CauseDeclined:
			return store.Transfer{}, New(CodeDeclined)
		default:
			return store.Transfer{}, New(CodeTransferNotFound)
		}
	}
	if tr.State != store.StateAwaitingAcceptance {
		// Already accepted, or never needed accepting. Not an error: two taps
		// on the same button should not produce a failure.
		return tr, nil
	}
	if pairing.AcceptanceExpired(tr.QueuedAt, t.Store.Now()) {
		t.Fail(id, store.CauseAcceptanceTimeout)
		return store.Transfer{}, New(CodeAcceptanceTimeout)
	}

	if err := t.Store.SetTransferState(id, store.StateQueued, ""); err != nil {
		return store.Transfer{}, Errorf(CodeInternal, err)
	}

	accepted, _ := t.Store.Transfer(id)
	t.publish("transfer_queued", accepted, map[string]any{"accepted": true})
	return accepted, nil
}

// Decline refuses an incoming transfer. FR-016a, and FR-038: the sender is told
// why rather than left watching a transfer that never starts.
func (t *Transfers) Decline(id store.ID) error {
	tr, err := t.Get(id)
	if err != nil {
		return err
	}
	if tr.State.Terminal() {
		return nil
	}
	t.Fail(id, store.CauseDeclined)
	return nil
}

// ExpireAcceptances fails every transfer that waited too long for an answer,
// and reports how many.
//
// FR-016d: an unanswered transfer is refused rather than queued indefinitely.
// Without this, a phone sending to a computer nobody is sitting at would hold
// the sender's attention forever, and its entry would sit in a queue that runs
// one thing at a time.
func (t *Transfers) ExpireAcceptances(now time.Time) int {
	all, err := t.Store.Transfers()
	if err != nil {
		t.Log.Warn("expire acceptances", "error", err)
		return 0
	}

	expired := 0
	for _, tr := range all {
		if tr.State != store.StateAwaitingAcceptance {
			continue
		}
		if !pairing.AcceptanceExpired(tr.QueuedAt, now) {
			continue
		}
		t.Fail(tr.ID, store.CauseAcceptanceTimeout)
		expired++
	}
	return expired
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
	// SC-019: whichever way a relayed transfer ended, zero bytes of it remain.
	t.discardRelay(finished)
	if err := t.Store.RecordHistory(finished, t.peerName(finished)); err != nil {
		t.Log.Error("record history", "transfer_id", id.String(), "error", err)
	}

	t.announceReceived(finished)

	t.publish("transfer_completed", finished, nil)
	t.Log.Info("transfer completed", "transfer_id", id.String(), "bytes", finished.TotalBytes)
	return nil
}

// announceReceived tells the desktop that files landed.
//
// Only for an incoming transfer, and only natively: an outgoing one finished
// because the user was watching a page, and saying so twice is noise. The
// receiving case is the one where nobody is necessarily looking.
func (t *Transfers) announceReceived(tr store.Transfer) {
	if t.Announce == nil || !t.Incoming(tr) || len(tr.Items) == 0 {
		return
	}
	receive, _ := t.folders()
	t.Announce.NotifyReceived(len(tr.Items), tr.Items[0].StoredName, receive)
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
		t.discardStaging(tr)
		t.discardRelay(tr)
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
	t.discardStaging(tr)
	t.discardRelay(tr)

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

// Recover repairs what a crash left behind, and reports how many transfers it
// touched. FR-035e.
//
// Nothing is running when a process starts, so a stored transfer that says
// `running` is a lie the previous process died holding, and a `verifying` one
// is the same lie caught a moment later. Left alone they are worse than
// cosmetic: the active slot is taken by a transfer nobody is driving, and
// FR-035a's single-slot rule then blocks every real transfer behind a ghost.
//
// They become interrupted rather than failed. The committed offsets are exact
// and the staged bytes are on disk, so this is precisely the state resume was
// built for; failing them would throw away a partial 10 GB file because the
// machine rebooted.
func (t *Transfers) Recover() (int, error) {
	all, err := t.Store.Transfers()
	if err != nil {
		return 0, Errorf(CodeInternal, err)
	}

	recovered := 0
	for _, tr := range all {
		if tr.State != store.StateRunning && tr.State != store.StateVerifying {
			continue
		}

		if err := t.Store.SetTransferState(tr.ID, store.StateInterrupted, ""); err != nil {
			t.Log.Warn("recover transfer", "transfer_id", tr.ID.String(), "error", err)
			continue
		}
		// Requeued rather than dropped: it is still something the user asked
		// for, and it goes behind whatever else is waiting rather than in front.
		if err := t.Store.Deactivate(tr.ID, true); err != nil {
			t.Log.Debug("deactivate", "transfer_id", tr.ID.String(), "error", err)
		}
		recovered++
	}

	if recovered > 0 {
		t.Log.Info("recovered transfers left running by a previous run", "count", recovered)
	}
	return recovered, nil
}

// Sweep applies the retention windows and says what it took. FR-034.
//
// Order matters here, and only in one direction: the staging files of the
// transfers about to be removed are released *before* the records go. A sink
// still open holds a file handle, and Windows refuses to delete a file that
// anything holds open, so a sweep in the other order would remove the record
// and leave the bytes behind forever, with nothing left pointing at them.
func (t *Transfers) Sweep(now time.Time) ([]store.Removal, error) {
	doomed, err := t.Store.AbandonedTransfers(now)
	if err != nil {
		return nil, Errorf(CodeInternal, err)
	}
	for _, tr := range doomed {
		t.discardStaging(tr)
	}

	removals, err := t.Store.Sweep(now)
	if err != nil {
		return nil, Errorf(CodeInternal, err)
	}

	// And whatever a crash left in the relay directory. Nothing should be there
	// for a transfer that has ended — every terminal path removes it — but a
	// process killed mid-relay leaves bytes that are not this machine's, and
	// FR-055 does not have an exception for that.
	if swept := t.SweepRelayed(); swept > 0 {
		t.Log.Info("cleared relayed data left by an earlier run", "transfers", swept)
	}
	if len(removals) == 0 {
		return removals, nil
	}

	// Relay sessions record their staging path rather than deriving it, so they
	// are the one kind whose bytes this layer cannot name in advance.
	var bytes uint64
	for _, r := range removals {
		bytes += r.Bytes
		if r.Path == "" {
			continue
		}
		if err := os.Remove(r.Path); err != nil && !os.IsNotExist(err) {
			t.Log.Warn("remove relay staging", "transfer_id", r.ID, "error", err)
		}
	}

	t.Log.Info("retention sweep", "removed", len(removals), "bytes", bytes)

	if t.Notify != nil {
		t.Notify.NotifyTransfer("sweep_removed", "", map[string]any{
			"removed": removals,
			"count":   len(removals),
			"bytes":   bytes,
		})
	}
	// A sweep runs at startup, which is precisely when no page is listening.
	// The event above reaches whoever is there; this reaches the user.
	if t.Announce != nil {
		t.Announce.NotifySwept(len(removals))
	}

	return removals, nil
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
