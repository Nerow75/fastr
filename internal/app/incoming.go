package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Nerow75/fastr/internal/store"
	"github.com/Nerow75/fastr/internal/transfer"
)

// The receiving side of a phone-to-computer transfer, per User Story 2.
//
// This is the one direction where the server owns the file. The desktop-to-phone
// path pipes bytes between two browsers and never touches this disk (see
// internal/transfer/pipe.go); here the phone pushes chunks and the server
// writes, verifies, and places them.
//
// Three rules hold the whole path together, and each is enforced in one place:
//
//   - The destination name is the receiver's decision, never the sender's
//     (FR-024). transfer.Resolve applies this machine's filename rules and finds
//     a free name.
//   - Nothing appears at its final path before its checksum verifies (FR-033).
//     Bytes land in staging, which is a directory config.Settings.Validate
//     refuses to let overlap the receive folder.
//   - An offset is only acknowledged once the bytes behind it are fsynced
//     (FR-031). transfer.Sink owns that.

// ErrNotIncoming reports a write attempted against a transfer this machine is
// not the target of.
var ErrNotIncoming = errors.New("this transfer is not addressed to this device")

// Incoming reports whether this machine is the one writing the file.
func (t *Transfers) Incoming(tr store.Transfer) bool {
	return t.SelfID != "" && tr.TargetDeviceID == t.SelfID
}

// stagingKey names the staging file for one item.
//
// Both parts are already constrained to a safe alphabet — a ULID is Crockford
// base32 and the index is an integer — so the name cannot carry a separator or
// a traversal in from anywhere.
func stagingKey(id store.ID, index int) string {
	return fmt.Sprintf("%s-%d", id.String(), index)
}

// Receive appends a chunk to an incoming item and returns the new committed
// offset.
//
// The offset in the request is the sender's belief about what arrived. When it
// disagrees with the file, the file wins and the sender is told the real value,
// so it corrects rather than writes a hole (FR-031).
func (t *Transfers) Receive(
	ctx context.Context,
	tr store.Transfer,
	index int,
	offset uint64,
	body io.Reader,
) (uint64, error) {
	if !t.Incoming(tr) {
		return 0, Errorf(CodeInvalidRequest, ErrNotIncoming)
	}
	if index < 0 || index >= len(tr.Items) {
		return 0, New(CodeInvalidRequest)
	}
	if tr.State.Terminal() {
		return 0, New(CodeTransferNotFound)
	}

	if tr.State == store.StateQueued || tr.State == store.StateInterrupted {
		if err := t.Start(tr.ID); err != nil {
			return 0, err
		}
	}

	item := tr.Items[index]
	sink, err := t.sink(tr, index)
	if err != nil {
		return 0, Errorf(CodeInternal, err)
	}

	committed, err := sink.Append(ctx, offset, body, item.Size, nil)

	// Whatever arrived is durable and must be recorded even when the request
	// then failed: that is what makes the next attempt resume rather than
	// restart.
	if committed > item.CommittedOffset {
		if perr := t.Progress(tr.ID, index, committed); perr != nil {
			t.Log.Debug("progress", "transfer_id", tr.ID.String(), "error", perr)
		}
	}

	if err != nil {
		if errors.Is(err, transfer.ErrOffsetMismatch) {
			return committed, Errorf(CodeOffsetMismatch, err).
				WithParam("committed_offset", committed)
		}

		// A dropped connection is an interruption, not a failure: the state
		// keeps the resume point and steps aside so the queue moves (FR-035d).
		// A destination that cannot hold the file is the opposite, and calling
		// it an interruption would leave the sender retrying into a full disk
		// forever while the interface said "interrupted".
		switch fault := transfer.Classify(err); fault {
		case transfer.FaultDestinationFull:
			t.Fail(tr.ID, store.CauseDestinationFull)
			return committed, Errorf(CodeInsufficientSpace, err)
		case transfer.FaultDestinationUnwritable:
			t.Fail(tr.ID, store.CauseDestinationUnwritable)
			return committed, Errorf(CodeInternal, err)
		default:
			t.Interrupt(tr.ID, err)
			return committed, Errorf(CodeInternal, err)
		}
	}

	return committed, nil
}

// CommittedOffset reports where a resuming sender should continue from.
func (t *Transfers) CommittedOffset(tr store.Transfer, index int) (uint64, error) {
	if !t.Incoming(tr) {
		return 0, Errorf(CodeInvalidRequest, ErrNotIncoming)
	}
	if index < 0 || index >= len(tr.Items) {
		return 0, New(CodeInvalidRequest)
	}

	// The store, not the sink: the store is what survives a restart, and the
	// sink is brought into agreement with it when it is next opened. Opening a
	// sink here would create a staging file for an item nobody has sent yet.
	return tr.Items[index].CommittedOffset, nil
}

