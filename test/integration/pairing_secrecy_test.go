package integration

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Nerow75/fastr/internal/pairing"
)

// What the pairing exchange must not leak, checked against the bytes that
// actually cross the wire.
//
// The exchange runs over plain HTTP in simple mode, by construction: a browser
// grants no secure context to a local address, so there is nothing to encrypt
// it with before a key exists. Everything here therefore assumes somebody is
// reading every byte, and asks what that buys them.
//
// The design this replaced failed the first test in this file outright. The
// joining device sent the six digits in the body of the confirm request, so a
// passive observer did not need an attack: they read the code. That is the
// concrete regression these tests exist to prevent, which is why they assert on
// recorded traffic rather than on the shape of a struct.

// wire records every byte sent and received during an exchange.
type wire struct {
	t    *testing.T
	sent [][]byte
	got  [][]byte
}

// post runs one unsealed request and keeps both bodies.
func (w *wire) post(url string, body map[string]any, out any) int {
	w.t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		w.t.Fatalf("marshal: %v", err)
	}
	w.sent = append(w.sent, payload)

	//nolint:forbidigo,noctx // deliberate: unsealed pairing endpoint on a loopback test server
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		w.t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	w.got = append(w.got, raw)

	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			w.t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp.StatusCode
}

// everything returns every recorded body as one searchable string.
func (w *wire) everything() string {
	var all []string
	for _, body := range append(append([][]byte{}, w.sent...), w.got...) {
		all = append(all, string(body))
	}
	return strings.Join(all, "\n")
}

