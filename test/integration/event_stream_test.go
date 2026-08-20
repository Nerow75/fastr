package integration

import (
	"net/http"
	"testing"
	"time"
)

// The event stream authenticates with a single-use ticket rather than a bearer
// header, because EventSource cannot set one. The whole point of the ticket is
// that a value which lands in browser history is worthless shortly afterwards,
// so these tests are about how quickly it stops working.

func (d *device) eventTicket(t *testing.T) string {
	t.Helper()

	resp := d.do("POST", "/api/events/ticket", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ticket: status %d", resp.StatusCode)
	}
	var out struct {
		Ticket    string `json:"ticket"`
		ExpiresIn int    `json:"expires_in"`
	}
	d.open("POST", "/api/events/ticket", resp, &out)

	if out.Ticket == "" {
		t.Fatal("empty ticket")
	}
	if out.ExpiresIn <= 0 || out.ExpiresIn > 60 {
		t.Errorf("expires_in = %d, want a short window", out.ExpiresIn)
	}
	return out.Ticket
}

func TestEventStreamRequiresATicket(t *testing.T) {
	h := newHarness(t)
	d := h.pair()

	for name, query := range map[string]string{
		"no ticket":      "",
		"empty ticket":   "?ticket=",
		"garbage ticket": "?ticket=not-a-real-ticket",
		// A bearer credential must not work here: the stream deliberately
		// accepts only tickets, so there is one way in rather than two.
		"credential as ticket": "?ticket=" + d.credential,
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := h.server.Client().Get(h.server.URL + "/api/events" + query) //nolint:noctx // test client
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status %d, want 401", resp.StatusCode)
			}
		})
	}
}

func TestEventTicketIsSingleUse(t *testing.T) {
	h := newHarness(t)
	d := h.pair()
	ticket := d.eventTicket(t)

	// The first redemption opens a stream, which we close immediately: the
	// point here is that the ticket was accepted, not what it delivers.
	req, _ := http.NewRequest("GET", h.server.URL+"/api/events?ticket="+ticket, nil) //nolint:noctx // test client
	ctx, cancel := contextWithTimeout(t, 2*time.Second)
	defer cancel()
	resp, err := h.server.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first redemption: status %d", resp.StatusCode)
	}
	resp.Body.Close()

	// The same ticket again must fail. This is what makes a value in browser
	// history harmless.
	second, err := h.server.Client().Get(h.server.URL + "/api/events?ticket=" + ticket) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("second redemption: %v", err)
	}
	defer second.Body.Close()

	if second.StatusCode != http.StatusUnauthorized {
		t.Errorf("reused ticket: status %d, want 401", second.StatusCode)
	}
}

// Revocation must bite during the ticket's window, not only at the next
// authenticated request. FR-015 says immediately.
func TestRevocationInvalidatesAnUnredeemedTicket(t *testing.T) {
	h := newHarness(t)
	victim := h.pair()
	admin := h.pair()

	ticket := victim.eventTicket(t)

	resp := admin.do("DELETE", "/api/pairings/"+victim.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: status %d", resp.StatusCode)
	}
	resp.Body.Close()

	stream, err := h.server.Client().Get(h.server.URL + "/api/events?ticket=" + ticket) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer stream.Body.Close()

	if stream.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", stream.StatusCode)
	}
	if body := errorBody(t, stream); body["error"] != "pairing_revoked" {
		t.Errorf("error = %v, want pairing_revoked", body["error"])
	}
}

// Unredeemed tickets must not pile up in memory.
func TestExpiredTicketsAreSwept(t *testing.T) {
	h := newHarness(t)
	base := time.Now()
	now := base
	h.tickets.SetClock(func() time.Time { return now })

	d := h.pair()
	for i := 0; i < 5; i++ {
		d.eventTicket(t)
	}
	if got := h.tickets.Outstanding(); got != 5 {
		t.Fatalf("outstanding = %d, want 5", got)
	}

	now = base.Add(time.Minute)
	if got := h.tickets.Outstanding(); got != 0 {
		t.Errorf("outstanding after expiry = %d, want 0", got)
	}
}

// An expired ticket is consumed on the attempt, so it cannot be retried until
// it happens to land inside a window.
func TestExpiredTicketIsRefused(t *testing.T) {
	h := newHarness(t)
	base := time.Now()
	now := base
	h.tickets.SetClock(func() time.Time { return now })

	d := h.pair()
	ticket := d.eventTicket(t)

	now = base.Add(time.Minute)
	resp, err := h.server.Client().Get(h.server.URL + "/api/events?ticket=" + ticket) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", resp.StatusCode)
	}
}
