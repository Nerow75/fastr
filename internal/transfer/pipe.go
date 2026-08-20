package transfer

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// The direct pipe.
//
// A browser never reveals a file's path, so the desktop page cannot tell the
// server "send /home/me/video.mkv". It holds a File object instead. And a phone
// is a browser too, so it cannot accept an inbound connection: it has to come
// and fetch.
//
// The two halves are joined here rather than through the disk. The phone's GET
// registers a demand and waits; the desktop's POST attaches its body as the
// supply; the bytes stream from one to the other through a fixed buffer and are
// never written anywhere.
//
// The alternative was staging the file on the sender first, which would mean
// needing 10 GB free to send a 10 GB file that is already on that disk, and
// taking twice as long. The cost of this choice is that the desktop tab must
// stay open while the transfer runs, and that resuming needs both sides to
// agree on an offset. Both are handled below.

// SupplyTimeout bounds how long a waiting demand holds a connection open.
//
// Long enough for the desktop to notice the event and start reading a file,
// short enough that a closed tab does not leave the phone hanging with no
// explanation.
const SupplyTimeout = 30 * time.Second

// Errors the pipe can produce.
var (
	// ErrNoSupplier means nobody attached in time: the sending tab is closed,
	// or the machine is asleep.
	ErrNoSupplier = errors.New("the sending device did not respond")
	// ErrNoDemand means a supplier arrived for something nobody is fetching.
	ErrNoDemand = errors.New("no transfer is waiting for this content")
	// ErrOffsetMismatch means the supplier started at the wrong place, which
	// would silently produce a file with a hole in it.
	ErrOffsetMismatch = errors.New("supplier offset does not match the demand")
)

// Key identifies one item of one transfer.
type Key struct {
	TransferID string
	ItemIndex  int
}

func (k Key) String() string { return fmt.Sprintf("%s#%d", k.TransferID, k.ItemIndex) }

// supply is what a sender attaches.
type supply struct {
	body   io.Reader
	offset uint64
}

// demand is a receiver waiting for bytes.
type demand struct {
	key    Key
	offset uint64

	// ready carries the supplier's reader to the waiting receiver.
	ready chan supply
	// done carries the outcome back to the supplier, so its request does not
	// return until the bytes have actually reached the receiver.
	done chan error

	once sync.Once
}

// close releases a demand exactly once, whichever side gives up first.
func (d *demand) close(err error) {
	d.once.Do(func() {
		select {
		case d.done <- err:
		default:
		}
		close(d.ready)
	})
}

// Pipes is the rendezvous between receivers and senders.
type Pipes struct {
	mu      sync.Mutex
	waiting map[string]*demand

	// OnDemand is called when a receiver starts waiting, so the caller can
	// tell the sender to supply. It must not block.
	OnDemand func(key Key, offset uint64)
}

// NewPipes returns an empty rendezvous.
func NewPipes() *Pipes {
	return &Pipes{waiting: make(map[string]*demand)}
}

// Await registers a demand and blocks until a supplier attaches.
//
// It returns the supplier's reader and a function to call once the copy is
// finished, which releases the supplier's request with the outcome.
func (p *Pipes) Await(key Key, offset uint64, cancel <-chan struct{}) (io.Reader, func(error), error) {
	d := &demand{
		key:    key,
		offset: offset,
		ready:  make(chan supply, 1),
		done:   make(chan error, 1),
	}

	p.mu.Lock()
	if existing, ok := p.waiting[key.String()]; ok {
		// A second fetch for the same item. The first is stale, most likely a
		// dropped connection the operating system has not reaped yet.
		existing.close(errors.New("superseded by a newer request"))
	}
	p.waiting[key.String()] = d
	onDemand := p.OnDemand
	p.mu.Unlock()

	if onDemand != nil {
		onDemand(key, offset)
	}

	timeout := time.NewTimer(SupplyTimeout)
	defer timeout.Stop()

	select {
	case s, ok := <-d.ready:
		if !ok {
			return nil, nil, ErrNoSupplier
		}
		return s.body, func(err error) { d.close(err) }, nil

	case <-cancel:
		p.remove(key, d)
		d.close(errors.New("the receiving device went away"))
		return nil, nil, ErrCancelled

	case <-timeout.C:
		p.remove(key, d)
		d.close(ErrNoSupplier)
		return nil, nil, ErrNoSupplier
	}
}

// Supply attaches a sender's body to a waiting receiver and blocks until the
// copy finishes.
//
// Blocking is deliberate: the sender's request must not return before the bytes
// have reached the receiver, or the desktop would report a file as sent while
// it was still in flight.
func (p *Pipes) Supply(key Key, offset uint64, body io.Reader) error {
	p.mu.Lock()
	d, ok := p.waiting[key.String()]
	if ok {
		delete(p.waiting, key.String())
	}
	p.mu.Unlock()

	if !ok {
		return ErrNoDemand
	}

	// A supplier starting at the wrong offset would produce a file with a hole
	// in it that still passes a length check. Refusing is the only safe answer.
	if offset != d.offset {
		d.close(ErrOffsetMismatch)
		return fmt.Errorf("%w: demand wants %d, supplier offered %d",
			ErrOffsetMismatch, d.offset, offset)
	}

	select {
	case d.ready <- supply{body: body, offset: offset}:
	default:
		return ErrNoDemand // the receiver gave up between the lookup and here
	}

	return <-d.done
}

// PendingOffset reports what a waiting receiver is asking for, so the sender
// can seek to it. Returns false when nothing is waiting.
func (p *Pipes) PendingOffset(key Key) (uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	d, ok := p.waiting[key.String()]
	if !ok {
		return 0, false
	}
	return d.offset, true
}

// Cancel releases a waiting demand, for a transfer the user stopped.
func (p *Pipes) Cancel(key Key) {
	p.mu.Lock()
	d, ok := p.waiting[key.String()]
	if ok {
		delete(p.waiting, key.String())
	}
	p.mu.Unlock()

	if ok {
		d.close(ErrCancelled)
	}
}

// Waiting reports how many demands are outstanding. Tests use it to check that
// they do not leak.
func (p *Pipes) Waiting() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.waiting)
}

// remove drops a demand only if it is still the current one, so a superseded
// demand cannot delete its replacement.
func (p *Pipes) remove(key Key, d *demand) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if current, ok := p.waiting[key.String()]; ok && current == d {
		delete(p.waiting, key.String())
	}
}
