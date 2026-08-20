package store

import (
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Direction of a transfer relative to this instance.
type Direction string

const (
	DirectionOutgoing Direction = "outgoing"
	DirectionIncoming Direction = "incoming"
	DirectionRelayed  Direction = "relayed"
)

// State is a transfer's position in its lifecycle. The machine is drawn in
// data-model.md; CanTransition below is the authority.
type State string

const (
	StateQueued             State = "queued"
	StateAwaitingAcceptance State = "awaiting_acceptance"
	StateRunning            State = "running"
	StateVerifying          State = "verifying"
	StateInterrupted        State = "interrupted"
	StateCompleted          State = "completed"
	StateFailed             State = "failed"
	StateCancelled          State = "cancelled"
)

// Terminal reports whether a state is final. Only terminal transfers become
// history entries, and only terminal transfers release their staging data.
func (s State) Terminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateCancelled:
		return true
	}
	return false
}

// transitions is the complete set of legal moves. Anything absent is a bug in
// the caller, not a condition to tolerate: a transfer that reaches an
// unexpected state is how a partial file gets presented as complete.
var transitions = map[State][]State{
	StateQueued:             {StateAwaitingAcceptance, StateRunning, StateFailed, StateCancelled},
	StateAwaitingAcceptance: {StateRunning, StateFailed, StateCancelled},
	StateRunning:            {StateVerifying, StateInterrupted, StateFailed, StateCancelled},
	StateVerifying:          {StateCompleted, StateFailed, StateCancelled},
	// Interrupted is deliberately not terminal: it keeps its resume state and
	// steps aside so the queue keeps moving. FR-035d.
	StateInterrupted: {StateRunning, StateQueued, StateFailed, StateCancelled},
	StateCompleted:   nil,
	StateFailed:      nil,
	StateCancelled:   nil,
}

