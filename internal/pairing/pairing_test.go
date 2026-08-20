package pairing

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// The handshake and the envelope are the whole of the security story in simple
// mode, where the wire is plain HTTP on a shared network. Everything below
// assumes an observer reads every byte and can replay anything they saw.

func TestHandshakeRoundTripDerivesTheSameKey(t *testing.T) {
	hs := NewHandshakes()
	const code = "482915"

	clientPriv, clientPub, err := GenerateClientKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}

	h, err := hs.Begin(clientPub)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	clientKey, proof, err := ClientDerive(clientPriv, h.ServerPublicKey(), clientPub, h.Salt, code, h.ID)
	if err != nil {
		t.Fatalf("ClientDerive: %v", err)
	}

	serverKey, err := hs.Complete(h.ID, code, proof)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if !bytes.Equal(clientKey, serverKey) {
		t.Fatal("the two sides derived different keys")
	}
	if len(serverKey) != keySize {
		t.Errorf("key is %d bytes, want %d", len(serverKey), keySize)
	}
	// A key of all zeros would mean the agreement silently failed.
	if bytes.Equal(serverKey, make([]byte, keySize)) {
		t.Error("derived key is all zeros")
	}
}

// This is the property the whole design rests on: an observer who captured the
// public keys, the salt, and the handshake identifier still cannot produce a
// valid confirmation without the code.
func TestWrongCodeCannotConfirm(t *testing.T) {
	hs := NewHandshakes()

	clientPriv, clientPub, _ := GenerateClientKeypair()
	h, err := hs.Begin(clientPub)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// The observer knows everything on the wire, and guesses the code.
	_, proof, err := ClientDerive(clientPriv, h.ServerPublicKey(), clientPub, h.Salt, "000000", h.ID)
	if err != nil {
		t.Fatalf("ClientDerive: %v", err)
	}

	if _, err := hs.Complete(h.ID, "482915", proof); !errors.Is(err, ErrBadProof) {
		t.Errorf("Complete with a guessed code = %v, want ErrBadProof", err)
	}
}

// A handshake is consumed whether or not the proof verifies, so a wrong guess
// does not get a second try on the same ephemeral keys.
func TestHandshakeIsSingleUse(t *testing.T) {
	hs := NewHandshakes()
	const code = "482915"

	clientPriv, clientPub, _ := GenerateClientKeypair()
	h, _ := hs.Begin(clientPub)
	_, proof, _ := ClientDerive(clientPriv, h.ServerPublicKey(), clientPub, h.Salt, code, h.ID)

	if _, err := hs.Complete(h.ID, code, proof); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if _, err := hs.Complete(h.ID, code, proof); !errors.Is(err, ErrUnknownHandshake) {
		t.Errorf("replayed Complete = %v, want ErrUnknownHandshake", err)
	}

	// And a failed attempt burns it too.
	h2, _ := hs.Begin(clientPub)
	_, _ = hs.Complete(h2.ID, code, []byte("wrong"))
	if _, err := hs.Complete(h2.ID, code, proof); !errors.Is(err, ErrUnknownHandshake) {
		t.Errorf("retry after a bad proof = %v, want ErrUnknownHandshake", err)
	}
}

func TestHandshakeExpires(t *testing.T) {
	hs := NewHandshakes()
	base := time.Now()
	hs.SetClock(func() time.Time { return base })

	clientPriv, clientPub, _ := GenerateClientKeypair()
	h, _ := hs.Begin(clientPub)
	_, proof, _ := ClientDerive(clientPriv, h.ServerPublicKey(), clientPub, h.Salt, "482915", h.ID)

	hs.SetClock(func() time.Time { return base.Add(handshakeTTL + time.Second) })
	if _, err := hs.Complete(h.ID, "482915", proof); !errors.Is(err, ErrHandshakeExpired) {
		t.Errorf("Complete after the deadline = %v, want ErrHandshakeExpired", err)
	}
}

// Abandoned handshakes must not accumulate in memory.
func TestAbandonedHandshakesAreSwept(t *testing.T) {
	hs := NewHandshakes()
	base := time.Now()
	hs.SetClock(func() time.Time { return base })

	_, clientPub, _ := GenerateClientKeypair()
	for i := 0; i < 5; i++ {
		if _, err := hs.Begin(clientPub); err != nil {
			t.Fatalf("Begin: %v", err)
		}
	}
	if got := hs.Open(); got != 5 {
		t.Fatalf("open = %d, want 5", got)
	}

	hs.SetClock(func() time.Time { return base.Add(handshakeTTL + time.Second) })
	if _, err := hs.Begin(clientPub); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if got := hs.Open(); got != 1 {
		t.Errorf("open after sweep = %d, want 1", got)
	}
}

