package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// The upward content and offset endpoints, per contracts/http-api.md.
//
// This is the direction where the server owns the file, so it is the direction
// where the server can get it wrong in ways nobody notices: a chunk written at
// the wrong place produces a file of exactly the right length and the wrong
// content. These tests exist mostly to pin that down.

// upload pushes a chunk of an item and returns the raw response.
func (d *device) upload(t *testing.T, transferID string, index int, offset uint64, chunk []byte) *http.Response {
	t.Helper()

	path := fmt.Sprintf("/api/transfers/%s/items/%d/content?offset=%d", transferID, index, offset)
	req, err := http.NewRequest("POST", d.h.server.URL+path, bytes.NewReader(chunk)) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.credential)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := d.h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	return resp
}

// uploadOK pushes a chunk and returns the committed offset, failing on anything
// but success.
func (d *device) uploadOK(t *testing.T, transferID string, index int, offset uint64, chunk []byte) uint64 {
	t.Helper()

	resp := d.upload(t, transferID, index, offset, chunk)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload at %d: status %d: %s", offset, resp.StatusCode, raw)
	}

	// A bulk content route answers in the clear, like /supply does.
	var out struct {
		CommittedOffset uint64 `json:"committed_offset"`
	}
	decodePlainBody(t, resp, &out)
	return out.CommittedOffset
}

// itemOffset asks where a resuming sender should continue from.
func (d *device) itemOffset(t *testing.T, transferID string, index int) uint64 {
	t.Helper()

	path := fmt.Sprintf("/api/transfers/%s/items/%d/offset", transferID, index)
	resp := d.do("GET", path, nil)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("offset: status %d: %s", resp.StatusCode, raw)
	}

	var out struct {
		CommittedOffset uint64 `json:"committed_offset"`
	}
	d.open("GET", path, resp, &out)
	return out.CommittedOffset
}

// completeItem declares an item finished, with the digest the sender computed.
func (d *device) completeItem(t *testing.T, transferID string, index int, digest []byte) *http.Response {
	t.Helper()

	path := fmt.Sprintf("/api/transfers/%s/items/%d/complete", transferID, index)
	body := map[string]any{}
	if digest != nil {
		body["checksum"] = base64.StdEncoding.EncodeToString(digest)
	}
	return d.do("POST", path, body)
}

// completeOK finishes an item and returns the transfer as the server sees it.
func (d *device) completeOK(t *testing.T, transferID string, index int, digest []byte) declaredTransfer {
	t.Helper()

	resp := d.completeItem(t, transferID, index, digest)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("complete: status %d: %s", resp.StatusCode, raw)
	}

	var out struct {
		Transfer declaredTransfer `json:"transfer"`
	}
	d.open("POST", fmt.Sprintf("/api/transfers/%s/items/%d/complete", transferID, index), resp, &out)
	return out.Transfer
}

func decodePlainBody(t *testing.T, resp *http.Response, out any) {
	t.Helper()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
}

// digestOf is what a sender computes as it reads, and what the server compares
// against what it wrote.
func digestOf(t *testing.T, payload []byte) []byte {
	t.Helper()

	h, err := blake2b.New256(nil)
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	h.Write(payload)
	return h.Sum(nil)
}

// A transfer addressed to this machine is written to disk, unlike one piped
// between two browsers.
func TestUploadWritesIntoTheReceiveFolder(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("a small video, allegedly")
	tr := phone.declare(t, h.selfID, "clip.mp4", uint64(len(payload)))

	if got := tr.Items[0].StoredName; got != "clip.mp4" {
		t.Errorf("stored name = %q, want the original", got)
	}

	if committed := phone.uploadOK(t, tr.ID, 0, 0, payload); committed != uint64(len(payload)) {
		t.Errorf("committed offset = %d, want %d", committed, len(payload))
	}

	// Nothing may appear at the final path before the checksum verifies.
	final := filepath.Join(h.receiveDir, "clip.mp4")
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("the file appeared before it was verified: %v", err)
	}

	phone.completeOK(t, tr.ID, 0, digestOf(t, payload))

	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("received %q, want %q", got, payload)
	}
}

// The offset endpoint is what a resuming sender reads before deciding where to
// continue.
func TestOffsetEndpointReportsWhatIsDurable(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := bytes.Repeat([]byte("x"), 4096)
	tr := phone.declare(t, h.selfID, "big.bin", uint64(len(payload)))

	if got := phone.itemOffset(t, tr.ID, 0); got != 0 {
		t.Errorf("offset before anything was sent = %d, want 0", got)
	}

	phone.uploadOK(t, tr.ID, 0, 0, payload[:1000])

	if got := phone.itemOffset(t, tr.ID, 0); got != 1000 {
		t.Errorf("offset after 1000 bytes = %d, want 1000", got)
	}
}

