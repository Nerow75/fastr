package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// Pairing codes, per FR-012 and FR-013.
//
// A code is single use, expires after 3 minutes, dies after 5 failed attempts,
// and attempts are rate limited with a delay that grows after each failure.
// It is displayed on the host only, never logged, never echoed in a response,
// and never shown again once used (FR-019).

const (
	// CodeDigits is the length shown to the user. Six digits is what a person
	// will retype from another screen without resenting it.
	CodeDigits = 6
	// CodeTTL bounds how long a displayed code stays usable.
	CodeTTL = 3 * time.Minute
	// MaxAttempts is how many wrong guesses a code survives.
	MaxAttempts = 5
)

// Errors a code check can produce. They are distinguished because the user's
// next move differs: retype, or ask for a new code.
var (
	ErrNoCode       = errors.New("no pairing code is active")
	ErrCodeExpired  = errors.New("pairing code expired")
	ErrCodeConsumed = errors.New("pairing code already used")
	ErrCodeDead     = errors.New("pairing code cancelled after too many attempts")
	ErrWrongCode    = errors.New("wrong pairing code")
	ErrRateLimited  = errors.New("too many attempts, wait before retrying")
)

// backoff is the delay enforced after each failed attempt. It grows so that an
// online guessing attempt costs real time, while a person who mistyped once
// barely notices.
var backoff = [MaxAttempts]time.Duration{
	0,
	1 * time.Second,
	3 * time.Second,
	10 * time.Second,
	30 * time.Second,
}

// Code is a displayed pairing code and its attempt state.
type Code struct {
	// digits is never exported and never rendered. Display goes through
	// Display(), which the caller shows on screen and nowhere else.
	digits string

	IssuedAt  time.Time
	ExpiresAt time.Time

	attempts   int
	consumed   bool
	lastFailed time.Time
}

// Display returns the code for showing on the host's own screen.
//
// This is the only accessor. Every other path, including logging and error
// responses, has no way to reach the value, which is what makes FR-019
// structural rather than a rule to remember.
func (c *Code) Display() string { return c.digits }

// Expired reports whether the code has passed its lifetime.
func (c *Code) Expired(now time.Time) bool { return !now.Before(c.ExpiresAt) }

// Usable reports whether the code could still authorize a pairing.
func (c *Code) Usable(now time.Time) bool {
	return !c.consumed && c.attempts < MaxAttempts && !c.Expired(now)
}

// Codes issues and verifies pairing codes for one host.
type Codes struct {
	mu      sync.Mutex
	current *Code
	now     func() time.Time
	rand    io.Reader

	// onIssue and onRetire let the caller register and forget the live value
	// with the log scrubber, so a code smuggled into a formatted message is
	// redacted for exactly as long as it is sensitive.
	onIssue  func(code string)
	onRetire func(code string)
}

// NewCodes returns a code issuer.
func NewCodes() *Codes {
	return &Codes{now: time.Now, rand: rand.Reader}
}

// SetClock replaces the time source, for tests.
func (cs *Codes) SetClock(now func() time.Time) { cs.now = now }

// SetScrubHooks registers callbacks invoked when a code becomes live and when
// it stops being live.
func (cs *Codes) SetScrubHooks(onIssue, onRetire func(string)) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.onIssue, cs.onRetire = onIssue, onRetire
}

