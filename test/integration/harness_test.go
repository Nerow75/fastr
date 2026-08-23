package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/discovery"
	"github.com/Nerow75/fastr/internal/httpapi"
	"github.com/Nerow75/fastr/internal/pairing"
	"github.com/Nerow75/fastr/internal/platform"
	"github.com/Nerow75/fastr/internal/store"
	"github.com/Nerow75/fastr/internal/transfer"
)

// harness is a running server with a real store, wired the way the binary wires
// it. Tests talk to it over HTTP rather than calling handlers directly, so what
// they prove is what a device on the network would actually meet.
type harness struct {
	t         *testing.T
	server    *httptest.Server
	store     *store.Store
	codes     *pairing.Codes
	sessions  *pairing.Sessions
	events    *httpapi.Events
	tickets   *httpapi.Tickets
	pendings  *pairing.Pendings
	pipes     *transfer.Pipes
	transfers *app.Transfers
	logs      *bytes.Buffer
	scrubber  *app.Scrubber

	// discovery is swappable so a test can put the instance on a network that
	// blocks multicast without needing one.
	discovery *swappableDiscovery

	// selfID is what a phone names as the target when it sends here.
	selfID     string
	receiveDir string
	stagingDir string
	trustDir   string
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
	tickets := httpapi.NewTickets()
	pendings := pairing.NewPendings()

	// The instance's own identifier, minted the way the binary mints it. A
	// transfer aimed at it is one this machine writes to disk.
	selfID, err := st.SelfDevice("Test Computer", "linux")
	if err != nil {
		t.Fatalf("self device: %v", err)
	}

	receiveDir, stagingDir := t.TempDir(), t.TempDir()
	trustDir := filepath.Join(t.TempDir(), "trust")

	pipes := transfer.NewPipes()
	sinks := transfer.NewSinks()
	// Windows refuses to remove a directory holding an open file, so a test
	// that leaves a transfer half-finished would fail in its own cleanup rather
	// than on what it was testing.
	t.Cleanup(sinks.CloseAll)

	transfers := &app.Transfers{
		Store:    st,
		Pipes:    pipes,
		Sinks:    sinks,
		Notify:   httpapi.NewNotifier(events),
		Space:    fakeSpace(1 << 40),
		Log:      logger,
		SelfID:   selfID,
		Rules:    platform.Rules(),
		ReceiveD: receiveDir,
		StagingD: stagingDir,
	}

	bundle := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>fastr</title>")},
	}

	// A working but empty network by default: nothing is found, and looking is
	// not broken. Tests that care put something else here.
	seen := &swappableDiscovery{inner: idleDiscovery{}}

	// Declared before the router and assigned after, the way the binary does
	// it: the address does not exist until something is listening, and the
	// closure reads it at call time.
	var srv *httptest.Server

	router := httpapi.NewRouter(httpapi.Deps{
		Log:        logger,
		Store:      st,
		Sessions:   sessions,
		Codes:      codes,
		Handshakes: pairing.NewHandshakes(),
		Pendings:   pendings,
		Bundle:     fs.FS(bundle),
		Events:     events,
		Tickets:    tickets,
		Transfers:  transfers,
		DeviceName: "Test Computer",
		DeviceID:   selfID,
		Trusted:    func(*http.Request) bool { return false },
		Discovery:  seen,
		Addresses: func() []string {
			if srv == nil {
				return nil
			}
			return []string{srv.Listener.Addr().String()}
		},
		// A directory for the certificate authority, created only if a test
		// asks for one. Trusted mode is opt-in, so an unused harness has none.
		TrustDir: trustDir,
	})

	srv = httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return &harness{
		t: t, server: srv, store: st, codes: codes,
		sessions: sessions, events: events, tickets: tickets, pendings: pendings, pipes: pipes, transfers: transfers,
		logs: logs, scrubber: scrubber, discovery: seen,
		selfID: selfID, receiveDir: receiveDir, stagingDir: stagingDir,
		trustDir: trustDir,
	}
}

// setDiscovery replaces what this instance can see on the network.
func (h *harness) setDiscovery(d httpapi.Discovery) { h.discovery.set(d) }

// swappableDiscovery lets a test change the network under a running server,
// which is the only way to exercise a blocked one without having a blocked one.
type swappableDiscovery struct {
	mu    sync.Mutex
	inner httpapi.Discovery
}

func (s *swappableDiscovery) set(d httpapi.Discovery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner = d
}

func (s *swappableDiscovery) current() httpapi.Discovery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner
}

func (s *swappableDiscovery) Peers() []discovery.Peer { return s.current().Peers() }
func (s *swappableDiscovery) Unavailable() error      { return s.current().Unavailable() }

// idleDiscovery is a network where looking works and finds nothing.
type idleDiscovery struct{}

