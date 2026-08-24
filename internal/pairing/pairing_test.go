package pairing

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

// The handshake and the envelope are the whole of the security story in simple
// mode, where the wire is plain HTTP on a shared network. Everything below
// assumes an observer reads every byte and can replay anything they saw.

// exchange runs the joining device's whole side against a live registry, and
// returns what it would send.
//
// The client's half in one place rather than spelled out in every test: what
// each test below actually varies is one input, and burying that in six lines
// of ceremony is how a test ends up asserting something other than its name.
func exchange(t *testing.T, hs *Handshakes, code, hostID string) (h *Handshake, proof []byte, key []byte) {
	t.Helper()

	sid, err := NewSessionID(rand.Reader)
	if err != nil {
		t.Fatalf("session id: %v", err)
	}
	secret, clientMessage, err := ClientBegin(code, hostID, sid)
	if err != nil {
		t.Fatalf("ClientBegin: %v", err)
	}

	h, serverMessage, err := hs.Begin(hostID, code, sid, clientMessage)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	key, proof, err = ClientComplete(secret, sid, clientMessage, serverMessage, h.ID)
	if err != nil {
		t.Fatalf("ClientComplete: %v", err)
	}
	return h, proof, key
}

func TestHandshakeRoundTripDerivesTheSameKey(t *testing.T) {
	hs := NewHandshakes()
	const code = "482915"

	h, proof, clientKey := exchange(t, hs, code, "host-1")

	serverKey, settled, err := hs.Complete(h.ID, proof)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if settled != code {
		t.Errorf("Complete reported code %q, want %q", settled, code)
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

// This is the property the whole design rests on. An observer reads everything
// on the wire and still cannot confirm, because everything on the wire is
// independent of the code: the two group elements are uniformly distributed
// whichever six digits produced them.
func TestWrongCodeCannotConfirm(t *testing.T) {
	hs := NewHandshakes()

	sid, err := NewSessionID(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// The host holds one code; whoever is joining guesses another.
	secret, clientMessage, err := ClientBegin("000000", "host-1", sid)
	if err != nil {
		t.Fatal(err)
	}
	h, serverMessage, err := hs.Begin("host-1", "482915", sid, clientMessage)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	_, proof, err := ClientComplete(secret, sid, clientMessage, serverMessage, h.ID)
	if err != nil {
		t.Fatal(err)
	}

	_, settled, err := hs.Complete(h.ID, proof)
	if !errors.Is(err, ErrBadProof) {
		t.Errorf("Complete with a guessed code = %v, want ErrBadProof", err)
	}
	// The code comes back even on failure, so the caller can spend one of the
	// five attempts on the guess. Without it a wrong guess would be free.
	if settled != "482915" {
		t.Errorf("failed Complete reported code %q, want the one the exchange began with", settled)
	}
}

// The same digits shown by two different computers must not produce the same
// exchange, so a proof cannot be carried from one to the other.
func TestAProofDoesNotTransferBetweenHosts(t *testing.T) {
	const code = "482915"

	sid, err := NewSessionID(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret, clientMessage, err := ClientBegin(code, "host-1", sid)
	if err != nil {
		t.Fatal(err)
	}

	// The proof is produced for host-1 and presented to host-2.
	elsewhere := NewHandshakes()
	h, serverMessage, err := elsewhere.Begin("host-2", code, sid, clientMessage)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, proof, err := ClientComplete(secret, sid, clientMessage, serverMessage, h.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := elsewhere.Complete(h.ID, proof); !errors.Is(err, ErrBadProof) {
		t.Errorf("a proof made for one host verified at another: %v", err)
	}
}

// A handshake is consumed whether or not the proof verifies, so one interaction
// is worth exactly one guess. This is what makes the five-attempt budget the
// real bound on a twenty-bit code.
func TestHandshakeIsSingleUse(t *testing.T) {
	hs := NewHandshakes()
	const code = "482915"

	h, proof, _ := exchange(t, hs, code, "host-1")

	if _, _, err := hs.Complete(h.ID, proof); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if _, _, err := hs.Complete(h.ID, proof); !errors.Is(err, ErrUnknownHandshake) {
		t.Errorf("replayed Complete = %v, want ErrUnknownHandshake", err)
	}

	// And a failed attempt burns it too.
	h2, proof2, _ := exchange(t, hs, code, "host-1")
	if _, _, err := hs.Complete(h2.ID, []byte("wrong")); !errors.Is(err, ErrBadProof) {
		t.Fatalf("a wrong proof = %v, want ErrBadProof", err)
	}
	if _, _, err := hs.Complete(h2.ID, proof2); !errors.Is(err, ErrUnknownHandshake) {
		t.Errorf("retry after a bad proof = %v, want ErrUnknownHandshake", err)
	}
}

func TestHandshakeExpires(t *testing.T) {
	hs := NewHandshakes()
	base := time.Now()
	hs.SetClock(func() time.Time { return base })

	h, proof, _ := exchange(t, hs, "482915", "host-1")

	hs.SetClock(func() time.Time { return base.Add(handshakeTTL + time.Second) })
	if _, _, err := hs.Complete(h.ID, proof); !errors.Is(err, ErrHandshakeExpired) {
		t.Errorf("Complete after the deadline = %v, want ErrHandshakeExpired", err)
	}
}

// Abandoned handshakes must not accumulate in memory. Starting one and walking
// away is free for whoever does it, and it is the cheapest thing an attacker
// can do repeatedly.
func TestAbandonedHandshakesAreSwept(t *testing.T) {
	hs := NewHandshakes()
	base := time.Now()
	hs.SetClock(func() time.Time { return base })

	for i := 0; i < 5; i++ {
		exchange(t, hs, "482915", "host-1")
	}
	if got := hs.Open(); got != 5 {
		t.Fatalf("open = %d, want 5", got)
	}

	hs.SetClock(func() time.Time { return base.Add(handshakeTTL + time.Second) })
	exchange(t, hs, "482915", "host-1")
	if got := hs.Open(); got != 1 {
		t.Errorf("open after sweep = %d, want 1", got)
	}
}

func TestHandshakeRejectsMalformedInputs(t *testing.T) {
	hs := NewHandshakes()

	sid, err := NewSessionID(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, clientMessage, err := ClientBegin("482915", "host-1", sid)
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range [][]byte{nil, {}, make([]byte, SessionIDSize-1), make([]byte, SessionIDSize+1)} {
		if _, _, err := hs.Begin("host-1", "482915", bad, clientMessage); !errors.Is(err, ErrBadSessionID) {
			t.Errorf("Begin with a %d-byte session id = %v, want ErrBadSessionID", len(bad), err)
		}
	}

	for _, bad := range [][]byte{nil, {}, make([]byte, ElementSize-1), bytes.Repeat([]byte{0xff}, ElementSize)} {
		if _, _, err := hs.Begin("host-1", "482915", sid, bad); !errors.Is(err, ErrBadElement) {
			t.Errorf("Begin with a %d-byte element = %v, want ErrBadElement", len(bad), err)
		}
	}

	// Nothing malformed may have been recorded on the way through.
	if got := hs.Open(); got != 0 {
		t.Errorf("%d handshakes were left open by refused requests", got)
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

// The accounting split in two.
//
// Live admits a guess and enforces the growing delay; Settle records how it
// turned out. They are two calls rather than one because under CPace the host
// does not learn whether the digits were right until a round trip later: it
// hands the code to a derivation, and the answer arrives with the confirmation.

func TestCodeIsSingleUse(t *testing.T) {
	cs := NewCodes()
	c, err := cs.Issue()
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(c.Display()) != CodeDigits {
		t.Fatalf("code is %q, want %d digits", c.Display(), CodeDigits)
	}

	live, err := cs.Live()
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if live != c.Display() {
		t.Fatal("Live returned digits other than the displayed ones")
	}
	cs.Settle(live, true)

	if _, err := cs.Live(); !errors.Is(err, ErrCodeConsumed) {
		t.Errorf("second Live = %v, want ErrCodeConsumed", err)
	}
}

func TestCodeExpires(t *testing.T) {
	cs := NewCodes()
	base := time.Now()
	cs.SetClock(func() time.Time { return base })

	if _, err := cs.Issue(); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cs.SetClock(func() time.Time { return base.Add(CodeTTL + time.Second) })

	if _, err := cs.Live(); !errors.Is(err, ErrCodeExpired) {
		t.Errorf("Live after expiry = %v, want ErrCodeExpired", err)
	}
}

// FR-013: a code dies after five failures, and the delay between attempts
// grows so an online guessing run costs real time.
//
// This is the whole defence now. With no offline oracle left, five guesses out
// of a million is the attacker's entire budget, and this is the test that says
// so.
func TestCodeDiesAfterFiveFailures(t *testing.T) {
	cs := NewCodes()
	base := time.Now()
	now := base
	cs.SetClock(func() time.Time { return now })

	if _, err := cs.Issue(); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Wait just past the longest backoff each time, and no more: five waits of
	// a minute would run past the 3-minute code lifetime and the code would
	// expire before it could die of failed attempts.
	for i := 0; i < MaxAttempts; i++ {
		now = now.Add(31 * time.Second)
		live, err := cs.Live()
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		cs.Settle(live, false)
	}

	// Even a correct guess must now be refused, and before the code would have
	// expired on its own.
	if base.Add(CodeTTL).Before(now) {
		t.Fatalf("the test outran the code lifetime, so this proves nothing")
	}
	if _, err := cs.Live(); !errors.Is(err, ErrCodeDead) {
		t.Errorf("after %d failures = %v, want ErrCodeDead", MaxAttempts, err)
	}
}

func TestCodeRateLimitsRepeatedAttempts(t *testing.T) {
	cs := NewCodes()
	base := time.Now()
	now := base
	cs.SetClock(func() time.Time { return now })

	if _, err := cs.Issue(); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	live, err := cs.Live()
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	cs.Settle(live, false)

	// Immediately again: the delay must bite before another guess is admitted.
	if _, err := cs.Live(); !errors.Is(err, ErrRateLimited) {
		t.Errorf("immediate retry = %v, want ErrRateLimited", err)
	}
	// After the delay, a guess is allowed again.
	now = now.Add(2 * time.Second)
	if _, err := cs.Live(); err != nil {
		t.Errorf("retry after backoff = %v", err)
	}
}

// A guess never settled must not buy an escape from the delay. Starting an
// exchange and abandoning it is free for whoever does it, and if it left the
// attempt uncounted while resetting nothing, the budget would still hold — but
// if it also cleared the delay, five guesses would become unlimited ones.
func TestAnAbandonedAttemptDoesNotResetTheDelay(t *testing.T) {
	cs := NewCodes()
	now := time.Now()
	cs.SetClock(func() time.Time { return now })

	if _, err := cs.Issue(); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	live, err := cs.Live()
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	cs.Settle(live, false)

	// Start another and walk away, then try again immediately.
	if _, err := cs.Live(); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second attempt = %v, want ErrRateLimited", err)
	}
	if _, err := cs.Live(); !errors.Is(err, ErrRateLimited) {
		t.Errorf("third attempt = %v, want ErrRateLimited", err)
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

	live, err := cs.Live()
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	cs.Settle(live, true)

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

	// An exchange that began against the code now retired settles against
	// nothing: three minutes is long enough for the host to have moved on, and
	// counting that failure would spend a budget the attempt never touched.
	cs.Settle(first.Display(), false)

	live, err := cs.Live()
	if err != nil {
		t.Fatalf("Live after a stale settlement = %v", err)
	}
	if live != second.Display() {
		t.Error("Live did not return the code now on screen")
	}
}

func TestLiveWithNoActiveCode(t *testing.T) {
	cs := NewCodes()
	if _, err := cs.Live(); !errors.Is(err, ErrNoCode) {
		t.Errorf("Live with no code = %v, want ErrNoCode", err)
	}
	// And settling against one that never existed does nothing rather than
	// panicking.
	cs.Settle("123456", false)
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
