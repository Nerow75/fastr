package integration

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Nerow75/fastr/internal/discovery"
)

// User Story 4: several devices on the network.
//
// The independent test the story names is "run fastr on two computers and
// verify each lists the others with recognizable names", and SC-011 puts five
// seconds on it. That is what TestADeviceOnTheNetworkAppearsWithinFiveSeconds
// does, over real multicast, between two real instances in one process.
//
// **These tests skip when the machine cannot see its own multicast.** That is
// checked once, by probing rather than guessing: a throwaway service is
// published and looked for, and if it does not come back then nothing on this
// machine could, whatever fastr does. Containers routinely cannot, and the
// GitHub Windows runner cannot either — so **discovery is not verified on
// Windows by CI**, which is recorded in docs/journal.md rather than papered
// over.
//
// The probe keeps these tests meaningful where they run: on a machine that
// passes it, a failure here is a real failure and not an environment. What is
// never skipped is everything that does not need the wire — the record's
// contents, name disambiguation, reachability, and the manual fallback — all of
// which have tests of their own beside this one.

// multicastWorks reports whether this machine can see its own mDNS traffic.
//
// Asked once and by experiment, because there is no way to ask the operating
// system. A machine that silently drops multicast is indistinguishable, from
// inside a browser, from a network with nothing on it — which is exactly the
// failure mode that made these tests fail on Windows CI while passing on Linux.
var multicastWorks = sync.OnceValue(func() bool {
	const probeID = "01PROBEMULTICASTCAPABILITY"

	published, err := discovery.Advertise(discovery.Advertisement{
		DeviceID:  probeID,
		Name:      "Multicast probe",
		OS:        "linux",
		Port:      1,
		Addresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	})
	if err != nil {
		return false
	}
	defer func() { _ = published.Close() }()

	browser := discovery.NewBrowser("01PROBEBROWSER0000000000", discardLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	browser.Start(ctx)

	for ctx.Err() == nil {
		if _, found := browser.Peer(probeID); found {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
})

// requireMulticast skips a test that cannot run on this machine.
func requireMulticast(t *testing.T) {
	t.Helper()
	if !multicastWorks() {
		t.Skip("this machine cannot see its own multicast; discovery over the wire cannot be tested here")
	}
}

// advertising publishes an instance and shuts it down with the test.
func advertising(t *testing.T, ad discovery.Advertisement) {
	t.Helper()
	requireMulticast(t)

	published, err := discovery.Advertise(ad)
	if err != nil {
		t.Fatalf("advertise: %v", err)
	}
	t.Cleanup(func() { _ = published.Close() })
}

// browsing starts a browser bounded by the test.
func browsing(t *testing.T, selfID string) *discovery.Browser {
	t.Helper()

	browser := discovery.NewBrowser(selfID, discardLogger())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	browser.Start(ctx)

	return browser
}

// waitForPeer polls until a device appears, and reports how long it took.
func waitForPeer(t *testing.T, b *discovery.Browser, deviceID string, within time.Duration) (discovery.Peer, time.Duration) {
	t.Helper()

	started := time.Now()
	deadline := started.Add(within)
	for time.Now().Before(deadline) {
		if peer, ok := b.Peer(deviceID); ok {
			return peer, time.Since(started)
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("%s did not appear within %s; saw %+v (browsing error: %v)",
		deviceID, within, b.Peers(), b.Unavailable())
	return discovery.Peer{}, 0
}

// SC-011, with the clock actually read.
func TestADeviceOnTheNetworkAppearsWithinFiveSeconds(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

	// A real listener, so the port advertised is one something answers on.
	// Discovery that points at a closed port is the failure this story is
	// meant to prevent, not a detail to stub out.
	server := httptest.NewServer(nil)
	t.Cleanup(server.Close)
	port := server.Listener.Addr().(*net.TCPAddr).Port

	advertising(t, discovery.Advertisement{
		DeviceID:  id,
		Name:      "Study Desktop",
		OS:        "linux",
		Port:      port,
		Addresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	})

	browser := browsing(t, "01BROWSERDEVICEID00000000")
	peer, took := waitForPeer(t, browser, id, 5*time.Second)

	t.Logf("appeared in %s", took.Round(time.Millisecond))

	// FR-004: a human-recognizable name, not an identifier.
	if peer.Name != "Study Desktop" {
		t.Errorf("name = %q, want the name the user chose", peer.Name)
	}
	if peer.Kind != "computer" {
		t.Errorf("kind = %q, want computer", peer.Kind)
	}
	if peer.OS != "linux" {
		t.Errorf("os = %q, want linux", peer.OS)
	}
	if peer.Version != discovery.Version {
		t.Errorf("version = %d, want %d", peer.Version, discovery.Version)
	}
	if peer.Source != discovery.SourceMDNS {
		t.Errorf("source = %q, want mdns", peer.Source)
	}

	// The address has to be dialable, which means it carries the real port.
	if _, portStr, err := net.SplitHostPort(peer.Address()); err != nil {
		t.Errorf("address %q is not host:port: %v", peer.Address(), err)
	} else if portStr != itoa(port) {
		t.Errorf("advertised port = %s, want %d", portStr, port)
	}
}

// A device must not discover itself. It answers its own queries, so without
// this every list would begin with the machine the user is already sitting at.
func TestAnInstanceDoesNotListItself(t *testing.T) {
	const id = "01SELFDEVICEIDENTIFIER000"

	server := httptest.NewServer(nil)
	t.Cleanup(server.Close)

	advertising(t, discovery.Advertisement{
		DeviceID:  id,
		Name:      "This Computer",
		OS:        "linux",
		Port:      server.Listener.Addr().(*net.TCPAddr).Port,
		Addresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	})

	// Browsing as the same device: this is the real configuration, where one
	// process both advertises and browses.
	browser := browsing(t, id)

	// Long enough that the record has certainly been answered — the previous
	// test measures that at well under a second — and short enough not to pad
	// the suite.
	time.Sleep(1500 * time.Millisecond)

	if _, found := browser.Peer(id); found {
		t.Error("the instance listed itself")
	}
}

// FR-007: the list says whether a device can actually be reached, and it asks
// rather than trusting the record. A record outlives the process that published
// it, and offering a closed machine as a destination produces a transfer that
// sits at nothing and never explains itself.
func TestReachabilityIsConfirmedAgainstTheDeviceItself(t *testing.T) {
	h := newHarness(t)

	browser := discovery.NewBrowser("01BROWSERDEVICEID00000000", discardLogger())
	prober := discovery.NewProber()

	// A device known at the harness's real address, which answers /connect.
	browser.Remember(discovery.Peer{
		DeviceID:  h.selfID,
		Name:      "Test Computer",
		Addresses: []string{hostPortOf(t, h.server.URL)},
		Source:    discovery.SourceMDNS,
	})

	prober.Refresh(t.Context(), browser)

	peer, _ := browser.Peer(h.selfID)
	if peer.Reachable == nil || !*peer.Reachable {
		t.Fatalf("a running instance was reported unreachable: %+v", peer)
	}

	// And when it stops answering, the device is marked unreachable rather than
	// removed: FR-004 scenario 3 forbids it vanishing under a selection.
	h.server.Close()
	prober.Refresh(t.Context(), browser)

	peer, found := browser.Peer(h.selfID)
	if !found {
		t.Fatal("the device disappeared from the list instead of being marked unreachable")
	}
	if peer.Reachable == nil || *peer.Reachable {
		t.Errorf("a closed instance was reported reachable: %+v", peer)
	}
}

// An address that answers as a different device is refused. DHCP reassigns
// addresses constantly, so a record remembered five minutes ago can point at
// someone else's machine, and sending a file to whoever holds an address is
// exactly what must not happen.
func TestAnAddressAnsweringAsAnotherDeviceIsRefused(t *testing.T) {
	h := newHarness(t)

	browser := discovery.NewBrowser("01BROWSERDEVICEID00000000", discardLogger())
	prober := discovery.NewProber()

	// The right address, the wrong identity: what a stale record looks like
	// after the address moved.
	stale := discovery.Peer{
		DeviceID:  "01SOMEONEELSESDEVICEID000",
		Name:      "Old Laptop",
		Addresses: []string{hostPortOf(t, h.server.URL)},
		Source:    discovery.SourceMDNS,
	}
	browser.Remember(stale)

	reachable, err := prober.Confirm(t.Context(), stale)
	if reachable {
		t.Error("an address belonging to another device was accepted")
	}
	if err == nil {
		t.Fatal("the mismatch was not reported")
	}
	t.Logf("refused with: %v", err)
}

// hostPortOf extracts host:port from an httptest URL.
func hostPortOf(t *testing.T, rawURL string) string {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return parsed.Host
}

// itoa keeps the port comparison above readable.
func itoa(n int) string { return strconv.Itoa(n) }

// One list, not two. A person thinks about the machines around them, some of
// which they have connected to before, and splitting the screen into "paired
// devices" and "computers on the network" would make them do that merge
// themselves.
func TestTheDeviceListMergesWhatIsPairedWithWhatIsOnTheNetwork(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	reachable := true
	h.setDiscovery(fixedDiscovery{peers: []discovery.Peer{
		// A computer nobody here has paired with.
		{
			DeviceID: "01STUDYDESKTOPID000000000", Name: "Study Desktop", Kind: "computer",
			Addresses: []string{"192.168.1.20:7420"}, Source: discovery.SourceMDNS,
			Reachable: &reachable, Version: discovery.Version,
		},
		// And this very phone, which the store already knows about.
		{
			DeviceID: phone.ID, Name: "Test Phone", Kind: "computer",
			Addresses: []string{"192.168.1.21:7420"}, Source: discovery.SourceMDNS,
		},
	}})

	body := phone.devices(t)

	if len(body.Discovered) != 1 {
		t.Fatalf("discovered = %+v, want only the device the store does not know", body.Discovered)
	}
	found := body.Discovered[0]
	if found.ID != "01STUDYDESKTOPID000000000" {
		t.Errorf("discovered %q, want the unpaired computer", found.ID)
	}
	if found.Label != "Study Desktop" {
		t.Errorf("label = %q, want the plain name when nothing collides", found.Label)
	}
	if found.Reachable == nil || !*found.Reachable {
		t.Errorf("reachability was not carried through: %+v", found)
	}
	if len(found.Addresses) == 0 {
		t.Error("no address was offered, so there is nowhere for the user to go")
	}

	// The paired phone appears once, from the store, with what only the store
	// knows: that it is paired.
	seen := 0
	for _, dev := range body.Devices {
		if dev.ID == phone.ID {
			seen++
			if !dev.Paired {
				t.Error("the paired phone is not shown as paired")
			}
		}
	}
	if seen != 1 {
		t.Errorf("the phone appears %d times, want once", seen)
	}
}

// fixedDiscovery is a network with a known set of devices on it.
type fixedDiscovery struct{ peers []discovery.Peer }

func (f fixedDiscovery) Peers() []discovery.Peer { return f.peers }
func (fixedDiscovery) Unavailable() error        { return nil }

// deviceListBody is what GET /api/devices answers with: the store's devices,
// what discovery has found, and whether looking is working at all.
type deviceListBody struct {
	Devices []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Paired    bool   `json:"paired"`
		Connected bool   `json:"connected"`
		TrustMode string `json:"trust_mode"`
	} `json:"devices"`
	Discovered []struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Label     string   `json:"label"`
		Addresses []string `json:"addresses"`
		Reachable *bool    `json:"reachable"`
		Source    string   `json:"source"`
	} `json:"discovered"`
	Discovery map[string]any `json:"discovery"`
}

// devices reads one device's view of the device list.
func (d *device) devices(t *testing.T) deviceListBody {
	t.Helper()

	const path = "/api/devices"
	resp := d.do("GET", path, nil)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("devices: status %d", resp.StatusCode)
	}

	var out deviceListBody
	d.open("GET", path, resp, &out)
	return out
}
