package store

import (
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"
)

// Outcome is how a transfer ended. Only terminal states appear here.
type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
)

// HistoryEntry is the durable record of a finished transfer.
//
// The peer's name and identifier are denormalized on purpose: history must
// remain readable after the device is deleted or the pairing revoked, and a
// dangling reference would produce an entry the user cannot interpret.
type HistoryEntry struct {
	TransferID ID        `json:"transfer_id"`
	Direction  Direction `json:"direction"`

	PeerName     string `json:"peer_name"`
	PeerDeviceID string `json:"peer_device_id"`

	ItemCount  int    `json:"item_count"`
	TotalBytes uint64 `json:"total_bytes"`

	Outcome      Outcome      `json:"outcome"`
	FailureCause FailureCause `json:"failure_cause,omitempty"`

	// ProtectionMode lets the user see afterwards which transfers were
	// encrypted and which were not, which the honesty duty in Principle V
	// makes more than a curiosity.
	ProtectionMode Protection `json:"protection"`

	EndedAt time.Time `json:"ended_at"`
}

// outcomeFor maps a terminal state to its history outcome.
func outcomeFor(s State) (Outcome, error) {
	switch s {
	case StateCompleted:
		return OutcomeCompleted, nil
	case StateFailed:
		return OutcomeFailed, nil
	case StateCancelled:
		return OutcomeCancelled, nil
	default:
		return "", fmt.Errorf("state %s is not terminal", s)
	}
}

// RecordHistory writes the history entry for a finished transfer.
//
// peerName is captured at this moment rather than looked up later, so renaming
// or deleting the device does not rewrite the past.
func (s *Store) RecordHistory(t Transfer, peerName string) error {
	outcome, err := outcomeFor(t.State)
	if err != nil {
		return err
	}

	peerID := t.TargetDeviceID
	if t.Direction == DirectionIncoming {
		peerID = t.SourceDeviceID
	}

	ended := s.clock()
	if t.EndedAt != nil {
		ended = *t.EndedAt
	}

	entry := HistoryEntry{
		TransferID:     t.ID,
		Direction:      t.Direction,
		PeerName:       peerName,
		PeerDeviceID:   peerID,
		ItemCount:      len(t.Items),
		TotalBytes:     t.TotalBytes,
		Outcome:        outcome,
		FailureCause:   t.FailureCause,
		ProtectionMode: t.ProtectionMode,
		EndedAt:        ended,
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx, bucketHistory, []byte(t.ID), entry)
	})
}

// History returns up to limit entries, newest first.
//
// Iteration walks the bucket backwards because the key is a time-sortable
// identifier, so "newest first" costs nothing and needs no index.
func (s *Store) History(limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	out := make([]HistoryEntry, 0, limit)

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHistory)
		if b == nil {
			return fmt.Errorf("missing bucket %s", bucketHistory)
		}
		c := b.Cursor()
		for k, v := c.Last(); k != nil && len(out) < limit; k, v = c.Prev() {
			if v == nil {
				continue
			}
			var e HistoryEntry
			if err := unmarshal(v, &e); err != nil {
				return fmt.Errorf("decode history/%s: %w", k, err)
			}
			out = append(out, e)
		}
		return nil
	})
	return out, err
}

// ClearHistory removes every entry. FR-039.
func (s *Store) ClearHistory() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bucketHistory); err != nil && !errors.Is(err, bolterrors.ErrBucketNotFound) {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bucketHistory)
		return err
	})
}

// --- relay sessions ----------------------------------------------------------

// RelayStagingWindow is how long relayed data survives without progress, the
// same 7 days as any abandoned partial transfer.
const RelayStagingWindow = 7 * 24 * time.Hour

// RelaySession is the temporary role a computer takes between two phones.
//
// The staging path is stored so cleanup is possible even after a crash, when
// nothing in memory remembers what was written.
type RelaySession struct {
	TransferID  ID        `json:"transfer_id"`
	StagingPath string    `json:"staging_path"`
	BytesStaged uint64    `json:"bytes_staged"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Expired reports whether the session may be swept.
func (r RelaySession) Expired(now time.Time) bool {
	return !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt)
}

// CreateRelaySession records a relay in progress.
//
// The caller is responsible for the staging path being outside the receive
// folder; config.Settings.Validate makes that structurally true by refusing
// settings where one contains the other.
func (s *Store) CreateRelaySession(transferID ID, stagingPath string) (RelaySession, error) {
	if stagingPath == "" {
		return RelaySession{}, errors.New("relay staging path is empty")
	}
	now := s.clock()
	r := RelaySession{
		TransferID:  transferID,
		StagingPath: stagingPath,
		CreatedAt:   now,
		ExpiresAt:   now.Add(RelayStagingWindow),
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx, bucketRelays, []byte(transferID), r)
	})
	return r, err
}

// RelaySession returns a relay session by transfer.
func (s *Store) RelaySession(transferID ID) (RelaySession, error) {
	var r RelaySession
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx, bucketRelays, []byte(transferID), &r)
	})
	return r, err
}

// RelaySessions returns every relay session, which is what the relaying user
// sees when they look at what is passing through their machine. FR-056.
func (s *Store) RelaySessions() ([]RelaySession, error) {
	var out []RelaySession
	err := s.db.View(func(tx *bolt.Tx) error {
		return forEachJSON(tx, bucketRelays, func(_ string, r RelaySession) error {
			out = append(out, r)
			return nil
		})
	})
	return out, err
}

// AdvanceRelay records staged bytes and pushes the expiry out, so a slow but
// live relay is not swept mid-transfer.
func (s *Store) AdvanceRelay(transferID ID, staged uint64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var r RelaySession
		if err := getJSON(tx, bucketRelays, []byte(transferID), &r); err != nil {
			return err
		}
		r.BytesStaged = staged
		r.ExpiresAt = s.clock().Add(RelayStagingWindow)
		return putJSON(tx, bucketRelays, []byte(transferID), r)
	})
}

// DeleteRelaySession removes the record. The caller deletes the staged bytes;
// FR-055 requires both, and the record is removed last so a crash between the
// two leaves something for the sweep to find rather than an orphaned file.
func (s *Store) DeleteRelaySession(transferID ID) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRelays).Delete([]byte(transferID))
	})
}
