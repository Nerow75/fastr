package discovery

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/mdns"

	"github.com/Nerow75/fastr/internal/localnet"
)

// Browsing, per FR-004 and FR-008.
//
// The shape here follows from one fact about mDNS: a responder answers a query
// *and* announces itself unprompted when it starts. So a browser that issues
// one query and then keeps listening sees both the devices that were already
// there and the ones that arrive later, without polling.
//
// That is what the long-lived query below is. It re-queries on a slow interval
// anyway, because an announcement can be lost — it is UDP multicast, and a
// phone waking a sleeping access point loses the first packet routinely — and a
// device that missed its one chance to be seen would stay invisible for as long
// as the application ran.
//
// **Nothing is removed here.** hashicorp/mdns does not surface goodbye packets,
// and it does not matter: the contract says a record going away marks a device
// unreachable rather than removing it, and reachability is confirmed against
// `/connect` rather than against the record. See reachability.go.

const (
	// requery is how often the browser asks again. Slow, because responders
	// also announce themselves: this is a safety net for lost packets, not the
	// mechanism. A tighter interval would put multicast traffic on the network
	// every few seconds for the life of the process, which is exactly the idle
	// cost research.md's processor budget exists to avoid.
	requery = 60 * time.Second

	// firstRequery is the one exception. A query answered while the network
	// stack is still settling — a laptop that just associated, a container that
	// just got its address — is a query nobody hears, and waiting a full minute
	// to try again would blow SC-011's five seconds for the device that was
	// already there.
	firstRequery = 3 * time.Second
)

