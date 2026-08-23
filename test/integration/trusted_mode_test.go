package integration

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/store"
	"github.com/Nerow75/fastr/internal/trust"
)

// Trusted mode, per FR-047a to FR-047e and constitution Principle V.
//
// The honest summary of what this buys, and it is worth restating because the
// whole feature is a response to a browser limitation rather than a
// preference: outside a secure context there is no service worker, so nothing
// can decrypt a stream while the browser writes it to disk, so a large
// encrypted file could only be received by holding it whole in memory — which
// iOS will not allow. A certificate the device trusts is the only route to that
// context on a LAN address.
//
// Three properties are tested here, and the third is the one most likely to rot
// quietly: content is unreadable on the wire (SC-016), a device set to require
// trusted mode is refused in the clear (FR-047c), and **abandoning setup leaves
// the simple pairing working** (FR-047d).

// SC-016: content in trusted mode is unreadable to anything capturing the
// network, checked by capturing it.
func TestContentIsUnreadableOnTheWireInTrustedMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := trust.Create(dir)
	if err != nil {
		t.Fatalf("create authority: %v", err)
	}
	certificate, err := authority.Issue([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	pair, err := certificate.TLS()
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}

	// A recognisable payload, so "unreadable" is checked against the actual
	// bytes rather than against an assumption about them.
	secret := bytes.Repeat([]byte("SENSITIVE-CONTENT-"), 64)

	// A listener that hands back everything it received, which is what a party
	// capturing traffic on the network would see.
	captured := make(chan []byte, 1)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	// The capture sits below TLS: a raw dial to the same port, reading what the
	// client actually puts on the wire.
	raw := capturingProxy(t, listener.Addr().String(), captured)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
	}()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(authority.CertificatePEM())

	client, err := tls.Dial("tcp", raw, &tls.Config{
		RootCAs:    pool,
		ServerName: "127.0.0.1",
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := client.Write(secret); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = client.Close()

	onTheWire := <-captured
	if len(onTheWire) == 0 {
		t.Fatal("nothing was captured, so nothing was proven")
	}
	if bytes.Contains(onTheWire, []byte("SENSITIVE-CONTENT")) {
		t.Fatal("the content appeared in plaintext on the wire")
	}

	// And the same payload over a plain connection *is* readable, so the test
	// above is measuring the encryption rather than a coincidence of framing.
	plain := make(chan []byte, 1)
	echo := plainListener(t, plain)
	conn, err := dialPlain(echo)
	if err != nil {
		t.Fatalf("dial plain: %v", err)
	}
	if _, err := conn.Write(secret); err != nil {
		t.Fatalf("write plain: %v", err)
	}
	_ = conn.Close()

	if !bytes.Contains(<-plain, []byte("SENSITIVE-CONTENT")) {
		t.Fatal("the control case did not find the content in the clear, so the test proves nothing")
	}
}

// FR-047c: a device the user set to require trusted mode is refused when it
// connects in the clear, with an explanation rather than a bare failure.
func TestADeviceRequiringTrustedModeIsRefusedInTheClear(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	if err := h.store.SetRequireTrusted(phone.ID, true); err != nil {
		t.Fatalf("set require trusted: %v", err)
	}

	// The harness serves plain HTTP, which is exactly the case under test.
	resp := phone.do("GET", "/api/devices", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want 426", resp.StatusCode)
	}

	body := errorBody(t, resp)
	if body["error"] != "trusted_mode_required" {
		t.Errorf("error = %v, want trusted_mode_required", body["error"])
	}
	// FR-038: the message says what to do about it.
	if body["detail_key"] == nil {
		t.Error("the refusal carries no translated explanation")
	}
}

// FR-047e: a transfer never drops out of trusted mode silently.
func TestATransferIsRefusedRatherThanSilentlyDowngraded(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	// The device has been connecting in trusted mode.
	if err := h.store.SetProtection(phone.ID, store.ProtectionTrusted); err != nil {
		t.Fatalf("set protection: %v", err)
	}

	// And now declares over plain HTTP, which is what a phone that lost its
	// certificate or joined another network looks like.
	resp := phone.do("POST", "/api/transfers", map[string]any{
		"target_device_id": h.selfID,
		"items":            []map[string]any{{"name": "would-be-readable.jpg", "size": 16}},
	})
	if resp.StatusCode == http.StatusCreated {
		resp.Body.Close()
		t.Fatal("a transfer silently dropped out of trusted mode")
	}
	if resp.StatusCode != http.StatusUpgradeRequired {
		resp.Body.Close()
		t.Fatalf("status = %d, want 426", resp.StatusCode)
	}
	if body := errorBody(t, resp); body["error"] != "would_downgrade" {
		t.Errorf("error = %v, want would_downgrade", body["error"])
	}

	// Refused, not forbidden: the user may say to go ahead, and then it goes.
	confirmed := phone.declareConfirmed(t, h.selfID, "sent-anyway.jpg", 16)
	if confirmed.State == "" {
		t.Fatal("a confirmed downgrade was still refused")
	}

	stored, err := h.store.Transfer(mustID(t, confirmed.ID))
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	// And it is recorded as what it actually was, so the history cannot claim a
	// protection the transfer did not have.
	if stored.ProtectionMode != store.ProtectionSimple {
		t.Errorf("protection = %q, want simple", stored.ProtectionMode)
	}
}

// A device that requires trusted mode gets the stronger refusal, with nothing
// to confirm: the user already said never in the clear.
func TestRequiringTrustedModeLeavesNothingToConfirm(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	if err := h.store.SetProtection(phone.ID, store.ProtectionTrusted); err != nil {
		t.Fatalf("set protection: %v", err)
	}

	decl := app.Declaration{
		TargetDeviceID:  h.selfID,
		Items:           []app.DeclaredItem{{Name: "never.jpg", Size: 16}},
		AcceptDowngrade: true,
	}
	if _, err := h.transfers.Declare(phone.ID, decl); err != nil {
		t.Fatalf("a confirmed downgrade was refused for a device that allows it: %v", err)
	}

	if err := h.store.SetRequireTrusted(phone.ID, true); err != nil {
		t.Fatalf("set require trusted: %v", err)
	}
	if _, err := h.transfers.Declare(phone.ID, decl); err == nil {
		t.Fatal("a device that requires trusted mode was downgraded by confirmation")
	}
}

// FR-047d: setup can be abandoned at any point, and what already worked keeps
// working. This is the property most likely to rot quietly, because nothing
// visibly breaks when it does — the user simply finds their phone locked out.
func TestAbandoningTrustedSetupLeavesTheSimplePairingWorking(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("still works")
	before := phone.declare(t, h.selfID, "before.bin", uint64(len(payload)))
	phone.uploadOK(t, before.ID, 0, 0, payload)
	phone.completeOK(t, before.ID, 0, digestOf(t, payload))

	// The user starts setting trusted mode up: an authority is created and a
	// certificate is issued, on the computer.
	dir := filepath.Join(t.TempDir(), "trust")
	authority, err := trust.Create(dir)
	if err != nil {
		t.Fatalf("create authority: %v", err)
	}
	if _, err := authority.Issue([]string{"127.0.0.1"}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	// And then walks away. Nothing was installed on the phone, nothing was
	// verified, and the pairing was never touched.
	pairing, err := h.store.Pairing(phone.ID)
	if err != nil {
		t.Fatalf("pairing: %v", err)
	}
	if pairing.Revoked() {
		t.Fatal("abandoning setup revoked the pairing")
	}
	if pairing.ProtectionMode != store.ProtectionSimple {
		t.Errorf("protection = %q, want simple", pairing.ProtectionMode)
	}
	if pairing.RequireTrusted {
		t.Error("abandoning setup left the device requiring trusted mode")
	}

	// The pairing still works, which is the whole requirement.
	after := phone.declare(t, h.selfID, "after.bin", uint64(len(payload)))
	phone.uploadOK(t, after.ID, 0, 0, payload)
	if got := phone.completeOK(t, after.ID, 0, digestOf(t, payload)).State; got != "completed" {
		t.Errorf("state = %q after abandoning setup, want completed", got)
	}
}

// A phone cannot claim to be trusted. The proof of trusted mode is the
// connection it arrived on, and nothing a client says can substitute for it.
func TestAPhoneCannotAssertItIsTrusted(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	resp := phone.do("POST", "/api/trust/verify", map[string]any{"protection": "trusted"})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatal("a plain connection was accepted as trusted")
	}

	pairing, err := h.store.Pairing(phone.ID)
	if err != nil {
		t.Fatalf("pairing: %v", err)
	}
	if pairing.ProtectionMode == store.ProtectionTrusted {
		t.Error("the pairing was recorded as trusted on a plain connection")
	}
}

// declareConfirmed declares a transfer having accepted the downgrade.
func (d *device) declareConfirmed(t *testing.T, target, name string, size uint64) declaredTransfer {
	t.Helper()

	resp := d.do("POST", "/api/transfers", map[string]any{
		"target_device_id": target,
		"items":            []map[string]any{{"name": name, "size": size}},
		"accept_downgrade": true,
	})
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("declare: status %d: %s", resp.StatusCode, raw)
	}

	var out declaredTransfer
	d.open("POST", "/api/transfers", resp, &out)
	return out
}

// capturingProxy forwards a connection and keeps a copy of what the client
// sent, which is what a party on the network would see.
func capturingProxy(t *testing.T, upstream string, captured chan<- []byte) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		//nolint:forbidigo // deliberate: this proxy is the capture the test is about
		out, err := net.Dial("tcp", upstream)
		if err != nil {
			return
		}
		defer out.Close()

		var seen bytes.Buffer
		go func() { _, _ = io.Copy(conn, out) }()
		_, _ = io.Copy(io.MultiWriter(out, &seen), conn)

		captured <- seen.Bytes()
	}()

	return listener.Addr().String()
}

// plainListener is the control case: the same payload, unencrypted.
func plainListener(t *testing.T, captured chan<- []byte) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		var seen bytes.Buffer
		_, _ = io.Copy(&seen, conn)
		captured <- seen.Bytes()
	}()

	return listener.Addr().String()
}

func dialPlain(address string) (net.Conn, error) {
	//nolint:forbidigo // deliberate: this is the control case for a capture test
	return net.Dial("tcp", address)
}