func TestHandshakeRejectsMalformedPublicKey(t *testing.T) {
	hs := NewHandshakes()
	for _, bad := range [][]byte{nil, {}, make([]byte, 31), make([]byte, 33)} {
		if _, err := hs.Begin(bad); !errors.Is(err, ErrBadPublicKey) {
			t.Errorf("Begin(%d bytes) = %v, want ErrBadPublicKey", len(bad), err)
		}
	}
}

// --- envelope ----------------------------------------------------------------

func newPair(t *testing.T) (client, server *Envelope) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	client, err := NewEnvelope(key, ClientToServer)
	if err != nil {
		t.Fatalf("client envelope: %v", err)
	}
	server, err = NewEnvelope(key, ServerToClient)
	if err != nil {
		t.Fatalf("server envelope: %v", err)
	}
	return client, server
}

func TestEnvelopeRoundTrip(t *testing.T) {
	client, server := newPair(t)
	payload := []byte(`{"device":"phone"}`)

	sealed, err := client.Seal("POST", "/api/transfers", ProtocolVersion, payload)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, payload) {
		t.Fatal("the payload appears in the clear inside the envelope")
	}

	got, err := server.Open("POST", "/api/transfers", ProtocolVersion, sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

func TestEnvelopeRejectsReplay(t *testing.T) {
	client, server := newPair(t)

	sealed, _ := client.Seal("POST", "/api/pair/confirm", ProtocolVersion, []byte("once"))
	if _, err := server.Open("POST", "/api/pair/confirm", ProtocolVersion, sealed); err != nil {
		t.Fatalf("first Open: %v", err)
	}

	// The same bytes, sent again. This is the attack the counter exists for.
	if _, err := server.Open("POST", "/api/pair/confirm", ProtocolVersion, sealed); !errors.Is(err, ErrReplay) {
		t.Errorf("replay = %v, want ErrReplay", err)
	}
}

func TestEnvelopeRejectsOutOfOrder(t *testing.T) {
	client, server := newPair(t)

	first, _ := client.Seal("GET", "/api/devices", ProtocolVersion, []byte("1"))
	second, _ := client.Seal("GET", "/api/devices", ProtocolVersion, []byte("2"))

	if _, err := server.Open("GET", "/api/devices", ProtocolVersion, second); err != nil {
		t.Fatalf("Open second: %v", err)
	}
	// Delivering the earlier message afterwards is either a replay or a
	// reorder. Neither is something a local request should produce.
	if _, err := server.Open("GET", "/api/devices", ProtocolVersion, first); !errors.Is(err, ErrReplay) {
		t.Errorf("out-of-order = %v, want ErrReplay", err)
	}
}

// The associated data binds a message to its endpoint. Without it, an envelope
// captured from a harmless path could be replayed against a dangerous one.
func TestEnvelopeIsBoundToMethodPathAndVersion(t *testing.T) {
	cases := []struct {
		name         string
		method, path string
		version      int
	}{
		{"different path", "POST", "/api/pairings/other/revoke", ProtocolVersion},
		{"different method", "DELETE", "/api/devices", ProtocolVersion},
		{"different version", "POST", "/api/devices", ProtocolVersion + 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := newPair(t)
			sealed, _ := client.Seal("POST", "/api/devices", ProtocolVersion, []byte("payload"))

			if _, err := server.Open(tc.method, tc.path, tc.version, sealed); !errors.Is(err, ErrAuthFailed) {
				t.Errorf("Open with %s = %v, want ErrAuthFailed", tc.name, err)
			}
		})
	}
}

