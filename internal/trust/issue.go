package trust

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Issuing the certificate this machine actually serves, per FR-047a.
//
// It names **addresses, not names**. A phone reaches this computer at
// 192.168.1.20, not at a host name, because resolving a name is a request off
// the local network and Principle I forbids one. So the certificate carries IP
// addresses in its subject alternative names, and a browser matches on exactly
// that.
//
// Which means it has to be reissued when the addresses change, and they do:
// a DHCP lease moves, a laptop joins another network, a cable is swapped for
// Wi-Fi. That is why leaves are short-lived and why `Issue` is cheap to call
// again — reissuing on every start is the intended use, not a fallback.

// ErrNoAddresses reports that there is nothing to issue a certificate for.
//
// A machine on loopback only is one no phone can reach anyway, so trusted mode
// has nothing to secure and says so rather than issuing a certificate for an
// address that means "this machine" on every machine.
var ErrNoAddresses = fmt.Errorf("no local address to issue a certificate for")

// Certificate is the material the TLS listener serves.
type Certificate struct {
	// Addresses are the ones this certificate is valid for, in the order they
	// were requested, so the interface can say which.
	Addresses []string
	NotAfter  time.Time

	certPEM []byte
	keyPEM  []byte
}

// TLS builds the key pair for a listener.
func (c *Certificate) TLS() (tls.Certificate, error) {
	return tls.X509KeyPair(c.certPEM, c.keyPEM)
}

// Fingerprint identifies this certificate, for the interface to display.
func (c *Certificate) Fingerprint() (string, error) {
	block, _ := pem.Decode(c.certPEM)
	if block == nil {
		return "", fmt.Errorf("not a PEM certificate")
	}
	return fingerprintOf(block.Bytes), nil
}

// Issue signs a certificate for the given addresses and stores it beside the
// authority.
//
// Addresses are filtered rather than trusted: an address that is not local has
// no business in a certificate this machine serves on a local network, and a
// certificate naming a public address is one that could be used to impersonate
// something on the internet to every phone that installed the authority.
func (a *Authority) Issue(addresses []string) (*Certificate, error) {
	ips := make([]net.IP, 0, len(addresses))
	named := make([]string, 0, len(addresses))

	for _, address := range addresses {
		host := address
		if h, _, err := net.SplitHostPort(address); err == nil {
			host = h
		}
		ip := net.ParseIP(host)
		if ip == nil || !isLocal(ip) {
			continue
		}
		ips = append(ips, ip)
		named = append(named, ip.String())
	}

	if len(ips) == 0 {
		return nil, ErrNoAddresses
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate certificate key: %w", err)
	}

	serial, err := newSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "fastr on " + named[0],
			Organization: []string{"fastr"},
		},
		NotBefore: now.Add(-time.Hour),
		NotAfter:  now.Add(leafLifetime),

		IPAddresses: ips,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// Not a CA. A leaf that could sign would let anything holding this
		// short-lived key mint certificates of its own.
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, &key.PublicKey, a.key)
	if err != nil {
		return nil, fmt.Errorf("sign certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode certificate key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(filepath.Join(a.Dir, tlsCertFile), certPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write certificate: %w", err)
	}
	if err := os.WriteFile(filepath.Join(a.Dir, tlsKeyFile), keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write certificate key: %w", err)
	}

	return &Certificate{
		Addresses: named,
		NotAfter:  template.NotAfter,
		certPEM:   certPEM,
		keyPEM:    keyPEM,
	}, nil
}

// Current loads the stored certificate, if there is one that still covers the
// given addresses.
//
// Returns nil without an error when there is nothing usable, because "not set
// up yet", "expired", and "this machine moved network" all lead to the same
// place: issue a new one.
func (a *Authority) Current(addresses []string) *Certificate {
	certPEM, err := os.ReadFile(filepath.Join(a.Dir, tlsCertFile)) //nolint:gosec // constructed path
	if err != nil {
		return nil
	}
	keyPEM, err := os.ReadFile(filepath.Join(a.Dir, tlsKeyFile)) //nolint:gosec // constructed path
	if err != nil {
		return nil
	}

	certificate, err := parseCertificate(certPEM)
	if err != nil {
		return nil
	}
	// A margin rather than the exact instant: a certificate that expires in an
	// hour will expire mid-transfer, and reissuing costs milliseconds.
	if time.Now().Add(24 * time.Hour).After(certificate.NotAfter) {
		return nil
	}

	held := make(map[string]bool, len(certificate.IPAddresses))
	for _, ip := range certificate.IPAddresses {
		held[ip.String()] = true
	}
	for _, address := range addresses {
		host := address
		if h, _, err := net.SplitHostPort(address); err == nil {
			host = h
		}
		ip := net.ParseIP(host)
		if ip == nil || !isLocal(ip) {
			continue
		}
		if !held[ip.String()] {
			return nil // this machine has an address the certificate does not cover
		}
	}

	named := make([]string, 0, len(certificate.IPAddresses))
	for _, ip := range certificate.IPAddresses {
		named = append(named, ip.String())
	}

	return &Certificate{
		Addresses: named,
		NotAfter:  certificate.NotAfter,
		certPEM:   certPEM,
		keyPEM:    keyPEM,
	}
}

// isLocal mirrors internal/localnet without importing it, because trust is
// below the network layer and must not depend on it.
func isLocal(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// fingerprintOf renders a SHA-256 the way a phone displays one, in colon-
// separated pairs, so the two can be compared character by character.
func fingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)

	parts := make([]string, 0, len(sum))
	for _, b := range sum {
		parts = append(parts, hex.EncodeToString([]byte{b}))
	}
	return strings.ToUpper(strings.Join(parts, ":"))
}
