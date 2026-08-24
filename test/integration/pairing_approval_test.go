package integration

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nerow75/fastr/internal/pairing"
)

// FR-010 and User Story 1 scenario 3: a human on the host decides.
//
// Knowing the pairing code proves someone read it off the host's screen. That
// is a strong signal, and it is not the same thing. These tests are about the
// gap between the two.

// startPairing runs the handshake up to the point where a human must answer.
func (h *harness) startPairing(t *testing.T) (pendingID string, key []byte) {
	t.Helper()

	code, err := h.codes.Issue()
	if err != nil {
		t.Fatalf("issue code: %v", err)
	}

	sid, err := pairing.NewSessionID(rand.Reader)
	if err != nil {
		t.Fatalf("session id: %v", err)
	}
	secret, clientMessage, err := pairing.ClientBegin(code.Display(), h.selfID, sid)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	var init struct {
		HandshakeID string `json:"handshake_id"`
		Message     string `json:"message"`
	}
	h.postPlain("/api/pair/init", map[string]any{
		"sid":         base64.StdEncoding.EncodeToString(sid),
		"message":     base64.StdEncoding.EncodeToString(clientMessage),
		"device_name": "Visitor Phone",
		"platform":    "ios",
	}, &init)

	serverMessage, _ := base64.StdEncoding.DecodeString(init.Message)

	derived, proof, err := pairing.ClientComplete(secret, sid, clientMessage, serverMessage, init.HandshakeID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	var confirm struct {
		PendingID string `json:"pending_id"`
		State     string `json:"state"`
	}
	h.postAccepted("/api/pair/confirm", map[string]any{
		"handshake_id": init.HandshakeID,
		"proof":        base64.StdEncoding.EncodeToString(proof),
		"device_name":  "Visitor Phone",
		"platform":     "ios",
	}, &confirm)

	return confirm.PendingID, derived
}

// The central case: a correct code alone grants nothing.
func TestCorrectCodeAloneGrantsNoAccess(t *testing.T) {
	h := newHarness(t)
	pendingID, key := h.startPairing(t)

	// Before anyone answers, there is no credential to collect.
	var status struct {
		State      string `json:"state"`
		Credential string `json:"credential"`
	}
	h.getPlain("/api/pair/status?pending="+pendingID, &status)

	if status.State != string(pairing.PendingAwaiting) {
		t.Errorf("state = %q, want %q", status.State, pairing.PendingAwaiting)
	}
	if status.Credential != "" {
		t.Fatal("a credential was issued without a human approving")
	}

	// And no pairing exists yet.
	pairings, err := h.store.Pairings()
	if err != nil {
		t.Fatalf("pairings: %v", err)
	}
	if len(pairings) != 0 {
		t.Errorf("%d pairings exist before approval", len(pairings))
	}
	_ = key
}

func TestRejectedRequestNeverYieldsACredential(t *testing.T) {
	h := newHarness(t)
	pendingID, _ := h.startPairing(t)

	h.postPlain("/api/pair/pending/"+pendingID+"/reject", map[string]any{}, nil)

	var status struct {
		State      string `json:"state"`
		Credential string `json:"credential"`
	}
	h.getPlain("/api/pair/status?pending="+pendingID, &status)

	if status.State != string(pairing.PendingRejected) {
		t.Errorf("state = %q, want %q", status.State, pairing.PendingRejected)
	}
	if status.Credential != "" {
		t.Error("a rejected request yielded a credential")
	}

	pairings, _ := h.store.Pairings()
	if len(pairings) != 0 {
		t.Errorf("a rejected request created %d pairings", len(pairings))
	}
}

// A request nobody answered must not sit there indefinitely waiting to be
// approved by whoever walks past next.
func TestUnansweredRequestExpires(t *testing.T) {
	h := newHarness(t)
	base := time.Now()
	now := base
	h.pendings.SetClock(func() time.Time { return now })

	pendingID, _ := h.startPairing(t)

	now = base.Add(pairing.PendingTTL + time.Second)

	var status struct {
		State string `json:"state"`
	}
	h.getPlain("/api/pair/status?pending="+pendingID, &status)
	if status.State != string(pairing.PendingExpired) {
		t.Errorf("state = %q, want %q", status.State, pairing.PendingExpired)
	}

	// Approving it afterwards must fail rather than resurrect it.
	resp, err := h.server.Client().Post( //nolint:noctx // test client
		h.server.URL+"/api/pair/pending/"+pendingID+"/approve", "application/json", nil)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Error("an expired request was approved")
	}
}

