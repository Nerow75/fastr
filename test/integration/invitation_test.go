package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The host's invitation, per FR-002.
//
// The property that matters most here is negative: a live pairing code is what
// turns a stranger on the same network into a paired device, so it must never
// be reachable from the network. The rest is that the page has enough to show
// an address and a QR without anyone reading a terminal.

type invitation struct {
	Code      string   `json:"code"`
	ExpiresIn int      `json:"expires_in"`
	Addresses []string `json:"addresses"`
	URL       string   `json:"url"`
	QR        string   `json:"qr"`
}

func (h *harness) invitation(t *testing.T) invitation {
	t.Helper()

	var out invitation
	h.getPlain("/api/pair/invitation", &out)
	return out
}

// FR-002: the host has a code to show and a URL to encode.
func TestInvitationCarriesACodeAndAnAddress(t *testing.T) {
	h := newHarness(t)

	inv := h.invitation(t)

	if len(inv.Code) != 6 {
		t.Errorf("code = %q, want six digits", inv.Code)
	}
	for _, r := range inv.Code {
		if r < '0' || r > '9' {
			t.Fatalf("code %q is not digits", inv.Code)
		}
	}
	if inv.ExpiresIn <= 0 {
		t.Errorf("expires_in = %d, want a live code", inv.ExpiresIn)
	}

	// httptest binds loopback, so there is no reachable address and the QR is
	// correspondingly absent. That is the honest answer rather than an address
	// no phone can use.
	if inv.URL == "" && inv.QR != "" {
		t.Error("a QR was offered for no address")
	}
	for _, addr := range inv.Addresses {
		if strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "[::1]") {
			t.Errorf("loopback %q was offered as something to type on a phone", addr)
		}
	}
}

// The code survives a reload, or the digits the user is halfway through typing
// on their phone would stop working every time the page polled.
func TestInvitationReusesALiveCode(t *testing.T) {
	h := newHarness(t)

	first := h.invitation(t)
	second := h.invitation(t)

	if first.Code != second.Code {
		t.Errorf("a second read reissued the code: %q then %q", first.Code, second.Code)
	}
}

// The 3-minute expiry used to be escapable only by restarting the binary.
func TestInvitationReissuesASpentCode(t *testing.T) {
	h := newHarness(t)

	first := h.invitation(t)

	// Spend it the way a real pairing does.
	if _, err := h.codes.Issue(); err != nil {
		t.Fatalf("issue: %v", err)
	}
	h.codes.Clear()

	second := h.invitation(t)
	if second.Code == "" || second.ExpiresIn <= 0 {
		t.Fatalf("no usable code after the previous one was spent: %+v", second)
	}
	if second.Code == first.Code {
		t.Error("the spent code was handed out again")
	}
}

// The one that matters: a live pairing code must never leave this machine.
// Serving it to the network would let anyone on the same Wi-Fi pair themselves
// without anybody approving anything, which is what FR-010 exists to prevent.
//
// The router is called directly so the remote address can be forged. A real
// client cannot set it, which is exactly why the decision is made on it rather
// than on a header.
func TestInvitationIsRefusedFromTheNetwork(t *testing.T) {
	h := newHarness(t)

	// The code that must not appear in any refusal.
	live := h.invitation(t).Code
	if live == "" {
		t.Fatal("no code to leak, so this test would prove nothing")
	}

	for _, remote := range []string{"192.168.1.50:41234", "10.0.0.7:5000", "8.8.8.8:80"} {
		req := httptest.NewRequest(http.MethodGet, "/api/pair/invitation", nil)
		req.RemoteAddr = remote
		// A header a phone could set must not change the answer.
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.Header.Set("X-Real-IP", "127.0.0.1")

		rec := httptest.NewRecorder()
		h.server.Config.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("from %s: status %d, want 401", remote, rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, live) {
			t.Errorf("from %s: the refusal carried the live code anyway: %s", remote, body)
		}
	}

	// And from loopback it works, or the restriction would be vacuous.
	req := httptest.NewRequest(http.MethodGet, "/api/pair/invitation", nil)
	req.RemoteAddr = "127.0.0.1:41234"
	rec := httptest.NewRecorder()
	h.server.Config.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("refused on loopback: status %d", rec.Code)
	}
}

// Holding a valid credential buys nothing here either: the invitation is the
// host's own screen, not an API for peers. A paired phone that could read it
// could mint itself a second identity without anyone approving it.
func TestAPairedDeviceCannotReadTheInvitation(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	live := h.invitation(t).Code

	req := httptest.NewRequest(http.MethodGet, "/api/pair/invitation", nil)
	req.RemoteAddr = "192.168.1.50:41234"
	req.Header.Set("Authorization", "Bearer "+phone.credential)

	rec := httptest.NewRecorder()
	h.server.Config.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a paired device read the invitation: status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), live) {
		t.Error("the response carried the live code")
	}
}
