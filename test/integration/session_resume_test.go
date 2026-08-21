package integration

import (
	"net/http"
	"testing"

	"github.com/Nerow75/fastr/internal/pairing"
)

// Surviving a page reload.
//
// The envelope refuses any counter it has already seen, and the server keeps
// the highest one per device for as long as the process runs. A browser page
// keeps its counter in memory, so a reload used to start again at one and have
// every request refused as a replay — the session stayed dead until the server
// was restarted. On a phone, a reload is among the most ordinary things that
// can happen, so this was not an edge case.
//
// The client now claims a block of counters at each load rather than counting
// from zero (see COUNTER_BLOCK in web/src/lib/session.ts). These tests pin both
// halves: a resumed page works, and a genuine replay is still refused.

// resumed rebuilds a device the way a reloaded page does: same credential and
// key from site data, a new envelope, and a counter past anything the previous
// page could have used.
func (d *device) resumed(t *testing.T, startCounter uint64) *device {
	t.Helper()

	p, err := d.h.store.Pairing(d.ID)
	if err != nil {
		t.Fatalf("pairing: %v", err)
	}

	env, err := pairing.NewEnvelopeAt(p.SessionKey, pairing.ClientToServer, startCounter)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	return &device{h: d.h, ID: d.ID, credential: d.credential, envelope: env}
}

func (d *device) mustSeal(t *testing.T, path string) {
	t.Helper()

	resp := d.do("POST", path, map[string]any{})
	if resp.StatusCode != http.StatusOK {
		body := errorBody(t, resp)
		t.Fatalf("%s refused: status %d, error %v", path, resp.StatusCode, body["error"])
	}
	d.open("POST", path, resp, nil)
}

// The whole point: reloading the page does not end the session.
func TestAReloadedPageKeepsItsSession(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	const path = "/api/events/ticket"
	for range 3 {
		phone.mustSeal(t, path)
	}

	// The page reloads and claims the next block.
	reloaded := phone.resumed(t, 1_000_000)
	for range 3 {
		reloaded.mustSeal(t, path)
	}

	// And again, as a second reload would.
	reloaded.resumed(t, 2_000_000).mustSeal(t, path)
}

// The protection this counter exists for is unchanged: a counter already seen
// is still refused, which is what stops an observer on the network replaying a
// captured request.
func TestAReplayedCounterIsStillRefused(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	const path = "/api/events/ticket"
	phone.resumed(t, 5_000).mustSeal(t, path) // server high-water is now 5001

	// A page that started lower, or an attacker resending what it captured.
	resp := phone.resumed(t, 10).do("POST", path, map[string]any{})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatal("a counter below the high-water mark was accepted")
	}
	if body := errorBody(t, resp); body["error"] != "replay_detected" {
		t.Errorf("error = %v, want replay_detected", body["error"])
	}
}
