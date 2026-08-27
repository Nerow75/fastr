package trust

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The local certificate authority, per FR-047a.
//
// Installing a CA on a phone is a real security decision: anything holding its
// private key can impersonate any site to that device. So most of what is
// tested here is about containment — that the key never leaves this machine,
// that the file it lives in is not readable by anyone else, and that the
// certificate it signs cannot be used for anything but this.

func TestAnAuthorityIsCreatedOnceAndReused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")

	if _, err := Load(dir); !errors.Is(err, ErrNoAuthority) {
		t.Fatalf("Load on a fresh directory = %v, want ErrNoAuthority", err)
	}

	first, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Creating twice is refused rather than silently replacing: a new authority
	// invalidates every phone already set up, and the user would find out when
	// a transfer stopped rather than when they asked for anything.
	if _, err := Create(dir); err == nil {
		t.Error("a second authority was created over the first")
	}

	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if second.Fingerprint() != first.Fingerprint() {
		t.Error("loading produced a different authority than creating did")
	}
}

// The key is the whole of the trust a user extends when they install the
// certificate. It is written where only this user can read it.
//
// **Windows asserts the same property somewhere else, because it cannot assert
// it here.** It does not implement POSIX permission bits: a file written with
// 0600 reports 0666, because access there is decided by a list rather than by a
// mode. The equivalent check reads that list, in
// `restrict_windows_test.go`.
func TestTheAuthorityKeyIsNotReadableByAnyoneElse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows decides access by a list, not by permission bits; see restrict_windows_test.go")
	}

	dir := filepath.Join(t.TempDir(), "trust")
	if _, err := Create(dir); err != nil {
		t.Fatalf("create: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, caKeyFile))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("key mode = %04o, want no group or other access", mode)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("directory mode = %04o, want no group or other access", mode)
	}
}

// What the user installs is the certificate, and only the certificate.
func TestOnlyTheCertificateIsHandedOut(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	pemBytes := authority.CertificatePEM()
	text := string(pemBytes)

	if !strings.Contains(text, "BEGIN CERTIFICATE") {
		t.Fatal("what is handed out is not a certificate")
	}
	if strings.Contains(text, "PRIVATE KEY") {
		t.Fatal("the private key was handed out with the certificate")
	}

	// And it is a copy: a caller that scribbles on it must not corrupt what the
	// next caller is given.
	pemBytes[0] = 'x'
	if authority.CertificatePEM()[0] == 'x' {
		t.Error("the certificate is handed out by reference")
	}
}

// A fingerprint the user can compare against what their phone shows. Without
// one, "install this certificate" is a request to trust whatever arrived.
func TestTheFingerprintIsComparableWithWhatAPhoneShows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	fingerprint := authority.Fingerprint()

	// 32 bytes of SHA-256, rendered as colon-separated uppercase pairs, which
	// is the form every phone displays.
	parts := strings.Split(fingerprint, ":")
	if len(parts) != 32 {
		t.Fatalf("fingerprint has %d parts, want 32: %q", len(parts), fingerprint)
	}
	for _, part := range parts {
		if len(part) != 2 || part != strings.ToUpper(part) {
			t.Fatalf("fingerprint part %q is not an uppercase byte", part)
		}
	}
}

// The authority signs certificates and nothing else.
func TestTheAuthorityCanOnlySignCertificates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if !authority.certificate.IsCA {
		t.Error("the authority is not marked as one")
	}
	if authority.certificate.KeyUsage&x509.KeyUsageKeyAgreement != 0 {
		t.Error("the authority may perform key agreement, which it was not installed for")
	}
	if authority.certificate.MaxPathLen != 0 {
		t.Error("the authority may sign intermediate authorities")
	}
	if len(authority.certificate.ExtKeyUsage) != 0 {
		t.Errorf("the authority carries extended usages: %v", authority.certificate.ExtKeyUsage)
	}
}