// Each direction has its own counter space, so a message the server sent
// cannot be turned around and replayed at the server.
func TestEnvelopeDirectionsDoNotShareCounters(t *testing.T) {
	client, server := newPair(t)

	fromServer, err := server.Seal("GET", "/api/events", ProtocolVersion, []byte("event"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Reflecting it back at the server must not authenticate.
	if _, err := server.Open("GET", "/api/events", ProtocolVersion, fromServer); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("reflected message = %v, want ErrAuthFailed", err)
	}
	// The client, which reads the other direction, accepts it.
	if _, err := client.Open("GET", "/api/events", ProtocolVersion, fromServer); err != nil {
		t.Errorf("client could not read a server message: %v", err)
	}
}

// A failed open must not move the high-water mark, or anyone on the network
// could wedge a session by sending garbage with a huge counter.
func TestFailedOpenDoesNotAdvanceTheCounter(t *testing.T) {
	client, server := newPair(t)

	legit, _ := client.Seal("GET", "/api/devices", ProtocolVersion, []byte("real"))

	// Garbage carrying a counter far ahead of anything legitimate.
	forged := make([]byte, len(legit))
	copy(forged, legit)
	forged[0], forged[1] = 0xff, 0xff
	for i := counterSize; i < len(forged); i++ {
		forged[i] ^= 0xff
	}
	if _, err := server.Open("GET", "/api/devices", ProtocolVersion, forged); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("forged envelope = %v, want ErrAuthFailed", err)
	}

	// The real message, sent afterwards, must still be accepted.
	if _, err := server.Open("GET", "/api/devices", ProtocolVersion, legit); err != nil {
		t.Errorf("legitimate message refused after a forgery: %v", err)
	}
}

func TestEnvelopeRejectsMalformedInput(t *testing.T) {
	_, server := newPair(t)
	for _, bad := range [][]byte{nil, {}, make([]byte, counterSize-1)} {
		if _, err := server.Open("GET", "/x", ProtocolVersion, bad); !errors.Is(err, ErrMalformed) {
			t.Errorf("Open(%d bytes) = %v, want ErrMalformed", len(bad), err)
		}
	}
	// Counters start at 1, so zero is malformed rather than merely stale.
	zero := make([]byte, counterSize+16)
	if _, err := server.Open("GET", "/x", ProtocolVersion, zero); !errors.Is(err, ErrMalformed) {
		t.Errorf("zero counter = %v, want ErrMalformed", err)
	}
}

func TestEnvelopeRejectsWrongKeySize(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33} {
		if _, err := NewEnvelope(make([]byte, size), ClientToServer); err == nil {
			t.Errorf("NewEnvelope accepted a %d-byte key", size)
		}
	}
}

// --- pairing codes -----------------------------------------------------------

