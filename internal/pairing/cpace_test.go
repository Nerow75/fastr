package pairing

import (
	"bytes"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/gtank/ristretto255"
)

// The published test vector for CPACE-RISTRETTO255-SHA512, from appendix B.3
// of draft-irtf-cfrg-cpace-14.
//
// This is the test that matters most in the package. Everything else here
// checks that the implementation is self-consistent, which a wrong
// implementation can be just as easily as a right one: two sides that agree on
// the same mistake agree. The vector is an answer produced by somebody else,
// from the specification, and reproducing it byte for byte is the only evidence
// available offline that this is CPace rather than something shaped like it.
//
// The intermediate values are checked as well as the final key, because a
// mismatch on the last line alone says nothing about where it went wrong. With
// these, a failure names the step: the length encoding, the padding, the map,
// the multiplication, or the transcript.
const (
	vectorPRS = "Password"
	vectorADa = "ADa"
	vectorADb = "ADb"

	vectorCI  = "6f630b425f726573706f6e6465720b415f696e69746961746f72"
	vectorSID = "7e4b4791d6a8ef019b936c79fb7f2c57"

	// Line for line as the draft prints it, so a transcription slip shows up as
	// a line that is not 56 characters rather than as a mystery.
	vectorGeneratorString = "11435061636552697374726574746f3235350850617373776f726464" +
		"00000000000000000000000000000000000000000000000000000000" +
		"00000000000000000000000000000000000000000000000000000000" +
		"00000000000000000000000000000000000000000000000000000000" +
		"000000000000000000000000000000001a6f630b425f726573706f6e" +
		"6465720b415f696e69746961746f72107e4b4791d6a8ef019b936c79" +
		"fb7f2c57"

	vectorGeneratorHash = "c63a5750e2439c17ccd8213be14fde2f87e1bc637001a97f5929c77b30ea0e08" +
		"afbc75ace5d3d73b2842a79d01488c5fd7ea30d475ee609545af1bfd1ff77c8e"

	vectorGenerator = "a6fc82c3b8968fbb2e06fee81ca858586dea50d248f0c7ca6a18b0902a30b36b"

	vectorScalarA = "da3d23700a9e5699258aef94dc060dfda5ebb61f02a5ea77fad53f4ff0976d08"
	vectorYa      = "d40fb265a7abeaee7939d91a585fe59f7053f982c296ec413c624c669308f87a"

	vectorScalarB = "d2316b454718c35362d83d69df6320f38578ed5984651435e2949762d900b80d"
	vectorYb      = "08bcf6e9777a9c313a3db6daa510f2d398403319c2341bd506a92e672eb7e307"

	vectorK = "e22b1ef7788f661478f3cddd4c600774fc0f41e6b711569190ff88fa0e607e09"

	vectorISK = "4c5469a16b2364c4b944ebc1a79e51d1674ad47db26e8718154f59faebfaa52d" +
		"8346f30aa58377117eb20d527f2cbc5c76381f7fd372e89df8239f87f2e02ed1"
)

func TestCPaceMatchesTheDraftVector(t *testing.T) {
	ci := decodeHex(t, vectorCI)
	sid := decodeHex(t, vectorSID)
	prs := []byte(vectorPRS)

	// The zero padding is stated as 100 bytes in the vector's own inputs.
	// Checked separately from the string it goes into, because an off-by-one
	// here is the single easiest mistake to make and the hardest to see in 172
	// bytes of hex.
	pad := sha512BlockSize - 1 - len(prependLen(prs)) - len(prependLen([]byte(cpaceDSI)))
	if pad != 100 {
		t.Errorf("zero padding is %d bytes, the vector says 100", pad)
	}

	built := generatorString(prs, ci, sid)
	if got := hex.EncodeToString(built); got != vectorGeneratorString {
		t.Errorf("generator string:\n got %s\nwant %s", got, vectorGeneratorString)
	}

	sum := sha512.Sum512(built)
	if got := hex.EncodeToString(sum[:]); got != vectorGeneratorHash {
		t.Errorf("generator string hash:\n got %s\nwant %s", got, vectorGeneratorHash)
	}

	g := CalculateGenerator(prs, ci, sid)
	if got := hex.EncodeToString(g.Encode(nil)); got != vectorGenerator {
		t.Errorf("generator:\n got %s\nwant %s", got, vectorGenerator)
	}

	// The scalars come from the vector rather than from the sampler: the two
	// sides never agree on how the other sampled, so sampling is not part of
	// what the vector pins.
	ya, yaEncoded := multiply(t, vectorScalarA, g)
	if yaEncoded != vectorYa {
		t.Errorf("Ya:\n got %s\nwant %s", yaEncoded, vectorYa)
	}

	yb, ybEncoded := multiply(t, vectorScalarB, g)
	if ybEncoded != vectorYb {
		t.Errorf("Yb:\n got %s\nwant %s", ybEncoded, vectorYb)
	}

	// Both directions, because the whole point of the exchange is that the two
	// sides reach the same point from different secrets.
	fromA, err := ScalarMultVfy(ya, decodeHex(t, vectorYb))
	if err != nil {
		t.Fatalf("scalar_mult_vfy(ya, Yb): %v", err)
	}
	if got := hex.EncodeToString(fromA); got != vectorK {
		t.Errorf("K from the initiator:\n got %s\nwant %s", got, vectorK)
	}

	fromB, err := ScalarMultVfy(yb, decodeHex(t, vectorYa))
	if err != nil {
		t.Fatalf("scalar_mult_vfy(yb, Ya): %v", err)
	}
	if got := hex.EncodeToString(fromB); got != vectorK {
		t.Errorf("K from the responder:\n got %s\nwant %s", got, vectorK)
	}

	isk := ISK(sid, decodeHex(t, vectorK),
		decodeHex(t, vectorYa), []byte(vectorADa),
		decodeHex(t, vectorYb), []byte(vectorADb))
	if got := hex.EncodeToString(isk); got != vectorISK {
		t.Errorf("ISK:\n got %s\nwant %s", got, vectorISK)
	}
}