// CommitItem verifies an incoming item and moves it into the receive folder.
//
// expected is the digest the sender computed over the same bytes as it read
// them. A mismatch fails the transfer and deletes the partial file: FR-032
// forbids reporting a corrupted transfer as a success, and FR-033 forbids the
// file ever appearing at its final path.
func (t *Transfers) CommitItem(tr store.Transfer, index int, expected []byte) (store.Transfer, error) {
	if index < 0 || index >= len(tr.Items) {
		return store.Transfer{}, New(CodeInvalidRequest)
	}

	item := tr.Items[index]
	if item.State == store.StateCompleted {
		return tr, nil // already placed; completing twice is not an error
	}
	// A transfer that has ended stays ended. Without this a sender could keep
	// completing a failed item until an attempt got through, and worse, the
	// attempt would open a sink and re-create the staging file the failure had
	// just deleted. Invisible rather than refused, like every other lookup of a
	// transfer the caller has no business in.
	if tr.State.Terminal() {
		return store.Transfer{}, New(CodeTransferNotFound)
	}
	if item.CommittedOffset != item.Size {
		return store.Transfer{}, Errorf(CodeInvalidRequest,
			fmt.Errorf("item %d has %d of %d bytes", index, item.CommittedOffset, item.Size))
	}

	sink, err := t.sink(tr, index)
	if err != nil {
		return store.Transfer{}, Errorf(CodeInternal, err)
	}

	// Resolved again rather than reusing the name chosen at declaration: another
	// transfer, or the user, may have created that name in the meantime, and
	// FR-025 forbids overwriting whatever is there now.
	receive, _ := t.folders()
	res, err := transfer.Resolve(receive, item.RelativePath, item.OriginalName, t.Rules)
	if err != nil {
		t.Fail(tr.ID, store.CauseSourceUnreadable)
		return store.Transfer{}, Errorf(CodeInvalidPath, err)
	}

	if err := sink.Commit(expected, res.Path); err != nil {
		t.Sinks.Release(transfer.Key{TransferID: tr.ID.String(), ItemIndex: index})

		if errors.Is(err, transfer.ErrChecksumMismatch) {
			t.Fail(tr.ID, store.CauseChecksumMismatch)
			return store.Transfer{}, New(CodeChecksumMismatch)
		}
		t.Fail(tr.ID, store.CauseDestinationFull)
		return store.Transfer{}, Errorf(CodeInternal, err)
	}
	t.Sinks.Release(transfer.Key{TransferID: tr.ID.String(), ItemIndex: index})

	// The name actually used, which is what the sender is shown so it can say
	// what changed and why.
	if res.StoredName != item.StoredName {
		tr.Items[index].StoredName = res.StoredName
		if err := t.Store.PutTransfer(tr); err != nil {
			return store.Transfer{}, Errorf(CodeInternal, err)
		}
	}

	if err := t.Complete(tr.ID, index); err != nil {
		return store.Transfer{}, err
	}

	return t.Get(tr.ID)
}

// sink opens the staging file and hash state for one incoming item.
func (t *Transfers) sink(tr store.Transfer, index int) (*transfer.Sink, error) {
	if t.Sinks == nil {
		return nil, errors.New("no staging is configured for incoming transfers")
	}
	_, staging := t.folders()

	return t.Sinks.Open(
		transfer.Key{TransferID: tr.ID.String(), ItemIndex: index},
		staging,
		stagingKey(tr.ID, index),
		tr.Items[index].CommittedOffset,
	)
}

// discardStaging deletes the partial data of an incoming transfer that will
// never finish. FR-033: a partial file is not left where it could be mistaken
// for a received one, and staging is swept even though it already sits outside
// the receive folder.
func (t *Transfers) discardStaging(tr store.Transfer) {
	if t.Sinks == nil || !t.Incoming(tr) {
		return
	}
	_, staging := t.folders()

	for index := range tr.Items {
		key := transfer.Key{TransferID: tr.ID.String(), ItemIndex: index}
		if err := t.Sinks.Discard(key); err != nil {
			t.Log.Debug("discard staging", "transfer_id", tr.ID.String(), "item", index, "error", err)
		}
		// And by name, for the partials this process is not holding: a transfer
		// abandoned before a restart, or one whose sink was released on the way
		// to the failure that brought us here.
		if err := transfer.DiscardStaged(staging, stagingKey(tr.ID, index)); err != nil {
			t.Log.Debug("discard staged file", "transfer_id", tr.ID.String(), "item", index, "error", err)
		}
	}
}
