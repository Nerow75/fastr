// Package pairing implements the trust handshake, session credentials, and the
// authenticated envelope that protects control traffic.
//
// Everything here is designed on the assumption that a passive observer reads
// every byte. In simple mode the channel is plain HTTP on a shared network, and
// constitution v2.0.1 still requires pairing exchanges, credentials, and
// metadata to be encrypted in every mode.
package pairing

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

// Direction separates the two counter spaces. Without it, a message the server
// sent could be replayed back at the server with a counter it would accept.
type Direction uint8

const (
	ClientToServer Direction = 0
	ServerToClient Direction = 1
)

// Wire layout: an 8-byte big-endian counter followed by the AEAD ciphertext.
// The counter travels in the clear because the receiver needs it to build the
// nonce, and it is covered by the authentication tag through the nonce itself.
const counterSize = 8

// Errors an envelope can produce. They are distinguished because the corrective
// action differs: a replay means reload the page, a bad key means re-pair.
var (
	ErrReplay         = errors.New("replayed or out-of-order counter")
	ErrMalformed      = errors.New("malformed envelope")
	ErrAuthFailed     = errors.New("authentication failed")
	ErrCounterWrapped = errors.New("counter space exhausted")
)

// Envelope seals and opens control-plane payloads for one session.
//
// It is safe for concurrent use. Sealing takes a lock because the counter must
// advance exactly once per message: two goroutines sharing a counter value
// would reuse a nonce, which is the one thing that breaks this construction
// outright.
type Envelope struct {
	mu   sync.Mutex
	aead interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
		NonceSize() int
	}

	sendDir Direction
	recvDir Direction

	sendCounter uint64
	// recvHighWater is the highest counter accepted so far. Anything at or
	// below it is a replay.
	recvHighWater uint64
	seenAny       bool
}

// NewEnvelope builds an envelope from a 32-byte session key.
//
// send is the direction this side writes in; the other direction is implied,
// so a caller cannot accidentally configure both sides to share one space.
func NewEnvelope(key []byte, send Direction) (*Envelope, error) {
	return NewEnvelopeAt(key, send, 0)
}

// NewEnvelopeAt builds an envelope whose send counter resumes at start.
//
// A browser page keeps its counter in memory, so a reload builds a new envelope
// while the peer still remembers the highest counter it accepted. Starting
// again at zero makes every message look like a replay and leaves the session
// refused until the peer restarts. The client therefore claims a fresh range of
// counters at each load, and this is how one is built. Nothing is weakened:
// counters still only ever increase, which is the property the replay check
// rests on.
func NewEnvelopeAt(key []byte, send Direction, start uint64) (*Envelope, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("session key must be %d bytes, got %d",
			chacha20poly1305.KeySize, len(key))
	}

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("build cipher: %w", err)
	}

	recv := ServerToClient
	if send == ServerToClient {
		recv = ClientToServer
	}

	return &Envelope{aead: aead, sendDir: send, recvDir: recv, sendCounter: start}, nil
}

// nonce builds the 12-byte nonce: one direction byte, the 8-byte counter, and
// three zero bytes. Direction and counter together are unique per key, which is
// what the AEAD requires.
func nonce(dir Direction, counter uint64) []byte {
	var n [chacha20poly1305.NonceSize]byte
	n[0] = byte(dir)
	binary.BigEndian.PutUint64(n[1:9], counter)
	return n[:]
}

// aad binds a message to the endpoint it was meant for.
//
// Without this, an envelope captured from one request could be replayed against
// a different path with the same counter on a fresh session. The protocol
// version is included so a downgrade cannot reuse traffic from another version.
func aad(method, path string, version int) []byte {
	out := make([]byte, 0, len(method)+len(path)+8)
	out = append(out, method...)
	out = append(out, 0)
	out = append(out, path...)
	out = append(out, 0)
	return binary.BigEndian.AppendUint32(out, uint32(version)) //nolint:gosec // version is a small constant
}

// Seal encrypts a payload for one request.
func (e *Envelope) Seal(method, path string, version int, plaintext []byte) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.sendCounter == ^uint64(0) {
		// Reaching this would mean reusing a nonce on the next message. At
		// roughly one message per millisecond it takes half a billion years,
		// but refusing is free and silent nonce reuse is not.
		return nil, ErrCounterWrapped
	}
	e.sendCounter++

	out := make([]byte, counterSize, counterSize+len(plaintext)+e.aead.NonceSize())
	binary.BigEndian.PutUint64(out, e.sendCounter)

	sealed := e.aead.Seal(nil, nonce(e.sendDir, e.sendCounter), plaintext, aad(method, path, version))
	return append(out, sealed...), nil
}

// Open decrypts a payload, refusing replays and out-of-order counters.
//
// The counter check happens before decryption so a flood of replayed messages
// costs a comparison rather than an AEAD operation.
func (e *Envelope) Open(method, path string, version int, sealed []byte) ([]byte, error) {
	if len(sealed) < counterSize {
		return nil, ErrMalformed
	}

	counter := binary.BigEndian.Uint64(sealed[:counterSize])

	e.mu.Lock()
	defer e.mu.Unlock()

	// Strictly increasing. Equal is a replay; lower is either a replay or a
	// reordered message, and neither is something a local network request
	// should produce.
	if e.seenAny && counter <= e.recvHighWater {
		return nil, ErrReplay
	}
	if counter == 0 {
		return nil, ErrMalformed // counters start at 1
	}

	plaintext, err := e.aead.Open(nil, nonce(e.recvDir, counter), sealed[counterSize:], aad(method, path, version))
	if err != nil {
		// Do not advance the high-water mark on a failed open: an attacker
		// sending garbage with a huge counter would otherwise be able to
		// wedge the session against its legitimate peer.
		return nil, ErrAuthFailed
	}

	e.recvHighWater = counter
	e.seenAny = true
	return plaintext, nil
}

// SendCounter reports how many messages this side has sealed. Tests use it;
// nothing in the protocol depends on it.
func (e *Envelope) SendCounter() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sendCounter
}