// CanTransition reports whether from -> to is legal.
func CanTransition(from, to State) bool {
	for _, allowed := range transitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ErrIllegalTransition is returned when a caller attempts an impossible move.
var ErrIllegalTransition = errors.New("illegal state transition")

// FailureCause is a stable, machine-readable reason. It maps to a translation
// key carrying a corrective action, per FR-038: no user ever sees a bare code.
type FailureCause string

const (
	CauseInsufficientSpace FailureCause = "insufficient_space"
	CauseChecksumMismatch  FailureCause = "checksum_mismatch"
	CauseNetworkLost       FailureCause = "network_lost"
	CauseDeclined          FailureCause = "declined"
	CauseAcceptanceTimeout FailureCause = "acceptance_timeout"
	CausePairingRevoked    FailureCause = "pairing_revoked"
	CausePairingExpired    FailureCause = "pairing_expired"
	CauseRelayUnavailable  FailureCause = "relay_unavailable"
	CauseTrustedRequired   FailureCause = "trusted_mode_required"
	CauseSourceUnreadable  FailureCause = "source_unreadable"
	CauseDestinationFull   FailureCause = "destination_full"
	CauseAbandoned         FailureCause = "abandoned"
)

// TransferItem is one file inside a transfer.
type TransferItem struct {
	// OriginalName is what the sender called it.
	OriginalName string `json:"original_name"`
	// StoredName is what the destination could accept. It differs from
	// OriginalName only when it had to, and the difference is reported.
	StoredName string `json:"stored_name"`
	// RelativePath positions the item inside a folder send. It is validated
	// against the destination root at write time, never at request time.
	RelativePath string `json:"relative_path,omitempty"`

	Size     uint64 `json:"size"`
	Checksum []byte `json:"checksum,omitempty"`

	// CommittedOffset is durably acknowledged bytes: the resume point. It only
	// advances after the data behind it has reached disk.
	CommittedOffset uint64 `json:"committed_offset"`

	State        State        `json:"state"`
	FailureCause FailureCause `json:"failure_cause,omitempty"`
}

// Remaining is how many bytes are still to move for this item.
func (i TransferItem) Remaining() uint64 {
	if i.CommittedOffset >= i.Size {
		return 0
	}
	return i.Size - i.CommittedOffset
}

// Transfer is one send operation between two devices.
type Transfer struct {
	ID        ID        `json:"id"`
	Direction Direction `json:"direction"`

	SourceDeviceID string `json:"source_device_id"`
	TargetDeviceID string `json:"target_device_id"`
	// RelayDeviceID is set only for a relayed transfer. FR-053.
	RelayDeviceID string `json:"relay_device_id,omitempty"`

	// ProtectionMode is fixed when the transfer starts. A downgrade must be
	// refused or explicitly confirmed, never silent. FR-047e.
	ProtectionMode Protection `json:"protection"`

	Items      []TransferItem `json:"items"`
	TotalBytes uint64         `json:"total_bytes"`
	// TransferredBytes is monotonic within an attempt and restored on resume.
	TransferredBytes uint64 `json:"transferred_bytes"`

	State        State        `json:"state"`
	FailureCause FailureCause `json:"failure_cause,omitempty"`

	QueuedAt  time.Time  `json:"queued_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// Validate checks the rules from data-model.md.
func (t Transfer) Validate() error {
	if err := t.ID.Validate(); err != nil {
		return fmt.Errorf("transfer id: %w", err)
	}
	if len(t.Items) == 0 {
		return errors.New("transfer has no items")
	}
	if t.SourceDeviceID == "" || t.TargetDeviceID == "" {
		return errors.New("transfer is missing a device")
	}
	if t.SourceDeviceID == t.TargetDeviceID {
		return errors.New("source and target are the same device")
	}
	if t.Direction == DirectionRelayed && t.RelayDeviceID == "" {
		return errors.New("relayed transfer has no relay device")
	}
	if t.Direction != DirectionRelayed && t.RelayDeviceID != "" {
		return errors.New("only a relayed transfer may name a relay device")
	}

	var sum uint64
	for i, item := range t.Items {
		if item.OriginalName == "" {
			return fmt.Errorf("item %d has no name", i)
		}
		sum += item.Size
	}
	if t.TotalBytes != sum {
		return fmt.Errorf("total_bytes is %d but items sum to %d", t.TotalBytes, sum)
	}
	return nil
}

// Progress is the fraction transferred, between 0 and 1.
func (t Transfer) Progress() float64 {
	if t.TotalBytes == 0 {
		return 1
	}
	return float64(t.TransferredBytes) / float64(t.TotalBytes)
}

// --- persistence -------------------------------------------------------------

// PutTransfer stores or replaces a transfer.
func (s *Store) PutTransfer(t Transfer) error {
	if err := t.Validate(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx, bucketTransfer, []byte(t.ID), t)
	})
}

// Transfer returns a transfer by identifier.
func (s *Store) Transfer(id ID) (Transfer, error) {
	var t Transfer
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx, bucketTransfer, []byte(id), &t)
	})
	return t, err
}

// Transfers returns every stored transfer, in creation order because the
// identifier sorts that way.
func (s *Store) Transfers() ([]Transfer, error) {
	var out []Transfer
	err := s.db.View(func(tx *bolt.Tx) error {
		return forEachJSON(tx, bucketTransfer, func(_ string, t Transfer) error {
			out = append(out, t)
			return nil
		})
	})
	return out, err
}

// SetTransferState moves a transfer, refusing illegal transitions.
//
// Timestamps are stamped here rather than by callers, so a transfer cannot end
// up completed without an end time.
func (s *Store) SetTransferState(id ID, to State, cause FailureCause) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var t Transfer
		if err := getJSON(tx, bucketTransfer, []byte(id), &t); err != nil {
			return err
		}
		if t.State == to {
			return nil
		}
		if !CanTransition(t.State, to) {
			return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, t.State, to)
		}

		now := s.clock()
		t.State = to
		switch {
		case to == StateRunning && t.StartedAt == nil:
			t.StartedAt = &now
		case to.Terminal():
			t.EndedAt = &now
		}
		if to == StateFailed {
			t.FailureCause = cause
		}

		return putJSON(tx, bucketTransfer, []byte(id), t)
	})
}

// AdvanceItem records durable progress for one item.
//
// The offset may only move forward. A caller that tries to rewind it is
// resuming from stale state, and honouring that would corrupt the file.
func (s *Store) AdvanceItem(id ID, index int, committed uint64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var t Transfer
		if err := getJSON(tx, bucketTransfer, []byte(id), &t); err != nil {
			return err
		}
		if index < 0 || index >= len(t.Items) {
			return fmt.Errorf("item index %d out of range", index)
		}

		item := &t.Items[index]
		if committed < item.CommittedOffset {
			return fmt.Errorf("committed offset would move backwards: %d -> %d",
				item.CommittedOffset, committed)
		}
		if committed > item.Size {
			return fmt.Errorf("committed offset %d exceeds size %d", committed, item.Size)
		}

		delta := committed - item.CommittedOffset
		item.CommittedOffset = committed
		t.TransferredBytes += delta

		return putJSON(tx, bucketTransfer, []byte(id), t)
	})
}

// DeleteTransfer removes a transfer record.
func (s *Store) DeleteTransfer(id ID) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTransfer).Delete([]byte(id))
	})
}

// --- queue -------------------------------------------------------------------

var (
	keyQueueEntries = []byte("entries")
	keyQueueActive  = []byte("active")
)

// ErrQueueBusy is returned when a second transfer tries to become active.
var ErrQueueBusy = errors.New("another transfer is active")

// Queue is the ordered list of waiting transfers plus the single active one.
type Queue struct {
	Entries  []ID `json:"entries"`
	ActiveID ID   `json:"active_id,omitempty"`
}

// Queue returns the current queue.
func (s *Store) Queue() (Queue, error) {
	var q Queue
	err := s.db.View(func(tx *bolt.Tx) error {
		return s.readQueue(tx, &q)
	})
	return q, err
}

func (s *Store) readQueue(tx *bolt.Tx, q *Queue) error {
	b := tx.Bucket(bucketQueue)
	if b == nil {
		return fmt.Errorf("missing bucket %s", bucketQueue)
	}
	if err := getJSON(tx, bucketQueue, keyQueueEntries, &q.Entries); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if raw := b.Get(keyQueueActive); raw != nil {
		q.ActiveID = ID(raw)
	}
	return nil
}

func (s *Store) writeQueue(tx *bolt.Tx, q Queue) error {
	if q.Entries == nil {
		q.Entries = []ID{}
	}
	if err := putJSON(tx, bucketQueue, keyQueueEntries, q.Entries); err != nil {
		return err
	}
	b := tx.Bucket(bucketQueue)
	if q.ActiveID == "" {
		return b.Delete(keyQueueActive)
	}
	return b.Put(keyQueueActive, []byte(q.ActiveID))
}

// Enqueue appends a transfer to the waiting list.
func (s *Store) Enqueue(id ID) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var q Queue
		if err := s.readQueue(tx, &q); err != nil {
			return err
		}
		for _, e := range q.Entries {
			if e == id {
				return nil // already waiting; enqueueing twice is not an error
			}
		}
		q.Entries = append(q.Entries, id)
		return s.writeQueue(tx, q)
	})
}

// Activate promotes a waiting transfer to active.
//
// FR-035a: at most one transfer runs at a time. This is the single place that
// invariant is enforced, so it cannot be bypassed by a caller in a hurry.
func (s *Store) Activate(id ID) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var q Queue
		if err := s.readQueue(tx, &q); err != nil {
			return err
		}
		if q.ActiveID != "" && q.ActiveID != id {
			return fmt.Errorf("%w: %s", ErrQueueBusy, q.ActiveID)
		}

		out := q.Entries[:0]
		for _, e := range q.Entries {
			if e != id {
				out = append(out, e)
			}
		}
		q.Entries = out
		q.ActiveID = id
		return s.writeQueue(tx, q)
	})
}

// Deactivate clears the active slot. When requeue is true the transfer goes to
// the back of the queue rather than being dropped, which is what an interrupted
// transfer does so it stops blocking the head. FR-035d.
func (s *Store) Deactivate(id ID, requeue bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var q Queue
		if err := s.readQueue(tx, &q); err != nil {
			return err
		}
		if q.ActiveID != id {
			return nil
		}
		q.ActiveID = ""
		if requeue {
			q.Entries = append(q.Entries, id)
		}
		return s.writeQueue(tx, q)
	})
}

// Reorder replaces the waiting order.
//
// It refuses an ordering that adds, drops, or duplicates an entry, and it never
// touches the active transfer: FR-035c allows reordering what waits, not
// interfering with what runs.
func (s *Store) Reorder(order []ID) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var q Queue
		if err := s.readQueue(tx, &q); err != nil {
			return err
		}

		if len(order) != len(q.Entries) {
			return fmt.Errorf("reorder has %d entries, queue has %d", len(order), len(q.Entries))
		}
		want := make(map[ID]int, len(q.Entries))
		for _, e := range q.Entries {
			want[e]++
		}
		for _, e := range order {
			if e == q.ActiveID {
				return errors.New("cannot reorder the active transfer")
			}
			if want[e] == 0 {
				return fmt.Errorf("reorder names a transfer that is not queued: %s", e)
			}
			want[e]--
		}

		q.Entries = append([]ID(nil), order...)
		return s.writeQueue(tx, q)
	})
}

// Dequeue removes one waiting entry. The active transfer is not affected.
func (s *Store) Dequeue(id ID) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var q Queue
		if err := s.readQueue(tx, &q); err != nil {
			return err
		}
		out := q.Entries[:0]
		for _, e := range q.Entries {
			if e != id {
				out = append(out, e)
			}
		}
		q.Entries = out
		return s.writeQueue(tx, q)
	})
}

// ClearQueue empties the waiting list, leaving the active transfer running.
// FR-035c.
func (s *Store) ClearQueue() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var q Queue
		if err := s.readQueue(tx, &q); err != nil {
			return err
		}
		q.Entries = nil
		return s.writeQueue(tx, q)
	})
}