// The certificate names addresses, because a phone reaches this machine at an
// address: resolving a name is a request off the local network.
func TestACertificateNamesTheAddressesAPhoneWillUse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	issued, err := authority.Issue([]string{"192.168.1.20:7420", "10.0.0.5"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	pair, err := issued.TLS()
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	named := map[string]bool{}
	for _, ip := range leaf.IPAddresses {
		named[ip.String()] = true
	}
	for _, want := range []string{"192.168.1.20", "10.0.0.5"} {
		if !named[want] {
			t.Errorf("the certificate does not cover %s", want)
		}
	}
	if len(leaf.DNSNames) != 0 {
		t.Errorf("the certificate names hosts: %v", leaf.DNSNames)
	}

	// And a browser that trusts the authority accepts it for that address.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(authority.CertificatePEM()) {
		t.Fatal("the authority certificate could not be added to a pool")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("a phone trusting the authority would refuse this certificate: %v", err)
	}
	if err := leaf.VerifyHostname("192.168.1.20"); err != nil {
		t.Errorf("the certificate is not valid for the address it names: %v", err)
	}
}

// An address that is not local has no business in a certificate this machine
// serves. One naming a public address could impersonate something on the
// internet to every phone that installed the authority.
func TestACertificateNeverCoversAnAddressOffTheLocalNetwork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	issued, err := authority.Issue([]string{"192.168.1.20", "8.8.8.8", "93.184.216.34"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	for _, address := range issued.Addresses {
		if ip := net.ParseIP(address); ip == nil || !isLocal(ip) {
			t.Errorf("the certificate covers %s, which is not on the local network", address)
		}
	}

	// And with nothing local at all, it refuses rather than issuing something
	// meaningless.
	if _, err := authority.Issue([]string{"8.8.8.8"}); !errors.Is(err, ErrNoAddresses) {
		t.Errorf("Issue with no local address = %v, want ErrNoAddresses", err)
	}
}

// Reissuing is the ordinary case, not a fallback: a DHCP lease moves, a laptop
// joins another network, a cable becomes Wi-Fi.
func TestAStoredCertificateIsReusedUntilTheAddressesChange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if authority.Current([]string{"192.168.1.20"}) != nil {
		t.Fatal("a certificate was found before one was issued")
	}

	if _, err := authority.Issue([]string{"192.168.1.20"}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	if authority.Current([]string{"192.168.1.20"}) == nil {
		t.Error("the stored certificate was not reused for the same address")
	}
	// The machine moved network: the stored certificate does not cover where it
	// now answers, so it is not offered.
	if authority.Current([]string{"192.168.1.20", "10.0.0.9"}) != nil {
		t.Error("a certificate was reused for an address it does not cover")
	}
}

// Leaves are short-lived on purpose: they name addresses a DHCP lease can move,
// and a certificate for an address this machine no longer holds is worse than
// useless. A long-lived one would paper over that.
func TestLeafCertificatesAreShortLived(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	issued, err := authority.Issue([]string{"192.168.1.20"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	remaining := time.Until(issued.NotAfter)
	if remaining > leafLifetime+time.Hour {
		t.Errorf("the certificate lives for %s, longer than the leaf lifetime", remaining)
	}
	// And comfortably outside the reissue margin, or every start would mint a
	// new one and the stored certificate would never be reused at all.
	if remaining < 48*time.Hour {
		t.Errorf("the certificate lives for %s, inside the reissue margin", remaining)
	}

	// The authority itself outlives it by a long way, because replacing that
	// one means visiting every phone again.
	if !authority.NotAfter().After(issued.NotAfter.Add(365 * 24 * time.Hour)) {
		t.Error("the authority does not outlive the certificates it signs")
	}
}

// The certificate actually works in a TLS handshake, which is the only thing
// any of this is for.
func TestTheIssuedCertificateServesTLS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	issued, err := authority.Issue([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	pair, err := issued.TLS()
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("hello"))
		_ = conn.Close()
	}()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(authority.CertificatePEM())

	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		RootCAs:    pool,
		ServerName: "127.0.0.1",
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("a client trusting the authority could not connect: %v", err)
	}
	defer conn.Close()

	buf := make([]byte, 5)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "hello" {
		t.Errorf("read %q over TLS", buf)
	}
}

// A client that has not installed the authority refuses, which is the whole
// reason the installation step exists.
func TestAClientWithoutTheAuthorityRefusesTheCertificate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	issued, err := authority.Issue([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	pair, err := issued.TLS()
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	go func() {
		if conn, err := listener.Accept(); err == nil {
			_ = conn.Close()
		}
	}()

	// An empty pool: a phone that never installed anything.
	_, err = tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		RootCAs:    x509.NewCertPool(),
		ServerName: "127.0.0.1",
		MinVersion: tls.VersionTLS12,
	})
	if err == nil {
		t.Fatal("a client with no trusted authority accepted the certificate")
	}
}
