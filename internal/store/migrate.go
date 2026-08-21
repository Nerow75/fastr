package store

import (
	"encoding/binary"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// schemaVersion is the layout this build understands. Bump it, and add a step
// to migrations, whenever a stored shape changes incompatibly.
const schemaVersion uint32 = 2

var keySchemaVersion = []byte("schema_version")

// migration transforms the store from version-1 to version.
type migration struct {
	to uint32
	fn func(tx *bolt.Tx) error
}

// migrations are applied in order. Version 1 is the initial layout.
var migrations = []migration{
	{to: 2, fn: dropReservedSelfDevice},
}

// dropReservedSelfDevice removes the device record an earlier build wrote under
// the literal key "self".
//
// That build meant it as this instance's own record but returned a freshly
// minted identifier on the run that created it and the string "self" on every
// run after, so the identity a phone paired against on day one was not the one
// the computer answered to on day two. The identifier now lives in the meta
// bucket and the record is keyed by it; the old entry would otherwise linger in
// the device list as a computer nobody can reach.
func dropReservedSelfDevice(tx *bolt.Tx) error {
	b := tx.Bucket(bucketDevices)
	if b == nil {
		return fmt.Errorf("missing bucket %s", bucketDevices)
	}
	if b.Get([]byte("self")) == nil {
		return nil
	}
	if err := b.Delete([]byte("self")); err != nil {
		return err
	}
	// A pairing keyed to it would be an authorization no interface lists.
	return tx.Bucket(bucketPairings).Delete([]byte("self"))
}

// migrate brings the store up to schemaVersion.
func (s *Store) migrate() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		current, err := readSchemaVersion(tx)
		if err != nil {
			return err
		}

		// A store written by a newer build may use shapes this one cannot read.
		// Refusing is the only safe response: silently ignoring unknown fields
		// would drop pairings and lose queued transfers.
		if current > schemaVersion {
			return fmt.Errorf(
				"store schema is version %d but this build understands %d: "+
					"upgrade fastr, or move %s aside to start fresh",
				current, schemaVersion, tx.DB().Path())
		}

		for _, m := range migrations {
			if m.to <= current {
				continue
			}
			if err := m.fn(tx); err != nil {
				return fmt.Errorf("migration to version %d: %w", m.to, err)
			}
			current = m.to
		}

		return writeSchemaVersion(tx, schemaVersion)
	})
}

// SchemaVersion reports the version recorded in the store.
func (s *Store) SchemaVersion() (uint32, error) {
	var v uint32
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		v, err = readSchemaVersion(tx)
		return err
	})
	return v, err
}

func readSchemaVersion(tx *bolt.Tx) (uint32, error) {
	b := tx.Bucket(bucketMeta)
	if b == nil {
		return 0, fmt.Errorf("missing bucket %s", bucketMeta)
	}
	raw := b.Get(keySchemaVersion)
	if raw == nil {
		return 0, nil // fresh store
	}
	if len(raw) != 4 {
		return 0, fmt.Errorf("corrupt schema version: %d bytes", len(raw))
	}
	return binary.BigEndian.Uint32(raw), nil
}

func writeSchemaVersion(tx *bolt.Tx, v uint32) error {
	b := tx.Bucket(bucketMeta)
	if b == nil {
		return fmt.Errorf("missing bucket %s", bucketMeta)
	}
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], v)
	return b.Put(keySchemaVersion, raw[:])
}
