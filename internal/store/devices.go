package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// DeviceKind distinguishes what a device can do. Only a computer serves files
// and relays; a phone exists through its browser session.
type DeviceKind string

const (
	KindComputer DeviceKind = "computer"
	KindPhone    DeviceKind = "phone"
)

// TrustMode governs whether incoming transfers need a human to accept them,
// and how long the pairing survives inactivity. FR-016a and FR-016b.
type TrustMode string

const (
	// TrustAuto accepts incoming transfers without asking. The default for a
	// pairing the user created themselves.
	TrustAuto TrustMode = "auto"
	// TrustAsk requires confirmation for every transfer. The default for a
	// device that is not the user's own.
	TrustAsk TrustMode = "ask"
)

// Protection is which channel a device uses. Constitution v2.0.1, Principle V.
type Protection string

const (
	// ProtectionSimple encrypts pairing, credentials, and metadata. File
	// content travels in the clear on the local network.
	ProtectionSimple Protection = "simple"
	// ProtectionTrusted encrypts content end to end, after a one-time setup.
	ProtectionTrusted Protection = "trusted"
)

// Inactivity windows, per FR-016. A device the user trusts enough to accept
// files unattended should not need re-pairing every quarter; a device set to
// ask is more likely to be a visitor's, and should lapse quickly.
const (
	ExpiryAuto = 365 * 24 * time.Hour
	ExpiryAsk  = 30 * 24 * time.Hour
)

// Device is a computer or phone known to this instance.
//
// A Device record grants nothing. Authorization comes only from a Pairing;
// this type carries no field an access decision may read. See the invariants
// in data-model.md.
type Device struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Platform  string     `json:"platform"`
	Kind      DeviceKind `json:"kind"`
	Addresses []string   `json:"addresses,omitempty"`
	Port      int        `json:"port,omitempty"`
	LastSeen  time.Time  `json:"last_seen"`
}

// ShortID is the disambiguating suffix shown when two devices share a name.
// FR-005: identical names must stay distinguishable.
func (d Device) ShortID() string {
	if len(d.ID) < 6 {
		return d.ID
	}
	return d.ID[len(d.ID)-6:]
}

// DisplayName is the name alone, or the name with its short identifier when
// another device on the network answers to the same name.
func (d Device) DisplayName(ambiguous bool) string {
	if !ambiguous {
		return d.Name
	}
	return fmt.Sprintf("%s (%s)", d.Name, d.ShortID())
}

// Validate checks the rules from data-model.md.
func (d Device) Validate() error {
	if strings.TrimSpace(d.ID) == "" {
		return errors.New("device id is empty")
	}
	name := strings.TrimSpace(d.Name)
	if name == "" {
		return errors.New("device name is empty")
	}
	if len(name) > 64 {
		return errors.New("device name exceeds 64 bytes")
	}
	if d.Kind != KindComputer && d.Kind != KindPhone {
		return fmt.Errorf("unknown device kind %q", d.Kind)
	}
	if d.Port < 0 || d.Port > 65535 {
		return fmt.Errorf("port out of range: %d", d.Port)
	}
	return nil
}

