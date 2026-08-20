package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Nerow75/fastr/internal/app"
)

// Stream tickets.
//
// EventSource cannot set a request header, so the event stream needs its
// credential somewhere else. Putting the session credential in the query string
// would work and is what most implementations do, but a URL lands in browser
// history, in a referrer, and in the access log of anything between the two
// devices. A long-lived credential does not belong in any of those.
//
// So the client spends an authenticated, sealed request to obtain a ticket that
// is single use and lives for thirty seconds, and puts that in the URL instead.
// A ticket leaked from history is worthless by the time anyone reads it.

const (
	ticketTTL  = 30 * time.Second
	ticketSize = 32
)

var errBadTicket = errors.New("unknown or expired ticket")

type ticket struct {
	deviceID  string
	expiresAt time.Time
}

// Tickets issues and redeems stream tickets.
type Tickets struct {
	mu     sync.Mutex
	issued map[string]ticket
	now    func() time.Time
}

// NewTickets returns an empty issuer.
func NewTickets() *Tickets {
	return &Tickets{issued: make(map[string]ticket), now: time.Now}
}

// SetClock replaces the time source, for tests.
func (ts *Tickets) SetClock(now func() time.Time) { ts.now = now }

// Issue mints a ticket for a device.
func (ts *Tickets) Issue(deviceID string) (string, error) {
	raw := make([]byte, ticketSize)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(raw)

	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.sweepLocked()
	ts.issued[value] = ticket{deviceID: deviceID, expiresAt: ts.now().Add(ticketTTL)}

	return value, nil
}

// Redeem consumes a ticket and returns the device it belongs to.
//
// Consumption happens whether or not the ticket had expired, so a leaked value
// cannot be retried until it happens to land inside a window.
func (ts *Tickets) Redeem(value string) (string, error) {
	if value == "" {
		return "", errBadTicket
	}

	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Constant-time comparison against each candidate rather than a map lookup,
	// so the timing does not reveal how much of a guessed ticket was right.
	for issued, t := range ts.issued {
		if subtle.ConstantTimeCompare([]byte(issued), []byte(value)) != 1 {
			continue
		}
		delete(ts.issued, issued)
		if ts.now().After(t.expiresAt) {
			return "", errBadTicket
		}
		return t.deviceID, nil
	}
	return "", errBadTicket
}

// Outstanding reports how many tickets are live. Tests use it to check that
// they do not accumulate.
func (ts *Tickets) Outstanding() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.sweepLocked()
	return len(ts.issued)
}

func (ts *Tickets) sweepLocked() {
	now := ts.now()
	for value, t := range ts.issued {
		if now.After(t.expiresAt) {
			delete(ts.issued, value)
		}
	}
}

// handleEventTicket issues a ticket to an authenticated caller.
func (d Deps) handleEventTicket(s *Session, w http.ResponseWriter, r *http.Request) {
	value, err := d.Tickets.Issue(s.DeviceID)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"ticket":     value,
		"expires_in": int(ticketTTL.Seconds()),
	})
}
