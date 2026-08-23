// Package store is the durable state of a fastr instance: devices, pairings,
// transfers, the queue, history, and relay sessions.
//
// It is one bbolt file. bbolt was chosen over a JSON file because FR-035e
// requires the queue to survive a restart while transfer progress is written
// constantly, and over SQLite because bbolt needs no cgo and cross-compilation
// must stay trivial (research.md section 9).
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Bucket names, matching data-model.md.
var (
	bucketMeta     = []byte("meta")
	bucketDevices  = []byte("devices")
	bucketPairings = []byte("pairings")
	bucketTransfer = []byte("transfers")
	bucketQueue    = []byte("queue")
	bucketRelays   = []byte("relays")
	bucketHistory  = []byte("history")
)

var allBuckets = [][]byte{
	bucketMeta, bucketDevices, bucketPairings,
	bucketTransfer, bucketQueue, bucketRelays, bucketHistory,
}

// ErrNotFound is returned when a key is absent.
var ErrNotFound = errors.New("not found")

// Store is the durable state. It is safe for concurrent use.
type Store struct {
	db  *bolt.DB
	now func() time.Time
}

// Open creates or opens the store at path, creating every bucket and applying
// migrations.
//
// The file holds session keys and hashed credentials, so it is created 0600 and
// its directory 0700. A world-readable store would hand a local attacker every
// pairing on the machine.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		// A timeout here means another instance holds the lock, which is the
		// single-instance guard doing its job. Say so plainly.
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}

	s := &Store{db: db, now: time.Now}

	if err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range allBuckets {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("create bucket %s: %w", name, err)
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

// Close releases the file lock.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path is the store file location.
func (s *Store) Path() string { return s.db.Path() }

// SetClock replaces the time source. Tests use it to exercise expiry and
// retention without waiting a year.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Now is the store's idea of the time. Callers that stamp a record they are
// about to store use it rather than time.Now, or a moved clock would produce a
// record whose timestamps disagree with each other.
func (s *Store) Now() time.Time { return s.clock() }

func (s *Store) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// putJSON stores a value as JSON under key in the named bucket.
func putJSON(tx *bolt.Tx, bucket, key []byte, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b := tx.Bucket(bucket)
	if b == nil {
		return fmt.Errorf("missing bucket %s", bucket)
	}
	return b.Put(key, data)
}

// getJSON loads a JSON value. It returns ErrNotFound when the key is absent,
// which callers distinguish from a real failure.
func getJSON(tx *bolt.Tx, bucket, key []byte, v any) error {
	b := tx.Bucket(bucket)
	if b == nil {
		return fmt.Errorf("missing bucket %s", bucket)
	}
	data := b.Get(key)
	if data == nil {
		return ErrNotFound
	}
	return json.Unmarshal(data, v)
}

// forEachJSON iterates a bucket, decoding each value. Returning an error from
// fn stops iteration and propagates.
func forEachJSON[T any](tx *bolt.Tx, bucket []byte, fn func(key string, v T) error) error {
	b := tx.Bucket(bucket)
	if b == nil {
		return fmt.Errorf("missing bucket %s", bucket)
	}
	return b.ForEach(func(k, data []byte) error {
		if data == nil {
			return nil // nested bucket, not a value
		}
		var v T
		if err := json.Unmarshal(data, &v); err != nil {
			return fmt.Errorf("decode %s/%s: %w", bucket, k, err)
		}
		return fn(string(k), v)
	})
}

// View runs a read-only transaction.
func (s *Store) View(fn func(tx *bolt.Tx) error) error { return s.db.View(fn) }

// Update runs a read-write transaction.
func (s *Store) Update(fn func(tx *bolt.Tx) error) error { return s.db.Update(fn) }

// unmarshal decodes a stored value. It exists so cursor-based iteration in
// history.go shares the decoding path with getJSON.
func unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
