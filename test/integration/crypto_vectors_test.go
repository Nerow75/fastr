package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/Nerow75/fastr/internal/pairing"
)

// The Go and TypeScript implementations of the handshake and the envelope must
// agree byte for byte. When they do not, pairing fails on a real phone with an
// authentication error and no indication of which side is wrong.
//
// Both sides check the same committed vectors: this test, and
// web/scripts/verify-crypto.ts via `make test-crypto`. A drift on either side
// fails a build rather than a device.
//
// Regenerate the vectors, deliberately, with `go run ./test/tools/genvectors`.
// A change to that file in a diff means the wire protocol changed, which is
// exactly the kind of thing a reviewer should be forced to notice.

type cryptoVectors struct {
	Handshakes []struct {
		Name        string `json:"name"`
		ClientPriv  string `json:"client_priv"`
		ClientPub   string `json:"client_pub"`
		ServerPub   string `json:"server_pub"`
		Salt        string `json:"salt"`
		Code        string `json:"code"`
		HandshakeID string `json:"handshake_id"`
		Key         string `json:"key"`
		Proof       string `json:"proof"`
	} `json:"handshakes"`
	Envelopes []struct {
		Name      string `json:"name"`
		Key       string `json:"key"`
		Direction int    `json:"direction"`
		Method    string `json:"method"`
		Path      string `json:"path"`
		Version   int    `json:"version"`
		Plaintext string `json:"plaintext"`
		Sealed    string `json:"sealed"`
	} `json:"envelopes"`
}

func loadVectors(t *testing.T) cryptoVectors {
	t.Helper()

	raw, err := os.ReadFile("../testdata/crypto-vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v cryptoVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(v.Handshakes) == 0 || len(v.Envelopes) == 0 {
		t.Fatal("the vector file is empty")
	}
	return v
}

func decode(t *testing.T, s string) []byte {
	t.Helper()
	out, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return out
}

func TestHandshakeMatchesCommittedVectors(t *testing.T) {
	vectors := loadVectors(t)

	for _, v := range vectors.Handshakes {
		t.Run(v.Name, func(t *testing.T) {
			key, proof, err := pairing.ClientDerive(
				decode(t, v.ClientPriv), decode(t, v.ServerPub), decode(t, v.ClientPub),
				decode(t, v.Salt), v.Code, v.HandshakeID,
			)
			if err != nil {
				t.Fatalf("ClientDerive: %v", err)
			}

			if got := base64.StdEncoding.EncodeToString(key); got != v.Key {
				t.Errorf("session key drifted\n got %s\nwant %s", got, v.Key)
			}
			if got := base64.StdEncoding.EncodeToString(proof); got != v.Proof {
				t.Errorf("confirmation proof drifted\n got %s\nwant %s", got, v.Proof)
			}
		})
	}
}

func TestEnvelopeMatchesCommittedVectors(t *testing.T) {
	vectors := loadVectors(t)

	for _, v := range vectors.Envelopes {
		t.Run(v.Name, func(t *testing.T) {
			key := decode(t, v.Key)
			plaintext := decode(t, v.Plaintext)

			// Sealing is deterministic for the first message, because the
			// counter starts at zero and the nonce is derived from it.
			sealer, err := pairing.NewEnvelope(key, pairing.Direction(v.Direction)) //nolint:gosec // fixture value
			if err != nil {
				t.Fatalf("NewEnvelope: %v", err)
			}
			sealed, err := sealer.Seal(v.Method, v.Path, v.Version, plaintext)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if got := base64.StdEncoding.EncodeToString(sealed); got != v.Sealed {
				t.Errorf("sealed bytes drifted\n got %s\nwant %s", got, v.Sealed)
			}

			// And the recorded bytes must open on the other side.
			other := pairing.ServerToClient
			if v.Direction == int(pairing.ServerToClient) {
				other = pairing.ClientToServer
			}
			opener, err := pairing.NewEnvelope(key, other)
			if err != nil {
				t.Fatalf("NewEnvelope: %v", err)
			}
			opened, err := opener.Open(v.Method, v.Path, v.Version, decode(t, v.Sealed))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(opened, plaintext) {
				t.Errorf("opened %q, want %q", opened, plaintext)
			}
		})
	}
}