// prepend_len is LEB128, so a length of 128 or more takes two bytes. No value
// this application passes is that long today, and that is exactly why this is
// worth pinning: the first person to widen the channel identifier past 127
// bytes would otherwise find out from a phone that will not pair.
func TestLengthPrefixCrossesTheSingleByteBoundary(t *testing.T) {
	for _, tc := range []struct {
		length int
		prefix string
	}{
		{0, "00"},
		{1, "01"},
		{127, "7f"},
		{128, "8001"},
		{300, "ac02"},
	} {
		got := hex.EncodeToString(prependLen(make([]byte, tc.length))[:len(tc.prefix)/2])
		if got != tc.prefix {
			t.Errorf("length %d encodes as %s, want %s", tc.length, got, tc.prefix)
		}
	}
}

// A code the two sides do not share must not produce a shared key. This is the
// property the whole exchange exists for, so it is asserted rather than assumed
// from the construction.
func TestADifferentCodeReachesADifferentKey(t *testing.T) {
	sid, err := NewSessionID(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	honest, ya, err := ClientBegin("482915", "host-1", sid)
	if err != nil {
		t.Fatal(err)
	}

	// The host holds a different code: somebody mistyped, or is guessing.
	g := CalculateGenerator([]byte("482916"), []byte("host-1"), sid)
	yb, err := RandomScalar(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ybEncoded := ristretto255.NewElement().ScalarMult(yb, g).Encode(nil)

	fromClient, err := ScalarMultVfy(honest, ybEncoded)
	if err != nil {
		t.Fatal(err)
	}
	fromHost, err := ScalarMultVfy(yb, ya)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(fromClient, fromHost) {
		t.Fatal("two different codes agreed on a shared secret")
	}
}

// The same code must reach the same key, or nobody can ever pair.
func TestTheSameCodeReachesTheSameKey(t *testing.T) {
	sid, err := NewSessionID(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	client, ya, err := ClientBegin("000042", "host-1", sid)
	if err != nil {
		t.Fatal(err)
	}
	host, yb, err := ClientBegin("000042", "host-1", sid)
	if err != nil {
		t.Fatal(err)
	}

	fromClient, err := ScalarMultVfy(client, yb)
	if err != nil {
		t.Fatal(err)
	}
	fromHost, err := ScalarMultVfy(host, ya)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(fromClient, fromHost) {
		t.Fatal("the same code reached two different secrets")
	}

	if !bytes.Equal(ISK(sid, fromClient, ya, nil, yb, nil), ISK(sid, fromHost, ya, nil, yb, nil)) {
		t.Fatal("the same secret reached two different session keys")
	}
}

// The channel identifier binds the exchange to one computer. Without it, a
// proof produced for the machine in front of you would be replayable at any
// other machine showing the same digits.
func TestTheChannelIdentifierSeparatesHosts(t *testing.T) {
	sid := bytes.Repeat([]byte{7}, SessionIDSize)

	here := CalculateGenerator([]byte("482915"), []byte("host-1"), sid)
	there := CalculateGenerator([]byte("482915"), []byte("host-2"), sid)

	if here.Equal(there) == 1 {
		t.Fatal("two different hosts derived the same generator from one code")
	}
}

// scalar_mult_vfy has to refuse what an honest peer never sends. The identity
// element is the dangerous one: it is a valid encoding, and multiplying it by
// any scalar yields the identity, so accepting it would let somebody who never
// knew the code fix the shared secret to a value they know.
func TestDegenerateElementsAreRefused(t *testing.T) {
	secret, err := RandomScalar(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	identity := ristretto255.NewElement().Zero().Encode(nil)
	if _, err := ScalarMultVfy(secret, identity); !errors.Is(err, ErrBadElement) {
		t.Errorf("the identity element was accepted: %v", err)
	}

	// Not a canonical encoding of anything.
	if _, err := ScalarMultVfy(secret, bytes.Repeat([]byte{0xff}, ElementSize)); !errors.Is(err, ErrBadElement) {
		t.Errorf("a malformed element was accepted: %v", err)
	}

	if _, err := ScalarMultVfy(secret, []byte{1, 2, 3}); !errors.Is(err, ErrBadElement) {
		t.Errorf("a short element was accepted: %v", err)
	}
}

func multiply(t *testing.T, scalarHex string, g *ristretto255.Element) (*ristretto255.Scalar, string) {
	t.Helper()

	s := ristretto255.NewScalar()
	if err := s.Decode(decodeHex(t, scalarHex)); err != nil {
		t.Fatalf("decode scalar: %v", err)
	}
	return s, hex.EncodeToString(ristretto255.NewElement().ScalarMult(s, g).Encode(nil))
}

func decodeHex(t *testing.T, s string) []byte {
	t.Helper()

	raw, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return raw
}