// Pairing is the trust relationship between this instance and a device, and
// the only source of access.
type Pairing struct {
	DeviceID string `json:"device_id"`

	// TokenHash is a hash of the session credential. The credential itself is
	// never stored: a stolen store must not yield a working token.
	TokenHash []byte `json:"token_hash"`

	// SessionKey encrypts control traffic in both protection modes, which is
	// what keeps FR-017 true even when file content travels in the clear.
	SessionKey []byte `json:"session_key"`

	TrustMode      TrustMode  `json:"trust_mode"`
	ProtectionMode Protection `json:"protection"`

	// RequireTrusted refuses simple-mode connections from this device. FR-047c.
	RequireTrusted bool `json:"require_trusted"`

	CreatedAt    time.Time  `json:"created_at"`
	LastActivity time.Time  `json:"last_activity"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

// ExpiryWindow is how long this pairing survives inactivity, per its trust mode.
func (p Pairing) ExpiryWindow() time.Duration {
	if p.TrustMode == TrustAsk {
		return ExpiryAsk
	}
	return ExpiryAuto
}

// Revoked reports whether the user has revoked this pairing. Revocation is
// terminal: re-pairing creates a new record.
func (p Pairing) Revoked() bool { return p.RevokedAt != nil }

// Expired reports whether the pairing has lapsed at the given instant.
func (p Pairing) Expired(now time.Time) bool {
	return !p.ExpiresAt.IsZero() && !now.Before(p.ExpiresAt)
}

// Active reports whether the pairing may authorize a request right now.
// This is the single question the authorization layer asks.
func (p Pairing) Active(now time.Time) bool {
	return !p.Revoked() && !p.Expired(now)
}

// AcceptsAutomatically reports whether an incoming transfer may start without
// a human accepting it. FR-016a.
func (p Pairing) AcceptsAutomatically() bool { return p.TrustMode == TrustAuto }

// --- persistence -------------------------------------------------------------

// PutDevice stores or replaces a device record.
func (s *Store) PutDevice(d Device) error {
	if err := d.Validate(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx, bucketDevices, []byte(d.ID), d)
	})
}

// Device returns a device by identifier.
func (s *Store) Device(id string) (Device, error) {
	var d Device
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx, bucketDevices, []byte(id), &d)
	})
	return d, err
}

// Devices returns every known device.
func (s *Store) Devices() ([]Device, error) {
	var out []Device
	err := s.db.View(func(tx *bolt.Tx) error {
		return forEachJSON(tx, bucketDevices, func(_ string, d Device) error {
			out = append(out, d)
			return nil
		})
	})
	return out, err
}

// DeleteDevice removes a device and any pairing with it. Removing the device
// without the pairing would leave an authorization that no interface lists,
// which is precisely the kind of invisible access Principle V forbids.
func (s *Store) DeleteDevice(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketDevices).Delete([]byte(id)); err != nil {
			return err
		}
		return tx.Bucket(bucketPairings).Delete([]byte(id))
	})
}

// CreatePairing records a new trust relationship and returns it.
//
// The caller supplies the derived session key and the hash of the credential;
// this layer never sees the credential itself.
func (s *Store) CreatePairing(deviceID string, tokenHash, sessionKey []byte, mode TrustMode) (Pairing, error) {
	if strings.TrimSpace(deviceID) == "" {
		return Pairing{}, errors.New("device id is empty")
	}
	if len(tokenHash) == 0 {
		return Pairing{}, errors.New("token hash is empty")
	}
	if len(sessionKey) == 0 {
		return Pairing{}, errors.New("session key is empty")
	}
	if mode != TrustAuto && mode != TrustAsk {
		return Pairing{}, fmt.Errorf("unknown trust mode %q", mode)
	}

	now := s.clock()
	p := Pairing{
		DeviceID:       deviceID,
		TokenHash:      tokenHash,
		SessionKey:     sessionKey,
		TrustMode:      mode,
		ProtectionMode: ProtectionSimple,
		CreatedAt:      now,
		LastActivity:   now,
	}
	p.ExpiresAt = now.Add(p.ExpiryWindow())

	err := s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx, bucketPairings, []byte(deviceID), p)
	})
	return p, err
}

// Pairing returns the pairing for a device.
func (s *Store) Pairing(deviceID string) (Pairing, error) {
	var p Pairing
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx, bucketPairings, []byte(deviceID), &p)
	})
	return p, err
}

// Pairings returns every pairing, including revoked and expired ones, because
// FR-016 requires the user to be able to see that a pairing lapsed.
func (s *Store) Pairings() ([]Pairing, error) {
	var out []Pairing
	err := s.db.View(func(tx *bolt.Tx) error {
		return forEachJSON(tx, bucketPairings, func(_ string, p Pairing) error {
			out = append(out, p)
			return nil
		})
	})
	return out, err
}

// TouchPairing refreshes last activity and pushes the expiry out. Called on
// every successful authorization, which is what makes expiry mean "unused"
// rather than "old".
func (s *Store) TouchPairing(deviceID string) error {
	return s.updatePairing(deviceID, func(p *Pairing) error {
		now := s.clock()
		p.LastActivity = now
		p.ExpiresAt = now.Add(p.ExpiryWindow())
		return nil
	})
}

// SetTrustMode changes a pairing's trust mode.
//
// The expiry is recomputed from last activity, not from now, per FR-016.
// Recomputing from now would let a user extend a stale pairing by toggling a
// switch, which is not what the setting means.
func (s *Store) SetTrustMode(deviceID string, mode TrustMode) error {
	if mode != TrustAuto && mode != TrustAsk {
		return fmt.Errorf("unknown trust mode %q", mode)
	}
	return s.updatePairing(deviceID, func(p *Pairing) error {
		p.TrustMode = mode
		p.ExpiresAt = p.LastActivity.Add(p.ExpiryWindow())
		return nil
	})
}

// SetRequireTrusted sets whether simple-mode connections from this device are
// refused. FR-047c.
func (s *Store) SetRequireTrusted(deviceID string, require bool) error {
	return s.updatePairing(deviceID, func(p *Pairing) error {
		p.RequireTrusted = require
		return nil
	})
}

// SetProtection records which channel the device is currently using.
func (s *Store) SetProtection(deviceID string, mode Protection) error {
	if mode != ProtectionSimple && mode != ProtectionTrusted {
		return fmt.Errorf("unknown protection mode %q", mode)
	}
	return s.updatePairing(deviceID, func(p *Pairing) error {
		p.ProtectionMode = mode
		return nil
	})
}

// RevokePairing makes a pairing unusable immediately, with no grace period.
// FR-015. The key material is zeroed at the same time: a revoked pairing must
// not leave a usable key sitting in the store.
func (s *Store) RevokePairing(deviceID string) error {
	return s.updatePairing(deviceID, func(p *Pairing) error {
		if p.Revoked() {
			return nil
		}
		now := s.clock()
		p.RevokedAt = &now
		p.SessionKey = nil
		p.TokenHash = nil
		return nil
	})
}

func (s *Store) updatePairing(deviceID string, fn func(*Pairing) error) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var p Pairing
		if err := getJSON(tx, bucketPairings, []byte(deviceID), &p); err != nil {
			return err
		}
		if err := fn(&p); err != nil {
			return err
		}
		return putJSON(tx, bucketPairings, []byte(deviceID), p)
	})
}

// ActivePairing returns the pairing for a device only if it may authorize a
// request right now. It is the single entry point the authorization layer uses,
// so "revoked" and "expired" cannot be forgotten at a call site.
func (s *Store) ActivePairing(deviceID string) (Pairing, error) {
	p, err := s.Pairing(deviceID)
	if err != nil {
		return Pairing{}, err
	}
	if p.Revoked() {
		return Pairing{}, ErrPairingRevoked
	}
	if p.Expired(s.clock()) {
		return Pairing{}, ErrPairingExpired
	}
	return p, nil
}

// Reasons a pairing cannot authorize. They are distinguished because the user
// needs different corrective actions: re-pair, or ask the owner to re-approve.
var (
	ErrPairingRevoked = errors.New("pairing revoked")
	ErrPairingExpired = errors.New("pairing expired")
)
