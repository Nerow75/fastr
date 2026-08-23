package integration

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nerow75/fastr/internal/discovery"
	"github.com/Nerow75/fastr/internal/localnet"
)

// FR-006: manual address entry, for networks where discovery cannot work.
//
// Offices block multicast, guest wifi blocks it, and any access point with
// client isolation blocks it. The contract is explicit that the answer is this
// path rather than a second discovery protocol of the project's own design.
//
// The property that matters is that **nothing downstream can tell the
// difference**. The user supplies an address; the identity comes from the same
// place it would have come from over mDNS, which is `/connect` on the machine
// itself. These tests hold that line, and hold the boundary that stops a typo
// from becoming a request off the local network.

func TestAManuallyAddedDeviceIsIndistinguishableFromADiscoveredOne(t *testing.T) {
	h := newHarness(t)

	browser := discovery.NewBrowser("01BROWSERDEVICEID00000000", discardLogger())
	prober := discovery.NewProber()

	peer, err := discovery.Add(t.Context(), browser, prober, hostPortOf(t, h.server.URL))
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	// Every identity field comes from the machine, not from what was typed.
	if peer.DeviceID != h.selfID {
		t.Errorf("device id = %q, want the one the machine reports", peer.DeviceID)
	}
	if peer.Name != "Test Computer" {
		t.Errorf("name = %q, want the name the machine reports", peer.Name)
	}
	if peer.Kind != "computer" {
		t.Errorf("kind = %q, want computer", peer.Kind)
	}
	if peer.Version == 0 {
		t.Error("no protocol version was recorded")
	}

	// It is in the list, and it is known to be reachable, because adding it
	// meant reaching it.
	stored, found := browser.Peer(h.selfID)
	if !found {
		t.Fatal("the device was not added to the list")
	}
	if stored.Reachable == nil || !*stored.Reachable {
		t.Errorf("a device that answered was not marked reachable: %+v", stored)
	}

	// The only field that differs from a discovered device says how it was
	// found, which is what lets the interface confirm the entry took effect.
	if stored.Source != discovery.SourceManual {
		t.Errorf("source = %q, want manual", stored.Source)
	}
}

// An address that answers nothing is a typo, and a typo must not become a
// permanent row in the device list.
func TestAnAddressThatAnswersNothingIsNotRemembered(t *testing.T) {
	browser := discovery.NewBrowser("01BROWSERDEVICEID00000000", discardLogger())
	prober := discovery.NewProber()

	// Port 1 on loopback: local, so the boundary check passes, and refused, so
	// the probe fails for the reason under test rather than a different one.
	if _, err := discovery.Add(t.Context(), browser, prober, "127.0.0.1:1"); err == nil {
		t.Fatal("an unreachable address was accepted")
	}
	if peers := browser.Peers(); len(peers) != 0 {
		t.Errorf("a failed entry was remembered anyway: %+v", peers)
	}
}

// Adding this computer by its own address is refused. It is an easy mistake on
// a machine with several addresses, and the result would be a device list whose
// first entry is the machine the user is sitting at.
func TestAddingThisComputerIsRefused(t *testing.T) {
	h := newHarness(t)

	browser := discovery.NewBrowser(h.selfID, discardLogger())
	prober := discovery.NewProber()

	_, err := discovery.Add(t.Context(), browser, prober, hostPortOf(t, h.server.URL))
	if !errors.Is(err, discovery.ErrSelf) {
		t.Fatalf("error = %v, want ErrSelf", err)
	}
	if peers := browser.Peers(); len(peers) != 0 {
		t.Errorf("this computer was added to its own list: %+v", peers)
	}
}

// What people actually type, and what the field has to accept.
func TestAddressesArePreparedTheWayPeopleTypeThem(t *testing.T) {
	cases := map[string]string{
		"192.168.1.20:7420":         "192.168.1.20:7420",
		"http://192.168.1.20:7420":  "192.168.1.20:7420",
		"http://192.168.1.20:7420/": "192.168.1.20:7420",
		"  192.168.1.20:7420  ":     "192.168.1.20:7420",
		// No port: the default, which is a guess the interface documents rather
		// than a promise the protocol makes.
		"192.168.1.20": "192.168.1.20:7420",
		"[::1]:7420":   "[::1]:7420",
	}

	for typed, want := range cases {
		got, err := discovery.NormalizeAddress(typed)
		if err != nil {
			t.Errorf("NormalizeAddress(%q) failed: %v", typed, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeAddress(%q) = %q, want %q", typed, got, want)
		}
	}
}

// Principle I, at the point a user could most easily breach it.
//
// The address field is the one place in the whole product where someone can
// name an arbitrary host. A public address typed into it must be refused with a
// reason, and so must a host name, because resolving one is itself a request
// off the local network.
func TestTheAddressFieldCannotReachOffTheLocalNetwork(t *testing.T) {
	var notLocal *localnet.ErrNotLocal

	for _, typed := range []string{"8.8.8.8:7420", "93.184.216.34", "http://1.1.1.1:80"} {
		got, err := discovery.NormalizeAddress(typed)
		if err == nil {
			t.Errorf("NormalizeAddress(%q) = %q, want a refusal", typed, got)
			continue
		}
		if !errors.As(err, &notLocal) {
			t.Errorf("NormalizeAddress(%q) failed with %v, want a local-network refusal", typed, err)
		}
	}

	// A name, not an address. Refused before anything is resolved, and the
	// message says what to type instead rather than reporting a DNS failure.
	for _, typed := range []string{"example.com", "my-laptop.local:7420", "localhost:7420"} {
		_, err := discovery.NormalizeAddress(typed)
		if err == nil {
			t.Errorf("NormalizeAddress(%q) was accepted; resolving a name is a request off the network", typed)
			continue
		}
		if !strings.Contains(err.Error(), "not an address") {
			t.Errorf("NormalizeAddress(%q) failed with %q, which does not say what to type", typed, err)
		}
	}
}

// T096: a network that blocks multicast degrades to the manual path rather than
// to a broken application.
//
// The interface has to be told, and told in a way it can act on: a device list
// that is simply empty looks like a network with no other computers on it, and
// the user has no reason to look for an address field. So the list carries the
// state of discovery itself.
func TestABlockedNetworkIsReportedRatherThanLookingEmpty(t *testing.T) {
	h := newHarness(t)
	h.setDiscovery(blockedDiscovery{})
	phone := h.pair()

	body := phone.devices(t)

	status, ok := body.Discovery["available"].(bool)
	if !ok || status {
		t.Fatalf("discovery status = %v, want available:false", body.Discovery)
	}
	if reason, _ := body.Discovery["reason"].(string); reason == "" {
		t.Error("no reason was given for discovery being unavailable")
	}

	// And the application still works: the whole point of degrading is that
	// everything except finding devices automatically carries on.
	payload := []byte("sent on a network with no multicast")
	tr := phone.declare(t, h.selfID, "blocked.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload)
	if got := phone.completeOK(t, tr.ID, 0, digestOf(t, payload)).State; got != "completed" {
		t.Errorf("state = %q, want completed", got)
	}
}

// blockedDiscovery stands in for a network that refuses multicast.
type blockedDiscovery struct{}

func (blockedDiscovery) Peers() []discovery.Peer { return nil }
func (blockedDiscovery) Unavailable() error {
	return errors.New("multicast is not available on this network")
}
