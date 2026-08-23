package integration

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// User Story 3: a dropped network never restarts a transfer from zero.
//
// SC-005 puts a number on it — an interruption at any point re-sends no more
// than 1% of what was already delivered — and that number is only meaningful if
// the interruption is a real one. A test that politely stops sending proves
// nothing about a phone that walks out of Wi-Fi range mid-request, because the
// interesting question is what happens to the bytes that were already in the
// air when the connection died.
//
// So these tests cut the connection underneath an HTTP request rather than
// simulating it at a higher level, and then measure what has to be sent again.

// cutUpload sends an upload whose body stops partway and then half-closes the
// connection, and reports how many bytes of the body left the device.
//
// A raw connection rather than the HTTP client, because the client offers no
// way to say "these bytes were delivered and the rest never will be". The half
// close is what makes it deterministic: TCP delivers everything already written
// before the FIN, so the server reads exactly `cut` bytes and then meets an EOF
// where the rest of the body should have been. That is a lost connection minus
// the timeout, which is the part of it no test should wait out.
func (d *device) cutUpload(
	t *testing.T,
	transferID string,
	index int,
	offset uint64,
	chunk []byte,
	cut int,
) int {
	t.Helper()

	endpoint, err := url.Parse(d.h.server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	// Raw on purpose, and the whole point of the test: no HTTP client offers a
	// way to say "these bytes were delivered and the rest never will be".
	//nolint:forbidigo // deliberate: severing a connection is what is under test
	conn, err := net.Dial("tcp", endpoint.Host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	path := fmt.Sprintf("/api/transfers/%s/items/%d/content?offset=%d", transferID, index, offset)
	request := "POST " + path + " HTTP/1.1\r\n" +
		"Host: " + endpoint.Host + "\r\n" +
		"Authorization: Bearer " + d.credential + "\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n", len(chunk)) +
		"\r\n"

	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write request head: %v", err)
	}
	if _, err := conn.Write(chunk[:cut]); err != nil {
		t.Fatalf("write body: %v", err)
	}

	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		t.Fatal("expected a TCP connection")
	}
	if err := tcp.CloseWrite(); err != nil {
		t.Fatalf("half close: %v", err)
	}

	// Draining to EOF is how the test knows the handler has finished with the
	// request, rather than racing the assertions that follow against it.
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("drain response: %v", err)
	}
	return cut
}

// SC-005, with the number checked rather than asserted in prose.
func TestAnInterruptedUploadResendsAlmostNothing(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	// Random, so a file assembled from the wrong pieces cannot pass by looking
	// plausible: every offset holds bytes that exist nowhere else.
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	tr := phone.declare(t, h.selfID, "walked-away.bin", uint64(len(payload)))

	// Everything that left the phone, including what the cut connection lost.
	// This is the number SC-005 is about: not what the server has, what the
	// sender had to spend.
	var wire int

	const attempt = 512 << 10
	const cut = 300 << 10
	wire += phone.cutUpload(t, tr.ID, 0, 0, payload[:attempt], cut)

	// The bytes that arrived are durable. This is the whole reason a resume can
	// be exact: a short copy is still fsynced and still committed, so the cut
	// costs the sender nothing it had already delivered.
	committed := phone.itemOffset(t, tr.ID, 0)
	if committed != cut {
		t.Fatalf("committed offset after the cut = %d, want %d", committed, cut)
	}

	// FR-035d: interrupted, not failed. A failed transfer is over; this one is
	// waiting to be continued, and it has released the active slot to say so.
	if got := phone.transferState(t, tr.ID).State; got != "interrupted" {
		t.Errorf("state after the cut = %q, want interrupted", got)
	}

	phone.uploadOK(t, tr.ID, 0, committed, payload[committed:])
	wire += len(payload) - int(committed)
	phone.completeOK(t, tr.ID, 0, digestOf(t, payload))

	resent := wire - len(payload)
	if limit := len(payload) / 100; resent > limit {
		t.Errorf("re-sent %d bytes of %d delivered, over the %d allowed by SC-005",
			resent, len(payload), limit)
	}

	got, err := os.ReadFile(filepath.Join(h.receiveDir, "walked-away.bin"))
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("the resumed file is not the file that was sent")
	}
}

// An interruption partway through several chunks is the ordinary case, and the
// hash has to survive it: the digest is built in memory as the chunks arrive,
// and a resume that rebuilt it wrong would fail verification on a file that is
// perfectly intact.
func TestResumingSeveralTimesStillVerifies(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := make([]byte, 400<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	tr := phone.declare(t, h.selfID, "twice-cut.bin", uint64(len(payload)))

	for _, cut := range []int{40 << 10, 90 << 10} {
		at := phone.itemOffset(t, tr.ID, 0)
		phone.cutUpload(t, tr.ID, 0, at, payload[at:], cut)

		if got := phone.itemOffset(t, tr.ID, 0); got != at+uint64(cut) {
			t.Fatalf("offset after a cut at %d = %d, want %d", at, got, at+uint64(cut))
		}
	}

	at := phone.itemOffset(t, tr.ID, 0)
	phone.uploadOK(t, tr.ID, 0, at, payload[at:])

	// The digest covers the whole file, assembled across three connections. If
	// the hash state had been rebuilt from the wrong prefix, this is where it
	// would show.
	final := phone.completeOK(t, tr.ID, 0, digestOf(t, payload))
	if final.State != "completed" {
		t.Fatalf("state = %q, want completed", final.State)
	}

	got, err := os.ReadFile(filepath.Join(h.receiveDir, "twice-cut.bin"))
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("a file cut twice did not survive intact")
	}
}

// An interrupted transfer must not sit on the single active slot, or one phone
// that went out of range would stop every other transfer on the machine.
// FR-035a and FR-035d together.
func TestAnInterruptedTransferReleasesTheQueue(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()
	other := h.pair()

	stalled := phone.declare(t, h.selfID, "stalled.bin", 64<<10)
	phone.cutUpload(t, stalled.ID, 0, 0, make([]byte, 64<<10), 8<<10)

	if got := phone.transferState(t, stalled.ID).State; got != "interrupted" {
		t.Fatalf("state = %q, want interrupted", got)
	}

	queue, err := h.store.Queue()
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if queue.ActiveID != "" {
		t.Errorf("the interrupted transfer still holds the active slot (%s)", queue.ActiveID)
	}

	// And the slot is genuinely usable: another device's transfer runs to
	// completion rather than being refused as queue_busy.
	payload := []byte("someone else's file")
	moving := other.declare(t, h.selfID, "moving.bin", uint64(len(payload)))
	other.uploadOK(t, moving.ID, 0, 0, payload)
	if got := other.completeOK(t, moving.ID, 0, digestOf(t, payload)).State; got != "completed" {
		t.Errorf("the second transfer reached %q, want completed", got)
	}
}
