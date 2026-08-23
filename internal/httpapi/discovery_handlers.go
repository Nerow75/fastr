package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/discovery"
	"github.com/Nerow75/fastr/internal/localnet"
)

// Discovery, exposed to the interface, per FR-004 to FR-008.
//
// There is one device list rather than two. A user does not think in terms of
// "paired devices" and "computers on the network": they think about the
// machines around them, some of which they have connected to before. So
// `/api/devices` merges the store with what discovery has found, and each row
// says which it is.
//
// Discovered devices are unpaired by construction. Being on the network buys
// nothing: a row appearing here means an address to connect to, and pairing is
// still a code typed by a human (FR-010). Nothing in a service record is ever
// an input to an access decision.

// Discovery is what the HTTP layer needs from the discovery package. An
// interface rather than the concrete browser, so a router can be built without
// one — which is what every test that does not care about the network does.
type Discovery interface {
	Peers() []discovery.Peer
	Unavailable() error
}

// discoveredView is a device found on the network but not in the store.
type discoveredView struct {
	ID string `json:"id"`
	// Name is what the device calls itself; Label is what to show, which is the
	// same thing unless two devices share a name (FR-005).
	Name      string   `json:"name"`
	Label     string   `json:"label"`
	Kind      string   `json:"kind"`
	Platform  string   `json:"platform,omitempty"`
	Addresses []string `json:"addresses"`
	// Reachable is the answer from /connect. Absent means not asked yet, which
	// the interface shows as "checking" rather than as either answer.
	Reachable *bool  `json:"reachable,omitempty"`
	Source    string `json:"source"`
	Version   int    `json:"version"`
	Paired    bool   `json:"paired"`
}

// discovered returns the peers this instance can see that the store does not
// already know, plus the labels to show them under.
func (d Deps) discovered(known map[string]bool) []discoveredView {
	if d.Discovery == nil {
		return nil
	}

	peers := d.Discovery.Peers()
	labels := discovery.Labels(peers)

	out := make([]discoveredView, 0, len(peers))
	for _, peer := range peers {
		if known[peer.DeviceID] {
			// Already in the store, so it is listed from there with its trust
			// mode and history. Listing it twice would be two rows for one
			// machine, and the user would have to guess which to use.
			continue
		}
		out = append(out, discoveredView{
			ID: peer.DeviceID, Name: peer.Name, Label: labels[peer.DeviceID],
			Kind: peer.Kind, Platform: peer.OS, Addresses: peer.Addresses,
			Reachable: peer.Reachable, Source: peer.Source, Version: peer.Version,
		})
	}
	return out
}

// discoveryStatus says whether automatic discovery is working, so the interface
// can offer manual entry with a reason rather than as a mysterious extra field.
func (d Deps) discoveryStatus() map[string]any {
	status := map[string]any{"available": true}
	if d.Discovery == nil {
		status["available"] = false
		status["reason"] = "discovery is not running"
		return status
	}
	if err := d.Discovery.Unavailable(); err != nil {
		status["available"] = false
		status["reason"] = err.Error()
	}
	return status
}

type manualDeviceRequest struct {
	Address string `json:"address"`
}

// handleAddManualDevice adds a device by address, per FR-006.
//
// Loopback only, like the other endpoints that change what this machine trusts.
// A paired phone must not be able to make its computer probe arbitrary
// addresses on the network: that turns a device list into a port scanner
// operated by someone else.
func (d Deps) handleAddManualDevice(s *Session, w http.ResponseWriter, r *http.Request) {
	if d.Browser == nil || d.Prober == nil {
		d.writeError(w, r, app.New(app.CodeNotFound))
		return
	}

	var req manualDeviceRequest
	if err := json.Unmarshal(s.Body, &req); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return
	}

	peer, err := discovery.Add(r.Context(), d.Browser, d.Prober, req.Address)
	if err != nil {
		d.writeError(w, r, manualError(err))
		return
	}

	labels := discovery.Labels(d.Browser.Peers())
	s.writeJSON(w, r, http.StatusOK, discoveredView{
		ID: peer.DeviceID, Name: peer.Name, Label: labels[peer.DeviceID],
		Kind: peer.Kind, Platform: peer.OS, Addresses: peer.Addresses,
		Reachable: peer.Reachable, Source: peer.Source, Version: peer.Version,
	})
}

// manualError turns a failure to add an address into something the interface
// can say out loud.
//
// Every one of these is a mistake a person makes at a keyboard, so each gets
// its own answer rather than a shared "invalid request": FR-038 asks for a
// corrective action, and "that address is this computer" is one while "400" is
// not.
func manualError(err error) error {
	var notLocal *localnet.ErrNotLocal

	switch {
	case errors.Is(err, discovery.ErrSelf):
		return app.Errorf(app.CodeInvalidRequest, err).WithParam("reason", "self")
	case errors.As(err, &notLocal):
		return app.Errorf(app.CodeInvalidRequest, err).WithParam("reason", "not_local")
	default:
		// Unreachable, not a fastr instance, or answering as something else.
		// They are one case from the user's side: nothing usable is there.
		return app.Errorf(app.CodeDeviceUnreachable, err)
	}
}