func TestCodeIsSingleUse(t *testing.T) {
	cs := NewCodes()
	c, err := cs.Issue()
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(c.Display()) != CodeDigits {
		t.Fatalf("code is %q, want %d digits", c.Display(), CodeDigits)
	}

	if err := cs.Verify(c.Display()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := cs.Verify(c.Display()); !errors.Is(err, ErrCodeConsumed) {
		t.Errorf("second Verify = %v, want ErrCodeConsumed", err)
	}
}

func TestCodeExpires(t *testing.T) {
	cs := NewCodes()
	base := time.Now()
	cs.SetClock(func() time.Time { return base })

	c, _ := cs.Issue()
	cs.SetClock(func() time.Time { return base.Add(CodeTTL + time.Second) })

	if err := cs.Verify(c.Display()); !errors.Is(err, ErrCodeExpired) {
		t.Errorf("Verify after expiry = %v, want ErrCodeExpired", err)
	}
}

// FR-013: a code dies after five failures, and the delay between attempts
// grows so an online guessing run costs real time.
func TestCodeDiesAfterFiveFailures(t *testing.T) {
	cs := NewCodes()
	base := time.Now()
	now := base
	cs.SetClock(func() time.Time { return now })

	c, _ := cs.Issue()
	wrong := "000000"
	if c.Display() == wrong {
		wrong = "111111"
	}

	// Wait just past the longest backoff each time, and no more: five waits of
	// a minute would run past the 3-minute code lifetime and the code would
	// expire before it could die of failed attempts.
	for i := 0; i < MaxAttempts; i++ {
		now = now.Add(31 * time.Second)
		if err := cs.Verify(wrong); !errors.Is(err, ErrWrongCode) {
			t.Fatalf("attempt %d = %v, want ErrWrongCode", i+1, err)
		}
	}

	// Even the correct code must now be refused, and before the code would
	// have expired on its own.
	if base.Add(CodeTTL).Before(now) {
		t.Fatalf("the test outran the code lifetime, so this proves nothing")
	}
	if err := cs.Verify(c.Display()); !errors.Is(err, ErrCodeDead) {
		t.Errorf("after %d failures = %v, want ErrCodeDead", MaxAttempts, err)
	}
}

func TestCodeRateLimitsRepeatedAttempts(t *testing.T) {
	cs := NewCodes()
	base := time.Now()
	now := base
	cs.SetClock(func() time.Time { return now })

	c, _ := cs.Issue()
	wrong := "000000"
	if c.Display() == wrong {
		wrong = "111111"
	}

	if err := cs.Verify(wrong); !errors.Is(err, ErrWrongCode) {
		t.Fatalf("first attempt = %v", err)
	}
	// Immediately again: the backoff must bite before an attempt is spent.
	if err := cs.Verify(wrong); !errors.Is(err, ErrRateLimited) {
		t.Errorf("immediate retry = %v, want ErrRateLimited", err)
	}
	// After the delay, an attempt is allowed again.
	now = now.Add(2 * time.Second)
	if err := cs.Verify(wrong); !errors.Is(err, ErrWrongCode) {
		t.Errorf("retry after backoff = %v, want ErrWrongCode", err)
	}
}

// A live code is registered with the log scrubber and forgotten once it stops
// being sensitive, which is what keeps the scrubber's set bounded.
func TestCodeRegistersAndRetiresWithTheScrubber(t *testing.T) {
	cs := NewCodes()

	var issued, retired []string
	cs.SetScrubHooks(
		func(code string) { issued = append(issued, code) },
		func(code string) { retired = append(retired, code) },
	)

	c, _ := cs.Issue()
	if len(issued) != 1 || issued[0] != c.Display() {
		t.Fatalf("issue hook got %v", issued)
	}
	if len(retired) != 0 {
		t.Fatalf("retire hook fired early: %v", retired)
	}

	if err := cs.Verify(c.Display()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(retired) != 1 || retired[0] != c.Display() {
		t.Errorf("consumed code was not retired: %v", retired)
	}
}

// Issuing a new code retires the previous one: two live codes on one screen is
// an interface nobody wants.
func TestIssuingReplacesTheCurrentCode(t *testing.T) {
	cs := NewCodes()
	now := time.Now()
	cs.SetClock(func() time.Time { return now })

	first, _ := cs.Issue()
	second, _ := cs.Issue()

	if first.Display() == second.Display() {
		t.Fatal("two consecutive codes were identical")
	}
	if err := cs.Verify(first.Display()); !errors.Is(err, ErrWrongCode) {
		t.Errorf("old code = %v, want ErrWrongCode", err)
	}

	// Typing the stale code spent an attempt against the live one, which is
	// correct: a wrong code is a wrong code whatever the user was reading. So
	// the backoff applies before the right code can be tried.
	now = now.Add(2 * time.Second)
	if err := cs.Verify(second.Display()); err != nil {
		t.Errorf("new code: %v", err)
	}
}

func TestVerifyWithNoActiveCode(t *testing.T) {
	cs := NewCodes()
	if err := cs.Verify("123456"); !errors.Is(err, ErrNoCode) {
		t.Errorf("Verify with no code = %v, want ErrNoCode", err)
	}
}

func TestCodesAreDrawnFromTheWholeSpace(t *testing.T) {
	cs := NewCodes()
	seen := make(map[string]bool)
	digits := make(map[byte]bool)

	for i := 0; i < 200; i++ {
		c, err := cs.Issue()
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		seen[c.Display()] = true
		for j := 0; j < len(c.Display()); j++ {
			digits[c.Display()[j]] = true
		}
	}

	if len(seen) < 190 {
		t.Errorf("only %d distinct codes in 200 draws", len(seen))
	}
	// All ten digits should appear across 1200 positions. Their absence would
	// mean the sampling is skewed.
	if len(digits) != 10 {
		t.Errorf("only %d distinct digits appeared, want 10", len(digits))
	}
}

// --- credentials -------------------------------------------------------------

func TestCredentialsAreStoredAsHashesOnly(t *testing.T) {
	credential, hash, err := NewCredential()
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	if len(credential) < 40 {
		t.Errorf("credential is only %d characters", len(credential))
	}
	if bytes.Contains(hash, []byte(credential)) {
		t.Error("the hash contains the credential")
	}
	if !bytes.Equal(HashCredential(credential), hash) {
		t.Error("HashCredential disagrees with NewCredential")
	}
	if bytes.Equal(HashCredential(credential+"x"), hash) {
		t.Error("a different credential produced the same hash")
	}

	other, _, _ := NewCredential()
	if other == credential {
		t.Error("two credentials were identical")
	}
}
