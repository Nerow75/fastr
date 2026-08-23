package integration

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/Nerow75/fastr/internal/httpapi"
	"github.com/Nerow75/fastr/internal/localnet"
)

// Principle I: no byte of user content, and no request of any kind, leaves the
// local network. This is quality gate 3 in the constitution.
//
// What these tests prove, and what they do not:
//
//   - They prove the bind logic refuses a non-local address, that the address
//     classifier is correct at its boundaries, and that the served bundle
//     references no external host.
//   - They do not observe real sockets. A process can only watch its own
//     syscalls with OS-level tooling, which is what `make test-network` in
//     quickstart.md does at the network boundary during a live transfer.
//
// The third leg is the linter: .golangci.yml forbids http.Get, http.DefaultClient,
// and net.Dial outright, so the ways an outbound request usually appears are
// rejected before they reach a review.

func TestLocalAddressClassification(t *testing.T) {
	local := []string{
		"127.0.0.1", "127.0.0.1:8443", "localhost", "localhost:8443",
		"::1", "[::1]:8443",
		"192.168.1.20", "192.168.1.20:8443",
		"10.0.0.5", "172.16.4.9", "172.31.255.254",
		"169.254.10.1", // link-local, which is what a network with no DHCP gives out
	}
	for _, addr := range local {
		if !localnet.IsAddr(addr) {
			t.Errorf("localnet.IsAddr(%q) = false, want true", addr)
		}
	}

	remote := []string{
		"8.8.8.8", "8.8.8.8:53",
		"1.1.1.1", "93.184.216.34:443",
		"example.com", "discord.com:443", "drive.google.com",
		"172.32.0.1",  // just outside the private range
		"172.15.0.1",  // just below it
		"11.0.0.1",    // adjacent to 10/8
		"192.169.0.1", // adjacent to 192.168/16
	}
	for _, addr := range remote {
		if localnet.IsAddr(addr) {
			t.Errorf("localnet.IsAddr(%q) = true, want false", addr)
		}
	}
}

// FR-001 and the inbound half of Principle I: the server refuses to bind an
// address that is not on the local network. A machine with a public address
// must not be talked into exposing itself to the internet.
func TestServerRefusesToBindANonLocalAddress(t *testing.T) {
	srv := httpapi.New(httpapi.Options{Logger: discardLogger(), Router: http.NotFoundHandler()})

	for _, addr := range []string{"8.8.8.8", "93.184.216.34", "0.0.0.0"} {
		err := srv.Start([]string{addr}, 0)
		if err == nil {
			_ = srv.Stop(t.Context())
			t.Errorf("Start(%q) succeeded, want a refusal", addr)
			continue
		}
		if !strings.Contains(err.Error(), "local network") && !strings.Contains(err.Error(), "not an IP") {
			t.Errorf("Start(%q) failed for the wrong reason: %v", addr, err)
		}
	}
}

// FR-001: nothing listens until the user starts it, and stopping releases the
// socket rather than leaving it bound.
func TestServerListensOnlyBetweenStartAndStop(t *testing.T) {
	srv := httpapi.New(httpapi.Options{Logger: discardLogger(), Router: http.NotFoundHandler()})

	if srv.Running() {
		t.Fatal("a freshly constructed server is already listening")
	}
	if len(srv.Addresses()) != 0 {
		t.Fatal("a freshly constructed server reports addresses")
	}

	if err := srv.Start([]string{"127.0.0.1"}, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !srv.Running() {
		t.Error("Running() is false after Start")
	}
	if len(srv.Addresses()) == 0 {
		t.Error("no address reported while running")
	}

	// Starting twice must fail rather than binding a second socket.
	if err := srv.Start([]string{"127.0.0.1"}, 0); err == nil {
		t.Error("a second Start succeeded")
	}

	if err := srv.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if srv.Running() {
		t.Error("Running() is true after Stop")
	}
	if len(srv.Addresses()) != 0 {
		t.Error("addresses are still reported after Stop")
	}
}

// externalURL matches an absolute URL to a host that is not loopback. Principle I
// forbids fetching a font, an icon, or a script from anywhere.
var externalURL = regexp.MustCompile(`https?://(?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}`)

// The served page must reference no external host. A CDN font is a request
// leaving the network just as surely as an upload is.
func TestServedBundleReferencesNoExternalHost(t *testing.T) {
	h := newHarness(t)

	resp, err := h.server.Client().Get(h.server.URL + "/") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if found := externalURL.FindAllString(string(body), -1); len(found) > 0 {
		t.Errorf("the served page references external hosts: %v", found)
	}
}

// The content policy must forbid connections anywhere but the origin, so even a
// compromised bundle cannot exfiltrate. This is the browser enforcing Principle I
// rather than us intending it.
func TestContentPolicyForbidsExternalConnections(t *testing.T) {
	h := newHarness(t)

	resp, err := h.server.Client().Get(h.server.URL + "/") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("no Content-Security-Policy on the served page")
	}

	required := map[string]string{
		"connect-src": "connect-src 'self'",
		"default-src": "default-src 'none'",
		"script-src":  "script-src 'self'",
		"font-src":    "font-src 'self'",
		"form-action": "form-action 'none'",
	}
	for name, directive := range required {
		if !strings.Contains(csp, directive) {
			t.Errorf("policy is missing %s: %q", name, csp)
		}
	}
}