// runExchange performs the joining device's side, exactly as
// web/src/lib/session.ts does, and records the traffic.
//
// Spelled out here rather than routed through the harness helper on purpose:
// what this file asserts about is the literal bytes on the wire, and a test
// that read them through a helper would be asserting about the helper.
func runExchange(t *testing.T, h *harness, code string) (*wire, int, string) {
	t.Helper()

	w := &wire{t: t}

	sid, err := pairing.NewSessionID(rand.Reader)
	if err != nil {
		t.Fatalf("session id: %v", err)
	}
	secret, clientMessage, err := pairing.ClientBegin(code, h.selfID, sid)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	var init struct {
		HandshakeID string `json:"handshake_id"`
		Message     string `json:"message"`
	}
	if status := w.post(h.server.URL+"/api/pair/init", map[string]any{
		"sid":         base64.StdEncoding.EncodeToString(sid),
		"message":     base64.StdEncoding.EncodeToString(clientMessage),
		"device_name": "Test Phone",
		"platform":    "android",
	}, &init); status != http.StatusOK {
		return w, status, ""
	}

	serverMessage, err := base64.StdEncoding.DecodeString(init.Message)
	if err != nil {
		t.Fatalf("decode server message: %v", err)
	}

	_, proof, err := pairing.ClientComplete(secret, sid, clientMessage, serverMessage, init.HandshakeID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	var confirm struct {
		PendingID string `json:"pending_id"`
	}
	status := w.post(h.server.URL+"/api/pair/confirm", map[string]any{
		"handshake_id": init.HandshakeID,
		"proof":        base64.StdEncoding.EncodeToString(proof),
		"device_name":  "Test Phone",
		"platform":     "android",
	}, &confirm)

	return w, status, confirm.PendingID
}

// FR-019 says a code is never logged and never echoed in a response. It is now
// also never sent, which is a stronger thing and the one that matters on a
// shared network: there is nothing to echo because nothing carries it.
func TestThePairingCodeNeverCrossesTheWire(t *testing.T) {
	h := newHarness(t)

	code, err := h.codes.Issue()
	if err != nil {
		t.Fatalf("issue code: %v", err)
	}

	w, status, pendingID := runExchange(t, h, code.Display())
	if status != http.StatusAccepted {
		t.Fatalf("confirm: status %d, want 202: %s", status, w.everything())
	}
	if pendingID == "" {
		t.Fatal("no pending identifier came back")
	}

	traffic := w.everything()
	if strings.Contains(traffic, code.Display()) {
		t.Errorf("the pairing code %q appears in the traffic:\n%s", code.Display(), traffic)
	}

	// A six-digit run reaching the wire in any encoding is worth failing on,
	// not only the exact string. Base64 of the digits is the obvious way it
	// would come back after somebody "fixed" a serialisation.
	encoded := base64.StdEncoding.EncodeToString([]byte(code.Display()))
	if strings.Contains(traffic, encoded) {
		t.Errorf("the pairing code appears base64-encoded in the traffic:\n%s", traffic)
	}
}

// The exchange is what it is for so that this is true: an attacker who reads
// everything and then guesses gets five attempts, not a million.
//
// Driven through the real endpoints rather than the package, because the
// accounting is split across two requests now — a guess is admitted at init and
// judged at confirm — and a mistake in the wiring between them is exactly the
// kind that leaves the budget uncounted.
func TestGuessingIsLimitedToFiveAttemptsOverHTTP(t *testing.T) {
	h := newHarness(t)

	now := time.Now()
	h.codes.SetClock(func() time.Time { return now })

	code, err := h.codes.Issue()
	if err != nil {
		t.Fatalf("issue code: %v", err)
	}

	wrong := "000000"
	if code.Display() == wrong {
		wrong = "111111"
	}

	for i := range pairing.MaxAttempts {
		// Past the longest delay each time, and no further: five waits of a
		// minute would outrun the code's own lifetime and the code would expire
		// rather than die of failed attempts, which would prove nothing.
		now = now.Add(31 * time.Second)

		w, status, _ := runExchange(t, h, wrong)
		if status != http.StatusUnauthorized && status != http.StatusBadRequest {
			t.Fatalf("guess %d: status %d, want a refusal: %s", i+1, status, w.everything())
		}
	}

	// The sixth attempt is refused before any cryptography runs, and the right
	// code is refused with it: the budget belongs to the code, not to the
	// guesser.
	now = now.Add(31 * time.Second)
	w, status, _ := runExchange(t, h, code.Display())
	if status == http.StatusOK || status == http.StatusAccepted {
		t.Fatalf("the correct code still worked after %d wrong guesses", pairing.MaxAttempts)
	}
	if !strings.Contains(w.everything(), "code_exhausted") {
		t.Errorf("expected code_exhausted, got:\n%s", w.everything())
	}
}

// Starting an exchange and walking away must not be a way around the delay.
// Without this the budget still holds — an abandoned attempt is never counted —
// but an attacker who could reset the clock between guesses would turn five
// spread over three minutes into five as fast as the network allows, and the
// growing delay is half of what FR-013 asks for.
func TestAnAbandonedExchangeDoesNotResetTheDelay(t *testing.T) {
	h := newHarness(t)

	now := time.Now()
	h.codes.SetClock(func() time.Time { return now })

	if _, err := h.codes.Issue(); err != nil {
		t.Fatalf("issue code: %v", err)
	}

	if _, status, _ := runExchange(t, h, "000000"); status == http.StatusAccepted {
		t.Fatal("a wrong code was accepted")
	}

	// Immediately again, with no clock movement: the delay must bite at init,
	// before the exchange costs the host any arithmetic.
	w := &wire{t: t}
	sid, err := pairing.NewSessionID(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, clientMessage, err := pairing.ClientBegin("000000", h.selfID, sid)
	if err != nil {
		t.Fatal(err)
	}

	status := w.post(h.server.URL+"/api/pair/init", map[string]any{
		"sid":         base64.StdEncoding.EncodeToString(sid),
		"message":     base64.StdEncoding.EncodeToString(clientMessage),
		"device_name": "Test Phone",
		"platform":    "android",
	}, nil)

	if status == http.StatusOK {
		t.Error("a second guess was admitted with no delay")
	}
	if !strings.Contains(w.everything(), "rate_limited") {
		t.Errorf("expected rate_limited, got:\n%s", w.everything())
	}
}

// A device that has not been approved holds no credential, whatever it knew.
// Knowing the code proves somebody read a screen; FR-010 wants somebody to
// decide at the machine that will receive the files.
func TestACorrectProofStillWaitsForAHuman(t *testing.T) {
	h := newHarness(t)

	code, err := h.codes.Issue()
	if err != nil {
		t.Fatalf("issue code: %v", err)
	}

	_, status, pendingID := runExchange(t, h, code.Display())
	if status != http.StatusAccepted {
		t.Fatalf("confirm: status %d, want 202", status)
	}

	var reply struct {
		State      string `json:"state"`
		Credential string `json:"credential"`
	}
	h.getPlain("/api/pair/status?pending="+pendingID, &reply)

	if reply.State != "awaiting_approval" {
		t.Errorf("state = %q, want awaiting_approval", reply.State)
	}
	if reply.Credential != "" {
		t.Error("a credential was handed over before anyone approved")
	}
}
