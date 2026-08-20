package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
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
	scoped map[string]scopedTicket
	now    func() time.Time
}

// NewTickets returns an empty issuer.
func NewTickets() *Tickets {
	return &Tickets{
		issued: make(map[string]ticket),
		scoped: make(map[string]scopedTicket),
		now:    time.Now,
	}
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

// Scoped tickets, for content downloads.
//
// The event stream is not the only place a browser cannot set a header. A file
// is saved by the browser's own download manager, which is the only mechanism
// that writes a multi-gigabyte file to a phone without holding it in memory,
// and it fetches a plain URL.
//
// A stream ticket will not do here, for one reason: the download manager
// re-requests the URL with a Range when it resumes, and a single-use ticket
// would be gone. So a content ticket is multi-use and *scoped*: it authorizes
// exactly one item of one transfer, for a device already a party to it.
// Leaking one exposes a file its holder was being sent anyway.

// contentTicketTTL bounds a scoped ticket. Long enough for a large transfer
// that resumes a few times, short enough that a URL left in history stops
// working the same day.
const contentTicketTTL = 6 * time.Hour

type scopedTicket struct {
	deviceID  string
	scope     string
	expiresAt time.Time
}

// IssueScoped mints a multi-use ticket bound to one scope.
func (ts *Tickets) IssueScoped(deviceID, scope string) (string, error) {
	raw := make([]byte, ticketSize)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(raw)

	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.scoped == nil {
		ts.scoped = make(map[string]scopedTicket)
	}
	ts.sweepScopedLocked()
	ts.scoped[value] = scopedTicket{
		deviceID:  deviceID,
		scope:     scope,
		expiresAt: ts.now().Add(contentTicketTTL),
	}

	return value, nil
}

// RedeemScoped checks a ticket against the scope it must authorize.
//
// Unlike a stream ticket it is not consumed, because the download manager comes
// back for the same URL when it resumes.
func (ts *Tickets) RedeemScoped(value, scope string) (string, error) {
	if value == "" {
		return "", errBadTicket
	}

	ts.mu.Lock()
	defer ts.mu.Unlock()

	for issued, t := range ts.scoped {
		if subtle.ConstantTimeCompare([]byte(issued), []byte(value)) != 1 {
			continue
		}
		if ts.now().After(t.expiresAt) {
			delete(ts.scoped, issued)
			return "", errBadTicket
		}
		// A ticket for one file must not open another. Without this it would
		// be a general credential in a URL, which is what these exist to avoid.
		if subtle.ConstantTimeCompare([]byte(t.scope), []byte(scope)) != 1 {
			return "", errBadTicket
		}
		return t.deviceID, nil
	}
	return "", errBadTicket
}

// RevokeScope drops every ticket for a scope, when its transfer ends.
func (ts *Tickets) RevokeScope(scope string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for value, t := range ts.scoped {
		if t.scope == scope {
			delete(ts.scoped, value)
		}
	}
}

func (ts *Tickets) sweepScopedLocked() {
	now := ts.now()
	for value, t := range ts.scoped {
		if now.After(t.expiresAt) {
			delete(ts.scoped, value)
		}
	}
}

// ContentScope names one item of one transfer.
func ContentScope(transferID string, itemIndex int) string {
	return fmt.Sprintf("%s#%d", transferID, itemIndex)
}
