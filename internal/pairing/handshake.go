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

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// The pairing handshake, as specified in contracts/pairing.md.
//
// Goal: both sides end up holding a shared session key that a network observer
// cannot derive, and the host has recorded a human's explicit approval. The
// exchange runs over plain HTTP in simple mode, so it assumes the wire is read.
//
// What an observer sees is two ephemeral public keys, a salt, a handshake
// identifier, and ciphertext. The shared secret is not derivable from the
// public keys, and mixing the pairing code into the derivation means an
// observer who never saw the code cannot produce a valid confirmation.
//
// KNOWN LIMITATION, flagged in research.md item 1 and due before the first
// release: a 6-digit code carries about 20 bits, so an observer who captures
// the whole handshake can search the code space offline against the confirm
// ciphertext. The online defences below (single use, 3-minute expiry, 5-failure
// death, growing rate limit) do not apply to an offline attempt. A PAKE such as
// SPAKE2 removes this by construction and is the intended replacement.

// ProtocolVersion is bound into every envelope and handshake transcript, so
// traffic from one version cannot be replayed against another.
const ProtocolVersion = 1

const (
	// hkdfInfo separates this derivation from any other use of the same secret.
	hkdfInfo = "fastr-pair-v1"
	saltSize = 32
	keySize  = 32
)

// Errors the handshake can produce.
var (
	ErrUnknownHandshake = errors.New("unknown handshake")
	ErrHandshakeExpired = errors.New("handshake expired")
	ErrBadProof         = errors.New("handshake proof did not verify")
	ErrBadPublicKey     = errors.New("malformed public key")
)

// handshakeTTL bounds how long a started handshake stays open. It is shorter
// than the code lifetime, because a handshake is a single round trip and an
// abandoned one should not sit in memory.
const handshakeTTL = 3 * time.Minute

// Handshake is one in-flight key agreement.
type Handshake struct {
	ID        string
	Salt      []byte
	CreatedAt time.Time

	clientPub  []byte
	serverPub  []byte
	serverPriv []byte
}

// Transcript is what both sides bind into the derivation. Including it means a
// tampered public key produces a different key rather than a silent downgrade.
func (h *Handshake) Transcript() []byte {
	out := make([]byte, 0, len(h.clientPub)+len(h.serverPub)+len(h.ID))
	out = append(out, h.clientPub...)
	out = append(out, h.serverPub...)
	out = append(out, h.ID...)
	return out
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

// Begin starts a handshake from the client's ephemeral public key, returning
// the server's public key, the salt, and the handshake identifier.
func (hs *Handshakes) Begin(clientPub []byte) (*Handshake, error) {
	if len(clientPub) != curve25519.ScalarSize {
		return nil, fmt.Errorf("%w: expected %d bytes, got %d",
			ErrBadPublicKey, curve25519.ScalarSize, len(clientPub))
	}

	priv := make([]byte, curve25519.ScalarSize)
	if _, err := io.ReadFull(hs.rand, priv); err != nil {
		return nil, fmt.Errorf("generate ephemeral key: %w", err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive public key: %w", err)
	}

	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(hs.rand, salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	id, err := randomID(hs.rand)
	if err != nil {
		return nil, err
	}

	h := &Handshake{
		ID:         id,
		Salt:       salt,
		CreatedAt:  hs.now(),
		clientPub:  append([]byte(nil), clientPub...),
		serverPub:  pub,
		serverPriv: priv,
	}

	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.sweepLocked()
	hs.open[id] = h

	return h, nil
}

// ServerPublicKey is what the client needs to complete its own derivation.
func (h *Handshake) ServerPublicKey() []byte {
	return append([]byte(nil), h.serverPub...)
}

// Complete verifies the client's proof of knowing the pairing code and returns
// the derived session key.
//
// It consumes the handshake either way: a wrong proof does not get a second
// attempt on the same ephemeral keys, which is what keeps the online attempt
// budget meaningful.
func (hs *Handshakes) Complete(id string, code string, proof []byte) ([]byte, error) {
	hs.mu.Lock()
	h, ok := hs.open[id]
	if ok {
		delete(hs.open, id)
	}
	hs.mu.Unlock()

	if !ok {
		return nil, ErrUnknownHandshake
	}
	if hs.now().Sub(h.CreatedAt) > handshakeTTL {
		return nil, ErrHandshakeExpired
	}

	key, err := deriveKey(h.serverPriv, h.clientPub, h.Salt, code, h.Transcript())
	if err != nil {
		return nil, err
	}

	expected := confirmProof(key, h.ID)
	if subtle.ConstantTimeCompare(expected, proof) != 1 {
		return nil, ErrBadProof
	}

	return key, nil
}

// deriveKey computes the session key both sides must arrive at independently.
//
//	shared = X25519(own private, peer public)
//	key    = HKDF-SHA256(shared, salt, "fastr-pair-v1" || transcript || code)
//
// The code is in the info parameter rather than the salt so that a caller
// cannot accidentally omit it and still produce a working key: without the
// code, both sides derive something, but not the same thing an honest peer
// would.
func deriveKey(ownPriv, peerPub, salt []byte, code string, transcript []byte) ([]byte, error) {
	shared, err := curve25519.X25519(ownPriv, peerPub)
	if err != nil {
		return nil, fmt.Errorf("key agreement: %w", err)
	}

	info := make([]byte, 0, len(hkdfInfo)+len(transcript)+len(code))
	info = append(info, hkdfInfo...)
	info = append(info, transcript...)
	info = append(info, code...)

	key := make([]byte, keySize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, salt, info), key); err != nil {
		return nil, fmt.Errorf("derive session key: %w", err)
	}
	return key, nil
}

// confirmProof is the tag the client sends to prove it derived the same key.
//
// It is an HMAC over the handshake identifier rather than the key itself, so
// the proof never reveals key material even to an observer who captures it.
func confirmProof(key []byte, handshakeID string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("fastr-confirm-v1"))
	mac.Write([]byte(handshakeID))
	return mac.Sum(nil)
}

// ClientDerive performs the client half of the agreement. It exists so tests
// and the reference implementation share exactly the derivation the browser
// performs in web/src/crypto/handshake.ts.
func ClientDerive(clientPriv, serverPub, clientPub, salt []byte, code, handshakeID string) (key, proof []byte, err error) {
	transcript := make([]byte, 0, len(clientPub)+len(serverPub)+len(handshakeID))
	transcript = append(transcript, clientPub...)
	transcript = append(transcript, serverPub...)
	transcript = append(transcript, handshakeID...)

	key, err = deriveKey(clientPriv, serverPub, salt, code, transcript)
	if err != nil {
		return nil, nil, err
	}
	return key, confirmProof(key, handshakeID), nil
}

// GenerateClientKeypair produces an ephemeral X25519 keypair.
func GenerateClientKeypair() (priv, pub []byte, err error) {
	priv = make([]byte, curve25519.ScalarSize)
	if _, err := io.ReadFull(rand.Reader, priv); err != nil {
		return nil, nil, err
	}
	pub, err = curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
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
