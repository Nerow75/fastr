package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// The event stream, per contracts/http-api.md.
//
// Server-sent events rather than a bidirectional socket: EventSource works
// outside a secure browser context, which the mobile page always is in simple
// mode, and it reconnects on its own. Commands are infrequent and travel as
// ordinary requests.

// EventType names what happened.
type EventType string

const (
	EventDeviceAppeared    EventType = "device_appeared"
	EventDeviceLost        EventType = "device_lost"
	EventPairingPending    EventType = "pairing_pending"
	EventPairingChanged    EventType = "pairing_changed"
	EventTransferQueued    EventType = "transfer_queued"
	EventTransferStarted   EventType = "transfer_started"
	EventTransferProgress  EventType = "transfer_progress"
	EventTransferInterrupt EventType = "transfer_interrupted"
	EventTransferResumed   EventType = "transfer_resumed"
	EventTransferCompleted EventType = "transfer_completed"
	EventTransferFailed    EventType = "transfer_failed"
	EventTransferCancelled EventType = "transfer_cancelled"
	EventQueueChanged      EventType = "queue_changed"
	EventSweepRemoved      EventType = "sweep_removed"
)

// Event is one notification.
type Event struct {
	Type       EventType `json:"type"`
	DeviceID   string    `json:"device_id,omitempty"`
	TransferID string    `json:"transfer_id,omitempty"`
	Payload    any       `json:"payload,omitempty"`
	// Announce tells the client whether to speak this event to assistive
	// technology. It travels on the wire so the rule has one definition rather
	// than one here and a second one in the browser that can drift from it.
	Announce bool `json:"announce"`
}

// Announced reports whether assistive technology should speak this event.
//
// FR-039i: progress is deliberately absent. A running transfer emits several
// events a second, and announcing each would flood a screen reader user with
// noise until the transfer finished. Start, interruption, resumption,
// completion, and failure are the moments that carry meaning.
func (e Event) Announced() bool {
	switch e.Type {
	case EventTransferStarted, EventTransferInterrupt, EventTransferResumed,
		EventTransferCompleted, EventTransferFailed:
		return true
	}
	return false
}

// progressInterval throttles progress events to at most four per second per
// transfer. Faster tells the user nothing a human eye can read, and costs
// battery on the phone.
const progressInterval = 250 * time.Millisecond

// subscriberBuffer is how many events a slow client may fall behind before it
// is dropped. A phone that has locked its screen must not be able to stall the
// transfer engine by not reading its event stream.
const subscriberBuffer = 64

type subscriber struct {
	ch     chan Event
	closed chan struct{}
}

// Events fans notifications out to connected clients.
type Events struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}

	// lastProgress is the last time a progress event was emitted per transfer.
	lastProgress map[string]time.Time
	now          func() time.Time
}

// NewEvents returns an empty bus.
func NewEvents() *Events {
	return &Events{
		subs:         make(map[*subscriber]struct{}),
		lastProgress: make(map[string]time.Time),
		now:          time.Now,
	}
}

// SetClock replaces the time source, for tests.
func (e *Events) SetClock(now func() time.Time) { e.now = now }

// Publish delivers an event to every subscriber.
//
// Progress events are throttled per transfer. Everything else passes straight
// through: a completion or a failure is never dropped, because it is the event
// that tells the user the transfer ended.
func (e *Events) Publish(ev Event) {
	ev.Announce = ev.Announced()

	e.mu.Lock()

	if ev.Type == EventTransferProgress {
		now := e.now()
		last, seen := e.lastProgress[ev.TransferID]
		if seen && now.Sub(last) < progressInterval {
			e.mu.Unlock()
			return
		}
		e.lastProgress[ev.TransferID] = now
	}

	if ev.Type == EventTransferCompleted || ev.Type == EventTransferFailed ||
		ev.Type == EventTransferCancelled {
		delete(e.lastProgress, ev.TransferID)
	}

	targets := make([]*subscriber, 0, len(e.subs))
	for s := range e.subs {
		targets = append(targets, s)
	}
	e.mu.Unlock()

	for _, s := range targets {
		select {
		case s.ch <- ev:
		case <-s.closed:
		default:
			// The subscriber is not keeping up. Dropping its event is the right
			// trade: blocking here would let a backgrounded phone stall the
			// whole application.
		}
	}
}

// Subscribe registers a client and returns its channel plus an unsubscribe.
func (e *Events) Subscribe() (<-chan Event, func()) {
	s := &subscriber{ch: make(chan Event, subscriberBuffer), closed: make(chan struct{})}

	e.mu.Lock()
	e.subs[s] = struct{}{}
	e.mu.Unlock()

	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			e.mu.Lock()
			delete(e.subs, s)
			e.mu.Unlock()
			close(s.closed)
		})
	}
}

// Subscribers reports how many clients are connected.
func (e *Events) Subscribers() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.subs)
}

// heartbeat keeps intermediaries from closing an idle stream, and lets the
// client notice a dead connection rather than waiting forever.
const heartbeat = 25 * time.Second

// handleEvents streams events to one client.
func (d Deps) handleEvents(_ *Session, w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		d.writeError(w, r, fmt.Errorf("streaming unsupported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Without this, a reverse proxy in front of the machine may buffer the
	// stream and progress would arrive in bursts at the end.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, unsubscribe := d.Events.Subscribe()
	defer unsubscribe()

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return

		case ev := <-events:
			payload, err := json.Marshal(ev)
			if err != nil {
				d.Log.Error("encode event", "type", string(ev.Type), "error", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, payload); err != nil {
				return
			}
			flusher.Flush()

		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