// Peer is another fastr computer, as this one currently understands it.
type Peer struct {
	// DeviceID is the identity. Two records with the same identifier are the
	// same device, whatever they call themselves or where they answer.
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	OS       string `json:"os"`
	Version  int    `json:"version"`
	TLS      bool   `json:"tls"`

	// Addresses are host:port, in the order they were published.
	Addresses []string `json:"addresses"`

	// Source says how this device was found, so the interface can tell a user
	// on a multicast-blocked network that their manual entry took effect.
	Source string `json:"source"`

	// FirstSeen and LastSeen bound what is known. LastSeen is when a record was
	// last observed, not when the device was last reachable.
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	// Reachable is the answer from /connect, not from the record. Nil means it
	// has not been asked yet.
	Reachable *bool     `json:"reachable,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
}

// Sources a peer can come from.
const (
	SourceMDNS   = "mdns"
	SourceManual = "manual"
)

// Labels is what to call each device on screen, keyed by identifier.
//
// FR-005: two devices reporting the same name must stay distinguishable. The
// short identifier is appended only where it is needed, because "Laptop" is
// what the user called it and "Laptop (5FAV)" everywhere would be a worse list
// for the household that has one of each.
//
// Computed here rather than in the interface so the desktop and the phone show
// the same thing, and so the rule has one place to be wrong in.
func Labels(peers []Peer) map[string]string {
	counts := make(map[string]int, len(peers))
	for _, p := range peers {
		counts[p.Name]++
	}

	out := make(map[string]string, len(peers))
	for _, p := range peers {
		if counts[p.Name] > 1 || strings.TrimSpace(p.Name) == "" {
			out[p.DeviceID] = InstanceName(p.Name, p.DeviceID)
			continue
		}
		out[p.DeviceID] = p.Name
	}
	return out
}

// Address is the first published address, which is what a caller probes.
func (p Peer) Address() string {
	if len(p.Addresses) == 0 {
		return ""
	}
	return p.Addresses[0]
}

// Browser keeps a live view of the network.
//
// Safe for concurrent use: the HTTP layer reads it on every device list while
// the query goroutine writes to it.
type Browser struct {
	// SelfID is this instance, which answers its own queries and must not
	// appear in its own device list.
	SelfID string
	Log    *slog.Logger

	mu        sync.RWMutex
	peers     map[string]*Peer
	available bool
	failure   error

	listeners []func()
}

// NewBrowser returns a browser that has not started looking.
func NewBrowser(selfID string, log *slog.Logger) *Browser {
	return &Browser{SelfID: selfID, Log: log, peers: make(map[string]*Peer), available: true}
}

// OnChange registers a callback fired whenever the peer set changes.
//
// FR-008 wants devices to appear and disappear without a manual refresh, and
// this is how the event stream learns to say so. Callbacks run on the browsing
// goroutine, so they must not block.
func (b *Browser) OnChange(fn func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listeners = append(b.listeners, fn)
}

// Start begins browsing and returns immediately. Browsing stops when ctx is
// cancelled.
//
// It does not return an error when multicast is unavailable. A network that
// blocks it is common — offices and guest wifi both do — and the answer is the
// manual fallback, not a failure to start: refusing to run would take the whole
// application down over a feature the user may not need. Unavailable() says so,
// and the interface offers manual entry.
func (b *Browser) Start(ctx context.Context) {
	go b.loop(ctx)
}

// Unavailable reports whether browsing could not be started, and why.
//
// A caller shows manual entry when this is set. It is a state rather than an
// error return because it can become true after Start succeeded: an interface
// going down takes multicast with it.
func (b *Browser) Unavailable() error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.available {
		return nil
	}
	return b.failure
}

// Peers returns the current view, newest activity last.
func (b *Browser) Peers() []Peer {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]Peer, 0, len(b.peers))
	for _, p := range b.peers {
		out = append(out, *p)
	}
	return out
}

// Peer returns one device by identifier.
func (b *Browser) Peer(deviceID string) (Peer, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	p, ok := b.peers[deviceID]
	if !ok {
		return Peer{}, false
	}
	return *p, true
}

// Remember records a device found some other way, which is the manual path.
// See manual.go.
func (b *Browser) Remember(p Peer) {
	if p.DeviceID == "" || p.DeviceID == b.SelfID {
		return
	}
	if b.merge(p) {
		b.notify()
	}
}

// SetReachable records the answer from a /connect probe.
func (b *Browser) SetReachable(deviceID string, reachable bool, at time.Time) {
	b.mu.Lock()
	peer, ok := b.peers[deviceID]
	changed := false
	if ok {
		changed = peer.Reachable == nil || *peer.Reachable != reachable
		value := reachable
		peer.Reachable = &value
		peer.CheckedAt = at
	}
	b.mu.Unlock()

	if changed {
		b.notify()
	}
}

// loop asks once briefly, then settles into long listening windows.
//
// The first window is short because a query sent while the network stack is
// still settling — a laptop that just associated, a container that just got its
// address — is a query nobody hears, and the second attempt has to come well
// inside SC-011's five seconds rather than a minute later.
func (b *Browser) loop(ctx context.Context) {
	window := firstRequery
	for {
		b.queryOnce(ctx, window)
		window = requery

		if ctx.Err() != nil {
			return
		}
	}
}

// queryOnce issues a query and keeps the listener open for the whole window, so
// unsolicited announcements arrive on the same channel.
func (b *Browser) queryOnce(ctx context.Context, window time.Duration) {
	entries := make(chan *mdns.ServiceEntry, 8)

	// Consumed in a goroutine because QueryContext writes to the channel while
	// it listens, and an unread channel would stall the library rather than the
	// caller.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for entry := range entries {
			if peer, ok := peerFrom(entry); ok && peer.DeviceID != b.SelfID {
				if b.merge(peer) {
					b.notify()
				}
			}
		}
	}()

	// The listener stays open for the whole window rather than for a second,
	// which is what turns a query into a subscription: a device that starts in
	// the meantime announces itself and lands on this channel.
	listening, cancel := context.WithTimeout(ctx, window)
	err := mdns.QueryContext(listening, &mdns.QueryParam{
		Service:             ServiceType,
		Domain:              strings.TrimSuffix(domain, "."),
		Timeout:             window,
		Entries:             entries,
		DisableIPv6:         true, // link-local IPv6 needs a zone the browser cannot use
		WantUnicastResponse: false,
		Logger:              libraryLogger(b.Log),
	})
	cancel()
	close(entries)
	<-done

	b.setAvailable(err == nil || ctx.Err() != nil, err)
}

func (b *Browser) setAvailable(ok bool, err error) {
	b.mu.Lock()
	changed := b.available != ok
	b.available, b.failure = ok, err
	b.mu.Unlock()

	if changed && !ok && b.Log != nil {
		// Info rather than error: a network that blocks multicast is a
		// configuration, not a fault, and the manual path exists for it.
		b.Log.Info("discovery unavailable, manual entry is the fallback", "error", err)
	}
	if changed {
		b.notify()
	}
}

// merge folds a sighting into the cache and reports whether anything a caller
// would notice changed.
func (b *Browser) merge(seen Peer) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	seen.LastSeen = now

	existing, ok := b.peers[seen.DeviceID]
	if !ok {
		seen.FirstSeen = now
		b.peers[seen.DeviceID] = &seen
		return true
	}

	// A device is not new because it answered again. Only a change in what is
	// displayed is worth telling anyone about, or every re-query would redraw
	// every list on the network.
	changed := existing.Name != seen.Name ||
		existing.TLS != seen.TLS ||
		existing.Version != seen.Version ||
		!sameAddresses(existing.Addresses, seen.Addresses)

	existing.LastSeen = now
	existing.Name = seen.Name
	existing.OS = seen.OS
	existing.Kind = seen.Kind
	existing.Version = seen.Version
	existing.TLS = seen.TLS
	if len(seen.Addresses) > 0 {
		existing.Addresses = seen.Addresses
	}
	// A device seen over mDNS after being added by hand is still discovered:
	// the manual entry was the user working around a network, and it stops
	// being the reason it is listed.
	if seen.Source == SourceMDNS {
		existing.Source = SourceMDNS
	}

	return changed
}

func (b *Browser) notify() {
	b.mu.RLock()
	listeners := append([]func(){}, b.listeners...)
	b.mu.RUnlock()

	for _, fn := range listeners {
		fn()
	}
}

// peerFrom reads a service entry into a peer, rejecting anything that is not a
// usable fastr record.
//
// The identifier is required and everything else is optional. A record is
// written by whatever is on the network, so this treats it as input: a missing
// identifier means the record cannot be attributed to a device at all, and
// there is nothing sensible to do with it.
func peerFrom(entry *mdns.ServiceEntry) (Peer, bool) {
	if entry == nil {
		return Peer{}, false
	}

	fields := parseTXT(entry.InfoFields)
	id := fields["id"]
	if id == "" {
		return Peer{}, false
	}

	version, _ := strconv.Atoi(fields["v"])

	peer := Peer{
		DeviceID: id,
		Name:     nameOf(fields, entry),
		Kind:     defaulted(fields["kind"], "computer"),
		OS:       fields["os"],
		Version:  version,
		TLS:      fields["tls"] == "1",
		Source:   SourceMDNS,
	}

	for _, ip := range addressesOf(entry) {
		// IPv4 only: a link-local IPv6 address needs a zone identifier that
		// does not survive the record, and connecting to one without it fails.
		if ip.To4() == nil {
			continue
		}
		// And local only. A record is written by whoever is on the network, so
		// one naming a public address is either a mistake or an attempt to
		// point this machine outward. The restricted client would refuse the
		// dial anyway; refusing here keeps the device out of the list rather
		// than listing one that can never be reached.
		if !localnet.IsIP(ip) {
			continue
		}
		peer.Addresses = append(peer.Addresses, net.JoinHostPort(ip.String(), strconv.Itoa(entry.Port)))
	}
	if len(peer.Addresses) == 0 {
		return Peer{}, false
	}
	return peer, true
}

func addressesOf(entry *mdns.ServiceEntry) []net.IP {
	var out []net.IP
	if entry.AddrV4 != nil {
		out = append(out, entry.AddrV4)
	}
	if entry.AddrV6 != nil {
		out = append(out, entry.AddrV6)
	}
	return out
}

// nameOf prefers the TXT name, which is what the user typed, over the instance
// name, which has a short identifier appended for uniqueness.
func nameOf(fields map[string]string, entry *mdns.ServiceEntry) string {
	if name := strings.TrimSpace(fields["name"]); name != "" {
		return name
	}
	instance := entry.Name
	if cut := strings.Index(instance, "."+ServiceType); cut > 0 {
		instance = instance[:cut]
	}
	return strings.TrimSpace(instance)
}

func parseTXT(fields []string) map[string]string {
	out := make(map[string]string, len(fields))
	for _, field := range fields {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		out[key] = value
	}
	return out
}

func defaulted(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func sameAddresses(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
