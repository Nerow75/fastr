package pairing

import (
	"crypto/rand"
	"crypto/sha512"
	"errors"
	"fmt"
	"io"

	"github.com/gtank/ristretto255"
)

// CPace, the balanced password-authenticated key exchange selected by the CFRG,
// in the CPACE-RISTRETTO255-SHA512 cipher suite of
// draft-irtf-cfrg-cpace-14.
//
// # Why this replaced the earlier exchange
//
// The first design was an ordinary X25519 agreement with the pairing code
// stirred into the key derivation, and it had two problems that a six-digit
// code makes fatal rather than theoretical:
//
//   - The code was **sent to the host in the clear**, in the body of the
//     confirm request. Simple mode is plain HTTP by construction, so anyone on
//     the network read the six digits off the wire.
//   - Even with the code withheld, the confirmation tag was an **offline
//     oracle**. Anyone who could play the host's part once — a rogue mDNS
//     record, a spoofed address, a captive network — collected a tag they could
//     test against all 10^6 candidate codes at their leisure. The online
//     defences (single use, three-minute expiry, death after five attempts, a
//     growing delay) do not apply to a search run offline.
//
// CPace removes both by construction. The code never leaves the device that
// holds it, and no value in the transcript can be tested against a candidate
// code without a fresh interaction. Every impersonation attempt tests exactly
// one guess and spends one of the five, which is what makes twenty bits of
// entropy an acceptable amount to ask a person to retype.
//
// # The construction
//
// Both sides map the code to a group generator that only they can compute:
//
//	G  = map_to_ristretto255(SHA-512(generator_string(DSI, code, CI, sid)))
//	Ya = ya·G                 (the device asking to be let in)
//	Yb = yb·G                 (the host)
//	K  = ya·Yb = yb·Ya
//
// An attacker who does not know the code picks a different generator, so the
// two sides land on unrelated points and K is unrecoverable. Knowing Ya, Yb and
// a candidate code buys nothing: relating them needs the discrete logarithm
// between the true generator and the candidate one.
//
// The implementation follows the draft byte for byte, and
// TestCPaceMatchesTheDraftVector checks it against the published test vector in
// appendix B.3. That vector is the reason to prefer conformance over a
// construction of our own: it is an external answer this code has to reproduce.
// web/src/crypto/cpace.ts is the other half and checks the same vector.

const (
	// cpaceDSI and cpaceISKDSI are the domain separation strings of the
	// ristretto255 suite, from section 7.3. They are part of the cipher suite
	// rather than of this application: changing them would leave a protocol
	// that is no longer the one the vector tests.
	cpaceDSI    = "CPaceRistretto255"
	cpaceISKDSI = "CPaceRistretto255_ISK"

	// sha512BlockSize is the input block size the zero padding is computed
	// against, in bytes.
	sha512BlockSize = 128

	// ElementSize is the length of an encoded ristretto255 group element.
	ElementSize = 32

	// SessionIDSize is the length of the session identifier the joining device
	// chooses. Sixteen random bytes is what the draft's own vector uses, and it
	// is far past what a collision would need.
	SessionIDSize = 16
)

// Errors the exchange can produce.
var (
	// ErrBadElement covers both halves of scalar_mult_vfy failing: an encoding
	// that is not a valid ristretto255 element, and a product that is the
	// identity. They are one error on purpose — both mean the peer sent
	// something no honest implementation sends, and telling them apart would
	// only help whoever sent it.
	ErrBadElement = errors.New("malformed or degenerate group element")
)

// Secret is an ephemeral CPace scalar, held by one side between the two steps
// of the exchange and discarded after them. Aliased so that callers who only
// pass it along — the handshake registry, the vector generator — do not have to
// name the group library.
type Secret = *ristretto255.Scalar

// prependLen returns the data with its length encoded as LEB128 in front.
//
// LEB128 rather than a fixed width because the draft says so, and the encoding
// has to match for the generator to match. Every length this code passes is
// well under 128 bytes and takes a single byte, but the loop is here so that a
// longer channel identifier later does not silently produce a different
// generator on one side.
func prependLen(data []byte) []byte {
	length := len(data)
	var encoded []byte
	for {
		if length < 128 {
			encoded = append(encoded, byte(length))
		} else {
			encoded = append(encoded, byte(length&0x7f)+0x80)
		}
		length >>= 7
		if length == 0 {
			break
		}
	}
	return append(encoded, data...)
}

// lvCat concatenates each part with its length in front.
func lvCat(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, prependLen(part)...)
	}
	return out
}

// generatorString builds the input the generator is derived from.
//
// The zero padding is not decoration. It makes the domain separator and the
// password fill the hash function's whole first input block, so that the
// compression of the block carrying the password cannot be distinguished from
// the compression of any other by how long it takes. Getting its length wrong
// produces a generator that differs from every other implementation's, which is
// why the draft's vector is checked rather than assumed.
func generatorString(prs, ci, sid []byte) []byte {
	pad := sha512BlockSize - 1 - len(prependLen(prs)) - len(prependLen([]byte(cpaceDSI)))
	if pad < 0 {
		pad = 0
	}
	return lvCat([]byte(cpaceDSI), prs, make([]byte, pad), ci, sid)
}

