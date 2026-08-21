package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/pairing"
	"github.com/Nerow75/fastr/internal/store"
)

// The host's own session.
//
// The computer's page used to pair with itself: it read the six digits from its
// own screen, typed them into its own form, and approved its own request. That
// was never a security measure, only a consequence of treating every client the
// same. Nobody found it. A user who scanned the QR with their phone paired the
// phone, saw the computer's screen unchanged, and had no way to guess that the
// page was waiting to be told about itself — so sending *from* the computer was
// unreachable.
//
// **Why loopback is enough.** Reaching 127.0.0.1 means running on this machine,
// which is the same boundary the approval endpoints already rest on: FR-010
// asks for "a human on the receiving device", and that is what being on the
// device means. This grants no power that boundary did not already give. A
// local process that could call this could equally approve any pending device
// through /api/pair/pending/{id}/approve, and could read the session keys
// straight out of the store file.
//
// The key travels in the clear because it never leaves the machine, and because
// there is no handshake to derive one from: a handshake exists to establish a
// secret between two parties, and here there is only one.

type hostSessionResponse struct {
	DeviceID   string `json:"device_id"`
	Credential string `json:"credential"`
	// Key is the envelope key, base64. Same value the store holds.
	Key string `json:"key"`
	// Name is what this computer calls itself, so the page can show it.
	Name string `json:"name"`
}

// handleHostSession grants the host's own page a session, without a code.
//
// Called once per browser profile: the page keeps the result in site data and
// restores it on later loads, so a second tab does not mint a second credential
// and invalidate the first.
func (d Deps) handleHostSession(w http.ResponseWriter, r *http.Request) {
	if d.DeviceID == "" {
		d.writeError(w, r, app.Errorf(app.CodeInternal,
			errors.New("this instance has no identity yet")))
		return
	}

	credential, hash, err := pairing.NewCredential()
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	// A fresh key every time, never the previous one.
	//
	// The envelope derives its nonces from a counter that restarts at zero for
	// a new session, so handing out a new session under an old key would reuse
	// nonces — which for ChaCha20-Poly1305 loses confidentiality outright. It
	// is also what keeps the two sides in step: the server caches one envelope
	// per device, and a page starting at counter zero against a cached counter
	// that had already advanced sees every one of its requests refused as a
	// replay.
	key, err := pairing.NewSessionKey()
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	// TrustAuto: this is the machine itself, and asking it to confirm every
	// transfer it initiated would be asking the user to approve their own click.
	if _, err := d.Store.CreatePairing(d.DeviceID, hash, key, store.TrustAuto); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	// Resets the cached envelope, and its counter, to match the key just
	// issued. Omitting this is exactly the replay above.
	if err := d.Sessions.Register(d.DeviceID, key); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	d.Log.Info("host session granted", "device_id", d.DeviceID)

	d.writePlainJSON(w, http.StatusOK, hostSessionResponse{
		DeviceID:   d.DeviceID,
		Credential: credential,
		Key:        base64.StdEncoding.EncodeToString(key),
		Name:       d.DeviceName,
	})
}