// A chunk that does not start where the file ends would produce a file of the
// right length and the wrong content. It must be refused, and the sender told
// the real offset so it can correct rather than corrupt. FR-031.
func TestUploadRefusesAChunkAtTheWrongOffset(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := bytes.Repeat([]byte("y"), 2048)
	tr := phone.declare(t, h.selfID, "gap.bin", uint64(len(payload)))

	phone.uploadOK(t, tr.ID, 0, 0, payload[:512])

	resp := phone.upload(t, tr.ID, 0, 1024, payload[1024:])
	if resp.StatusCode != http.StatusConflict {
		resp.Body.Close()
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}

	body := errorBody(t, resp)
	if body["error"] != "offset_mismatch" {
		t.Errorf("error = %v, want offset_mismatch", body["error"])
	}

	params, ok := body["params"].(map[string]any)
	if !ok {
		t.Fatalf("no params in %v; the sender cannot correct without the real offset", body)
	}
	if got := params["committed_offset"]; got != float64(512) {
		t.Errorf("committed_offset = %v, want 512", got)
	}

	// The refusal must not have moved anything.
	if got := phone.itemOffset(t, tr.ID, 0); got != 512 {
		t.Errorf("offset after the refusal = %d, want 512", got)
	}
}

// FR-032: a corrupted transfer is reported as failed, never as a success, and
// FR-033: the partial file does not survive at the destination.
func TestCompleteRefusesAChecksumMismatch(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("the bytes that were actually sent")
	tr := phone.declare(t, h.selfID, "corrupt.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload)

	resp := phone.completeItem(t, tr.ID, 0, digestOf(t, []byte("a different file entirely")))
	if resp.StatusCode != http.StatusUnprocessableEntity {
		resp.Body.Close()
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	if body := errorBody(t, resp); body["error"] != "checksum_mismatch" {
		t.Errorf("error = %v, want checksum_mismatch", body["error"])
	}

	entries, err := os.ReadDir(h.receiveDir)
	if err != nil {
		t.Fatalf("read receive folder: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the receive folder holds %d entries after a failed transfer", len(entries))
	}

	// And the partial is gone from staging too, rather than waiting for a sweep.
	staged, err := os.ReadDir(h.stagingDir)
	if err != nil {
		t.Fatalf("read staging folder: %v", err)
	}
	for _, e := range staged {
		if filepath.Ext(e.Name()) == ".part" {
			t.Errorf("partial data survived a checksum failure: %s", e.Name())
		}
	}

	final := phone.transferState(t, tr.ID)
	if final.State != "failed" {
		t.Errorf("state = %q, want failed", final.State)
	}
}

// Holding a credential is not enough. A paired phone must not be able to append
// to a file a different phone is sending: the checksum would then fail against
// the innocent party.
func TestOnlyTheSourceMayUpload(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()
	intruder := h.pair()

	tr := phone.declare(t, h.selfID, "private.bin", 16)

	resp := intruder.upload(t, tr.ID, 0, 0, bytes.Repeat([]byte("z"), 16))
	defer resp.Body.Close()

	// Not a participant at all: the transfer is invisible rather than refused,
	// so identifiers cannot be enumerated.
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a third device was allowed to write into someone else's transfer")
	}
}

// The declared size is the contract. A body longer than what remains is not
// written, or a declaration of one byte could fill the disk.
func TestUploadWillNotWritePastTheDeclaredSize(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	declared := []byte("exactly this much")
	tr := phone.declare(t, h.selfID, "bounded.bin", uint64(len(declared)))

	overlong := append(append([]byte{}, declared...), bytes.Repeat([]byte("!"), 4096)...)
	committed := phone.uploadOK(t, tr.ID, 0, 0, overlong)

	if committed != uint64(len(declared)) {
		t.Fatalf("committed %d bytes, want %d", committed, len(declared))
	}

	phone.completeOK(t, tr.ID, 0, digestOf(t, declared))

	got, err := os.ReadFile(filepath.Join(h.receiveDir, "bounded.bin"))
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if !bytes.Equal(got, declared) {
		t.Errorf("received %d bytes, want %d", len(got), len(declared))
	}
}
