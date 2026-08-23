package integration

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Nerow75/fastr/internal/pairing"
	"github.com/Nerow75/fastr/internal/store"
)

// Trust modes, per FR-016 and FR-016a to FR-016d.
//
// Pairing answers "may this device talk to me at all". The trust mode answers a
// narrower question that comes up on every transfer: **may it write to my disk
// without anyone looking?** The two are separate because a pairing lasts a year
// and the second answer changes inside one: a phone that was mine becomes a
// phone I lent to someone.
//
// Everything here is about what arrives *unasked*. No trust mode ever lets a
// device read something it was not sent.

// setTrust changes a device's mode the way the interface does.
func (d *device) setTrust(t *testing.T, deviceID string, mode store.TrustMode) {
	t.Helper()

	path := "/api/pairings/" + deviceID
	resp := d.do("PATCH", path, map[string]any{"trust_mode": string(mode)})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("set trust: status %d: %s", resp.StatusCode, raw)
	}
	resp.Body.Close()
}

// answer accepts or declines an incoming transfer, as the target device.
func (d *device) answer(t *testing.T, transferID, verb string) *http.Response {
	t.Helper()
	return d.do("POST", fmt.Sprintf("/api/transfers/%s/%s", transferID, verb), map[string]any{})
}

// FR-016a: an ask-every-time device waits for a human, and nothing it sends
// touches the disk before one answers.
func TestAnAskEveryTimeDeviceWaitsForAHuman(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	if err := h.store.SetTrustMode(phone.ID, store.TrustAsk); err != nil {
		t.Fatalf("set trust: %v", err)
	}

	payload := []byte("not until someone says yes")
	tr := phone.declare(t, h.selfID, "asked.bin", uint64(len(payload)))

	if tr.State != "awaiting_acceptance" {
		t.Fatalf("state = %q, want awaiting_acceptance", tr.State)
	}

	// The sender is told to wait, not told it failed: waiting is a different
	// answer from broken, and it is the one a retry should act on.
	resp := phone.upload(t, tr.ID, 0, 0, payload)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if body := errorBody(t, resp); body["error"] != "awaiting_acceptance" {
		t.Errorf("error = %v, want awaiting_acceptance", body["error"])
	}

	// And nothing was written, which is the whole point of asking.
	if staged := stagedFiles(t, h); len(staged) != 0 {
		t.Errorf("bytes reached staging before anyone accepted: %v", staged)
	}
}

// And once a human says yes, it runs like any other transfer.
func TestAcceptingLetsTheTransferRun(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	if err := h.store.SetTrustMode(phone.ID, store.TrustAsk); err != nil {
		t.Fatalf("set trust: %v", err)
	}

	payload := []byte("allowed after a tap")
	tr := phone.declare(t, h.selfID, "accepted.bin", uint64(len(payload)))

	// Accepted by the computer, which is the target. Its own page holds a
	// session as this device, which is what makes it the one who may answer.
	accepted, err := h.transfers.Accept(mustID(t, tr.ID))
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.State != store.StateQueued {
		t.Fatalf("state after accepting = %q, want queued", accepted.State)
	}

	phone.uploadOK(t, tr.ID, 0, 0, payload)
	if got := phone.completeOK(t, tr.ID, 0, digestOf(t, payload)).State; got != "completed" {
		t.Errorf("state = %q, want completed", got)
	}
}

// FR-038: declining is an answer, and the sender is told what it was rather
// than left watching a transfer that never starts.
func TestDecliningTellsTheSenderWhy(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	if err := h.store.SetTrustMode(phone.ID, store.TrustAsk); err != nil {
		t.Fatalf("set trust: %v", err)
	}

	tr := phone.declare(t, h.selfID, "refused.bin", 32)

	if err := h.transfers.Decline(mustID(t, tr.ID)); err != nil {
		t.Fatalf("decline: %v", err)
	}

	final := phone.transferState(t, tr.ID)
	if final.State != "failed" {
		t.Fatalf("state = %q, want failed", final.State)
	}

	stored, err := h.store.Transfer(mustID(t, tr.ID))
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if stored.FailureCause != store.CauseDeclined {
		t.Errorf("cause = %q, want declined", stored.FailureCause)
	}
}

