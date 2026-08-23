package integration

import (
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nerow75/fastr/internal/discovery"
)

// FR-005: devices that report the same name stay distinguishable.
//
// Two machines called "Laptop" is the ordinary case in a household, not an edge
// one, and a list with two identical rows is a list nobody can choose from. It
// is also a protocol problem before it is a display problem: a DNS-SD instance
// name is the uniqueness key, so two responders claiming the same one collide
// on the wire and the stack renames one of them to something the user never
// chose.
//
// Both halves are checked here: the instance name carries the short identifier,
// and the list shows it only where it is needed.

// The wire half. Two instances with the same name, on real multicast, arrive as
// two devices rather than one.
func TestTwoDevicesWithTheSameNameBothAppear(t *testing.T) {
	const (
		first  = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		second = "01BXAV6R2A5B9C0D1E2F3G4H5J"
	)

	for _, id := range []string{first, second} {
		server := httptest.NewServer(nil)
		t.Cleanup(server.Close)

		advertising(t, discovery.Advertisement{
			DeviceID:  id,
			Name:      "Laptop", // deliberately identical
			OS:        "linux",
			Port:      server.Listener.Addr().(*net.TCPAddr).Port,
			Addresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		})
	}

	browser := browsing(t, "01BROWSERDEVICEID00000000")
	waitForPeer(t, browser, first, 5*time.Second)
	waitForPeer(t, browser, second, 5*time.Second)

	peers := browser.Peers()
	if len(peers) != 2 {
		t.Fatalf("saw %d devices, want 2: %+v", len(peers), peers)
	}

	// Same name, different identity, different address. The name is the only
	// thing they share, which is the whole point.
	labels := discovery.Labels(peers)
	if labels[first] == labels[second] {
		t.Fatalf("both devices are shown as %q", labels[first])
	}
	for _, id := range []string{first, second} {
		if !strings.Contains(labels[id], "Laptop") {
			t.Errorf("label %q lost the name the user chose", labels[id])
		}
		if !strings.Contains(labels[id], discovery.ShortID(id)) {
			t.Errorf("label %q does not say which Laptop it is", labels[id])
		}
	}
}

// The display half, without the wire. A name that is unique is shown as it is:
// appending an identifier to every row would make the common list worse to
// serve the uncommon one.
func TestAUniqueNameIsShownWithoutAnIdentifier(t *testing.T) {
	peers := []discovery.Peer{
		{DeviceID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Name: "Study Desktop"},
		{DeviceID: "01BXAV6R2A5B9C0D1E2F3G4H5J", Name: "Kitchen Pi"},
	}

	labels := discovery.Labels(peers)
	if labels[peers[0].DeviceID] != "Study Desktop" {
		t.Errorf("label = %q, want the bare name", labels[peers[0].DeviceID])
	}
	if labels[peers[1].DeviceID] != "Kitchen Pi" {
		t.Errorf("label = %q, want the bare name", labels[peers[1].DeviceID])
	}
}

// A device with no name at all is still addressable. Empty rows in a list are
// unusable, and a machine can reach this state by being configured badly rather
// than maliciously.
func TestANamelessDeviceStillGetsALabel(t *testing.T) {
	peers := []discovery.Peer{{DeviceID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Name: ""}}

	label := discovery.Labels(peers)[peers[0].DeviceID]
	if strings.TrimSpace(label) == "" {
		t.Fatal("a nameless device produced an empty label")
	}
	if !strings.Contains(label, discovery.ShortID(peers[0].DeviceID)) {
		t.Errorf("label = %q, want something that identifies the device", label)
	}
}

// The identifier shown is the tail, not the head.
//
// A ULID starts with a timestamp, so two devices that first ran on the same day
// share their leading characters. Truncating from the front would produce two
// rows reading "Laptop (01ARZ3)", which is the bug this test exists to prevent
// rather than a style preference.
func TestTheShortIdentifierDistinguishesDevicesFromTheSameDay(t *testing.T) {
	// Two ULIDs minted moments apart share everything but their tail.
	const (
		a = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		b = "01ARZ3NDEKTSV4RRFFQ69G5FZZ"
	)

	if discovery.ShortID(a) == discovery.ShortID(b) {
		t.Fatalf("both devices short to %q", discovery.ShortID(a))
	}
	if discovery.InstanceName("Laptop", a) == discovery.InstanceName("Laptop", b) {
		t.Fatal("two instances of the same name collide on the wire")
	}
}
