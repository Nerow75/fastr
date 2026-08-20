package integration

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// User Story 1 end to end, through the real endpoints.
//
// The sender declares, the receiver fetches, the sender supplies, and the bytes
// stream from one to the other without touching the disk. These tests are the
// only place that whole path is exercised together.

type declaredTransfer struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Items []struct {
		Index      int    `json:"index"`
		Name       string `json:"name"`
		StoredName string `json:"stored_name"`
		Size       uint64 `json:"size"`
	} `json:"items"`
}

// declare proposes a transfer from one device to another.
func (d *device) declare(t *testing.T, target string, name string, size uint64) declaredTransfer {
	t.Helper()

	resp := d.do("POST", "/api/transfers", map[string]any{
		"target_device_id": target,
		"items":            []map[string]any{{"name": name, "size": size}},
	})
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("declare: status %d: %s", resp.StatusCode, body)
	}

	var out declaredTransfer
	d.open("POST", "/api/transfers", resp, &out)
	return out
}

// fetch pulls one item, returning the body and the response.
func (d *device) fetch(t *testing.T, transferID string, index int, rangeHeader string) (*http.Response, []byte) {
	t.Helper()

	path := fmt.Sprintf("/api/transfers/%s/items/%d/content", transferID, index)
	req, err := http.NewRequest("GET", d.h.server.URL+path, nil) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.credential)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	resp, err := d.h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return resp, body
}

// supply attaches content for a waiting fetch.
func (d *device) supply(t *testing.T, transferID string, index int, offset uint64, content []byte) *http.Response {
	t.Helper()

	path := fmt.Sprintf("/api/transfers/%s/items/%d/supply?offset=%d", transferID, index, offset)
	req, err := http.NewRequest("POST", d.h.server.URL+path, bytes.NewReader(content)) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.credential)

	resp, err := d.h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("supply: %v", err)
	}
	return resp
}

// The whole story: a file goes from one device to another, intact.
func TestTransferMovesAFileEndToEnd(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	payload := make([]byte, 3<<20) // 3 MB, several buffers' worth
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	tr := sender.declare(t, receiver.ID, "holiday.mp4", uint64(len(payload)))
	if tr.State != "queued" {
		t.Fatalf("state = %q, want queued", tr.State)
	}

	var received []byte
	var status int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, body := receiver.fetch(t, tr.ID, 0, "")
		status, received = resp.StatusCode, body
	}()

	// Wait for the receiver's demand to register, then supply it.
	waitForPipe(t, h, 1)
	resp := sender.supply(t, tr.ID, 0, 0, payload)
	resp.Body.Close()
	wg.Wait()

	if status != http.StatusOK {
		t.Fatalf("fetch status = %d, want 200", status)
	}
	if !bytes.Equal(received, payload) {
		t.Fatalf("received %d bytes, want %d, and they differ", len(received), len(payload))
	}

	// The transfer must end up completed, with its bytes accounted for.
	final := receiver.transferState(t, tr.ID)
	if final.State != "completed" {
		t.Errorf("final state = %q, want completed", final.State)
	}
}

// The download carries the sanitized name, not whatever the sender said.
func TestDownloadCarriesTheSanitizedName(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	payload := []byte("small")
	tr := sender.declare(t, receiver.ID, "rapport été.pdf", uint64(len(payload)))

	var disposition string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, _ := receiver.fetch(t, tr.ID, 0, "")
		disposition = resp.Header.Get("Content-Disposition")
	}()

	waitForPipe(t, h, 1)
	sender.supply(t, tr.ID, 0, 0, payload).Body.Close()
	wg.Wait()

	if !strings.Contains(disposition, "attachment") {
		t.Errorf("disposition = %q", disposition)
	}
	// A non-ASCII name must survive through filename*, and the plain filename
	// must stay ASCII for anything that cannot read it.
	if !strings.Contains(disposition, "filename*=UTF-8''") {
		t.Errorf("no RFC 5987 filename in %q", disposition)
	}
	if strings.Contains(strings.SplitN(disposition, "filename*", 2)[0], "é") {
		t.Errorf("the plain filename is not ASCII: %q", disposition)
	}
}

