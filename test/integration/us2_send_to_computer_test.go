package integration

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// User Story 2 end to end: a phone picks a file and sends it to the computer,
// which writes it into its receive folder.
//
// The independent test the story names is "send a video to the computer and
// verify it lands intact in the receive folder with its original name". That is
// TestPhoneSendsAFileToTheComputer below; the rest cover the ways a phone
// actually behaves, which is to say badly: it locks its screen, it loses Wi-Fi,
// and it comes back halfway through.

// uploadInChunks sends a payload the way the browser client does, in pieces,
// checking the committed offset after each one.
func (d *device) uploadInChunks(t *testing.T, transferID string, index int, payload []byte, chunk int) {
	t.Helper()

	for offset := 0; offset < len(payload); offset += chunk {
		end := offset + chunk
		if end > len(payload) {
			end = len(payload)
		}

		committed := d.uploadOK(t, transferID, index, uint64(offset), payload[offset:end])
		if committed != uint64(end) {
			t.Fatalf("after the chunk ending at %d, committed = %d", end, committed)
		}
	}
}

// The story's own acceptance test: a file goes from the phone to the receive
// folder, intact, under its original name.
func TestPhoneSendsAFileToTheComputer(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	// Large enough to cross the copy buffer several times, so the chunking is
	// exercised rather than trivially satisfied by one write.
	payload := make([]byte, 3<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	tr := phone.declare(t, h.selfID, "holiday.mp4", uint64(len(payload)))
	if tr.State != "queued" {
		t.Fatalf("state after declaring = %q", tr.State)
	}

	phone.uploadInChunks(t, tr.ID, 0, payload, 256<<10)
	final := phone.completeOK(t, tr.ID, 0, digestOf(t, payload))

	if final.State != "completed" {
		t.Errorf("state = %q, want completed", final.State)
	}
	if got := final.Items[0].StoredName; got != "holiday.mp4" {
		t.Errorf("stored name = %q, want the original", got)
	}

	got, err := os.ReadFile(filepath.Join(h.receiveDir, "holiday.mp4"))
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("the received file differs from what was sent (%d vs %d bytes)", len(got), len(payload))
	}
}

// Scenario 2: progress is visible while it runs. The phone reads it from the
// transfer record, which is the same thing the event stream carries.
func TestUploadProgressIsVisibleWhileItRuns(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := bytes.Repeat([]byte("p"), 8192)
	tr := phone.declare(t, h.selfID, "progress.bin", uint64(len(payload)))

	phone.uploadOK(t, tr.ID, 0, 0, payload[:2048])

	midway := phone.transferState(t, tr.ID)
	if midway.State != "running" {
		t.Errorf("state midway = %q, want running", midway.State)
	}

	// The committed offset is the honest number: it is what survives a power
	// cut, not what the kernel has been handed.
	if got := phone.itemOffset(t, tr.ID, 0); got != 2048 {
		t.Errorf("committed offset midway = %d, want 2048", got)
	}
}

// A phone that lost Wi-Fi halfway resumes from what the server durably holds,
// rather than sending the whole file again. FR-031.
func TestUploadResumesFromTheCommittedOffset(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := make([]byte, 512<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	tr := phone.declare(t, h.selfID, "interrupted.bin", uint64(len(payload)))

	const stoppedAt = 200 << 10
	phone.uploadOK(t, tr.ID, 0, 0, payload[:stoppedAt])

	// The phone comes back and asks where it got to, exactly as web/src/lib/
	// upload.ts does before resuming.
	resume := phone.itemOffset(t, tr.ID, 0)
	if resume != stoppedAt {
		t.Fatalf("resume point = %d, want %d", resume, stoppedAt)
	}

	phone.uploadOK(t, tr.ID, 0, resume, payload[resume:])
	phone.completeOK(t, tr.ID, 0, digestOf(t, payload))

	got, err := os.ReadFile(filepath.Join(h.receiveDir, "interrupted.bin"))
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("the resumed file does not match what was sent")
	}
}

