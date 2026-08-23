package app

import (
	"fmt"
	"path/filepath"

	"github.com/Nerow75/fastr/internal/store"
	"github.com/Nerow75/fastr/internal/transfer"
)

// Relaying between two phones, per FR-053 to FR-058.
//
// This is the only case where the computer holds data that is not its own, and
// the shape follows from that. It has **two halves**, and the transfer is not
// finished until both are:
//
//  1. The sending phone pushes chunks, exactly as it would to this computer.
//     They land in a relay directory, are hashed on the way in, and are
//     verified against the sender's digest before anything else happens.
//  2. The receiving phone fetches them, exactly as it would fetch a file this
//     computer was sending. Only when that finishes does the transfer complete
//     and the staged bytes go.
//
// So a relayed item that has finished uploading sits in `verifying`: the bytes
// are here and checked, waiting to be handed on. Marking it completed at that
// point would tell the sender the file had arrived somewhere it had not.
//
// Two things this machine never does with those bytes: write them into its
// receive folder, or keep them. The first is structural — they go to a
// directory that is not the receive folder, and cannot be reached from it — and
// the second is every terminal path calling Discard, with the retention sweep
// catching whatever a crash left behind.

// Relaying reports whether this machine is passing a transfer between two other
// devices.
func (t *Transfers) Relaying(tr store.Transfer) bool {
	return tr.Direction == store.DirectionRelayed &&
		t.SelfID != "" && tr.RelayDeviceID == t.SelfID
}

// Writes reports whether the bytes of this transfer pass through this machine's
// disk at all, whichever of the two reasons applies: it is the destination, or
// it is the relay. Anything else is piped between two browsers and never
// touches the disk.
func (t *Transfers) Writes(tr store.Transfer) bool {
	return t.Incoming(tr) || t.Relaying(tr)
}

// writeDir is where this machine stages the bytes of a transfer.
//
// The receive folder's staging area for something addressed here; a directory
// of its own for something merely passing through. Keeping them apart is what
// makes "relayed data never appears as a file this computer received" a
// property of the layout rather than a promise about cleanup code.
func (t *Transfers) writeDir(tr store.Transfer) string {
	_, staging := t.folders()
	if t.Relaying(tr) {
		return transfer.RelayDir(staging, tr.ID.String())
	}
	return staging
}

// RelayItemPath is where a verified relayed item waits for the receiving phone.
func (t *Transfers) RelayItemPath(tr store.Transfer, index int) string {
	return t.relayPath(tr, index)
}

// relayPath is where a verified relayed item waits for the receiving phone.
//
// The name is built from the index and the stored name, both of which this
// machine chose: the index is an integer and the stored name has already been
// through the destination's filename rules, so nothing a peer supplied reaches
// the filesystem unchecked.
func (t *Transfers) relayPath(tr store.Transfer, index int) string {
	name := fmt.Sprintf("%d-%s", index, tr.Items[index].StoredName)
	return filepath.Join(t.writeDir(tr), name)
}

// discardRelay removes everything a relayed transfer staged here. FR-055.
func (t *Transfers) discardRelay(tr store.Transfer) {
	if !t.Relaying(tr) {
		return
	}
	_, staging := t.folders()
	if err := transfer.DiscardRelay(staging, tr.ID.String()); err != nil {
		t.Log.Warn("discard relayed data", "transfer_id", tr.ID.String(), "error", err)
	}
}

// RelayedTransfers lists what is currently passing through this machine, with
// how much of it is on disk. FR-056: the relaying user can see it, and cancel.
func (t *Transfers) RelayedTransfers() ([]store.Transfer, error) {
	all, err := t.Store.Transfers()
	if err != nil {
		return nil, Errorf(CodeInternal, err)
	}

	out := make([]store.Transfer, 0)
	for _, tr := range all {
		if t.Relaying(tr) && !tr.State.Terminal() {
			out = append(out, tr)
		}
	}
	return out, nil
}

// RelayResidue reports transfers whose staged bytes are still on disk.
//
// Nothing should ever be here for a transfer that has ended; this is what SC-019
// is measured against, and what the retention sweep clears when a crash leaves
// something behind.
func (t *Transfers) RelayResidue() ([]string, error) {
	_, staging := t.folders()

	residue, err := transfer.RelayResidue(staging)
	if err != nil {
		return nil, Errorf(CodeInternal, err)
	}
	return residue, nil
}

// SweepRelayed deletes staged bytes belonging to transfers that have ended or
// no longer exist, and reports how many directories went.
func (t *Transfers) SweepRelayed() int {
	residue, err := t.RelayResidue()
	if err != nil {
		t.Log.Warn("relay residue", "error", err)
		return 0
	}

	_, staging := t.folders()
	removed := 0
	for _, id := range residue {
		tr, err := t.Store.Transfer(store.ID(id))
		if err == nil && !tr.State.Terminal() {
			continue // still in flight, and still its owner's
		}
		if err := transfer.DiscardRelay(staging, id); err != nil {
			t.Log.Warn("sweep relayed data", "transfer_id", id, "error", err)
			continue
		}
		removed++
	}
	return removed
}

// stageRelayed marks a relayed item as verified and waiting to be collected.
//
// `verifying` rather than `completed`: the bytes are here and checked, and the
// phone they belong to has not seen them yet. The transfer itself keeps running
// until it has.
func (t *Transfers) stageRelayed(tr store.Transfer, index int) error {
	current, err := t.Get(tr.ID)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(current.Items) {
		return New(CodeInvalidRequest)
	}

	current.Items[index].State = store.StateVerifying
	if err := t.Store.PutTransfer(current); err != nil {
		return Errorf(CodeInternal, err)
	}

	t.publish("transfer_progress", current, map[string]any{
		"item":    index,
		"staged":  true,
		"waiting": "receiver",
	})
	return nil
}

// RelayStagedBytes is how much of a relayed transfer is on this disk right now.
//
// Read from the filesystem rather than from the record: the number the relaying
// user cares about is how much of their space somebody else's file is using,
// and only the disk can answer that honestly.
func (t *Transfers) RelayStagedBytes(tr store.Transfer) (uint64, error) {
	if !t.Relaying(tr) {
		return 0, nil
	}
	_, staging := t.folders()

	staged, err := transfer.RelayBytes(staging, tr.ID.String())
	if err != nil {
		return 0, Errorf(CodeInternal, err)
	}
	return staged, nil
}