// FR-031: a resumed fetch asks for a range and the sender continues from there.
func TestResumedFetchContinuesFromTheOffset(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	whole := bytes.Repeat([]byte("0123456789"), 1000) // 10 000 bytes
	const resumeAt = 4000

	tr := sender.declare(t, receiver.ID, "big.bin", uint64(len(whole)))

	var resp *http.Response
	var body []byte
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, body = receiver.fetch(t, tr.ID, 0, fmt.Sprintf("bytes=%d-", resumeAt))
	}()

	waitForPipe(t, h, 1)
	sender.supply(t, tr.ID, 0, resumeAt, whole[resumeAt:]).Body.Close()
	wg.Wait()

	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != fmt.Sprintf("bytes %d-%d/%d", resumeAt, len(whole)-1, len(whole)) {
		t.Errorf("Content-Range = %q", got)
	}
	if !bytes.Equal(body, whole[resumeAt:]) {
		t.Errorf("resumed body is %d bytes, want %d", len(body), len(whole)-resumeAt)
	}
}

// A supplier starting at the wrong offset would produce a file with a hole in
// it that still passes a length check.
func TestSupplyAtTheWrongOffsetIsRefusedOverHTTP(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	tr := sender.declare(t, receiver.ID, "file.bin", 1000)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		receiver.fetch(t, tr.ID, 0, "bytes=500-")
	}()

	waitForPipe(t, h, 1)
	resp := sender.supply(t, tr.ID, 0, 0, make([]byte, 1000))
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatal("a supplier at the wrong offset was accepted")
	}
	if body := errorBody(t, resp); body["error"] != "offset_mismatch" {
		t.Errorf("error = %v, want offset_mismatch", body["error"])
	}
	wg.Wait()
}

// A paired device must not reach a transfer between two other devices just by
// holding a valid credential and guessing an identifier.
func TestOutsiderCannotTouchSomeoneElsesTransfer(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()
	outsider := h.pair()

	tr := sender.declare(t, receiver.ID, "private.pdf", 100)

	for _, attempt := range []struct {
		name   string
		method string
		path   string
	}{
		{"read state", "GET", "/api/transfers/" + tr.ID},
		{"fetch content", "GET", "/api/transfers/" + tr.ID + "/items/0/content"},
		{"cancel", "POST", "/api/transfers/" + tr.ID + "/cancel"},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			req, _ := http.NewRequest(attempt.method, h.server.URL+attempt.path, nil) //nolint:noctx // test client
			req.Header.Set("Authorization", "Bearer "+outsider.credential)

			resp, err := h.server.Client().Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				t.Errorf("an outsider reached %s", attempt.path)
			}
			// The answer must be the same as for an unknown transfer, or a
			// caller could enumerate other people's transfers.
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404 so nothing is revealed", resp.StatusCode)
			}
		})
	}
}

// Only the source supplies. Otherwise a paired phone could inject content into
// a transfer between two other devices.
func TestOnlyTheSourceMaySupply(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	tr := sender.declare(t, receiver.ID, "file.bin", 10)

	resp := receiver.supply(t, tr.ID, 0, 0, []byte("injected!!"))
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatal("the receiver was allowed to supply its own content")
	}
}

// FR-028: a transfer larger than the destination is refused before any byte
// moves, and the message carries both numbers.
func TestDeclarationRefusedWhenSpaceIsShort(t *testing.T) {
	h := newHarnessWithSpace(t, 1<<20) // 1 MB free
	sender := h.pair()
	receiver := h.pair()

	resp := sender.do("POST", "/api/transfers", map[string]any{
		"target_device_id": receiver.ID,
		"items":            []map[string]any{{"name": "huge.bin", "size": 1 << 30}},
	})

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}

	body := errorBody(t, resp)
	if body["error"] != "insufficient_space" {
		t.Fatalf("error = %v", body["error"])
	}
	params, _ := body["params"].(map[string]any)
	if params["needed"] == nil || params["available"] == nil {
		t.Errorf("the error does not carry both numbers: %v", body)
	}
}

func TestEmptyDeclarationIsRefused(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	resp := sender.do("POST", "/api/transfers", map[string]any{
		"target_device_id": receiver.ID,
		"items":            []map[string]any{},
	})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		t.Error("a transfer with no files was accepted")
	}
}

func TestTransferToSelfIsRefused(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()

	resp := sender.do("POST", "/api/transfers", map[string]any{
		"target_device_id": sender.ID,
		"items":            []map[string]any{{"name": "f", "size": 1}},
	})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		t.Error("a transfer to itself was accepted")
	}
}

