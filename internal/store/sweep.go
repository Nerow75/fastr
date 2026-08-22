package store

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// The retention sweep, per data-model.md and FR-034.
//
// One sweep, run at startup and daily, covering everything that expires:
//
//	| Abandoned partial transfers | 7 days of inactivity |
//	| Relay staging data          | 7 days               |
//	| Pairings, auto trust        | 1 year of inactivity |
//	| Pairings, ask trust         | 30 days of inactivity|
//
// Two rules shape the implementation. The first is that a sweep that removes
// something **tells the user what went and why** (FR-034): deleting a week-old
// transfer silently would be indistinguishable, from the outside, from losing
// it. So the sweep returns what it took rather than a count, and the caller
// turns that into events and a notification.
//
// The second is that this layer removes records, not files. Deleting bytes
// belongs to whoever holds the staging directory, and it is done after the
// record is gone: a deleted file with a surviving record is a transfer that can
// never resume, while a surviving file with a deleted record is one sweep away
// from being cleaned up anyway.

// PartialRetention is how long the partial data of an inactive transfer is kept.
const PartialRetention = 7 * 24 * time.Hour

// RemovalKind says what a sweep took away.
type RemovalKind string

const (
	// RemovedTransfer is an abandoned transfer and the partial data behind it.
	RemovedTransfer RemovalKind = "transfer"
	// RemovedPairing is a device whose trust lapsed through inactivity.
	RemovedPairing RemovalKind = "pairing"
	// RemovedRelay is staged data from a transfer relayed between two phones.
	RemovedRelay RemovalKind = "relay"
)

// Removal is one thing a sweep took away, in the terms a person would use.
//
// It carries a name rather than only an identifier because it is shown: FR-034
// asks the user to be told what went, and "01H8… was removed" tells them
// nothing they can act on.
type Removal struct {
	Kind RemovalKind `json:"kind"`
	ID   string      `json:"id"`
	Name string      `json:"name"`

	// Bytes is how much data went, for a transfer or a relay session.
	Bytes uint64 `json:"bytes,omitempty"`
	// Path is staged data the caller must now delete. Set for relay sessions,
	// whose staging path is recorded rather than derived.
	Path string `json:"path,omitempty"`
	// IdleFor is how long the thing had been untouched, so the reason given can
	// be the honest one rather than a restatement of the rule.
	IdleFor time.Duration `json:"idle_for,omitempty"`
}

// Sweep removes everything past its retention window and reports what it took.
//
// now is a parameter rather than read from the clock so a caller can sweep
// against a fixed instant, and so tests can exercise a year of inactivity
// without waiting one.
func (s *Store) Sweep(now time.Time) ([]Removal, error) {
	removals := make([]Removal, 0)

	transfers, err := s.sweepTransfers(now)
	if err != nil {
		return nil, err
	}
	removals = append(removals, transfers...)

	pairings, err := s.sweepPairings(now)
	if err != nil {
		return nil, err
	}
	removals = append(removals, pairings...)

	relays, err := s.sweepRelays(now)
	if err != nil {
		return nil, err
	}
	return append(removals, relays...), nil
}

// AbandonedTransfers lists the transfers a sweep at now would take.
//
// Separate from the removal so a caller can look before it leaps: the app layer
// releases open staging files for these before the records go, and on Windows a
// file still held open cannot be deleted at all.
func (s *Store) AbandonedTransfers(now time.Time) ([]Transfer, error) {
	all, err := s.Transfers()
	if err != nil {
		return nil, err
	}

	var out []Transfer
	for _, t := range all {
		if t.State.Terminal() {
			continue
		}
		if t.Idle(now) >= PartialRetention {
			out = append(out, t)
		}
	}
	return out, nil
}

// sweepTransfers ends abandoned transfers and drops their records.
//
// They are failed rather than deleted outright, and the failure is recorded in
// history with a cause: a transfer that vanishes leaves the user wondering
// whether it worked, which FR-037 and FR-038 both exist to prevent. The
// transfer record itself then goes, because nothing can resume it any more.
func (s *Store) sweepTransfers(now time.Time) ([]Removal, error) {
	abandoned, err := s.AbandonedTransfers(now)
	if err != nil {
		return nil, err
	}

	removals := make([]Removal, 0, len(abandoned))
	for _, t := range abandoned {
		idle := t.Idle(now)

		// Through failed rather than straight to deleted, so the transition
		// rules are respected and the history entry has a cause to record.
		if err := s.SetTransferState(t.ID, StateFailed, CauseAbandoned); err != nil {
			return nil, fmt.Errorf("abandon %s: %w", t.ID, err)
		}
		ended, err := s.Transfer(t.ID)
		if err != nil {
			return nil, err
		}
		if err := s.RecordHistory(ended, ""); err != nil {
			return nil, fmt.Errorf("record abandoned %s: %w", t.ID, err)
		}
		if err := s.Dequeue(t.ID); err != nil {
			return nil, err
		}
		if err := s.Deactivate(t.ID, false); err != nil {
			return nil, err
		}
		if err := s.DeleteTransfer(t.ID); err != nil {
			return nil, err
		}

		removals = append(removals, Removal{
			Kind:    RemovedTransfer,
			ID:      t.ID.String(),
			Name:    firstName(t),
			Bytes:   t.TransferredBytes,
			IdleFor: idle,
		})
	}
	return removals, nil
}

// firstName is what the user would call the transfer: its first file, and how
// many others came with it is the caller's business to phrase.
func firstName(t Transfer) string {
	if len(t.Items) == 0 {
		return ""
	}
	return t.Items[0].OriginalName
}

// sweepPairings removes trust that lapsed through inactivity, per FR-016.
//
// The device record stays. A device that has been away for a year is still one
// the user may recognise when it comes back, and leaving the name behind means
// the interface can say "this device is no longer connected" rather than
// forgetting it existed. Access is what expires, and access is only the
// pairing.
func (s *Store) sweepPairings(now time.Time) ([]Removal, error) {
	pairings, err := s.Pairings()
	if err != nil {
		return nil, err
	}

	removals := make([]Removal, 0)
	for _, p := range pairings {
		if !p.Expired(now) {
			continue
		}

		name := p.DeviceID
		if dev, err := s.Device(p.DeviceID); err == nil && dev.Name != "" {
			name = dev.Name
		}

		if err := s.db.Update(func(tx *bolt.Tx) error {
			return tx.Bucket(bucketPairings).Delete([]byte(p.DeviceID))
		}); err != nil {
			return nil, err
		}

		removals = append(removals, Removal{
			Kind:    RemovedPairing,
			ID:      p.DeviceID,
			Name:    name,
			IdleFor: now.Sub(p.LastActivity),
		})
	}
	return removals, nil
}

// sweepRelays drops expired relay sessions, handing their staging paths back so
// the caller can delete the bytes.
func (s *Store) sweepRelays(now time.Time) ([]Removal, error) {
	sessions, err := s.RelaySessions()
	if err != nil {
		return nil, err
	}

	removals := make([]Removal, 0)
	for _, r := range sessions {
		if !r.Expired(now) {
			continue
		}
		if err := s.DeleteRelaySession(r.TransferID); err != nil {
			return nil, err
		}
		removals = append(removals, Removal{
			Kind:    RemovedRelay,
			ID:      r.TransferID.String(),
			Bytes:   r.BytesStaged,
			Path:    r.StagingPath,
			IdleFor: now.Sub(r.CreatedAt),
		})
	}
	return removals, nil
}
