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
	"github.com/Nerow75/fastr/internal/transfer"
)

// vector pins one CPace exchange from fixed inputs.
//
// Both scalars are given as the uniform bytes they reduce from rather than as
// scalars, because that is the input each implementation actually takes, and
// reducing them differently is exactly the kind of disagreement this file
// exists to catch.
type vector struct {
	Name         string `json:"name"`
	Code         string `json:"code"`
	HostID       string `json:"host_id"`
	SessionID    string `json:"sid"`
	ClientSecret string `json:"client_secret"`
	ServerSecret string `json:"server_secret"`
	HandshakeID  string `json:"handshake_id"`

	ClientMessage string `json:"client_message"`
	ServerMessage string `json:"server_message"`
	Key           string `json:"key"`
	Proof         string `json:"proof"`
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

// checksumVector pins the integrity digest across the two implementations.
//
// The phone computes it while reading the file and the computer computes it
// while writing, and a mismatch fails the transfer (FR-032). If the two
// implementations disagreed, every upload from a real phone would fail its
// verification with nothing to point at. Chunks are fed in order, because that
// is how web/src/lib/upload.ts feeds it: the incremental digest must equal the
// one-shot digest of the same bytes.
type checksumVector struct {
	Name   string   `json:"name"`
	Chunks []string `json:"chunks"`
	Digest string   `json:"digest"`
}

func main() {
	b64 := base64.StdEncoding.EncodeToString

	out := struct {
		Handshakes []vector         `json:"handshakes"`
		Envelopes  []envelopeVector `json:"envelopes"`
		Checksums  []checksumVector `json:"checksums"`
	}{}

	// Fixed inputs, so the vectors are reproducible.
	cases := []struct{ name, code, hostID, id string }{
		{"basic", "482915", "host-abcdef", "abcdefghijklmnopqrstuv"},
		{"leading zeros in code", "000042", "host-abcdef", "AAAAAAAAAAAAAAAAAAAAAA"},
		{"non ascii identifiers", "999999", "hôte-éé", "zz-_09AZ______________"},
	}

	for i, c := range cases {
		clientUniform := fixed(byte(i+1), pairing.UniformSecretSize)
		serverUniform := fixed(byte(i+100), pairing.UniformSecretSize)
		sid := fixed(byte(i+200), pairing.SessionIDSize)

		clientSecret, err := pairing.SecretFrom(clientUniform)
		must(err)
		serverSecret, err := pairing.SecretFrom(serverUniform)
		must(err)

		clientMessage := pairing.Message(clientSecret, c.code, c.hostID, sid)
		serverMessage := pairing.Message(serverSecret, c.code, c.hostID, sid)

		key, proof, err := pairing.ClientComplete(clientSecret, sid, clientMessage, serverMessage, c.id)
		must(err)

		// The host has to land on the same two values from the other secret, or
		// the vector would pin one side's arithmetic rather than the agreement.
		hostKey, hostProof, err := pairing.HostComplete(serverSecret, sid, clientMessage, serverMessage, c.id)
		must(err)
		if b64(hostKey) != b64(key) || b64(hostProof) != b64(proof) {
			fmt.Fprintln(os.Stderr, "the two sides of the exchange disagree")
			os.Exit(1)
		}

		out.Handshakes = append(out.Handshakes, vector{
			Name: c.name, Code: c.code, HostID: c.hostID, SessionID: b64(sid),
			ClientSecret: b64(clientUniform), ServerSecret: b64(serverUniform),
			HandshakeID:   c.id,
			ClientMessage: b64(clientMessage), ServerMessage: b64(serverMessage),
			Key: b64(key), Proof: b64(proof),
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
		key := fixed(byte(i+50), 32)
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

	checksumCases := []struct {
		name   string
		chunks [][]byte
	}{
		{"empty file", [][]byte{}},
		{"one short chunk", [][]byte{[]byte("hello fastr")}},
		{"several chunks", [][]byte{[]byte("one"), []byte("two"), []byte("three")}},
		// A chunk of exactly one BLAKE2b block, so an off-by-one in either
		// implementation's buffering shows up rather than hiding.
		{"block boundary", [][]byte{ramp(128), ramp(128)}},
		{"every byte value", [][]byte{ramp(256)}},
		// What a resumed upload does: a prefix rehashed, then the rest.
		{"uneven chunks", [][]byte{ramp(1), ramp(200), ramp(31), ramp(4096)}},
	}

	for _, c := range checksumCases {
		h, err := transfer.NewHasher()
		must(err)

		encoded := make([]string, 0, len(c.chunks))
		for _, chunk := range c.chunks {
			_, err := h.Write(chunk)
			must(err)
			encoded = append(encoded, b64(chunk))
		}

		out.Checksums = append(out.Checksums, checksumVector{
			Name: c.name, Chunks: encoded, Digest: b64(h.Sum(nil)),
		})
	}

	data, err := json.MarshalIndent(out, "", "  ")
	must(err)
	// A fixture that lives in the repository and is read by both test suites.
	must(os.WriteFile("test/testdata/crypto-vectors.json", append(data, '\n'), 0o644)) //nolint:gosec // committed test fixture
	fmt.Println("wrote test/testdata/crypto-vectors.json")
}

// ramp returns n bytes cycling through every value, so a vector exercises the
// whole byte range rather than printable ASCII.
func ramp(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i)
	}
	return out
}

func fixed(seed byte, n int) []byte {
	out := make([]byte, n)
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