// Cancelling releases a waiting fetch immediately rather than leaving it to
// time out. FR-035.
func TestCancelReleasesAWaitingFetch(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	tr := sender.declare(t, receiver.ID, "file.bin", 1000)

	done := make(chan int, 1)
	go func() {
		resp, _ := receiver.fetch(t, tr.ID, 0, "")
		done <- resp.StatusCode
	}()

	waitForPipe(t, h, 1)
	sender.do("POST", "/api/transfers/"+tr.ID+"/cancel", map[string]any{}).Body.Close()

	select {
	case status := <-done:
		if status == http.StatusOK {
			t.Error("a cancelled fetch returned success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelling did not release the waiting fetch")
	}
}

// transferState reads a transfer's current state.
func (d *device) transferState(t *testing.T, id string) declaredTransfer {
	t.Helper()

	path := "/api/transfers/" + id
	resp := d.do("GET", path, nil)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("state: status %d", resp.StatusCode)
	}

	var out declaredTransfer
	d.open("GET", path, resp, &out)
	return out
}

// waitForPipe blocks until the expected number of demands are registered.
func waitForPipe(t *testing.T, h *harness, want int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.pipes.Waiting() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("expected %d waiting demands, got %d", want, h.pipes.Waiting())
}

// A download is performed by the browser's own manager, which cannot set a
// header. These tests are about the scoped ticket that replaces it, and about
// how narrow its power is.

func (d *device) contentTicket(t *testing.T, transferID string, index int) string {
	t.Helper()

	path := fmt.Sprintf("/api/transfers/%s/items/%d/ticket", transferID, index)
	resp := d.do("POST", path, map[string]any{})
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("ticket: status %d", resp.StatusCode)
	}

	var out struct {
		Ticket string `json:"ticket"`
	}
	d.open("POST", path, resp, &out)
	return out.Ticket
}

func TestContentTicketAuthorizesOneItemOnly(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	payload := []byte("the file")
	tr := sender.declare(t, receiver.ID, "a.bin", uint64(len(payload)))
	other := sender.declare(t, receiver.ID, "b.bin", uint64(len(payload)))

	ticket := receiver.contentTicket(t, tr.ID, 0)

	// The right file: the ticket works, without any credential header.
	var body []byte
	var status int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := h.server.Client().Get( //nolint:noctx // test client
			fmt.Sprintf("%s/api/transfers/%s/items/0/content?ticket=%s", h.server.URL, tr.ID, ticket))
		if err != nil {
			t.Errorf("get: %v", err)
			return
		}
		defer resp.Body.Close()
		status = resp.StatusCode
		body, _ = io.ReadAll(resp.Body)
	}()

	waitForPipe(t, h, 1)
	sender.supply(t, tr.ID, 0, 0, payload).Body.Close()
	wg.Wait()

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !bytes.Equal(body, payload) {
		t.Error("the ticketed download returned the wrong bytes")
	}

	// A different file: the same ticket must not open it. Otherwise it would be
	// a general credential sitting in a URL.
	resp, err := h.server.Client().Get( //nolint:noctx // test client
		fmt.Sprintf("%s/api/transfers/%s/items/0/content?ticket=%s", h.server.URL, other.ID, ticket))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a ticket for one file opened another: status %d", resp.StatusCode)
	}
}

func TestContentWithoutCredentialOrTicketIsRefused(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	tr := sender.declare(t, receiver.ID, "a.bin", 10)

	for _, query := range []string{"", "?ticket=", "?ticket=guessed"} {
		resp, err := h.server.Client().Get( //nolint:noctx // test client
			fmt.Sprintf("%s/api/transfers/%s/items/0/content%s", h.server.URL, tr.ID, query))
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("query %q: status %d, want 401", query, resp.StatusCode)
		}
	}
}

// Revocation means immediately, including for a ticket already minted.
func TestRevocationInvalidatesAContentTicket(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()

	tr := sender.declare(t, receiver.ID, "a.bin", 10)
	ticket := receiver.contentTicket(t, tr.ID, 0)

	sender.do("DELETE", "/api/pairings/"+receiver.ID, nil).Body.Close()

	resp, err := h.server.Client().Get( //nolint:noctx // test client
		fmt.Sprintf("%s/api/transfers/%s/items/0/content?ticket=%s", h.server.URL, tr.ID, ticket))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if body := errorBody(t, resp); body["error"] != "pairing_revoked" {
		t.Errorf("error = %v, want pairing_revoked", body["error"])
	}
}

// An outsider cannot mint a ticket for a transfer it is not part of.
func TestOutsiderCannotMintAContentTicket(t *testing.T) {
	h := newHarness(t)
	sender := h.pair()
	receiver := h.pair()
	outsider := h.pair()

	tr := sender.declare(t, receiver.ID, "private.pdf", 10)

	resp := outsider.do("POST", fmt.Sprintf("/api/transfers/%s/items/0/ticket", tr.ID), map[string]any{})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Error("an outsider minted a ticket for someone else's transfer")
	}
}
