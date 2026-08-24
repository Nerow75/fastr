package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/Nerow75/fastr/internal/pairing"
	"github.com/Nerow75/fastr/internal/transfer"
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
		Name          string `json:"name"`
		Code          string `json:"code"`
		HostID        string `json:"host_id"`
		SessionID     string `json:"sid"`
		ClientSecret  string `json:"client_secret"`
		ServerSecret  string `json:"server_secret"`
		HandshakeID   string `json:"handshake_id"`
		ClientMessage string `json:"client_message"`
		ServerMessage string `json:"server_message"`
		Key           string `json:"key"`
		Proof         string `json:"proof"`
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
	Checksums []struct {
		Name   string   `json:"name"`
		Chunks []string `json:"chunks"`
		Digest string   `json:"digest"`
	} `json:"checksums"`
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
	if len(v.Handshakes) == 0 || len(v.Envelopes) == 0 || len(v.Checksums) == 0 {
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
	b64 := base64.StdEncoding.EncodeToString

	for _, v := range vectors.Handshakes {
		t.Run(v.Name, func(t *testing.T) {
			sid := decode(t, v.SessionID)

			clientSecret, err := pairing.SecretFrom(decode(t, v.ClientSecret))
			if err != nil {
				t.Fatalf("client secret: %v", err)
			}
			serverSecret, err := pairing.SecretFrom(decode(t, v.ServerSecret))
			if err != nil {
				t.Fatalf("server secret: %v", err)
			}

			// Recomputed rather than read out of the file, so a change in how a
			// secret reduces into the scalar field is caught here instead of
			// hiding behind a value copied from the fixture.
			clientMessage := pairing.Message(clientSecret, v.Code, v.HostID, sid)
			serverMessage := pairing.Message(serverSecret, v.Code, v.HostID, sid)

			if got := b64(clientMessage); got != v.ClientMessage {
				t.Errorf("client message drifted\n got %s\nwant %s", got, v.ClientMessage)
			}
			if got := b64(serverMessage); got != v.ServerMessage {
				t.Errorf("server message drifted\n got %s\nwant %s", got, v.ServerMessage)
			}

			// Both halves, because the point of the exchange is that they land
			// in the same place from different secrets. Checking only the
			// client's would pass just as well if the host's arithmetic were
			// wrong in a way no test here drove.
			for _, side := range []struct {
				name string
				run  func() ([]byte, []byte, error)
			}{
				{"joining device", func() ([]byte, []byte, error) {
					return pairing.ClientComplete(clientSecret, sid, clientMessage, serverMessage, v.HandshakeID)
				}},
				{"host", func() ([]byte, []byte, error) {
					return pairing.HostComplete(serverSecret, sid, clientMessage, serverMessage, v.HandshakeID)
				}},
			} {
				key, proof, err := side.run()
				if err != nil {
					t.Fatalf("%s: %v", side.name, err)
				}
				if got := b64(key); got != v.Key {
					t.Errorf("%s: session key drifted\n got %s\nwant %s", side.name, got, v.Key)
				}
				if got := b64(proof); got != v.Proof {
					t.Errorf("%s: confirmation proof drifted\n got %s\nwant %s", side.name, got, v.Proof)
				}
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

// The integrity digest, which is what makes a corrupted transfer fail rather
// than succeed quietly (FR-032).
//
// It is checked chunk by chunk because that is how both sides feed it: the
// phone hashes each chunk as it reads it, and the computer hashes each chunk as
// it writes it. A one-shot digest agreeing proves less than this does.
func TestChecksumMatchesCommittedVectors(t *testing.T) {
	vectors := loadVectors(t)

	for _, v := range vectors.Checksums {
		t.Run(v.Name, func(t *testing.T) {
			h, err := transfer.NewHasher()
			if err != nil {
				t.Fatalf("hasher: %v", err)
			}
			for _, chunk := range v.Chunks {
				if _, err := h.Write(decode(t, chunk)); err != nil {
					t.Fatalf("write chunk: %v", err)
				}
			}

			got := base64.StdEncoding.EncodeToString(h.Sum(nil))
			if got != v.Digest {
				t.Errorf("digest = %s, want %s", got, v.Digest)
			}
		})
	}
}
