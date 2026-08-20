package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/httpapi"
	"github.com/Nerow75/fastr/internal/pairing"
	"github.com/Nerow75/fastr/internal/store"
)

// harness is a running server with a real store, wired the way the binary wires
// it. Tests talk to it over HTTP rather than calling handlers directly, so what
// they prove is what a device on the network would actually meet.
type harness struct {
	t        *testing.T
	server   *httptest.Server
	store    *store.Store
	codes    *pairing.Codes
	sessions *pairing.Sessions
	events   *httpapi.Events
	logs     *bytes.Buffer
	scrubber *app.Scrubber
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "fastr.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	logs := &bytes.Buffer{}
	logger, scrubber := app.NewLogger(logs, slog.LevelDebug)

	codes := pairing.NewCodes()
	codes.SetScrubHooks(scrubber.Add, scrubber.Remove)

	sessions := pairing.NewSessions(st)
	events := httpapi.NewEvents()

	bundle := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>fastr</title>")},
	}

	router := httpapi.NewRouter(httpapi.Deps{
		Log:        logger,
		Store:      st,
		Sessions:   sessions,
		Codes:      codes,
		Handshakes: pairing.NewHandshakes(),
		Bundle:     fs.FS(bundle),
		Events:     events,
		DeviceName: "Test Computer",
		DeviceID:   "computer-1",
		Trusted:    func(*http.Request) bool { return false },
	})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return &harness{
		t: t, server: srv, store: st, codes: codes,
		sessions: sessions, events: events, logs: logs, scrubber: scrubber,
	}
}

// device is a paired client: its credential and its envelope.
type device struct {
	h          *harness
	ID         string
	credential string
	envelope   *pairing.Envelope
}

// pair runs the full handshake the way a browser does, and returns a usable
// client. Anything that pairs in a test pairs through the real endpoints.
func (h *harness) pair() *device {
	h.t.Helper()

	code, err := h.codes.Issue()
	if err != nil {
		h.t.Fatalf("issue code: %v", err)
	}

	clientPriv, clientPub, err := pairing.GenerateClientKeypair()
	if err != nil {
		h.t.Fatalf("keypair: %v", err)
	}

	var init struct {
		HandshakeID     string `json:"handshake_id"`
		ServerPublicKey string `json:"server_pub"`
		Salt            string `json:"salt"`
	}
	h.postPlain("/api/pair/init", map[string]any{
		"client_pub":  base64.StdEncoding.EncodeToString(clientPub),
		"device_name": "Test Phone",
		"platform":    "android",
	}, &init)

	serverPub, _ := base64.StdEncoding.DecodeString(init.ServerPublicKey)
	salt, _ := base64.StdEncoding.DecodeString(init.Salt)

	key, proof, err := pairing.ClientDerive(clientPriv, serverPub, clientPub, salt, code.Display(), init.HandshakeID)
	if err != nil {
		h.t.Fatalf("derive: %v", err)
	}

	var confirm struct {
		Credential string `json:"credential"`
		DeviceID   string `json:"device_id"`
	}
	h.postPlain("/api/pair/confirm", map[string]any{
		"handshake_id": init.HandshakeID,
		"code":         code.Display(),
		"proof":        base64.StdEncoding.EncodeToString(proof),
		"device_name":  "Test Phone",
		"platform":     "android",
	}, &confirm)

	env, err := pairing.NewEnvelope(key, pairing.ClientToServer)
	if err != nil {
		h.t.Fatalf("envelope: %v", err)
	}

	return &device{h: h, ID: confirm.DeviceID, credential: confirm.Credential, envelope: env}
}

// postPlain sends an unsealed request, for the pairing endpoints.
func (h *harness) postPlain(path string, body any, out any) {
	h.t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		h.t.Fatalf("marshal: %v", err)
	}

	resp, err := http.Post(h.server.URL+path, "application/json", bytes.NewReader(payload)) //nolint:noctx // test client
	if err != nil {
		h.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("POST %s: status %d: %s", path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			h.t.Fatalf("decode %s: %v", path, err)
		}
	}
}

// do sends an authenticated, sealed request and returns the raw response.
func (d *device) do(method, path string, body any) *http.Response {
	d.h.t.Helper()

	var reader io.Reader
	if body != nil {
		plain, err := json.Marshal(body)
		if err != nil {
			d.h.t.Fatalf("marshal: %v", err)
		}
		sealed, err := d.envelope.Seal(method, path, pairing.ProtocolVersion, plain)
		if err != nil {
			d.h.t.Fatalf("seal: %v", err)
		}
		reader = bytes.NewReader([]byte(base64.StdEncoding.EncodeToString(sealed)))
	}

	req, err := http.NewRequest(method, d.h.server.URL+path, reader) //nolint:noctx // test client
	if err != nil {
		d.h.t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.credential)

	resp, err := d.h.server.Client().Do(req)
	if err != nil {
		d.h.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// open decrypts a sealed response body.
func (d *device) open(method, path string, resp *http.Response, out any) {
	d.h.t.Helper()
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		d.h.t.Fatalf("read: %v", err)
	}
	sealed, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		d.h.t.Fatalf("decode base64: %v (body was %q)", err, raw)
	}
	plain, err := d.envelope.Open(method, path, pairing.ProtocolVersion, sealed)
	if err != nil {
		d.h.t.Fatalf("open envelope: %v", err)
	}
	if out != nil {
		if err := json.Unmarshal(plain, out); err != nil {
			d.h.t.Fatalf("decode payload: %v", err)
		}
	}
}

// errorBody decodes a catalogue error response.
func errorBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body
}

// discardLogger is for tests that exercise the server without inspecting logs.
func discardLogger() *slog.Logger {
	logger, _ := app.NewLogger(io.Discard, slog.LevelError)
	return logger
}