// Issue generates a fresh code, replacing any current one.
//
// Replacing rather than queueing is deliberate: two live codes on one screen
// is a user interface nobody wants, and the old code stops working the moment
// a new one is displayed.
func (cs *Codes) Issue() (*Code, error) {
	digits, err := randomDigits(cs.rand, CodeDigits)
	if err != nil {
		return nil, err
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.current != nil && cs.onRetire != nil {
		cs.onRetire(cs.current.digits)
	}

	now := cs.now()
	cs.current = &Code{
		digits:    digits,
		IssuedAt:  now,
		ExpiresAt: now.Add(CodeTTL),
	}

	if cs.onIssue != nil {
		cs.onIssue(digits)
	}
	return cs.current, nil
}

// Current returns the active code, or nil.
func (cs *Codes) Current() *Code {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.current
}

// Ensure returns a code the host can display, issuing one only when the
// current one can no longer authorize a pairing.
//
// Reusing a still-usable code matters: the host's screen is polled, and issuing
// a fresh one on every poll would invalidate the digits the user is halfway
// through typing on their phone. Issuing when the current one is spent is what
// keeps the 3-minute expiry from being a dead end escapable only by restarting.
func (cs *Codes) Ensure() (*Code, error) {
	cs.mu.Lock()
	if cs.current != nil && cs.current.Usable(cs.now()) {
		defer cs.mu.Unlock()
		return cs.current, nil
	}
	cs.mu.Unlock()

	// Issue takes the lock itself, and retires the spent code as it goes.
	return cs.Issue()
}

// Verify checks a submitted code and consumes it on success.
//
// The comparison is not constant time, and does not need to be: a wrong code
// is counted and rate limited, and the code dies after five attempts, so
// timing carries no useful signal about a value that survives at most five
// guesses. What matters here is the attempt budget, not the comparison.
func (cs *Codes) Verify(submitted string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	c := cs.current
	if c == nil {
		return ErrNoCode
	}

	now := cs.now()
	switch {
	case c.consumed:
		return ErrCodeConsumed
	case c.attempts >= MaxAttempts:
		return ErrCodeDead
	case c.Expired(now):
		return ErrCodeExpired
	}

	// Enforce the growing delay before spending an attempt, so a caller
	// hammering the endpoint does not burn the budget faster than a person.
	if c.attempts > 0 {
		wait := backoff[c.attempts]
		if elapsed := now.Sub(c.lastFailed); elapsed < wait {
			return fmt.Errorf("%w: %s remaining", ErrRateLimited, (wait - elapsed).Round(time.Second))
		}
	}

	if submitted != c.digits {
		c.attempts++
		c.lastFailed = now
		if c.attempts >= MaxAttempts && cs.onRetire != nil {
			// Dead codes stop being sensitive.
			cs.onRetire(c.digits)
		}
		return ErrWrongCode
	}

	c.consumed = true
	if cs.onRetire != nil {
		cs.onRetire(c.digits)
	}
	return nil
}

// Clear retires the current code, for instance when the user stops the server.
func (cs *Codes) Clear() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.current != nil && cs.onRetire != nil {
		cs.onRetire(cs.current.digits)
	}
	cs.current = nil
}

// randomDigits produces n decimal digits from a cryptographic source.
//
// Rejection sampling through big.Int rather than modulo, which would bias the
// leading digit. The bias would be small; the fix is one line.
func randomDigits(r io.Reader, n int) (string, error) {
	ten := big.NewInt(10)
	out := make([]byte, n)
	for i := range out {
		// crypto/rand.Int does the rejection sampling, and takes a reader so
		// tests can supply a deterministic one.
		d, err := rand.Int(r, ten)
		if err != nil {
			return "", fmt.Errorf("generate pairing code: %w", err)
		}
		out[i] = byte('0' + d.Int64())
	}
	return string(out), nil
}

// randomID produces a URL-safe identifier for a handshake.
func randomID(r io.Reader) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// NewCredential produces a 256-bit session credential and its hash.
//
// The credential itself is returned once, to be handed to the paired device.
// Only the hash is stored, so a stolen store yields no working token.
// NewSessionKey returns a fresh key for the sealed envelope.
//
// Used where there is no handshake to derive one from, which is the host's own
// page: it is not a peer being let in, it is the machine itself.
func NewSessionKey() ([]byte, error) {
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate session key: %w", err)
	}
	return key, nil
}

func NewCredential() (credential string, hash []byte, err error) {
	var raw [32]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", nil, fmt.Errorf("generate credential: %w", err)
	}
	credential = base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(credential))
	return credential, sum[:], nil
}

// HashCredential hashes a presented credential for comparison against storage.
func HashCredential(credential string) []byte {
	sum := sha256.Sum256([]byte(credential))
	return sum[:]
}
