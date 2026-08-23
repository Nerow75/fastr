package integration

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nerow75/fastr/internal/trust"
)

// The trusted-mode endpoints, end to end through the real router.
//
// The pieces below the router have their own tests: the authority in
// internal/trust, the refusals in trusted_mode_test.go. What is left, and what
// is here, is the walkthrough a person actually performs — ask the computer to
// set it up, fetch the certificate, and have the phone confirm it arrived —
// plus the two boundaries that make it safe to offer at all.

// The setup call is the computer owner's, and it is not reachable from the
// network. Generating an authority is the most consequential thing this product
// does: a key that can impersonate any site to every phone that installs it.
func TestCreatingTheAuthorityIsNotReachableFromTheNetwork(t *testing.T) {
	h := newHarness(t)

	for _, remote := range []string{"192.168.1.50:41234", "10.0.0.7:5000", "8.8.8.8:80"} {
		req := httptest.NewRequest(http.MethodPost, "/api/trust/init", nil)
		req.RemoteAddr = remote
		// Headers a phone can set must not buy loopback.
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.Header.Set("X-Real-IP", "127.0.0.1")

		rec := httptest.NewRecorder()
		h.server.Config.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("from %s: status %d, want 401", remote, rec.Code)
		}
	}

	// And nothing was created, so a refused request leaves no key behind.
	if _, err := trust.Load(h.trustDir); err == nil {
		t.Fatal("a refused request created a certificate authority")
	}
}

// The walkthrough: set it up, then fetch what the phone installs.
func TestSettingUpTrustedModeProducesACertificateToInstall(t *testing.T) {
	h := newHarness(t)

	initialised := h.initTrust(t)

	if initialised.Fingerprint == "" {
		t.Fatal("no fingerprint to compare against what the phone will show")
	}
	if len(strings.Split(initialised.Fingerprint, ":")) != 32 {
		t.Errorf("fingerprint %q is not in the form a phone displays", initialised.Fingerprint)
	}
	if initialised.CertificateURL == "" {
		t.Fatal("no certificate to install")
	}

	// The certificate is fetched without a session, because the phone needs it
	// before it can be trusted. It is public by nature and inert without the
	// key that never leaves this machine.
	resp, err := h.server.Client().Get(h.server.URL + initialised.CertificateURL) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("fetch certificate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if !strings.Contains(string(body), "BEGIN CERTIFICATE") {
		t.Fatal("what is served is not a certificate")
	}
	// The one thing that must never be served.
	if strings.Contains(string(body), "PRIVATE KEY") {
		t.Fatal("the authority's private key was served over HTTP")
	}

	// It is a usable authority, not just a plausible file.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(body) {
		t.Fatal("the served certificate cannot be used as an authority")
	}

	// And the fingerprint matches the file, so what the user compares is what
	// they installed.
	authority, err := trust.Load(h.trustDir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if authority.Fingerprint() != initialised.Fingerprint {
		t.Error("the fingerprint shown does not belong to the certificate served")
	}
}

// Setting up twice reuses the authority rather than replacing it. A new one
// would invalidate every phone already set up, and the user would find out when
// a transfer stopped rather than when they asked for anything.
func TestSettingUpTwiceKeepsTheSameAuthority(t *testing.T) {
	h := newHarness(t)

	first := h.initTrust(t)
	second := h.initTrust(t)

	if first.Fingerprint != second.Fingerprint {
		t.Error("a second setup replaced the authority every phone had installed")
	}
}

// The status endpoint is what the walkthrough reads to know which step is next.
func TestTrustStatusSaysWhereSetupStands(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	before := phone.trustStatus(t)
	if before.Ready {
		t.Error("status reports ready before anything was set up")
	}
	if before.Trusted {
		t.Error("a plain connection reports itself as trusted")
	}

	h.initTrust(t)

	after := phone.trustStatus(t)
	if !after.Ready {
		t.Error("status does not report ready after setup")
	}
	if after.Fingerprint == "" {
		t.Error("status carries no fingerprint for the user to compare")
	}
	// Still not trusted: the phone has not installed anything, and the status
	// answers about *this* connection rather than about the computer's
	// intentions.
	if after.Trusted {
		t.Error("a plain connection reports itself as trusted after setup")
	}

	// The devices it lists are the ones with live pairings, with the mode each
	// is using, per FR-047b.
	found := false
	for _, dev := range after.Devices {
		if dev.DeviceID == phone.ID {
			found = true
			if dev.Protection == "" {
				t.Error("the device list does not say which mode the device is using")
			}
		}
	}
	if !found {
		t.Errorf("the paired device is missing from the trust status: %+v", after.Devices)
	}
}

// The TLS listener actually serves, and a phone that trusts the authority
// reaches it. This is the only test that exercises T130 rather than compiling
// it.
func TestTheTrustedListenerServesWhatTheAuthoritySigned(t *testing.T) {
	dir := t.TempDir()

	authority, err := trust.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	certificate, err := authority.Issue([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	pair, err := certificate.TLS()
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}

	// A server that answers one thing, so what is being tested is the
	// connection rather than the application on top of it.
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The property everything about trusted mode rests on: the server can
		// tell, from the connection alone, that this arrived over TLS.
		if r.TLS == nil {
			http.Error(w, "not trusted", http.StatusUpgradeRequired)
			return
		}
		_, _ = io.WriteString(w, "trusted")
	}), ReadHeaderTimeout: readHeaderTimeoutForTest}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(authority.CertificatePEM())

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}

	_, port, _ := net.SplitHostPort(listener.Addr().String())
	resp, err := client.Get("https://127.0.0.1:" + port + "/") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("a phone trusting the authority could not connect: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "trusted" {
		t.Errorf("body = %q, want the server to have seen a TLS connection", body)
	}
}

const readHeaderTimeoutForTest = 5_000_000_000 // 5s, as a duration literal

type trustInitBody struct {
	Fingerprint    string   `json:"fingerprint"`
	CertificateURL string   `json:"certificate_url"`
	Addresses      []string `json:"addresses"`
}

// initTrust performs the computer owner's half of the setup.
func (h *harness) initTrust(t *testing.T) trustInitBody {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/trust/init", nil)
	req.RemoteAddr = "127.0.0.1:41234"

	rec := httptest.NewRecorder()
	h.server.Config.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("trust init: status %d: %s", rec.Code, rec.Body.String())
	}

	var out trustInitBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

type trustStatusBody struct {
	Available   bool   `json:"available"`
	Ready       bool   `json:"ready"`
	Fingerprint string `json:"fingerprint"`
	Trusted     bool   `json:"trusted"`
	Devices     []struct {
		DeviceID       string `json:"device_id"`
		Name           string `json:"name"`
		Protection     string `json:"protection"`
		RequireTrusted bool   `json:"require_trusted"`
	} `json:"devices"`
}

func (d *device) trustStatus(t *testing.T) trustStatusBody {
	t.Helper()

	const path = "/api/trust/status"
	resp := d.do("GET", path, nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("trust status: %d: %s", resp.StatusCode, raw)
	}

	var out trustStatusBody
	d.open("GET", path, resp, &out)
	return out
}
