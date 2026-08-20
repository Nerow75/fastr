package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/pairing"
	"github.com/Nerow75/fastr/internal/store"
)

// Pairing endpoints, per contracts/http-api.md.
//
// These are the only routes reachable without a session, because their purpose
// is to create one. They are protected by the pairing code, the handshake, and
// a human confirming on the host.

type connectResponse struct {
	Name     string `json:"name"`
	DeviceID string `json:"device_id"`
	Version  int    `json:"version"`
	Kind     string `json:"kind"`
}

// handleConnect answers the reachability probe and identifies this instance.
//
// Unauthenticated on purpose: it carries a name, an identifier, and a version,
// which the mDNS record already broadcasts to everyone on the network. Adding a
// credential requirement here would break discovery without protecting
// anything.
func (d Deps) handleConnect(w http.ResponseWriter, _ *http.Request) {
	d.writePlainJSON(w, http.StatusOK, connectResponse{
		Name:     d.DeviceName,
		DeviceID: d.DeviceID,
		Version:  pairing.ProtocolVersion,
		Kind:     string(store.KindComputer),
	})
}

type pairInitRequest struct {
	ClientPublicKey string `json:"client_pub"`
	DeviceName      string `json:"device_name"`
	Platform        string `json:"platform"`
}

type pairInitResponse struct {
	HandshakeID     string `json:"handshake_id"`
	ServerPublicKey string `json:"server_pub"`
	Salt            string `json:"salt"`
}

// maxPairBody bounds an unauthenticated request. Two public keys and a name do
// not need more, and an unauthenticated endpoint should never read an unbounded
// body.
const maxPairBody = 4 << 10

// handlePairInit starts a key agreement.
func (d Deps) handlePairInit(w http.ResponseWriter, r *http.Request) {
	var req pairInitRequest
	if !d.decodePlain(w, r, &req) {
		return
	}

	clientPub, err := base64.StdEncoding.DecodeString(req.ClientPublicKey)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return
	}

	h, err := d.Handshakes.Begin(clientPub)
	if err != nil {
		if errors.Is(err, pairing.ErrBadPublicKey) {
			d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
			return
		}
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	d.writePlainJSON(w, http.StatusOK, pairInitResponse{
		HandshakeID:     h.ID,
		ServerPublicKey: base64.StdEncoding.EncodeToString(h.ServerPublicKey()),
		Salt:            base64.StdEncoding.EncodeToString(h.Salt),
	})
}

type pairConfirmRequest struct {
	HandshakeID string `json:"handshake_id"`
	Code        string `json:"code"`
	Proof       string `json:"proof"`
	DeviceName  string `json:"device_name"`
	Platform    string `json:"platform"`
}

type pairConfirmResponse struct {
	PendingID string `json:"pending_id"`
	State     string `json:"state"`
	ExpiresIn int    `json:"expires_in"`
}

// handlePairConfirm verifies the code and the proof, then queues the device for
// a human to approve on the host.
//
// The code is checked first so a wrong guess is counted and rate limited before
// any cryptography runs. Both checks must pass; neither reveals which failed,
// beyond what the attempt budget already tells an honest user.
//
// Knowing the code proves someone read it off the host's screen, which is a
// strong signal but not the one FR-010 asks for: a human on the *receiving*
// device deciding. So a correct code buys a place in the queue, not access.
func (d Deps) handlePairConfirm(w http.ResponseWriter, r *http.Request) {
	var req pairConfirmRequest
	if !d.decodePlain(w, r, &req) {
		return
	}

	if err := d.Codes.Verify(req.Code); err != nil {
		d.writeError(w, r, codeError(err))
		return
	}

	proof, err := base64.StdEncoding.DecodeString(req.Proof)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return
	}

	sessionKey, err := d.Handshakes.Complete(req.HandshakeID, req.Code, proof)
	if err != nil {
		d.writeError(w, r, handshakeError(err))
		return
	}

	pending, err := d.Pendings.Add(deviceDisplayName(req.DeviceName), req.Platform, sessionKey)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	d.Events.Publish(Event{Type: EventPairingPending})
	d.Log.Info("pairing request awaiting approval", "name", pending.DeviceName)

	d.writePlainJSON(w, http.StatusAccepted, pairConfirmResponse{
		PendingID: pending.ID,
		State:     string(pending.State),
		ExpiresIn: int(pairing.PendingTTL.Seconds()),
	})
}

type pairStatusResponse struct {
	State string `json:"state"`
	// Credential is sealed with the key derived during the handshake, so it is
	// readable only by the device that completed it. Handed over exactly once.
	Credential string `json:"credential,omitempty"`
	DeviceID   string `json:"device_id,omitempty"`
	TrustMode  string `json:"trust_mode,omitempty"`
	Protection string `json:"protection,omitempty"`
}

