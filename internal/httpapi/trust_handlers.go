package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/store"
	"github.com/Nerow75/fastr/internal/trust"
)

// The trusted-mode endpoints, per contracts/http-api.md and FR-047a to FR-047d.
//
// Three of them, and the split follows who is doing what:
//
//   - `/api/trust/init` is the **computer's user** deciding to set this up. It
//     generates the authority if there is none and issues a certificate for the
//     addresses this machine currently answers on. Loopback only, because
//     creating an authority is the most consequential thing this product does
//     and it is not a phone's decision.
//   - `/api/trust/status` is the interface asking where things stand, on either
//     device, so the walkthrough can say which step is next.
//   - `/api/trust/verify` is the **phone** saying it has arrived: it can only
//     succeed over the TLS listener, which is the proof. A phone claiming to be
//     trusted over plain HTTP is claiming exactly the thing it cannot do.
//
// Abandoning at any point leaves the simple pairing untouched, because none of
// this writes anything the simple path reads.

// Certificate is the file a phone installs. Served unauthenticated and in the
// clear on purpose: it is a public certificate, it is useless without the key
// that stays here, and the phone has to fetch it *before* it can be trusted.
// The fingerprint shown on the computer is what makes it checkable.
const certificatePath = "/trust/ca.crt"

type trustStatus struct {
	// Available reports whether this build can do trusted mode at all.
	Available bool `json:"available"`
	// Ready reports whether an authority exists and a certificate is served.
	Ready bool `json:"ready"`
	// Fingerprint is what the phone will show when it asks to install.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Addresses are where trusted mode answers. Empty until it is running.
	Addresses []string `json:"addresses"`
	// CertificateURL is where to download the authority.
	CertificateURL string `json:"certificate_url,omitempty"`
	// ExpiresAt is when the authority lapses, so the interface can say so
	// before it becomes a mystery.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Trusted reports whether *this* request arrived over the TLS listener,
	// which is how the walkthrough knows the phone has arrived.
	Trusted bool `json:"trusted"`
	// Devices using trusted mode, and the step each is on.
	Devices []trustDeviceView `json:"devices"`
}

type trustDeviceView struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	// Protection is what this device last connected with.
	Protection string `json:"protection"`
	// RequireTrusted refuses simple-mode connections from it. FR-047c.
	RequireTrusted bool `json:"require_trusted"`
}

// handleTrustStatus reports where trusted mode stands.
func (d Deps) handleTrustStatus(s *Session, w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, d.trustStatus(r))
}

func (d Deps) trustStatus(r *http.Request) trustStatus {
	status := trustStatus{
		Available: d.TrustDir != "",
		Trusted:   TrustedRequest(r),
		Addresses: []string{},
		Devices:   []trustDeviceView{},
	}

	if pairings, err := d.Store.Pairings(); err == nil {
		for _, p := range pairings {
			if p.Revoked() {
				continue
			}
			status.Devices = append(status.Devices, trustDeviceView{
				DeviceID:       p.DeviceID,
				Name:           d.deviceName(p.DeviceID),
				Protection:     string(p.ProtectionMode),
				RequireTrusted: p.RequireTrusted,
			})
		}
	}

	if !status.Available {
		return status
	}

	authority, err := trust.Load(d.TrustDir)
	if err != nil {
		return status // never set up, which is the ordinary state
	}

	expires := authority.NotAfter()
	status.Ready = true
	status.Fingerprint = authority.Fingerprint()
	status.ExpiresAt = &expires
	status.CertificateURL = certificatePath
	if d.TrustedAddresses != nil {
		status.Addresses = d.TrustedAddresses()
	}
	return status
}

// handleTrustInit creates the authority and issues a certificate.
//
// Loopback only. Generating an authority is the most consequential thing this
// product does — a key that can impersonate any site to every phone that
// installs it — and it is the computer owner's decision, never a phone's.
// Sealed like every other control-plane answer, and loopback-restricted on top
// of that: the fingerprint is a fact about this machine's identity, and the
// page asking is the one holding a session.
func (d Deps) handleTrustInit(s *Session, w http.ResponseWriter, r *http.Request) {
	if d.TrustDir == "" {
		d.writeError(w, r, app.New(app.CodeNotFound))
		return
	}

	authority, err := trust.LoadOrCreate(d.TrustDir)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	addresses := []string{}
	if d.Addresses != nil {
		addresses = d.Addresses()
	}

	certificate, err := authority.Issue(addresses)
	if errors.Is(err, trust.ErrNoAddresses) {
		// A laptop with its Wi-Fi off, which is a thing a person can fix rather
		// than an internal failure. FR-038 asks for the corrective action.
		d.writeError(w, r, app.Errorf(app.CodeNoLocalAddress, err))
		return
	}
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	if d.EnableTrusted != nil {
		if err := d.EnableTrusted(certificate); err != nil {
			// The authority exists and the certificate is on disk; only the
			// listener failed. Saying which matters, because the user can
			// retry the second without redoing the first.
			d.writeError(w, r, app.Errorf(app.CodeInternal, err))
			return
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"fingerprint":     authority.Fingerprint(),
		"certificate_url": certificatePath,
		"addresses":       trustedAddressesOf(d),
		"expires_at":      authority.NotAfter(),
	})
}

// handleTrustVerify records that a device reached the trusted origin.
//
// The proof is the connection itself: this can only succeed over the TLS
// listener. A phone asserting over plain HTTP that it is trusted is asserting
// precisely the thing it has not done, and is told so.
func (d Deps) handleTrustVerify(s *Session, w http.ResponseWriter, r *http.Request) {
	if !TrustedRequest(r) {
		d.writeError(w, r, app.New(app.CodeTrustedRequired))
		return
	}

	if err := d.Store.SetProtection(s.DeviceID, store.ProtectionTrusted); err != nil {
		d.writeError(w, r, storeError(err))
		return
	}

	d.Events.Publish(Event{Type: EventPairingChanged, DeviceID: s.DeviceID})
	d.Log.Info("device reached trusted mode", "device_id", s.DeviceID)

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"device_id":  s.DeviceID,
		"protection": string(store.ProtectionTrusted),
	})
}

// handleCertificate serves the authority for a phone to install.
//
// Unauthenticated, and that is correct: the phone needs this *before* it can be
// trusted, the certificate is public by nature, and it is inert without the key
// that never leaves this machine. What makes it safe to install is the
// fingerprint the user compares, not the secrecy of the download.
func (d Deps) handleCertificate(w http.ResponseWriter, r *http.Request) {
	if d.TrustDir == "" {
		d.writeError(w, r, app.New(app.CodeNotFound))
		return
	}

	authority, err := trust.Load(d.TrustDir)
	if err != nil {
		d.writeError(w, r, app.New(app.CodeNotFound))
		return
	}

	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="fastr-ca.crt"`)
	securityHeaders(w)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(authority.CertificatePEM())
}

func trustedAddressesOf(d Deps) []string {
	if d.TrustedAddresses == nil {
		return []string{}
	}
	return d.TrustedAddresses()
}