// CalculateGenerator maps the pairing code to the group element both sides
// raise their ephemeral scalar against.
//
// prs is the pairing code, ci identifies the channel — this application uses
// the host's device identifier, so a proof produced for one computer cannot be
// replayed at another — and sid is the per-exchange identifier.
func CalculateGenerator(prs, ci, sid []byte) *ristretto255.Element {
	sum := sha512.Sum512(generatorString(prs, ci, sid))
	// The one-way map on 64 uniform bytes, which is what the draft's
	// map_to_group_mod_ristretto255 is. Not a hash-to-curve with a domain
	// separation tag: that is a different function and would land on a
	// different point.
	return ristretto255.NewElement().FromUniformBytes(sum[:])
}

// UniformSecretSize is how many bytes a secret scalar is derived from.
const UniformSecretSize = 64

// SecretFrom reduces uniform bytes into an ephemeral secret scalar.
//
// Sixty-four bytes reduced into the scalar field, so the result is
// indistinguishable from uniform rather than merely 252 bits wide. The two
// sides never have to agree on how the other sampled, so this is free to be the
// stronger of the two options the draft allows.
//
// Separate from the sampling so that a vector can pin a scalar without pinning
// a random source, which is the only way the browser's implementation and this
// one can be compared on the same inputs.
func SecretFrom(uniform []byte) (Secret, error) {
	if len(uniform) != UniformSecretSize {
		return nil, fmt.Errorf("secret needs %d uniform bytes, got %d", UniformSecretSize, len(uniform))
	}
	return ristretto255.NewScalar().FromUniformBytes(uniform), nil
}

// RandomScalar samples an ephemeral secret scalar.
func RandomScalar(r io.Reader) (Secret, error) {
	var raw [UniformSecretSize]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return nil, fmt.Errorf("sample scalar: %w", err)
	}
	return SecretFrom(raw[:])
}

// Message returns the group element a side publishes: its secret scalar applied
// to the generator the pairing code maps to.
func Message(secret Secret, code, hostID string, sid []byte) []byte {
	g := CalculateGenerator([]byte(code), []byte(hostID), sid)
	return ristretto255.NewElement().ScalarMult(secret, g).Encode(nil)
}

// ScalarMultVfy multiplies a received group element by a secret scalar,
// refusing anything an honest peer would not have sent.
//
// Both refusals matter. An encoding that is not a canonical ristretto255
// element is rejected by Decode, which is what keeps a crafted point from
// steering the result. A product equal to the identity is rejected here,
// because that is the one outcome that would be the same whatever the scalar
// was, and accepting it would hand a shared secret to somebody who never knew
// the code.
func ScalarMultVfy(secret Secret, encodedPeer []byte) ([]byte, error) {
	peer := ristretto255.NewElement()
	if err := peer.Decode(encodedPeer); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadElement, err)
	}

	product := ristretto255.NewElement().ScalarMult(secret, peer)
	if product.Equal(ristretto255.NewElement().Zero()) == 1 {
		return nil, fmt.Errorf("%w: shared point is the identity", ErrBadElement)
	}
	return product.Encode(nil), nil
}

// ISK derives the intermediate session key both sides end up holding.
//
//	ISK = SHA-512( lv_cat(DSI_ISK, sid, K) || lv_cat(Ya, ADa) || lv_cat(Yb, ADb) )
//
// The order is the initiator's message then the responder's, which is the
// draft's transcript_ir. It is fixed rather than sorted because this exchange
// has a clear initiator: the device asking to be let in speaks first. Binding
// both messages means an attacker who alters either produces a different key on
// the side they altered it towards, and the confirmation then fails.
func ISK(sid, k, ya, adA, yb, adB []byte) []byte {
	input := lvCat([]byte(cpaceISKDSI), sid, k)
	input = append(input, lvCat(ya, adA)...)
	input = append(input, lvCat(yb, adB)...)
	sum := sha512.Sum512(input)
	return sum[:]
}

// NewSessionID produces the identifier the joining device chooses for one
// exchange.
func NewSessionID(r io.Reader) ([]byte, error) {
	sid := make([]byte, SessionIDSize)
	if _, err := io.ReadFull(r, sid); err != nil {
		return nil, fmt.Errorf("generate session identifier: %w", err)
	}
	return sid, nil
}

// ClientBegin performs the joining device's first step: it maps the code to a
// generator and returns the message the host needs, along with the secret to
// finish with.
//
// It exists in Go so that the reference implementation and the browser's are
// the same protocol rather than two readings of one document, and so that the
// cross-implementation vectors can be generated from it.
func ClientBegin(code string, hostID string, sid []byte) (secret Secret, ya []byte, err error) {
	return begin(code, hostID, sid, rand.Reader)
}

func begin(code, hostID string, sid []byte, r io.Reader) (Secret, []byte, error) {
	secret, err := RandomScalar(r)
	if err != nil {
		return nil, nil, err
	}
	return secret, Message(secret, code, hostID, sid), nil
}
