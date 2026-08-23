package integration

import (
	"net/http"
	"testing"
	"time"

	"github.com/Nerow75/fastr/internal/store"
)

// FR-054: trust is never transitive.
//
// A relayed transfer needs **both** phones paired with the relaying computer.
// Being paired with the sender says nothing whatever about the machine it wants
// to reach: a visitor's phone that can talk to my computer must not, by that
// fact alone, be able to push files at my partner's phone because it happened
// to learn its identifier.
//
// The identifier is not a secret either — it travels in the device list every
// paired device can read — so this cannot rest on nobody knowing it. It rests
// on the relay asking about the target's pairing every time.

// A device that is not paired here cannot be sent to through here.
func TestARelayRefusesATargetItHasNoPairingWith(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()

	// A device the computer knows of but has no live pairing with: what a
	// revoked or expired phone looks like, and what an identifier picked out of
	// thin air looks like too.
	const stranger = "01STRANGERPHONEID00000000"

	resp := sender.do("POST", "/api/transfers", map[string]any{
		"target_device_id": stranger,
		"items":            []map[string]any{{"name": "uninvited.jpg", "size": 16}},
	})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		t.Fatal("a transfer was accepted for a device this computer has no pairing with")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// Revocation reaches the relay too, and reaches it immediately. A phone removed
// from this computer stops being a destination the same moment it stops being a
// sender.
func TestRevokingTheTargetStopsItBeingRelayedTo(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	target := h.pair()
	admin := h.pair()

	// It works while both are paired, so the refusal below is about the change
	// rather than about the setup.
	first := sender.declare(t, target.ID, "before.jpg", 16)
	if first.State == "" {
		t.Fatal("the first transfer was not accepted")
	}

	admin.revoke(t, target.ID)

	resp := sender.do("POST", "/api/transfers", map[string]any{
		"target_device_id": target.ID,
		"items":            []map[string]any{{"name": "after.jpg", "size": 16}},
	})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		t.Fatal("a revoked device was still relayed to")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// An expired pairing is not a pairing. The window is the trust mode's, and the
// relay asks the same question every other entry point asks.
func TestAnExpiredTargetPairingIsNotRelayedTo(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	target := h.pair()

	// Wind the clock past the automatic-trust window rather than waiting a
	// year: the store takes its time from a function for exactly this.
	h.at(timeAfter(store.ExpiryAuto))

	resp := sender.do("POST", "/api/transfers", map[string]any{
		"target_device_id": target.ID,
		"items":            []map[string]any{{"name": "too-late.jpg", "size": 16}},
	})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		t.Fatal("a device with a lapsed pairing was relayed to")
	}
}

// The sender's own standing is checked as well, which is the other half of
// "both phones". A revoked sender cannot reach anything, relay included.
func TestARevokedSenderCannotRelay(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	target := h.pair()
	admin := h.pair()

	admin.revoke(t, sender.ID)

	resp := sender.do("POST", "/api/transfers", map[string]any{
		"target_device_id": target.ID,
		"items":            []map[string]any{{"name": "nope.jpg", "size": 16}},
	})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		t.Fatal("a revoked device declared a relayed transfer")
	}
}

// The relaying computer's own trust setting applies to relayed transfers too.
// The bytes land on its disk, so the question "may this device write here
// without anyone looking" is exactly as relevant as it is for a file addressed
// to the computer itself.
func TestTheRelayHonoursItsOwnTrustModeForTheSender(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	target := h.pair()

	if err := h.store.SetTrustMode(sender.ID, store.TrustAsk); err != nil {
		t.Fatalf("set trust: %v", err)
	}

	tr := sender.declare(t, target.ID, "asked-first.jpg", 16)
	if tr.State != "awaiting_acceptance" {
		t.Errorf("state = %q, want awaiting_acceptance", tr.State)
	}
}

// timeAfter is now plus a window, for winding the store's clock forward.
func timeAfter(d time.Duration) time.Time { return time.Now().Add(d + time.Hour) }
