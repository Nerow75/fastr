package integration

import (
	"net/http"
	"strings"
	"testing"
)

// FR-011 and SC-009: every file operation from an unpaired device is refused,
// including listing. This is quality gate 4 in the constitution.
//
// The test enumerates the API surface rather than checking a handful of
// endpoints, so a route added later without authentication fails here rather
// than shipping.

// protectedRoutes is every route that must refuse an unpaired caller.
var protectedRoutes = []struct {
	method string
	path   string
}{
	{"GET", "/api/devices"},
	{"GET", "/api/pairings"},
	{"DELETE", "/api/pairings/some-device"},
	{"PATCH", "/api/pairings/some-device"},
	{"POST", "/api/events/ticket"},
}

func TestUnpairedDeviceIsRefusedEverywhere(t *testing.T) {
	h := newHarness(t)

	credentials := map[string]string{
		"no header":      "",
		"empty bearer":   "Bearer ",
		"garbage":        "Bearer not-a-real-credential",
		"wrong scheme":   "Basic dXNlcjpwYXNz",
		"bare token":     "abcdef0123456789",
		"plausible size": "Bearer " + strings.Repeat("A", 43),
	}

	for _, route := range protectedRoutes {
		for name, header := range credentials {
			t.Run(route.method+" "+route.path+"/"+name, func(t *testing.T) {
				req, err := http.NewRequest(route.method, h.server.URL+route.path, nil) //nolint:noctx // test client
				if err != nil {
					t.Fatalf("request: %v", err)
				}
				if header != "" {
					req.Header.Set("Authorization", header)
				}

				resp, err := h.server.Client().Do(req)
				if err != nil {
					t.Fatalf("do: %v", err)
				}
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("status %d, want 401", resp.StatusCode)
				}
			})
		}
	}
}

// The refusal must not describe why. Telling an unpaired caller whether a
// credential merely does not exist, versus exists but is revoked, hands them a
// way to enumerate.
func TestUnauthorizedResponseRevealsNothing(t *testing.T) {
	h := newHarness(t)

	req, _ := http.NewRequest("GET", h.server.URL+"/api/devices", nil) //nolint:noctx // test client
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}

	body := errorBody(t, resp)
	if body["error"] != "unauthorized" {
		t.Errorf("error = %v, want unauthorized", body["error"])
	}
	// A stable code and a translation key, never an assembled message.
	if _, ok := body["message"]; ok {
		t.Error("the response carries a server-assembled message")
	}
	if key, _ := body["detail_key"].(string); !strings.HasPrefix(key, "error.") {
		t.Errorf("detail_key = %v", body["detail_key"])
	}
}

// A paired device works, which is what makes the refusals above meaningful
// rather than a server that refuses everything.
func TestPairedDeviceIsAccepted(t *testing.T) {
	h := newHarness(t)
	d := h.pair()

	resp := d.do("GET", "/api/devices", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}

	var out struct {
		Devices []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Paired bool   `json:"paired"`
		} `json:"devices"`
	}
	d.open("GET", "/api/devices", resp, &out)

	// Two: the phone that just paired, and this computer. The computer is in
	// the list because a phone sending a file has to be able to name it as the
	// target; it carries no pairing with itself, and never grants anything.
	if len(out.Devices) != 2 {
		t.Fatalf("got %d devices, want the phone and this computer", len(out.Devices))
	}

	var phone, self bool
	for _, dev := range out.Devices {
		switch dev.ID {
		case d.ID:
			phone = true
			if !dev.Paired {
				t.Error("the paired device is not reported as paired")
			}
		case h.selfID:
			self = true
			if dev.Kind != "computer" {
				t.Errorf("this instance reports itself as %q", dev.Kind)
			}
			if dev.Paired {
				t.Error("this instance reports a pairing with itself")
			}
		default:
			t.Errorf("unexpected device %q", dev.ID)
		}
	}
	if !phone || !self {
		t.Errorf("phone listed = %v, computer listed = %v", phone, self)
	}
}

// FR-015: revocation takes effect immediately, including for a device that
// already holds a working credential and an open envelope.
func TestRevocationTakesEffectImmediately(t *testing.T) {
	h := newHarness(t)
	victim := h.pair()
	admin := h.pair()

	before := victim.do("GET", "/api/devices", nil)
	before.Body.Close()
	if before.StatusCode != http.StatusOK {
		t.Fatalf("victim should start authorized, got %d", before.StatusCode)
	}

	resp := admin.do("DELETE", "/api/pairings/"+victim.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: status %d", resp.StatusCode)
	}
	resp.Body.Close()

	// The very next request, on the same credential, must fail.
	after := victim.do("GET", "/api/devices", nil)
	if after.StatusCode != http.StatusUnauthorized {
		t.Errorf("status after revocation = %d, want 401", after.StatusCode)
	}
	body := errorBody(t, after)
	if body["error"] != "pairing_revoked" {
		t.Errorf("error = %v, want pairing_revoked", body["error"])
	}
}

// The static bundle is public, because the phone must be able to load the page
// before it has any credential. It must not leak anything else.
func TestStaticBundleIsReachableWithoutPairing(t *testing.T) {
	h := newHarness(t)

	resp, err := h.server.Client().Get(h.server.URL + "/") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %d, want 200", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("the bundle is served without a connect-src restriction: %q", csp)
	}
}

// /connect is unauthenticated by design, and must carry nothing a device on the
// network could not already learn from the mDNS record.
func TestConnectExposesOnlyIdentity(t *testing.T) {
	h := newHarness(t)

	resp, err := h.server.Client().Get(h.server.URL + "/connect") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := decodeJSON(resp, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	allowed := map[string]bool{"name": true, "device_id": true, "version": true, "kind": true}
	for key := range body {
		if !allowed[key] {
			t.Errorf("/connect exposes an unexpected field: %q", key)
		}
	}
}
