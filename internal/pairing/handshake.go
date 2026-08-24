package pairing

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// The pairing handshake, as specified in contracts/pairing.md.
//
// Goal: both sides end up holding a shared session key that a network observer
// cannot derive, and the host has recorded a human's explicit approval. The
// exchange runs over plain HTTP in simple mode, so it assumes the wire is read
// by somebody, and that somebody may also be answering.
//
// The key agreement is CPace, in cpace.go, which is where the reasoning for it
// lives. Two properties this file depends on:
//
//   - **The code never travels.** It is an input to a derivation on each side,
//     never a field in a request. Reading the whole exchange reveals nothing
//     about the six digits.
//   - **Guessing is online only.** Every candidate code has to be committed to
//     before the exchange starts and produces a different generator, so an
//     attempt is one interaction that tests one guess. That is what makes the
//     five-attempt budget in code.go the real bound, and twenty bits of
//     entropy an acceptable amount to ask a person to retype.
//
// What an observer sees is two group elements, a session identifier, a
// handshake identifier, and a confirmation tag. None of them can be tested
// against a candidate code without a fresh interaction.

// ProtocolVersion is bound into every envelope and handshake transcript, so
// traffic from one version cannot be replayed against another.
//
// Version 2 is CPace. Version 1 sent the pairing code to the host in the clear
// and left a confirmation tag anyone could search offline; a device holding a
// version 1 credential has to pair again, which is one screen and is the right
// price.
const ProtocolVersion = 2

const (
	// confirmLabel separates the confirmation tag from any other use of the
	// same key material.
	confirmLabel = "fastr-pair-confirm-v2"
	// keySize is the length of the session key handed to the envelope.
	keySize = 32
)

// Errors the handshake can produce.
var (
	ErrUnknownHandshake = errors.New("unknown handshake")
	ErrHandshakeExpired = errors.New("handshake expired")
	ErrBadProof         = errors.New("handshake proof did not verify")
	ErrBadSessionID     = errors.New("malformed session identifier")
)

// handshakeTTL bounds how long a started handshake stays open. It is shorter
// than the code lifetime, because a handshake is a single round trip and an
// abandoned one should not sit in memory.
const handshakeTTL = 3 * time.Minute

// Handshake is one in-flight key agreement.
//
// The host's ephemeral scalar is gone by the time this exists: Begin does the
// whole of the host's side and keeps only what confirming needs. Holding a
// secret scalar for three minutes would buy nothing and lose something.
type Handshake struct {
	ID        string
	CreatedAt time.Time

	// code is the pairing code this exchange was bound to. Kept so the caller
	// settles the attempt against the code that was live when the exchange
	// began rather than whichever one is live when it finishes, which may be a
	// different one three minutes later.
	code string

	sessionKey []byte
	// expected is the confirmation tag the joining device must present. Kept
	// rather than the key it came from, so nothing here can produce a tag: this
	// side only ever compares one.
	expected []byte
}

// Handshakes tracks in-flight key agreements.
type Handshakes struct {
	mu   sync.Mutex
	open map[string]*Handshake
	now  func() time.Time
	rand io.Reader
}

// NewHandshakes returns an empty registry.
func NewHandshakes() *Handshakes {
	return &Handshakes{
		open: make(map[string]*Handshake),
		now:  time.Now,
		rand: rand.Reader,
	}
}

// SetClock replaces the time source, for tests.
func (hs *Handshakes) SetClock(now func() time.Time) { hs.now = now }

// Begin runs the host's half of the exchange.
//
// It takes the code rather than reading it, so that this package never has to
// know how codes are issued or accounted for, and so a test can drive the
// exchange without a live issuer.
//
// The message returned is the host's group element. The caller sends it back
// with the handshake identifier and nothing else: no salt, no public key, and
// above all no code.
func (hs *Handshakes) Begin(hostID, code string, sid, clientMessage []byte) (*Handshake, []byte, error) {
	if len(sid) != SessionIDSize {
		return nil, nil, fmt.Errorf("%w: expected %d bytes, got %d",
			ErrBadSessionID, SessionIDSize, len(sid))
	}

	id, err := randomID(hs.rand)
	if err != nil {
		return nil, nil, err
	}

	secret, message, err := begin(code, hostID, sid, hs.rand)
	if err != nil {
		return nil, nil, err
	}

	// HostComplete refuses a malformed or degenerate element from the joining
	// device before anything is recorded, so a crafted message cannot fix the
	// shared secret.
	sessionKey, expected, err := HostComplete(secret, sid, clientMessage, message, id)
	if err != nil {
		return nil, nil, err
	}

	h := &Handshake{
		ID:         id,
		CreatedAt:  hs.now(),
		code:       code,
		sessionKey: sessionKey,
		expected:   expected,
	}

	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.sweepLocked()
	hs.open[id] = h

	return h, message, nil
}