// Several files in one send, which is what selecting a camera roll produces.
func TestPhoneSendsSeveralFilesAtOnce(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	files := map[string][]byte{
		"first.jpg":  bytes.Repeat([]byte("1"), 1024),
		"second.jpg": bytes.Repeat([]byte("2"), 2048),
		"third.jpg":  bytes.Repeat([]byte("3"), 512),
	}
	names := []string{"first.jpg", "second.jpg", "third.jpg"}

	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		items = append(items, map[string]any{"name": name, "size": len(files[name])})
	}

	resp := phone.do("POST", "/api/transfers", map[string]any{
		"target_device_id": h.selfID,
		"items":            items,
	})
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("declare: status %d: %s", resp.StatusCode, raw)
	}

	var tr declaredTransfer
	phone.open("POST", "/api/transfers", resp, &tr)

	for index, name := range names {
		phone.uploadOK(t, tr.ID, index, 0, files[name])
		phone.completeOK(t, tr.ID, index, digestOf(t, files[name]))
	}

	for _, name := range names {
		got, err := os.ReadFile(filepath.Join(h.receiveDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(got, files[name]) {
			t.Errorf("%s differs from what was sent", name)
		}
	}

	if final := phone.transferState(t, tr.ID); final.State != "completed" {
		t.Errorf("state = %q, want completed", final.State)
	}
}

// A folder send keeps its structure, and the structure is still confined to the
// receive folder. FR-018 and FR-021.
func TestFolderStructureIsPreservedInsideTheReceiveFolder(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := []byte("nested")
	resp := phone.do("POST", "/api/transfers", map[string]any{
		"target_device_id": h.selfID,
		"items": []map[string]any{{
			"name":          "note.txt",
			"relative_path": "trip/day one",
			"size":          len(payload),
		}},
	})
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("declare: status %d: %s", resp.StatusCode, raw)
	}

	var tr declaredTransfer
	phone.open("POST", "/api/transfers", resp, &tr)

	phone.uploadOK(t, tr.ID, 0, 0, payload)
	phone.completeOK(t, tr.ID, 0, digestOf(t, payload))

	got, err := os.ReadFile(filepath.Join(h.receiveDir, "trip", "day one", "note.txt"))
	if err != nil {
		t.Fatalf("read nested file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("nested file = %q, want %q", got, payload)
	}
}

// Cancelling an upload leaves nothing behind: not at the destination, and not
// in staging either. FR-033.
func TestCancellingAnUploadRemovesThePartialData(t *testing.T) {
	h := newHarness(t)
	phone := h.pair()

	payload := bytes.Repeat([]byte("c"), 4096)
	tr := phone.declare(t, h.selfID, "abandoned.bin", uint64(len(payload)))
	phone.uploadOK(t, tr.ID, 0, 0, payload[:1024])

	resp := phone.do("POST", fmt.Sprintf("/api/transfers/%s/cancel", tr.ID), map[string]any{})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("cancel: status %d: %s", resp.StatusCode, raw)
	}
	resp.Body.Close()

	entries, err := os.ReadDir(h.receiveDir)
	if err != nil {
		t.Fatalf("read receive folder: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the receive folder holds %d entries after a cancellation", len(entries))
	}

	staged, err := os.ReadDir(h.stagingDir)
	if err != nil {
		t.Fatalf("read staging folder: %v", err)
	}
	for _, e := range staged {
		if filepath.Ext(e.Name()) == ".part" {
			t.Errorf("partial data survived a cancellation: %s", e.Name())
		}
	}
}

// FR-028: a transfer too large for the destination is refused before a byte
// moves, not discovered at 90%.
func TestUploadIsRefusedWhenTheDiskIsTooFull(t *testing.T) {
	h := newHarnessWithSpace(t, 1<<20)
	phone := h.pair()

	resp := phone.do("POST", "/api/transfers", map[string]any{
		"target_device_id": h.selfID,
		"items":            []map[string]any{{"name": "huge.iso", "size": 8 << 20}},
	})
	if resp.StatusCode != http.StatusConflict {
		resp.Body.Close()
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}

	body := errorBody(t, resp)
	if body["error"] != "insufficient_space" {
		t.Errorf("error = %v, want insufficient_space", body["error"])
	}
	// FR-038: the message has to say what to do, which needs both numbers.
	params, ok := body["params"].(map[string]any)
	if !ok || params["needed"] == nil || params["available"] == nil {
		t.Errorf("params = %v, want needed and available", body["params"])
	}
}