func (idleDiscovery) Peers() []discovery.Peer { return nil }
func (idleDiscovery) Unavailable() error      { return nil }

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

	// A correct code buys a place in the queue, not access: FR-010 wants a
	// human on the host to decide.
	var confirm struct {
		PendingID string `json:"pending_id"`
		State     string `json:"state"`
	}
	h.postAccepted("/api/pair/confirm", map[string]any{
		"handshake_id": init.HandshakeID,
		"code":         code.Display(),
		"proof":        base64.StdEncoding.EncodeToString(proof),
		"device_name":  "Test Phone",
		"platform":     "android",
	}, &confirm)

	if confirm.State != "awaiting_approval" {
		h.t.Fatalf("state = %q, want awaiting_approval", confirm.State)
	}

	h.approve(confirm.PendingID)

	credential := h.collectCredential(confirm.PendingID, key)

	env, err := pairing.NewEnvelope(key, pairing.ClientToServer)
	if err != nil {
		h.t.Fatalf("envelope: %v", err)
	}

	var deviceID string
	if pending, err := h.pendings.Get(confirm.PendingID); err == nil {
		deviceID = pending.DeviceID()
	}
	if deviceID == "" {
		deviceID = h.lastPairedDevice()
	}

	return &device{h: h, ID: deviceID, credential: credential, envelope: env}
}

// approve is the human on the host saying yes.
func (h *harness) approve(pendingID string) {
	h.t.Helper()
	h.postPlain("/api/pair/pending/"+pendingID+"/approve", map[string]any{}, nil)
}

// collectCredential polls the status endpoint and unseals the credential.
//
// A throwaway envelope is used, not the session one: opening here would
// advance the session envelope's receive counter, and the first real response
// would then look like a replay.
func (h *harness) collectCredential(pendingID string, key []byte) string {
	h.t.Helper()

	const path = "/api/pair/status"
	var status struct {
		State      string `json:"state"`
		Credential string `json:"credential"`
		DeviceID   string `json:"device_id"`
	}
	h.getPlain(path+"?pending="+pendingID, &status)

	if status.State != "approved" {
		h.t.Fatalf("state = %q, want approved", status.State)
	}
	if status.Credential == "" {
		h.t.Fatal("no credential returned after approval")
	}

	opener, err := pairing.NewEnvelope(key, pairing.ClientToServer)
	if err != nil {
		h.t.Fatalf("envelope: %v", err)
	}
	sealed, err := base64.StdEncoding.DecodeString(status.Credential)
	if err != nil {
		h.t.Fatalf("decode credential: %v", err)
	}
	plain, err := opener.Open(http.MethodGet, path, pairing.ProtocolVersion, sealed)
	if err != nil {
		h.t.Fatalf("open credential: %v", err)
	}
	return string(plain)
}

// lastPairedDevice finds the most recently created pairing, for the case where
// the pending entry has already been forgotten.
func (h *harness) lastPairedDevice() string {
	h.t.Helper()

	pairings, err := h.store.Pairings()
	if err != nil || len(pairings) == 0 {
		h.t.Fatalf("no pairing was created: %v", err)
	}
	newest := pairings[0]
	for _, p := range pairings[1:] {
		if p.CreatedAt.After(newest.CreatedAt) {
			newest = p
		}
	}
	return newest.DeviceID
}

// postAccepted posts and expects 202.
func (h *harness) postAccepted(path string, body any, out any) {
	h.t.Helper()
	h.postExpecting(path, body, out, http.StatusAccepted)
}

// getPlain fetches an unsealed JSON payload.
func (h *harness) getPlain(path string, out any) {
	h.t.Helper()

	resp, err := h.server.Client().Get(h.server.URL + path) //nolint:noctx // test client
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("GET %s: status %d: %s", path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			h.t.Fatalf("decode %s: %v", path, err)
		}
	}
}

// postPlain sends an unsealed request, for the pairing endpoints.
func (h *harness) postPlain(path string, body any, out any) {
	h.t.Helper()
	h.postExpecting(path, body, out, http.StatusOK)
}

func (h *harness) postExpecting(path string, body any, out any, want int) {
	h.t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		h.t.Fatalf("marshal: %v", err)
	}

	// Raw on purpose: these endpoints are reached before a session exists, so
	// there is no restricted client to route through yet, and the target is the
	// test server on loopback.
	//nolint:forbidigo,noctx // deliberate: unsealed pairing endpoint on a loopback test server
	resp, err := http.Post(h.server.URL+path, "application/json", bytes.NewReader(payload))
	if err != nil {
		h.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		h.t.Fatalf("POST %s: status %d, want %d: %s", path, resp.StatusCode, want, raw)
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

// contextWithTimeout bounds a request that would otherwise stream forever.
func contextWithTimeout(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), d)
}

// fakeSpace reports a fixed amount of free space, so tests are not at the mercy
// of the machine running them.
type fakeSpace uint64

func (f fakeSpace) FreeSpace(string) (uint64, error) { return uint64(f), nil }

// newHarnessWithSpace builds a harness whose destination reports a fixed amount
// of free space, so the refusal path can be exercised without filling a disk.
func newHarnessWithSpace(t *testing.T, free uint64) *harness {
	t.Helper()
	h := newHarness(t)
	h.transfers.Space = fakeSpace(free)
	return h
}

// host returns a client acting as this computer's own page.
//
// The desktop-to-phone path is the one that goes through the pipe, and it is
// the *host* that supplies into it. A test that used two paired phones for that
// was testing a topology the product does not have: two phones going through
// this computer is a relay (FR-053), which stages rather than pipes, because a
// phone cannot hold one streaming request open for the length of a file.
func (h *harness) host(t *testing.T) *device {
	t.Helper()

	granted := h.hostSession(t)

	key, err := base64.StdEncoding.DecodeString(granted.Key)
	if err != nil {
		t.Fatalf("decode host key: %v", err)
	}
	env, err := pairing.NewEnvelope(key, pairing.ClientToServer)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}

	return &device{h: h, ID: granted.DeviceID, credential: granted.Credential, envelope: env}
}
