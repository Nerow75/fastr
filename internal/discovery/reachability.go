package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Nerow75/fastr/internal/localnet"
)

// Reachability, per FR-007 and contracts/discovery.md.
//
// A service record says a device published itself. It does not say the process
// is still running, that the address still belongs to it, or that anything is
// listening: mDNS records have a time to live measured in minutes, and a laptop
// that closed its lid leaves one behind. Offering that device as a destination
// produces a transfer that sits at nothing, which is the failure T051e was
// about in the other direction.
//
// So the record populates the list and `/connect` decides the dot. It is the
// cheapest thing this project serves, it is unauthenticated by design, and it
// answers with the identity the record claimed — which is also how a stale
// record pointing at an address someone else now holds is caught.

// probeTimeout bounds a check. Long enough for a sleeping access point to wake,
// short enough that a list of ten devices settles while the user is still
// looking at it.
const probeTimeout = 2 * time.Second

// Identity is what /connect reports about a machine.
type Identity struct {
	Name     string `json:"name"`
	DeviceID string `json:"device_id"`
	Version  int    `json:"version"`
	Kind     string `json:"kind"`
}

// ErrIdentityMismatch reports an address answering as a different device.
//
// Not a curiosity: addresses are reassigned by DHCP all the time, so a record
// remembered five minutes ago can point at someone else's machine. Sending a
// file to whoever happens to hold an address is precisely what must not happen.
var ErrIdentityMismatch = errors.New("that address belongs to a different device now")

// Prober asks devices whether they are there.
type Prober struct {
	// Client is the restricted client. Nil builds one, which is the normal
	// case; tests supply their own.
	Client *http.Client
}

// NewProber returns a prober using a client that cannot leave the local
// network.
func NewProber() *Prober {
	return &Prober{Client: localnet.Client(probeTimeout)}
}

// Identify asks an address who it is.
//
// The address is checked before the request rather than only in the dialer,
// so a caller gets an error that names the reason instead of a connection
// failure it has to interpret.
func (p *Prober) Identify(ctx context.Context, address string) (Identity, error) {
	if !localnet.IsAddr(address) {
		return Identity{}, &localnet.ErrNotLocal{Address: address}
	}

	client := p.Client
	if client == nil {
		client = localnet.Client(probeTimeout)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/connect", nil)
	if err != nil {
		return Identity{}, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return Identity{}, err
	}
	// Read-only and already drained below; a failed close loses nothing.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("%s answered %d", address, resp.StatusCode)
	}

	// Bounded: the body comes from a machine this one has no relationship with
	// yet, and four fields do not need more. An unbounded read from an
	// unauthenticated peer is how a device list becomes a memory exhaustion.
	const maxIdentity = 4 << 10
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxIdentity))
	if err != nil {
		return Identity{}, err
	}

	var identity Identity
	if err := json.Unmarshal(raw, &identity); err != nil {
		return Identity{}, fmt.Errorf("%s is not a fastr instance: %w", address, err)
	}
	if strings.TrimSpace(identity.DeviceID) == "" {
		return Identity{}, fmt.Errorf("%s is not a fastr instance", address)
	}
	return identity, nil
}

// Confirm checks one known peer and returns whether it answered as itself.
func (p *Prober) Confirm(ctx context.Context, peer Peer) (bool, error) {
	address := peer.Address()
	if address == "" {
		return false, nil
	}

	identity, err := p.Identify(ctx, address)
	if err != nil {
		return false, err
	}
	if identity.DeviceID != peer.DeviceID {
		return false, fmt.Errorf("%w: expected %s, got %s",
			ErrIdentityMismatch, ShortID(peer.DeviceID), ShortID(identity.DeviceID))
	}
	return true, nil
}

// Refresh probes every known peer and records the answers.
//
// Sequential rather than concurrent. A household has a handful of computers,
// each probe is bounded at two seconds, and a burst of parallel connections to
// every machine on the network is a worse neighbour than a short serial sweep.
func (p *Prober) Refresh(ctx context.Context, b *Browser) {
	for _, peer := range b.Peers() {
		if ctx.Err() != nil {
			return
		}
		reachable, err := p.Confirm(ctx, peer)
		if err != nil && b.Log != nil {
			// Debug: a device being off is the normal state of most devices,
			// and logging it as a problem would bury the ones that matter.
			b.Log.Debug("reachability", "device_id", peer.DeviceID, "error", err)
		}
		b.SetReachable(peer.DeviceID, reachable, time.Now())
	}
}

// Watch refreshes reachability on an interval until ctx ends.
func (p *Prober) Watch(ctx context.Context, b *Browser, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	p.Refresh(ctx, b)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Refresh(ctx, b)
		}
	}
}