// FR-016d: nobody answering is also an answer, after a bounded wait. A transfer
// queued forever holds the sender's attention and a place in a queue that runs
// one thing at a time.
func TestATransferNobodyAnswersIsRefusedRatherThanQueuedForever(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	if err := h.store.SetTrustMode(phone.ID, store.TrustAsk); err != nil {
		t.Fatalf("set trust: %v", err)
	}

	tr := phone.declare(t, h.selfID, "unanswered.bin", 32)

	// Nothing expires while somebody might still be walking to the computer.
	if expired := h.transfers.ExpireAcceptances(time.Now()); expired != 0 {
		t.Fatalf("%d transfers expired immediately", expired)
	}

	// And after the window, it is refused with a cause.
	later := time.Now().Add(pairing.AcceptanceWindow + time.Second)
	if expired := h.transfers.ExpireAcceptances(later); expired != 1 {
		t.Fatalf("%d transfers expired, want 1", expired)
	}

	stored, err := h.store.Transfer(mustID(t, tr.ID))
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if stored.State != store.StateFailed || stored.FailureCause != store.CauseAcceptanceTimeout {
		t.Errorf("ended as %q/%q, want failed/acceptance_timeout", stored.State, stored.FailureCause)
	}

	// And accepting it afterwards does not resurrect it.
	if _, err := h.transfers.Accept(mustID(t, tr.ID)); err == nil {
		t.Error("an expired transfer was accepted")
	}
}

// FR-016b: the default is to accept automatically, and changing the mode takes
// effect on the next transfer rather than on the next pairing.
func TestChangingTheModeTakesEffectImmediately(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	// The default for a device the user paired themselves.
	first := phone.declare(t, h.selfID, "before.bin", 16)
	if first.State != "queued" {
		t.Fatalf("a newly paired device produced %q, want queued", first.State)
	}

	phone.setTrust(t, phone.ID, store.TrustAsk)

	second := phone.declare(t, h.selfID, "after.bin", 16)
	if second.State != "awaiting_acceptance" {
		t.Errorf("after switching to ask, state = %q", second.State)
	}

	// And back again, without re-pairing.
	phone.setTrust(t, phone.ID, store.TrustAuto)

	third := phone.declare(t, h.selfID, "back.bin", 16)
	if third.State != "queued" {
		t.Errorf("after switching back to auto, state = %q", third.State)
	}
}

// FR-016: the expiry window follows the mode, and changing it recomputes from
// the device's last activity rather than from now.
//
// Recomputing from now would let anyone extend a stale pairing indefinitely by
// toggling a switch, which is not what the setting means.
func TestChangingTheModeRecomputesExpiryFromLastActivity(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	before, err := h.store.Pairing(phone.ID)
	if err != nil {
		t.Fatalf("pairing: %v", err)
	}
	if want := before.LastActivity.Add(store.ExpiryAuto); !before.ExpiresAt.Equal(want) {
		t.Fatalf("auto expiry = %s, want last activity plus a year", before.ExpiresAt)
	}

	if err := h.store.SetTrustMode(phone.ID, store.TrustAsk); err != nil {
		t.Fatalf("set trust: %v", err)
	}

	after, err := h.store.Pairing(phone.ID)
	if err != nil {
		t.Fatalf("pairing: %v", err)
	}
	if want := before.LastActivity.Add(store.ExpiryAsk); !after.ExpiresAt.Equal(want) {
		t.Errorf("ask expiry = %s, want last activity plus thirty days (%s)", after.ExpiresAt, want)
	}
	if !after.LastActivity.Equal(before.LastActivity) {
		t.Error("changing the mode moved the device's last activity")
	}
}

// The mode is visible wherever devices are listed, so the user can tell at a
// glance which of them can write to their machine unattended. FR-016c.
func TestTheTrustModeIsVisibleInTheDeviceList(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	for _, mode := range []store.TrustMode{store.TrustAuto, store.TrustAsk} {
		if err := h.store.SetTrustMode(phone.ID, mode); err != nil {
			t.Fatalf("set trust: %v", err)
		}

		body := phone.devices(t)
		found := false
		for _, dev := range body.Devices {
			if dev.ID != phone.ID {
				continue
			}
			found = true
			if dev.TrustMode != string(mode) {
				t.Errorf("trust mode in the list = %q, want %q", dev.TrustMode, mode)
			}
		}
		if !found {
			t.Fatalf("the device is missing from the list: %+v", body.Devices)
		}
	}
}

// Only the target may answer. A sender that could accept on the recipient's
// behalf would make the whole setting decorative.
func TestOnlyTheTargetMayAcceptATransfer(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	if err := h.store.SetTrustMode(phone.ID, store.TrustAsk); err != nil {
		t.Fatalf("set trust: %v", err)
	}

	tr := phone.declare(t, h.selfID, "not-yours-to-accept.bin", 32)

	for _, verb := range []string{"accept", "decline"} {
		resp := phone.answer(t, tr.ID, verb)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("the sender was allowed to %s its own transfer", verb)
		}
	}

	// And it is still waiting, rather than having been quietly resolved.
	if got := phone.transferState(t, tr.ID).State; got != "awaiting_acceptance" {
		t.Errorf("state = %q, want awaiting_acceptance", got)
	}
}
