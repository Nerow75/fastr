// Package trust is the opt-in path to full content encryption, per FR-047a and
// constitution Principle V.
//
// **Why a certificate authority at all.** Browsers grant a "secure context"
// only to loopback, never to a local network address. Outside one there is no
// `crypto.subtle` and no service worker, and without a service worker nothing
// can decrypt a stream while the browser writes it to disk — so a large
// encrypted file could only be received by holding it whole in memory, which
// iOS will not allow. A self-signed certificate does not help: the browser
// shows a warning instead of granting the context. The only route to a real
// secure context on a LAN address is a certificate the device already trusts,
// which means an authority the user installed themselves.
//
// **What that costs, stated plainly.** Installing a CA on a phone is a real
// security decision: anything holding its private key can impersonate any site
// to that device. So the key never leaves this machine, is written 0600 into a
// directory this process creates 0700, and is generated per installation rather
// than shipped. A CA shared between installations would be a master key for
// every fastr user on earth, which is why one is never distributed.
//
// **It is optional, and abandoning it is free.** FR-047d: setup can be stopped
// at any point without breaking the simple-mode pairing that already works.
// Nothing here is on the path of an ordinary transfer.
package trust

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Lifetimes.
//
// The authority outlives any reasonable use of one installation, because
// replacing it means visiting every phone again. Leaf certificates are short,
// because they name addresses a DHCP lease can move: a certificate for an
// address this machine no longer holds is worse than useless, and reissuing is
// one call away.
const (
	authorityLifetime = 10 * 365 * 24 * time.Hour
	leafLifetime      = 90 * 24 * time.Hour
)

// File names inside the trust directory.
const (
	caCertFile  = "ca.crt"
	caKeyFile   = "ca.key"
	tlsCertFile = "server.crt"
	tlsKeyFile  = "server.key"
)

// ErrNoAuthority reports that trusted mode has never been set up here.
var ErrNoAuthority = errors.New("no local certificate authority has been created")

// Authority is this installation's own certificate authority.
type Authority struct {
	// Dir is where the material lives. Created 0700.
	Dir string

	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pemBytes    []byte
}

// Load opens the authority in dir, or reports ErrNoAuthority.
func Load(dir string) (*Authority, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, caCertFile)) //nolint:gosec // path built from the configured data directory
	if os.IsNotExist(err) {
		return nil, ErrNoAuthority
	}
	if err != nil {
		return nil, fmt.Errorf("read authority certificate: %w", err)
	}

	keyPEM, err := os.ReadFile(filepath.Join(dir, caKeyFile)) //nolint:gosec // same constructed path
	if os.IsNotExist(err) {
		return nil, ErrNoAuthority
	}
	if err != nil {
		return nil, fmt.Errorf("read authority key: %w", err)
	}

	certificate, err := parseCertificate(certPEM)
	if err != nil {
		return nil, err
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}

	return &Authority{Dir: dir, certificate: certificate, key: key, pemBytes: certPEM}, nil
}