// handlePairStatus is polled by the waiting device.
//
// Unauthenticated by necessity: the device has no credential yet, which is the
// whole point of the exchange. The pending identifier is 128 random bits and
// the credential it eventually yields is sealed, so guessing one buys nothing.
func (d Deps) handlePairStatus(w http.ResponseWriter, r *http.Request) {
	pending, err := d.Pendings.Get(r.URL.Query().Get("pending"))
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeNotFound, err))
		return
	}

	if pending.State != pairing.PendingApproved {
		d.writePlainJSON(w, http.StatusOK, pairStatusResponse{State: string(pending.State)})
		return
	}

	credential, ok := d.Pendings.TakeCredential(pending.ID)
	if !ok {
		// Already collected. The credential is handed over once, so a second
		// poll gets the state and nothing else.
		d.writePlainJSON(w, http.StatusOK, pairStatusResponse{State: string(pending.State)})
		return
	}

	p, err := d.Store.Pairing(pending.DeviceID())
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	// Sealed with the handshake key, so the credential is unreadable to anyone
	// watching this plain HTTP exchange.
	sealed, err := sealForPending(pending.SessionKey(), r.URL.Path, credential)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}
	d.Pendings.Collected(pending.ID)

	d.writePlainJSON(w, http.StatusOK, pairStatusResponse{
		State:      string(pending.State),
		Credential: sealed,
		DeviceID:   pending.DeviceID(),
		TrustMode:  string(p.TrustMode),
		Protection: string(p.ProtectionMode),
	})
}

// handlePendingList shows the host what is waiting.
func (d Deps) handlePendingList(w http.ResponseWriter, _ *http.Request) {
	d.writePlainJSON(w, http.StatusOK, map[string]any{"pending": d.Pendings.List()})
}

// handlePendingApprove is the human saying yes. FR-010.
func (d Deps) handlePendingApprove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	existing, err := d.Pendings.Get(id)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeNotFound, err))
		return
	}

	deviceID := store.NewID().String()
	if err := d.Store.PutDevice(store.Device{
		ID:       deviceID,
		Name:     existing.DeviceName,
		Platform: existing.Platform,
		Kind:     store.KindPhone,
		LastSeen: time.Now(),
	}); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	approved, err := d.Pendings.Approve(id, deviceID)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return
	}

	credential, hash, err := pairing.NewCredential()
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	// A device approved here is one the user is standing in front of, having
	// just read a code off this screen and said yes. FR-016b makes that the
	// automatic-acceptance default; the user can change it afterwards.
	if _, err := d.Store.CreatePairing(deviceID, hash, approved.SessionKey(), store.TrustAuto); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}
	if err := d.Sessions.Register(deviceID, approved.SessionKey()); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}
	d.Pendings.HoldCredential(id, credential)

	d.Events.Publish(Event{Type: EventPairingChanged, DeviceID: deviceID})
	d.Log.Info("device paired", "device_id", deviceID, "name", existing.DeviceName)

	d.writePlainJSON(w, http.StatusOK, map[string]any{"device_id": deviceID})
}

// handlePendingReject is the human saying no.
func (d Deps) handlePendingReject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, err := d.Pendings.Reject(id); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeNotFound, err))
		return
	}
	d.Events.Publish(Event{Type: EventPairingPending})

	d.writePlainJSON(w, http.StatusOK, map[string]any{"rejected": id})
}

// sealForPending encrypts the credential with the handshake key.
//
// A fresh envelope is used rather than the session's, because the device has
// not established one yet: this exchange is what gives it the credential the
// session will authenticate with.
func sealForPending(sessionKey []byte, path, credential string) (string, error) {
	env, err := pairing.NewEnvelope(sessionKey, pairing.ServerToClient)
	if err != nil {
		return "", err
	}
	sealed, err := env.Seal(http.MethodGet, path, pairing.ProtocolVersion, []byte(credential))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

type deviceView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Platform   string    `json:"platform"`
	Kind       string    `json:"kind"`
	LastSeen   time.Time `json:"last_seen"`
	Paired     bool      `json:"paired"`
	TrustMode  string    `json:"trust_mode,omitempty"`
	Protection string    `json:"protection,omitempty"`
}

// handleDevices lists known devices with their pairing state.
func (d Deps) handleDevices(s *Session, w http.ResponseWriter, r *http.Request) {
	devices, err := d.Store.Devices()
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	out := make([]deviceView, 0, len(devices))
	for _, dev := range devices {
		view := deviceView{
			ID: dev.ID, Name: dev.Name, Platform: dev.Platform,
			Kind: string(dev.Kind), LastSeen: dev.LastSeen,
		}
		if p, err := d.Store.Pairing(dev.ID); err == nil && !p.Revoked() {
			view.Paired = true
			view.TrustMode = string(p.TrustMode)
			view.Protection = string(p.ProtectionMode)
		}
		out = append(out, view)
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"devices": out})
}

