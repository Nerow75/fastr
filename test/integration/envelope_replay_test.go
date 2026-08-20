package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/Nerow75/fastr/internal/pairing"
)

// The envelope is what keeps FR-017 true in simple mode, where the channel is
// plain HTTP on a shared network. These tests exercise it through the real
// server, so they prove what an observer on the wire could actually do with
// what they captured.

// The central case: an observer records a request and sends it again.
func TestReplayedRequestIsRefused(t *testing.T) {
	h := newHarness(t)
	d := h.pair()

	const method, path = "PATCH", "/api/pairings/"
	target := path + d.ID

	// Capture exactly what went over the wire.
	plain, _ := json.Marshal(map[string]any{"trust_mode": "ask"})
	sealed, err := d.envelope.Seal(method, target, pairing.ProtocolVersion, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	captured := base64.StdEncoding.EncodeToString(sealed)

	first := h.send(t, d, method, target, captured)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first request: status %d", first.StatusCode)
	}
	first.Body.Close()

	// The same bytes again. This is the whole attack.
	second := h.send(t, d, method, target, captured)
	if second.StatusCode != http.StatusBadRequest {
		t.Errorf("replay: status %d, want 400", second.StatusCode)
	}
	if body := errorBody(t, second); body["error"] != "replay_detected" {
		t.Errorf("replay: error = %v, want replay_detected", body["error"])
	}
}

// An envelope captured from one endpoint must not work against another. The
// associated data binds method, path, and protocol version, so a harmless
// request cannot be turned into a dangerous one.
func TestEnvelopeCannotBeRedirectedToAnotherEndpoint(t *testing.T) {
	h := newHarness(t)
	d := h.pair()
	other := h.pair()

	// Sealed for a harmless update of the device's own pairing.
	plain, _ := json.Marshal(map[string]any{"trust_mode": "ask"})
	sealed, _ := d.envelope.Seal("PATCH", "/api/pairings/"+d.ID, pairing.ProtocolVersion, plain)
	captured := base64.StdEncoding.EncodeToString(sealed)

	// Aimed at another device's pairing instead.
	resp := h.send(t, d, "PATCH", "/api/pairings/"+other.ID, captured)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("an envelope sealed for one path was accepted on another")
	}
	if body := errorBody(t, resp); body["error"] != "invalid_request" {
		t.Errorf("error = %v, want invalid_request", body["error"])
	}

	// And the other device's trust mode is untouched.
	p, err := h.store.Pairing(other.ID)
	if err != nil {
		t.Fatalf("pairing: %v", err)
	}
	if p.TrustMode != "auto" {
		t.Errorf("the redirected request changed state: trust mode is %q", p.TrustMode)
	}
}

// A tampered ciphertext must fail to authenticate rather than decrypt to
// something the handler acts on.
func TestTamperedEnvelopeIsRefused(t *testing.T) {
	h := newHarness(t)
	d := h.pair()

	plain, _ := json.Marshal(map[string]any{"trust_mode": "ask"})
	sealed, _ := d.envelope.Seal("PATCH", "/api/pairings/"+d.ID, pairing.ProtocolVersion, plain)

	// Flip a bit in the ciphertext, past the counter prefix.
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x01

	resp := h.send(t, d, "PATCH", "/api/pairings/"+d.ID,
		base64.StdEncoding.EncodeToString(tampered))
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a tampered envelope was accepted")
	}
	resp.Body.Close()
}

// A device's envelope must not open another device's traffic, even though both
// are paired to the same computer. Trust is never transitive (FR-054).
func TestOneDeviceCannotUseAnothersEnvelope(t *testing.T) {
	h := newHarness(t)
	first := h.pair()
	second := h.pair()

	plain, _ := json.Marshal(map[string]any{"trust_mode": "ask"})
	sealed, _ := first.envelope.Seal("PATCH", "/api/pairings/"+first.ID, pairing.ProtocolVersion, plain)

	// Second device's credential, first device's sealed payload.
	req, _ := http.NewRequest("PATCH", h.server.URL+"/api/pairings/"+first.ID, //nolint:noctx // test client
		bytes.NewReader([]byte(base64.StdEncoding.EncodeToString(sealed))))
	req.Header.Set("Authorization", "Bearer "+second.credential)

	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatal("a payload sealed by one device was opened for another")
	}
}

// The payload must not be readable on the wire. This is the property that
// makes FR-017 true in simple mode.
func TestControlPayloadIsNotReadableOnTheWire(t *testing.T) {
	h := newHarness(t)
	d := h.pair()

	const marker = "this-value-must-not-appear-in-the-clear"
	plain, _ := json.Marshal(map[string]any{"trust_mode": marker})
	sealed, _ := d.envelope.Seal("PATCH", "/api/pairings/"+d.ID, pairing.ProtocolVersion, plain)

	if bytes.Contains(sealed, []byte(marker)) {
		t.Error("the sealed payload contains the plaintext")
	}

	// And the response body likewise.
	resp := d.do("GET", "/api/pairings", nil)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	decoded, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		t.Fatalf("response was not a sealed envelope: %v", err)
	}
	if bytes.Contains(decoded, []byte("device_id")) {
		t.Error("the response body contains readable JSON field names")
	}
}

// send posts a pre-sealed body, which is how a captured request is replayed.
func (h *harness) send(t *testing.T, d *device, method, path, sealedBase64 string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(method, h.server.URL+path, //nolint:noctx // test client
		bytes.NewReader([]byte(sealedBase64)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.credential)

	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func decodeJSON(resp *http.Response, out any) error {
	return json.NewDecoder(resp.Body).Decode(out)
}
