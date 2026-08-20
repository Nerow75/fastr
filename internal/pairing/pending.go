package pairing

import (
	"crypto/rand"
	"errors"
	"sort"
	"sync"
	"time"
)

// Pending pairing requests, per FR-010 and User Story 1 scenario 3.
//
// Knowing the code proves someone read it off the host's screen, which is
// already a strong signal. It is not the same as a human on the host deciding,
// which is what FR-010 asks for, so a correct code buys a place in this queue
// rather than access.
//
// The cost is one extra confirmation the first time a device connects. It is
// paid once per device, and it is the difference between "somebody who saw my
// screen" and "I said yes".

// PendingTTL bounds how long an unanswered request waits.
//
// Two minutes: long enough to walk to the computer, short enough that a request
// nobody answered does not sit in the list until it is meaningless.
const PendingTTL = 2 * time.Minute

// PendingState is where a request stands.
type PendingState string

const (
	PendingAwaiting PendingState = "awaiting_approval"
	PendingApproved PendingState = "approved"
	PendingRejected PendingState = "rejected"
	PendingExpired  PendingState = "expired"
)

// Errors the pending queue can produce.
var (
	ErrUnknownPending = errors.New("unknown pairing request")
	ErrPendingSettled = errors.New("pairing request already answered")
)

// PendingRequest is a device waiting for a human to say yes.
type PendingRequest struct {
	ID         string       `json:"id"`
	DeviceName string       `json:"device_name"`
	Platform   string       `json:"platform"`
	State      PendingState `json:"state"`
	CreatedAt  time.Time    `json:"created_at"`
	ExpiresAt  time.Time    `json:"expires_at"`

	// sessionKey is the key derived during the handshake. It is held here
	// until approval and never rendered, so it cannot reach the pending list
	// the interface displays.
	sessionKey []byte
	// deviceID is assigned on approval.
	deviceID string
	// credential is held between approval and collection, then cleared.
	credential string
}

// SessionKey returns the derived key. Only the approval path calls it.
func (p *PendingRequest) SessionKey() []byte { return p.sessionKey }

// DeviceID returns the identifier assigned on approval.
func (p *PendingRequest) DeviceID() string { return p.deviceID }

// Pendings tracks requests awaiting a human.
type Pendings struct {
	mu    sync.Mutex
	queue map[string]*PendingRequest
	now   func() time.Time
}

// NewPendings returns an empty queue.
func NewPendings() *Pendings {
	return &Pendings{queue: make(map[string]*PendingRequest), now: time.Now}
}

// SetClock replaces the time source, for tests.
func (ps *Pendings) SetClock(now func() time.Time) { ps.now = now }

// Add registers a request that has already proved it knows the code.
func (ps *Pendings) Add(deviceName, platform string, sessionKey []byte) (*PendingRequest, error) {
	id, err := randomID(rand.Reader)
	if err != nil {
		return nil, err
	}

	now := ps.now()
	req := &PendingRequest{
		ID:         id,
		DeviceName: deviceName,
		Platform:   platform,
		State:      PendingAwaiting,
		CreatedAt:  now,
		ExpiresAt:  now.Add(PendingTTL),
		sessionKey: sessionKey,
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.sweepLocked()
	ps.queue[id] = req

	return req, nil
}

// List returns the requests the host should show, newest first.
//
// Settled requests are included briefly so the interface can show the outcome
// rather than having the row vanish under the user's cursor.
func (ps *Pendings) List() []PendingRequest {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.sweepLocked()

	out := make([]PendingRequest, 0, len(ps.queue))
	for _, req := range ps.queue {
		copy := *req
		// Neither ever leaves this package, let alone reaches the interface.
		copy.sessionKey = nil
		copy.credential = ""
		out = append(out, copy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Get returns a request by identifier, refreshing its state first.
func (ps *Pendings) Get(id string) (*PendingRequest, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	req, ok := ps.queue[id]
	if !ok {
		return nil, ErrUnknownPending
	}
	if req.State == PendingAwaiting && ps.now().After(req.ExpiresAt) {
		req.State = PendingExpired
		req.sessionKey = nil
	}
	return req, nil
}

// Approve marks a request accepted and records the device identifier assigned
// to it. The caller creates the pairing; this only records the human's answer.
func (ps *Pendings) Approve(id, deviceID string) (*PendingRequest, error) {
	return ps.settle(id, PendingApproved, deviceID)
}

// Reject marks a request refused. The key is dropped immediately: a refused
// device must not leave usable material behind.
func (ps *Pendings) Reject(id string) (*PendingRequest, error) {
	return ps.settle(id, PendingRejected, "")
}

func (ps *Pendings) settle(id string, state PendingState, deviceID string) (*PendingRequest, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	req, ok := ps.queue[id]
	if !ok {
		return nil, ErrUnknownPending
	}
	if req.State != PendingAwaiting {
		return nil, ErrPendingSettled
	}
	if ps.now().After(req.ExpiresAt) {
		req.State = PendingExpired
		req.sessionKey = nil
		return nil, ErrPendingSettled
	}

	req.State = state
	req.deviceID = deviceID
	if state == PendingRejected {
		req.sessionKey = nil
	}
	return req, nil
}

// Collected clears the key material once the device has taken its credential,
// while keeping the record itself.
//
// The record stays because a phone whose connection hiccups just after the
// response will poll again. Deleting the entry would answer that retry with
// "unknown request", and the device would conclude the pairing failed when it
// had in fact succeeded, having already lost the credential. Keeping the state
// costs nothing and the sweep removes it shortly.
func (ps *Pendings) Collected(id string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if req, ok := ps.queue[id]; ok {
		req.sessionKey = nil
		req.credential = ""
	}
}

// Forget drops a request entirely.
func (ps *Pendings) Forget(id string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if req, ok := ps.queue[id]; ok {
		req.sessionKey = nil
		req.credential = ""
		delete(ps.queue, id)
	}
}

// Count reports how many requests are tracked. Tests use it to check that they
// do not accumulate.
func (ps *Pendings) Count() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.sweepLocked()
	return len(ps.queue)
}

// sweepLocked drops requests that have been settled or expired long enough that
// nobody is still looking at them.
func (ps *Pendings) sweepLocked() {
	cutoff := ps.now().Add(-PendingTTL)
	for id, req := range ps.queue {
		if req.CreatedAt.Before(cutoff) {
			req.sessionKey = nil
			req.credential = ""
			delete(ps.queue, id)
		}
	}
}

// HoldCredential parks the freshly issued credential until the waiting device
// collects it.
//
// It lives here rather than in the store because it is handed over exactly once
// and then forgotten: persisting it would mean writing a usable credential to
// disk in plaintext, which is precisely what hashing it in the store avoids.
func (ps *Pendings) HoldCredential(id, credential string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if req, ok := ps.queue[id]; ok {
		req.credential = credential
	}
}

// TakeCredential returns the held credential and clears it, so a second poll
// cannot collect it again.
func (ps *Pendings) TakeCredential(id string) (string, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	req, ok := ps.queue[id]
	if !ok || req.credential == "" {
		return "", false
	}
	credential := req.credential
	req.credential = ""
	return credential, true
}
