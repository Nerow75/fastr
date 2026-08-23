package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Nerow75/fastr/internal/localnet"
)

// Manual address entry, per FR-006.
//
// Networks that block multicast are ordinary: offices do it, guest wifi does
// it, and so does any access point with client isolation on. The contract is
// explicit that the answer is this path rather than a second discovery protocol
// of the project's own design.
//
// The one rule that matters here is that **nothing downstream can tell the
// difference**. A manually added device produces the same record, with the same
// identity fields, as one that arrived over mDNS — because the fields come from
// the same place either way: `/connect`, on the machine itself. The user
// supplies an address, not an identity.

// DefaultPort is assumed when the user types a bare address. It is not a
// promise: fastr binds whatever port it is given, and a machine on another port
// has to be typed with it.
const DefaultPort = 7420

// ErrSelf reports an address that turned out to be this machine.
var ErrSelf = errors.New("that address is this computer")

// Add resolves an address the user typed and folds it into the browser's view.
//
// It probes before recording anything. An entry that was never reachable is a
// typo, and remembering typos as devices makes the list worse than empty.
func Add(ctx context.Context, b *Browser, p *Prober, typed string) (Peer, error) {
	address, err := NormalizeAddress(typed)
	if err != nil {
		return Peer{}, err
	}

	identity, err := p.Identify(ctx, address)
	if err != nil {
		return Peer{}, err
	}
	if identity.DeviceID == b.SelfID {
		return Peer{}, ErrSelf
	}

	now := time.Now()
	reachable := true
	peer := Peer{
		DeviceID:  identity.DeviceID,
		Name:      identity.Name,
		Kind:      defaulted(identity.Kind, "computer"),
		Version:   identity.Version,
		Addresses: []string{address},
		Source:    SourceManual,
		FirstSeen: now,
		LastSeen:  now,
		Reachable: &reachable,
		CheckedAt: now,
	}

	b.Remember(peer)

	// Read back rather than returned as built: the browser may already know
	// this device from mDNS, in which case the caller should see the merged
	// record and not a second, thinner view of the same machine.
	if merged, ok := b.Peer(peer.DeviceID); ok {
		return merged, nil
	}
	return peer, nil
}

// NormalizeAddress turns what a person typed into a host:port this can dial.
//
// It accepts what people actually type: an address, an address with a port, and
// either of those with a scheme or a trailing slash pasted from a browser bar.
// It does not accept a host name, because resolving one is a DNS query, which
// is a request off the local network that Principle I forbids — and the
// interface says so rather than failing at the socket with something
// unreadable.
func NormalizeAddress(typed string) (string, error) {
	address := strings.TrimSpace(typed)
	if address == "" {
		return "", errors.New("no address")
	}

	address = strings.TrimPrefix(address, "http://")
	address = strings.TrimPrefix(address, "https://")
	address = strings.TrimSuffix(address, "/")
	if cut := strings.IndexAny(address, "/?#"); cut >= 0 {
		address = address[:cut]
	}

	host, port, err := net.SplitHostPort(address)
	if err != nil {
		// No port, or an IPv6 address written without brackets. Try the
		// bracket-free form as a bare host before giving up.
		host, port = address, strconv.Itoa(DefaultPort)
	}
	host = strings.Trim(host, "[]")

	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("%q is not an address; type the numbers shown on the other computer", typed)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number <= 0 || number > 65535 {
		return "", fmt.Errorf("%q has no usable port", typed)
	}

	joined := net.JoinHostPort(host, strconv.Itoa(number))
	if !localnet.IsAddr(joined) {
		return "", &localnet.ErrNotLocal{Address: joined}
	}
	return joined, nil
}
