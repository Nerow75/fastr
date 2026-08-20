package httpapi

import (
	"github.com/Nerow75/fastr/internal/store"
)

// eventNotifier adapts the event bus to what the transfer service needs.
//
// The service publishes by name so it does not depend on the transport; this is
// the one place those names become event types.
type eventNotifier struct{ events *Events }

// NewNotifier returns a notifier backed by the event bus.
func NewNotifier(events *Events) *eventNotifier { //nolint:revive // deliberately unexported type
	return &eventNotifier{events: events}
}

func (n *eventNotifier) NotifyTransfer(kind string, transferID store.ID, payload map[string]any) {
	n.events.Publish(Event{
		Type:       EventType(kind),
		TransferID: transferID.String(),
		Payload:    payload,
	})
}