// Create generates a new authority and writes it to dir.
//
// Refuses to overwrite an existing one. Replacing an authority silently would
// invalidate every phone already set up, and the user would find out when a
// transfer stopped working rather than when they asked for anything.
func Create(dir string) (*Authority, error) {
	if _, err := Load(dir); err == nil {
		return nil, errors.New("a certificate authority already exists here")
	} else if !errors.Is(err, ErrNoAuthority) {
		return nil, err
	}

	// 0700: this directory is about to hold a key that can impersonate any site
	// to every phone that trusts it.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create trust directory: %w", err)
	}
	// And 0700 means nothing on Windows, which decides access by a list rather
	// than by permission bits. Restricted before anything is written into it,
	// so the key below inherits the restriction rather than being created
	// readable and tightened a moment later. A no-op on POSIX.
	if err := restrictToOwner(dir); err != nil {
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate authority key: %w", err)
	}

	serial, err := newSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			// Named so a person scrolling their phone's certificate list can
			// tell what it is and where it came from. A generic name would be
			// indistinguishable from something they did not install.
			CommonName:   "fastr local authority",
			Organization: []string{"fastr"},
		},
		NotBefore: now.Add(-time.Hour), // tolerate a phone whose clock is behind
		NotAfter:  now.Add(authorityLifetime),

		IsCA:                  true,
		BasicConstraintsValid: true,
		// Signing certificates and nothing else. A CA that could also do key
		// agreement is a CA that can be used for more than it was installed for.
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		// A path length of zero, encoded as such. MaxPathLen alone means
		// "unset" in this struct, so without MaxPathLenZero the certificate
		// would quietly permit intermediate authorities — which is a longer
		// chain of trust than the user agreed to when they installed it.
		MaxPathLen:     0,
		MaxPathLenZero: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("sign authority certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, caCertFile), certPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write authority certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode authority key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	// 0600, and never anywhere else. This key is the whole of the trust a user
	// extends when they install the certificate on their phone.
	keyPath := filepath.Join(dir, caKeyFile)
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write authority key: %w", err)
	}
	// Stated on the key itself rather than left to what it inherited, because
	// the directory's entry is the only thing standing between this key and
	// every other account named on %LOCALAPPDATA%.
	if err := restrictToOwner(keyPath); err != nil {
		return nil, err
	}

	certificate, err := parseCertificate(certPEM)
	if err != nil {
		return nil, err
	}
	return &Authority{Dir: dir, certificate: certificate, key: key, pemBytes: certPEM}, nil
}

// LoadOrCreate opens the authority, creating it on first use.
func LoadOrCreate(dir string) (*Authority, error) {
	existing, err := Load(dir)
	if err == nil {
		// An authority written by a build that set no access list is repaired
		// here rather than left as it was found. This is the call the trusted
		// mode setup makes, which is the moment the user is being told the key
		// stays on their machine; `Load` stays a read, and does not.
		if err := existing.restrict(); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if !errors.Is(err, ErrNoAuthority) {
		return nil, err
	}
	return Create(dir)
}

// restrict re-states who may reach the directory and the keys inside it.
//
// The certificates are not in the list: one of them is handed out to every
// phone that sets up trusted mode, so restricting it would protect nothing and
// suggest it was a secret.
func (a *Authority) restrict() error {
	if err := restrictToOwner(a.Dir); err != nil {
		return err
	}
	for _, name := range []string{caKeyFile, tlsKeyFile} {
		path := filepath.Join(a.Dir, name)
		if _, err := os.Stat(path); err != nil {
			continue // no server key yet, which is the ordinary case before the first issue
		}
		if err := restrictToOwner(path); err != nil {
			return err
		}
	}
	return nil
}

// CertificatePEM is what the user installs on their phone.
//
// The certificate only: the key that signs with it stays here, and nothing in
// this package ever hands it out.
func (a *Authority) CertificatePEM() []byte {
	out := make([]byte, len(a.pemBytes))
	copy(out, a.pemBytes)
	return out
}

// Fingerprint is the SHA-256 of the certificate, in the form a phone shows when
// it asks whether to install it.
//
// Displayed on the computer so the two can be compared. Without it, "install
// this certificate" is a request to trust whatever happened to arrive.
func (a *Authority) Fingerprint() string {
	return fingerprintOf(a.certificate.Raw)
}

// NotAfter is when the authority expires, so the interface can say so before it
// becomes a mystery.
func (a *Authority) NotAfter() time.Time { return a.certificate.NotAfter }

func parseCertificate(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("not a PEM key")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// newSerial draws a random serial number.
//
// Random rather than sequential: a counter would have to be stored, and a
// counter that resets after a restore from backup issues two certificates with
// one serial, which some clients reject outright.
func newSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("serial number: %w", err)
	}
	return serial, nil
}