// Complete verifies the joining device's proof and returns the session key,
// along with the code the exchange was bound to.
//
// The code comes back on a bad proof as well as a good one, because a wrong
// proof is a wrong guess and the caller has to spend one of the five attempts
// on it. It is empty only when there was no handshake to speak of.
//
// The handshake is consumed either way: a wrong proof does not get a second
// attempt against the same exchange, which is what keeps one interaction worth
// exactly one guess.
func (hs *Handshakes) Complete(id string, proof []byte) (key []byte, code string, err error) {
	hs.mu.Lock()
	h, ok := hs.open[id]
	if ok {
		delete(hs.open, id)
	}
	hs.mu.Unlock()

	if !ok {
		return nil, "", ErrUnknownHandshake
	}
	if hs.now().Sub(h.CreatedAt) > handshakeTTL {
		return nil, "", ErrHandshakeExpired
	}

	if subtle.ConstantTimeCompare(h.expected, proof) != 1 {
		return nil, h.code, ErrBadProof
	}

	return h.sessionKey, h.code, nil
}

// confirmProof is the tag proving a side reached the same key.
//
// Over the second half of the exchange's output rather than the half used as
// the session key, so a tag that necessarily travels in the clear says nothing
// about the key that has to stay secret.
func confirmProof(confirmKey []byte) []byte {
	mac := hmac.New(sha256.New, confirmKey)
	mac.Write([]byte(confirmLabel))
	return mac.Sum(nil)
}

// ClientComplete performs the joining device's second step, and HostComplete
// the host's. Both return the same two values, because the whole purpose of the
// exchange is that they do.
//
// Two functions rather than one with a flag, because the difference between
// them is which message is the peer's, and that is the one thing in the whole
// exchange that must not be got wrong: multiplying by your own message instead
// of the other side's produces a key nobody else can reach, silently, and every
// self-consistency test still passes. Naming the two sides makes the mistake
// unspellable rather than merely unlikely.
//
// The transcript order is the same in both: the joining device's message first,
// the host's second, because that is the order they were sent in.
//
// ClientComplete exists in Go so the reference implementation and the browser's
// are the same protocol rather than two readings of one document, and so the
// cross-implementation vectors can be generated from it. The browser's copy is
// web/src/lib/session.ts, over web/src/crypto/cpace.ts.
func ClientComplete(secret Secret, sid, clientMessage, serverMessage []byte, handshakeID string) (key, proof []byte, err error) {
	return complete(secret, serverMessage, sid, clientMessage, serverMessage, handshakeID)
}

// HostComplete derives the host's half.
func HostComplete(secret Secret, sid, clientMessage, serverMessage []byte, handshakeID string) (key, proof []byte, err error) {
	return complete(secret, clientMessage, sid, clientMessage, serverMessage, handshakeID)
}

// complete is the shared body: agree with the peer, then bind the whole
// transcript into the key.
//
// The handshake identifier is the responder's associated data, so it is bound
// into the key itself rather than only into the tag that follows it.
func complete(secret Secret, peerMessage, sid, clientMessage, serverMessage []byte, handshakeID string) (key, proof []byte, err error) {
	shared, err := ScalarMultVfy(secret, peerMessage)
	if err != nil {
		return nil, nil, err
	}

	derived := ISK(sid, shared, clientMessage, nil, serverMessage, []byte(handshakeID))
	return derived[:keySize], confirmProof(derived[keySize:]), nil
}

// sweepLocked drops handshakes that timed out. Called on every Begin, which is
// often enough for a registry that only ever holds a handful of entries.
func (hs *Handshakes) sweepLocked() {
	cutoff := hs.now().Add(-handshakeTTL)
	for id, h := range hs.open {
		if h.CreatedAt.Before(cutoff) {
			delete(hs.open, id)
		}
	}
}

// Open reports how many handshakes are in flight.
func (hs *Handshakes) Open() int {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return len(hs.open)
}
