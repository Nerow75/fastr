package store

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

// ID is a 128-bit lexicographically sortable identifier: 48 bits of millisecond
// timestamp followed by 80 bits of randomness, encoded in Crockford base32.
//
// This is the ULID layout. It is implemented here rather than imported because
// research.md commits to a nine-dependency budget, and 40 lines of standard
// library is a poor reason to spend one of them.
//
// Sortability is not decoration: history and the queue are iterated in key
// order, so creation order comes free from the storage layer.
type ID string

const (
	idEncoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // Crockford: no I, L, O, U
	idLen      = 26
)

// NewID returns an identifier for the current time.
func NewID() ID { return newIDAt(time.Now()) }

func newIDAt(t time.Time) ID {
	var raw [16]byte

	ms := uint64(t.UnixMilli())
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)

	// rand.Read never fails on any supported platform; it panics internally on
	// a broken entropy source, which is the correct response here too.
	if _, err := rand.Read(raw[6:]); err != nil {
		panic("fastr: entropy source unavailable: " + err.Error())
	}

	return ID(encodeID(raw))
}

// encodeID renders 16 bytes as 26 base32 characters, most significant first.
func encodeID(raw [16]byte) string {
	hi := binary.BigEndian.Uint64(raw[0:8])
	lo := binary.BigEndian.Uint64(raw[8:16])

	out := make([]byte, idLen)
	for i := idLen - 1; i >= 0; i-- {
		out[i] = idEncoding[lo&0x1f]
		// Shift the 128-bit value right by 5, carrying across the two halves.
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out)
}

// ErrInvalidID is returned for a malformed identifier.
var ErrInvalidID = errors.New("invalid id")

// Validate reports whether the identifier is well formed. Anything arriving
// from the network is checked before it reaches a bucket key.
func (id ID) Validate() error {
	if len(id) != idLen {
		return ErrInvalidID
	}
	for i := 0; i < len(id); i++ {
		if strings.IndexByte(idEncoding, id[i]) < 0 {
			return ErrInvalidID
		}
	}
	return nil
}

// Time recovers the creation instant.
func (id ID) Time() (time.Time, error) {
	if err := id.Validate(); err != nil {
		return time.Time{}, err
	}
	// 26 characters carry 130 bits for a 128-bit value, so the two leading bits
	// are zero padding. The first 10 characters are therefore those 2 pad bits
	// followed by exactly the 48-bit timestamp, and the 50-bit value they
	// decode to *is* the timestamp. No shift, no mask.
	var ms uint64
	for i := 0; i < 10; i++ {
		ms = ms<<5 | uint64(strings.IndexByte(idEncoding, id[i]))
	}
	return time.UnixMilli(int64(ms)), nil
}

func (id ID) String() string { return string(id) }
