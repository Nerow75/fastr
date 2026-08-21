package integration

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nerow75/fastr/internal/pairing"
)

// The host's own session.
//
// The computer's page used to pair with itself, and nobody found the step, so
// sending from the computer was unreachable in practice. It is now granted a
// session over loopback instead.
//
// The security argument rests entirely on that restriction, so that is what
// these tests are mostly about: this endpoint hands out a working credential
// with no code, and it must be unreachable from the network.

type hostSession struct {
	DeviceID   string `json:"device_id"`
	Credential string `json:"credential"`
	Key        string `json:"key"`
	Name       string `json:"name"`
}

func (h *harness) hostSession(t *testing.T) hostSession {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/pair/host", nil)
	req.RemoteAddr = "127.0.0.1:41234"

	rec := httptest.NewRecorder()
	h.server.Config.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("host session: status %d: %s", rec.Code, rec.Body.String())
	}

	var out hostSession
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// The credential works, or the whole point is missed: the page has to be able
// to declare a transfer without ever typing a code.
func TestHostSessionGrantsAWorkingCredential(t *testing.T) {
	h := newHarness(t)

	granted := h.hostSession(t)

	if granted.DeviceID != h.selfID {
		t.Errorf("device_id = %q, want this instance's own %q", granted.DeviceID, h.selfID)
	}
	if granted.Credential == "" || granted.Key == "" {
		t.Fatalf("incomplete grant: %+v", granted)
	}

	key, err := base64.StdEncoding.DecodeString(granted.Key)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}

	// Use it exactly as the page does: a sealed request on the shared key.
	env, err := pairing.NewEnvelope(key, pairing.ClientToServer)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	host := &device{h: h, ID: granted.DeviceID, credential: granted.Credential, envelope: env}

	resp := host.do("GET", "/api/devices", nil)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("the granted credential was refused: status %d", resp.StatusCode)
	}

	var body struct {
		Devices []struct {
			ID string `json:"id"`
		} `json:"devices"`
	}
	host.open("GET", "/api/devices", resp, &body)

	if len(body.Devices) == 0 {
		t.Error("the host session sees no devices at all, not even this computer")
	}
}

// The one that matters. This endpoint hands out a credential to anyone who asks,
// so everything depends on "anyone" meaning "on this machine". Reaching it from
// the network would make every pairing code and every approval pointless.
func TestHostSessionIsRefusedFromTheNetwork(t *testing.T) {
	h := newHarness(t)

	for _, remote := range []string{"192.168.1.50:41234", "10.0.0.7:5000", "8.8.8.8:80"} {
		req := httptest.NewRequest(http.MethodPost, "/api/pair/host", nil)
		req.RemoteAddr = remote
		// A header a phone could set must not buy loopback.
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.Header.Set("X-Real-IP", "127.0.0.1")

		rec := httptest.NewRecorder()
		h.server.Config.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("from %s: status %d, want 401", remote, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "credential") {
			t.Errorf("from %s: the refusal carried credential material: %s", remote, rec.Body.String())
		}
	}
}

// A paired phone holding a valid credential is still not the host.
func TestAPairedDeviceCannotTakeTheHostSession(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	req := httptest.NewRequest(http.MethodPost, "/api/pair/host", nil)
	req.RemoteAddr = "192.168.1.50:41234"
	req.Header.Set("Authorization", "Bearer "+phone.credential)

	rec := httptest.NewRecorder()
	h.server.Config.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a paired device took the host session: status %d", rec.Code)
	}
}

// Every grant brings a new key, and the server's counter is reset to match.
//
// Reusing the key would reuse nonces, because the envelope derives them from a
// counter that restarts at zero for a new session. It would also desynchronise
// the two sides: the server caches one envelope per device, so a page starting
// at zero against an advanced counter has every request refused as a replay.
// That is not hypothetical — it is what the first version of this endpoint did,
// and it made the computer's page unusable within a second of loading.
func TestHostSessionIssuesAFreshKeyAndResetsTheCounter(t *testing.T) {
	h := newHarness(t)

	first := h.hostSession(t)
	second := h.hostSession(t)

	if first.Key == second.Key {
		t.Error("the same envelope key was handed out twice, which reuses nonces")
	}
	if first.Credential == second.Credential {
		t.Error("the same credential was handed out twice")
	}

	// The newest grant works, repeatedly: several sealed requests in a row must
	// all be accepted, which is what the replay storm broke.
	host := &device{
		h: h, ID: second.DeviceID, credential: second.Credential,
		envelope: mustEnvelope(t, second.Key),
	}
	for i := range 3 {
		resp := host.do("GET", "/api/devices", nil)
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("request %d refused: status %d", i+1, resp.StatusCode)
		}
		host.open("GET", "/api/devices", resp, nil)
	}

	// And the replaced credential no longer does.
	resp := (&device{
		h: h, ID: first.DeviceID, credential: first.Credential,
		envelope: mustEnvelope(t, first.Key),
	}).do("GET", "/api/devices", nil)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Error("the replaced credential still works")
	}
}

func mustEnvelope(t *testing.T, key string) *pairing.Envelope {
	t.Helper()

	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	env, err := pairing.NewEnvelope(raw, pairing.ClientToServer)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	return env
}

// A pairing lasts a year, so the device list keeps every phone ever connected.
// Whether one can be sent to right now is a different question, and offering an
// unreachable device as a destination with nothing to say so is the interface
// promising what it cannot do.
func TestDeviceListSaysWhoIsReachable(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	if h.events.Connected(phone.ID) {
		t.Error("a device with no stream open is reported as reachable")
	}

	// Opening the stream is what makes it reachable.
	ticket, err := h.tickets.Issue(phone.ID)
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}

	ctx, cancel := contextWithTimeout(t, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		h.server.URL+"/api/events?ticket="+url.QueryEscape(ticket), nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	deadline := time.Now().Add(3 * time.Second)
	for !h.events.Connected(phone.ID) {
		if time.Now().After(deadline) {
			t.Fatal("the stream is open but the device is not reported as reachable")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// And closing it makes it unreachable again.
	cancel()
	deadline = time.Now().Add(3 * time.Second)
	for h.events.Connected(phone.ID) {
		if time.Now().After(deadline) {
			t.Fatal("the stream closed but the device is still reported as reachable")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
