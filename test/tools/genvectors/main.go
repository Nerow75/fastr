// Command genvectors writes the cross-implementation test vectors.
//
// The Go and TypeScript implementations of the handshake and the envelope must
// agree byte for byte, or pairing fails on a real phone with an authentication
// error and no clue why. The vectors are committed, and both sides verify
// against them, so a drift on either side fails a build rather than a device.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Nerow75/fastr/internal/pairing"
)

type vector struct {
	Name        string `json:"name"`
	ClientPriv  string `json:"client_priv"`
	ClientPub   string `json:"client_pub"`
	ServerPriv  string `json:"server_priv"`
	ServerPub   string `json:"server_pub"`
	Salt        string `json:"salt"`
	Code        string `json:"code"`
	HandshakeID string `json:"handshake_id"`
	Key         string `json:"key"`
	Proof       string `json:"proof"`
}

type envelopeVector struct {
	Name      string `json:"name"`
	Key       string `json:"key"`
	Direction int    `json:"direction"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Version   int    `json:"version"`
	Plaintext string `json:"plaintext"`
	Sealed    string `json:"sealed"`
}

func main() {
	b64 := base64.StdEncoding.EncodeToString

	out := struct {
		Handshakes []vector         `json:"handshakes"`
		Envelopes  []envelopeVector `json:"envelopes"`
	}{}

	// Fixed keys, so the vectors are reproducible.
	cases := []struct{ name, code, id string }{
		{"basic", "482915", "abcdefghijklmnopqrstuv"},
		{"leading zeros in code", "000042", "AAAAAAAAAAAAAAAAAAAAAA"},
		{"non ascii id", "999999", "zz-_09AZ______________"},
	}

	for i, c := range cases {
		clientPriv := fixed(byte(i + 1))
		serverPriv := fixed(byte(i + 100))
		salt := fixed(byte(i + 200))

		clientPub, err := pairing.PublicKey(clientPriv)
		must(err)
		serverPub, err := pairing.PublicKey(serverPriv)
		must(err)

		key, proof, err := pairing.ClientDerive(clientPriv, serverPub, clientPub, salt, c.code, c.id)
		must(err)

		out.Handshakes = append(out.Handshakes, vector{
			Name: c.name, ClientPriv: b64(clientPriv), ClientPub: b64(clientPub),
			ServerPriv: b64(serverPriv), ServerPub: b64(serverPub), Salt: b64(salt),
			Code: c.code, HandshakeID: c.id, Key: b64(key), Proof: b64(proof),
		})
	}

	envCases := []struct {
		name, method, path, plaintext string
		direction                     int
	}{
		{"client to server", "POST", "/api/transfers", `{"a":1}`, 0},
		{"server to client", "GET", "/api/devices", `{"devices":[]}`, 1},
		{"empty payload", "DELETE", "/api/pairings/x", "", 0},
		{"path with unicode", "PATCH", "/api/devices/été", `{"name":"été"}`, 0},
	}

	for i, c := range envCases {
		key := fixed(byte(i + 50))
		env, err := pairing.NewEnvelope(key, pairing.Direction(c.direction)) //nolint:gosec
		must(err)
		sealed, err := env.Seal(c.method, c.path, pairing.ProtocolVersion, []byte(c.plaintext))
		must(err)

		out.Envelopes = append(out.Envelopes, envelopeVector{
			Name: c.name, Key: b64(key), Direction: c.direction,
			Method: c.method, Path: c.path, Version: pairing.ProtocolVersion,
			Plaintext: b64([]byte(c.plaintext)), Sealed: b64(sealed),
		})
	}

	data, err := json.MarshalIndent(out, "", "  ")
	must(err)
	must(os.WriteFile("test/testdata/crypto-vectors.json", append(data, '\n'), 0o644))
	fmt.Println("wrote test/testdata/crypto-vectors.json")
}

func fixed(seed byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = seed + byte(i)*7
	}
	return out
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
