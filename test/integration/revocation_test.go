package integration

import (
	"io"
	"net/http"
	"os"
	"testing"
)

// Revocation, per FR-015.
//
// "Immediately" is the whole requirement. A revocation that takes effect at the
// next restart, or when a cached credential happens to expire, is not a
// revocation — it is a note to self. The case that matters is the one where
// somebody is removing a device they no longer control, and every second
// between the decision and the effect is a second that device can still write
// to their disk.
//
// The moment itself is covered by TestRevocationTakesEffectImmediately in
// unpaired_access_test.go, which is where the unpaired-access rules live. What
// is here is everything around it: what happens to work in flight, what the
// user is shown afterwards, and whether the device can come back.

// revoke removes a device's access, as the user would from their own screen.
func (d *device) revoke(t *testing.T, deviceID string) {
	t.Helper()

	resp := d.do("DELETE", "/api/pairings/"+deviceID, nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("revoke: status %d: %s", resp.StatusCode, raw)
	}
	resp.Body.Close()
}

// A transfer already in flight stops. Letting it finish would mean the removed
// device still wrote a file after being removed, which is the one outcome the
// user was trying to prevent.
func TestRevokingADeviceStopsWhatItWasSending(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()
	admin := h.pair()

	payload := make([]byte, 8192)
	tr := phone.declare(t, h.selfID, "interrupted-by-revocation.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload[:2048])

	admin.revoke(t, phone.ID)

	// The next chunk is refused, and refused as an authorization failure rather
	// than as something about the transfer.
	resp := phone.upload(t, tr.ID, 0, 2048, payload[2048:])
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}

	// And the file never appears, because it never verified.
	if entries, err := listNames(h.receiveDir); err != nil || len(entries) != 0 {
		t.Errorf("the receive folder holds %v after a revocation (%v)", entries, err)
	}
}

// FR-015 asks for the list as much as for the removal: a user who cannot see
// which devices have access cannot decide which to remove.
func TestARevokedDeviceIsShownAsNoLongerPaired(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()
	admin := h.pair()

	before := admin.devices(t)
	if !pairedIn(before, phone.ID) {
		t.Fatal("the device is not shown as paired before revocation")
	}

	admin.revoke(t, phone.ID)

	after := admin.devices(t)
	if pairedIn(after, phone.ID) {
		t.Error("a revoked device is still shown as paired")
	}

	// The device record stays, so the user recognises the name rather than
	// meeting an unknown device if it ever comes back.
	found := false
	for _, dev := range after.Devices {
		if dev.ID == phone.ID {
			found = true
			if dev.Name == "" {
				t.Error("the revoked device lost its name")
			}
		}
	}
	if !found {
		t.Error("the revoked device vanished from the list entirely")
	}
}

// Revocation is terminal for that pairing. A device that comes back has to be
// let in again by a human, which is what makes removing one meaningful.
func TestARevokedDeviceCannotResumeOnItsOldCredential(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()
	admin := h.pair()

	admin.revoke(t, phone.ID)

	// Every entry point, not just the one that was in use.
	for _, attempt := range []struct {
		method, path string
	}{
		{"GET", "/api/devices"},
		{"GET", "/api/queue"},
		{"POST", "/api/events/ticket"},
	} {
		resp := phone.do(attempt.method, attempt.path, map[string]any{})
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s %s still worked on a revoked credential", attempt.method, attempt.path)
		}
	}

	// And the pairing record says so rather than having been deleted, so the
	// interface can explain what happened.
	p, err := h.store.Pairing(phone.ID)
	if err != nil {
		t.Fatalf("pairing: %v", err)
	}
	if !p.Revoked() {
		t.Error("the pairing is not marked revoked")
	}
	if p.Active(p.CreatedAt) {
		t.Error("a revoked pairing still reports itself as able to authorize")
	}
}

// Pairing again after a revocation produces a new relationship rather than
// reviving the old one.
func TestPairingAgainAfterRevocationWorks(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()
	admin := h.pair()

	admin.revoke(t, phone.ID)

	returning := h.pair()
	if returning.ID == phone.ID {
		t.Fatal("the returning device reused the revoked identity")
	}

	// And it works, which is what "the user can let them back in" means.
	payload := []byte("back again")
	tr := returning.declare(t, h.selfID, "second-chance.bin", uint64(len(payload)))
	returning.uploadOK(t, tr.ID, 0, 0, payload)
	if got := returning.completeOK(t, tr.ID, 0, digestOf(t, payload)).State; got != "completed" {
		t.Errorf("state = %q, want completed", got)
	}
}

// pairedIn reports whether a device is listed as paired.
func pairedIn(body deviceListBody, deviceID string) bool {
	for _, dev := range body.Devices {
		if dev.ID == deviceID {
			return dev.Paired
		}
	}
	return false
}

// listNames returns the entries of a directory.
func listNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}
