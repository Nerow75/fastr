package integration

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// FR-002: the connection point is presented as a QR code.

func TestQRRendersAScannableCode(t *testing.T) {
	h := newHarness(t)

	resp, err := h.server.Client().Get(h.server.URL + "/qr?url=" + //nolint:noctx // test client
		"http%3A%2F%2F192.168.1.20%3A8443")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("content type = %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	svg := string(body)

	for _, want := range []string{"<svg", "viewBox", "<path", `fill="#ffffff"`} {
		if !strings.Contains(svg, want) {
			t.Errorf("the SVG is missing %q", want)
		}
	}
	// A code with no dark modules is a blank square, which scans as nothing.
	if !strings.Contains(svg, "M") {
		t.Error("the code has no modules")
	}
	// An explicit light background: a transparent code on a dark theme is
	// invisible to a scanner, which expects dark on light.
	if !strings.Contains(svg, `<rect`) {
		t.Error("the code has no background")
	}
}

// Rendering an arbitrary URL would let anyone put their link on the user's
// screen looking as though fastr had produced it.
func TestQRRefusesNonLocalURLs(t *testing.T) {
	h := newHarness(t)

	for _, target := range []string{
		"http%3A%2F%2Fexample.com",
		"https%3A%2F%2Fevil.test%2Fphish",
		"http%3A%2F%2F8.8.8.8",
		"javascript%3Aalert(1)",
		"",
	} {
		resp, err := h.server.Client().Get(h.server.URL + "/qr?url=" + target) //nolint:noctx // test client
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			t.Errorf("the QR endpoint accepted %q", target)
		}
	}
}

func TestQRAcceptsEveryLocalForm(t *testing.T) {
	h := newHarness(t)

	for _, target := range []string{
		"http%3A%2F%2F127.0.0.1%3A8443",
		"http%3A%2F%2Flocalhost%3A8443",
		"http%3A%2F%2F192.168.68.59%3A8443",
		"http%3A%2F%2F10.0.0.5%3A8443%2Fpair",
		"https%3A%2F%2F192.168.1.20%3A8443",
	} {
		resp, err := h.server.Client().Get(h.server.URL + "/qr?url=" + target) //nolint:noctx // test client
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("the QR endpoint refused %q: status %d", target, resp.StatusCode)
		}
	}
}
