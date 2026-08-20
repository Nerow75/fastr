package transfer

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// The pipe joins a phone fetching bytes to a desktop tab supplying them, with
// nothing written to disk in between. These tests are about what happens when
// one side is not there.

func TestPipeStreamsFromSupplierToReceiver(t *testing.T) {
	pipes := NewPipes()
	key := Key{TransferID: "t1", ItemIndex: 0}
	payload := bytes.Repeat([]byte("stream me "), 5000)

	var received bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		reader, finish, err := pipes.Await(key, 0, nil)
		if err != nil {
			t.Errorf("Await: %v", err)
			return
		}
		_, copyErr := io.Copy(&received, reader)
		finish(copyErr)
	}()

	// Give the receiver a moment to register its demand.
	waitFor(t, func() bool { return pipes.Waiting() == 1 })

	if err := pipes.Supply(key, 0, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Supply: %v", err)
	}
	wg.Wait()

	if !bytes.Equal(received.Bytes(), payload) {
		t.Errorf("received %d bytes, want %d", received.Len(), len(payload))
	}
	if pipes.Waiting() != 0 {
		t.Errorf("%d demands leaked", pipes.Waiting())
	}
}

// The sender's request must not return before the bytes have reached the
// receiver, or the desktop would report a file as sent while it was in flight.
func TestSupplyBlocksUntilTheCopyFinishes(t *testing.T) {
	pipes := NewPipes()
	key := Key{TransferID: "t1", ItemIndex: 0}

	release := make(chan struct{})
	go func() {
		reader, finish, err := pipes.Await(key, 0, nil)
		if err != nil {
			t.Errorf("Await: %v", err)
			return
		}
		<-release // hold the copy open
		_, _ = io.Copy(io.Discard, reader)
		finish(nil)
	}()

	waitFor(t, func() bool { return pipes.Waiting() == 1 })

	done := make(chan error, 1)
	go func() { done <- pipes.Supply(key, 0, bytes.NewReader([]byte("payload"))) }()

	select {
	case <-done:
		t.Fatal("Supply returned before the receiver had read anything")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-done; err != nil {
		t.Errorf("Supply: %v", err)
	}
}

// The sending tab is closed, or the machine is asleep. The phone must be told,
// not left hanging.
func TestAwaitTimesOutWithoutASupplier(t *testing.T) {
	pipes := NewPipes()
	key := Key{TransferID: "t1", ItemIndex: 0}

	// Shorten the wait by cancelling, which is the same path a dropped
	// connection takes.
	cancel := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(cancel)
	}()

	_, _, err := pipes.Await(key, 0, cancel)
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
	if pipes.Waiting() != 0 {
		t.Errorf("a cancelled demand leaked: %d waiting", pipes.Waiting())
	}
}

func TestSupplyWithoutADemandIsRefused(t *testing.T) {
	pipes := NewPipes()
	err := pipes.Supply(Key{TransferID: "t1", ItemIndex: 0}, 0, bytes.NewReader(nil))
	if !errors.Is(err, ErrNoDemand) {
		t.Errorf("err = %v, want ErrNoDemand", err)
	}
}

// A supplier starting at the wrong offset would produce a file with a hole in
// it that still passes a length check.
func TestSupplyAtTheWrongOffsetIsRefused(t *testing.T) {
	pipes := NewPipes()
	key := Key{TransferID: "t1", ItemIndex: 0}

	failed := make(chan error, 1)
	go func() {
		_, _, err := pipes.Await(key, 1000, nil)
		failed <- err
	}()

	waitFor(t, func() bool { return pipes.Waiting() == 1 })

	err := pipes.Supply(key, 0, bytes.NewReader([]byte("from the start")))
	if !errors.Is(err, ErrOffsetMismatch) {
		t.Errorf("Supply = %v, want ErrOffsetMismatch", err)
	}
	if err := <-failed; err == nil {
		t.Error("the receiver was not released after the mismatch")
	}
}

// A resumed fetch supersedes the stale one rather than both waiting forever.
func TestSecondDemandSupersedesTheFirst(t *testing.T) {
	pipes := NewPipes()
	key := Key{TransferID: "t1", ItemIndex: 0}

	first := make(chan error, 1)
	go func() {
		_, _, err := pipes.Await(key, 0, nil)
		first <- err
	}()
	waitFor(t, func() bool { return pipes.Waiting() == 1 })

	second := make(chan struct{})
	go func() {
		reader, finish, err := pipes.Await(key, 500, nil)
		if err != nil {
			t.Errorf("second Await: %v", err)
			close(second)
			return
		}
		_, _ = io.Copy(io.Discard, reader)
		finish(nil)
		close(second)
	}()

	// The first demand must be released rather than left hanging.
	select {
	case err := <-first:
		if err == nil {
			t.Error("the superseded demand returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("the superseded demand was never released")
	}

	waitFor(t, func() bool {
		offset, ok := pipes.PendingOffset(key)
		return ok && offset == 500
	})

	if err := pipes.Supply(key, 500, bytes.NewReader([]byte("resumed"))); err != nil {
		t.Fatalf("Supply: %v", err)
	}
	<-second
}

func TestCancelReleasesAWaitingDemand(t *testing.T) {
	pipes := NewPipes()
	key := Key{TransferID: "t1", ItemIndex: 0}

	result := make(chan error, 1)
	go func() {
		_, _, err := pipes.Await(key, 0, nil)
		result <- err
	}()
	waitFor(t, func() bool { return pipes.Waiting() == 1 })

	pipes.Cancel(key)

	select {
	case err := <-result:
		if err == nil {
			t.Error("a cancelled demand returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("Cancel did not release the demand")
	}
	if pipes.Waiting() != 0 {
		t.Errorf("%d demands leaked after cancel", pipes.Waiting())
	}
}

// The caller is told a receiver is waiting, so it can ask the sender to supply.
func TestOnDemandFires(t *testing.T) {
	pipes := NewPipes()
	key := Key{TransferID: "t1", ItemIndex: 3}

	var gotKey Key
	var gotOffset uint64
	fired := make(chan struct{})
	pipes.OnDemand = func(k Key, offset uint64) {
		gotKey, gotOffset = k, offset
		close(fired)
	}

	go func() { _, _, _ = pipes.Await(key, 4096, nil) }()

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("OnDemand never fired")
	}

	if gotKey != key || gotOffset != 4096 {
		t.Errorf("OnDemand got %v at %d, want %v at 4096", gotKey, gotOffset, key)
	}
	pipes.Cancel(key)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was never met")
}