// The credential is handed over exactly once. A second poll gets the state and
// nothing else, so a value replayed from a log or a proxy buys nothing.
func TestCredentialIsHandedOverOnce(t *testing.T) {
	h := newHarness(t)
	pendingID, key := h.startPairing(t)
	h.approve(pendingID)

	first := h.collectCredential(pendingID, key)
	if first == "" {
		t.Fatal("no credential on the first poll")
	}

	var second struct {
		State      string `json:"state"`
		Credential string `json:"credential"`
	}
	h.getPlain("/api/pair/status?pending="+pendingID, &second)
	if second.Credential != "" {
		t.Error("the credential was handed over twice")
	}
}

// The credential must not be readable by anyone watching this plain HTTP
// exchange, which is the whole reason it is sealed with the handshake key.
func TestCredentialIsSealedOnTheWire(t *testing.T) {
	h := newHarness(t)
	pendingID, key := h.startPairing(t)
	h.approve(pendingID)

	var status struct {
		Credential string `json:"credential"`
	}
	h.getPlain("/api/pair/status?pending="+pendingID, &status)

	sealed, err := base64.StdEncoding.DecodeString(status.Credential)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Without the key, it is noise.
	wrongKey := make([]byte, 32)
	opener, err := pairing.NewEnvelope(wrongKey, pairing.ClientToServer)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if _, err := opener.Open(http.MethodGet, "/api/pair/status", pairing.ProtocolVersion, sealed); err == nil {
		t.Error("the credential opened with the wrong key")
	}

	// With it, it works.
	right, _ := pairing.NewEnvelope(key, pairing.ClientToServer)
	if _, err := right.Open(http.MethodGet, "/api/pair/status", pairing.ProtocolVersion, sealed); err != nil {
		t.Errorf("the credential did not open with the right key: %v", err)
	}
}

// Approving is the host deciding, so it is restricted to loopback. A phone on
// the network must not be able to approve itself.
func TestApprovalIsRestrictedToLoopback(t *testing.T) {
	h := newHarness(t)
	pendingID, _ := h.startPairing(t)

	for _, route := range []string{
		"/api/pair/pending",
		"/api/pair/pending/" + pendingID + "/approve",
		"/api/pair/pending/" + pendingID + "/reject",
	} {
		method := http.MethodPost
		if route == "/api/pair/pending" {
			method = http.MethodGet
		}

		// Call the router directly so the remote address can be forged. A real
		// client cannot set RemoteAddr, which is exactly why the decision is
		// made on it rather than on a header.
		for _, remote := range []string{"192.168.1.50:41234", "10.0.0.7:5000", "8.8.8.8:80"} {
			req := httptest.NewRequest(method, route, nil)
			req.RemoteAddr = remote
			// A header a phone could set must not change the answer.
			req.Header.Set("X-Forwarded-For", "127.0.0.1")

			rec := httptest.NewRecorder()
			h.server.Config.Handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s from %s: status %d, want 401", route, remote, rec.Code)
			}
		}

		// And from loopback it works, or the restriction would be vacuous.
		req := httptest.NewRequest(method, route, nil)
		req.RemoteAddr = "127.0.0.1:41234"
		rec := httptest.NewRecorder()
		h.server.Config.Handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s was refused on loopback", route)
		}
	}
}

// The pending list must never carry key material, whatever else it shows.
func TestPendingListCarriesNoKeyMaterial(t *testing.T) {
	h := newHarness(t)
	pendingID, _ := h.startPairing(t)

	var listing struct {
		Pending []map[string]any `json:"pending"`
	}
	h.getPlain("/api/pair/pending", &listing)

	if len(listing.Pending) != 1 {
		t.Fatalf("got %d pending requests, want 1", len(listing.Pending))
	}

	entry := listing.Pending[0]
	if entry["id"] != pendingID {
		t.Errorf("id = %v, want %v", entry["id"], pendingID)
	}

	allowed := map[string]bool{
		"id": true, "device_name": true, "platform": true,
		"state": true, "created_at": true, "expires_at": true,
	}
	for key := range entry {
		if !allowed[key] {
			t.Errorf("the pending list exposes an unexpected field: %q", key)
		}
	}
}

// Settled requests must not accumulate in memory.
func TestPendingRequestsAreSwept(t *testing.T) {
	h := newHarness(t)
	base := time.Now()
	now := base
	h.pendings.SetClock(func() time.Time { return now })

	for i := 0; i < 3; i++ {
		h.startPairing(t)
	}
	if got := h.pendings.Count(); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}

	now = base.Add(pairing.PendingTTL + time.Minute)
	if got := h.pendings.Count(); got != 0 {
		t.Errorf("count after sweep = %d, want 0", got)
	}
}