type pairingView struct {
	DeviceID       string     `json:"device_id"`
	TrustMode      string     `json:"trust_mode"`
	Protection     string     `json:"protection"`
	RequireTrusted bool       `json:"require_trusted"`
	CreatedAt      time.Time  `json:"created_at"`
	LastActivity   time.Time  `json:"last_activity"`
	ExpiresAt      time.Time  `json:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

// handlePairings lists pairings, including revoked and expired ones: FR-016
// requires the user to be able to see that a pairing lapsed.
//
// No key material is included. The views deliberately omit TokenHash and
// SessionKey rather than relying on json tags, so a future field cannot leak
// by being added to the store type.
func (d Deps) handlePairings(s *Session, w http.ResponseWriter, r *http.Request) {
	pairings, err := d.Store.Pairings()
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	out := make([]pairingView, 0, len(pairings))
	for _, p := range pairings {
		out = append(out, pairingView{
			DeviceID: p.DeviceID, TrustMode: string(p.TrustMode),
			Protection: string(p.ProtectionMode), RequireTrusted: p.RequireTrusted,
			CreatedAt: p.CreatedAt, LastActivity: p.LastActivity,
			ExpiresAt: p.ExpiresAt, RevokedAt: p.RevokedAt,
		})
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"pairings": out})
}

// handleRevoke removes a device's access. FR-015: immediately, no grace period.
func (d Deps) handleRevoke(s *Session, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := d.Store.RevokePairing(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			d.writeError(w, r, app.New(app.CodeNotFound))
			return
		}
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}
	// Forgetting the envelope is what makes revocation bite on a connection
	// already in flight, rather than at the next lookup.
	d.Sessions.Forget(id)

	d.Events.Publish(Event{Type: EventPairingChanged, DeviceID: id})
	d.Log.Info("pairing revoked", "device_id", id)

	s.writeJSON(w, r, http.StatusOK, map[string]any{"revoked": id})
}

type pairingUpdate struct {
	TrustMode      *string `json:"trust_mode,omitempty"`
	RequireTrusted *bool   `json:"require_trusted,omitempty"`
}

// handlePairingUpdate changes a pairing's trust mode or trusted-mode
// requirement. Both take effect immediately, per FR-016b and FR-047c.
func (d Deps) handlePairingUpdate(s *Session, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req pairingUpdate
	if len(s.Body) > 0 {
		if err := json.Unmarshal(s.Body, &req); err != nil {
			d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
			return
		}
	}

	if req.TrustMode != nil {
		mode := store.TrustMode(*req.TrustMode)
		if mode != store.TrustAuto && mode != store.TrustAsk {
			d.writeError(w, r, app.New(app.CodeInvalidRequest))
			return
		}
		if err := d.Store.SetTrustMode(id, mode); err != nil {
			d.writeError(w, r, storeError(err))
			return
		}
	}

	if req.RequireTrusted != nil {
		if err := d.Store.SetRequireTrusted(id, *req.RequireTrusted); err != nil {
			d.writeError(w, r, storeError(err))
			return
		}
	}

	p, err := d.Store.Pairing(id)
	if err != nil {
		d.writeError(w, r, storeError(err))
		return
	}

	d.Events.Publish(Event{Type: EventPairingChanged, DeviceID: id})

	s.writeJSON(w, r, http.StatusOK, pairingView{
		DeviceID: p.DeviceID, TrustMode: string(p.TrustMode),
		Protection: string(p.ProtectionMode), RequireTrusted: p.RequireTrusted,
		CreatedAt: p.CreatedAt, LastActivity: p.LastActivity,
		ExpiresAt: p.ExpiresAt, RevokedAt: p.RevokedAt,
	})
}

// decodePlain reads an unsealed JSON body from an unauthenticated endpoint.
func (d Deps) decodePlain(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPairBody))
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInvalidRequest, err))
		return false
	}
	return true
}

// deviceDisplayName falls back to a generic name rather than accepting an empty
// one, which would render as a blank row in the device list.
func deviceDisplayName(name string) string {
	if len(name) == 0 || len(name) > 64 {
		return "Phone"
	}
	return name
}

// codeError maps a pairing-code failure to its catalogue code.
func codeError(err error) error {
	switch {
	case errors.Is(err, pairing.ErrCodeExpired), errors.Is(err, pairing.ErrNoCode):
		return app.Errorf(app.CodeCodeExpired, err)
	case errors.Is(err, pairing.ErrCodeDead), errors.Is(err, pairing.ErrCodeConsumed):
		return app.Errorf(app.CodeCodeExhausted, err)
	case errors.Is(err, pairing.ErrRateLimited):
		return app.Errorf(app.CodeRateLimited, err)
	default:
		return app.Errorf(app.CodeInvalidCode, err)
	}
}

// handshakeError maps a handshake failure to its catalogue code.
//
// A bad proof and an unknown handshake both surface as an invalid code: they
// are the same event from the user's side, and distinguishing them would tell
// an attacker which half of their guess was wrong.
func handshakeError(err error) error {
	switch {
	case errors.Is(err, pairing.ErrHandshakeExpired):
		return app.Errorf(app.CodeCodeExpired, err)
	default:
		return app.Errorf(app.CodeInvalidCode, err)
	}
}

// storeError maps a store failure to its catalogue code.
func storeError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return app.Errorf(app.CodeNotFound, err)
	}
	return app.Errorf(app.CodeInternal, err)
}
