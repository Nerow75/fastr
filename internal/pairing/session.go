package pairing

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/Nerow75/fastr/internal/store"
)

// Sessions authorizes requests from paired devices.
//
// FR-011: every file operation from an unpaired device is refused, including
// listing. There is exactly one place that decision is made, so it cannot be
// forgotten at a call site.

// Errors authorization can produce.
var (
	ErrNoCredential  = errors.New("no credential presented")
	ErrBadCredential = errors.New("credential not recognised")
	ErrTrustedOnly   = errors.New("device requires trusted mode")
)

// Session is an authorized request's identity.
type Session struct {
	DeviceID string
	Pairing  store.Pairing
	// Envelope seals and opens this session's control payloads.
	Envelope *Envelope
	// Trusted reports whether the request arrived over the trusted channel.
	Trusted bool
}

// Sessions resolves credentials to sessions.
type Sessions struct {
	store *store.Store

	mu        sync.RWMutex
	envelopes map[string]*Envelope
}

// NewSessions returns a resolver backed by the store.
func NewSessions(s *store.Store) *Sessions {
	return &Sessions{store: s, envelopes: make(map[string]*Envelope)}
}

// Register records the envelope for a freshly paired device.
func (ss *Sessions) Register(deviceID string, sessionKey []byte) error {
	env, err := NewEnvelope(sessionKey, ServerToClient)
	if err != nil {
		return err
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.envelopes[deviceID] = env
	return nil
}

// Forget drops a device's envelope, which is what makes revocation immediate
// even for a connection already in flight.
func (ss *Sessions) Forget(deviceID string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.envelopes, deviceID)
}

// envelopeFor returns the device's envelope, rebuilding it from the stored key
// when the process has restarted since pairing.
func (ss *Sessions) envelopeFor(p store.Pairing) (*Envelope, error) {
	ss.mu.RLock()
	env, ok := ss.envelopes[p.DeviceID]
	ss.mu.RUnlock()
	if ok {
		return env, nil
	}

	env, err := NewEnvelope(p.SessionKey, ServerToClient)
	if err != nil {
		return nil, err
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	// Another request may have rebuilt it while we were not holding the lock.
	// Sharing one envelope per device is required, because two would each keep
	// their own counter and reuse nonces.
	if existing, ok := ss.envelopes[p.DeviceID]; ok {
		return existing, nil
	}
	ss.envelopes[p.DeviceID] = env
	return env, nil
}

// Resolve turns a request into a session, or explains why it cannot.
//
// trusted says whether the request arrived over the trusted channel; the caller
// knows that from which listener accepted it, not from anything the client sent.
func (ss *Sessions) Resolve(r *http.Request, trusted bool) (*Session, error) {
	credential, ok := bearerToken(r)
	if !ok {
		return nil, ErrNoCredential
	}

	deviceID, p, err := ss.lookup(credential)
	if err != nil {
		return nil, err
	}

	// FR-047c: a device set to require trusted mode is refused on the plain
	// channel, with an explanation rather than a bare rejection.
	if p.RequireTrusted && !trusted {
		return nil, ErrTrustedOnly
	}

	env, err := ss.envelopeFor(p)
	if err != nil {
		return nil, err
	}

	// Any successful authorization is activity, which is what makes expiry
	// mean "unused" rather than "old".
	if err := ss.store.TouchPairing(deviceID); err != nil {
		return nil, err
	}

	return &Session{DeviceID: deviceID, Pairing: p, Envelope: env, Trusted: trusted}, nil
}

// lookup finds the pairing whose stored hash matches the presented credential.
//
// The scan is linear over pairings, which is a handful of records on a home
// network. Comparison is constant time so the loop leaks nothing about which
// device matched.
func (ss *Sessions) lookup(credential string) (string, store.Pairing, error) {
	want := HashCredential(credential)

	pairings, err := ss.store.Pairings()
	if err != nil {
		return "", store.Pairing{}, err
	}

	for _, p := range pairings {
		if len(p.TokenHash) == 0 {
			continue
		}
		if subtle.ConstantTimeCompare(p.TokenHash, want) != 1 {
			continue
		}
		// The credential matches. Whether it may still authorize is a separate
		// question, and its answer decides what the user is told to do next.
		active, err := ss.store.ActivePairing(p.DeviceID)
		if err != nil {
			return "", store.Pairing{}, err
		}
		return p.DeviceID, active, nil
	}

	return "", store.Pairing{}, ErrBadCredential
}

// bearerToken extracts the credential from the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}
