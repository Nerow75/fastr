package httpapi

import (
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/Nerow75/fastr/internal/app"
)

// The host's invitation, per FR-002.
//
// FR-002 requires the connection entry point to be presented as both a short
// local address and a QR code encoding it. Two pieces existed and were never
// joined: /qr renders any local URL it is handed, and the pairing screen accepts
// typed digits. Neither told the host's own page which URL to encode or which
// code to show, so the only place a pairing code ever appeared was the
// terminal's standard output — which Principle VI, a first transfer in under two
// minutes without reading documentation, does not survive.
//
// **Loopback only, and that is the whole security argument.** A live pairing
// code is the one secret that turns a stranger on the same Wi-Fi into a paired
// device. Serving it to the network would let anyone there pair themselves and
// would empty FR-010 — a human on the receiving device says yes — of meaning.
// The restriction is the same one the approval endpoints use, and for the same
// reason: reaching loopback means being on this machine, which is the trust
// boundary the operating system already enforces.

type invitationResponse struct {
	// Code is the six digits to type on the phone.
	Code string `json:"code"`
	// ExpiresIn is seconds remaining, so the page can count down and refetch
	// rather than showing digits that stopped working a minute ago.
	ExpiresIn int `json:"expires_in"`
	// Addresses are the reachable host:port pairs, loopback excluded.
	Addresses []string `json:"addresses"`
	// URL is the one to encode and to read aloud. Empty when the server is
	// bound only to loopback, which is a real state and not an error: it means
	// no phone can reach this instance yet.
	URL string `json:"url"`
	// QR is the path rendering URL as a scannable code, ready to use as an
	// image source. Empty exactly when URL is.
	QR string `json:"qr,omitempty"`
}

// handleInvitation returns what the host needs to show to bring a device in.
func (d Deps) handleInvitation(w http.ResponseWriter, r *http.Request) {
	// Issued on demand rather than once at startup. A code that expired while
	// the user walked to their phone previously left them restarting the
	// binary, which is not a recovery anyone should have to discover.
	code, err := d.Codes.Ensure()
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	reachable := reachableAddresses(d.Addresses)

	out := invitationResponse{
		Code:      code.Display(),
		ExpiresIn: int(time.Until(code.ExpiresAt).Seconds()),
		Addresses: reachable,
	}
	if out.ExpiresIn < 0 {
		out.ExpiresIn = 0
	}

	if len(reachable) > 0 {
		out.URL = "http://" + reachable[0]
		out.QR = "/qr?url=" + url.QueryEscape(out.URL)
	}

	// In the clear, and only ever to this machine. Sealing is impossible here
	// anyway: the host's page has no session yet on first run, which is the run
	// that needs this most.
	d.writePlainJSON(w, http.StatusOK, out)
}

// reachableAddresses drops loopback, which is the one address a phone can never
// use. Showing 127.0.0.1 as the thing to type would be worse than showing
// nothing: it looks like an answer and cannot work.
func reachableAddresses(addresses func() []string) []string {
	if addresses == nil {
		return nil
	}

	out := make([]string, 0, 2)
	for _, addr := range addresses() {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); ip == nil || ip.IsLoopback() {
			continue
		}
		out = append(out, addr)
	}
	return out
}
