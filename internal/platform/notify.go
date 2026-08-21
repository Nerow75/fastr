package platform

import (
	"context"
	"errors"
	"time"
)

// Desktop notifications.
//
// A transfer that finishes while no page is open has no other way to be
// noticed. That is the whole job: tell the user, once, that files landed.
//
// Principle IV requires the same behaviour on both platforms, and it holds in
// the way that matters — a notification appears, carrying the same text from
// the same catalogue — but not in the mechanism, which is irreducibly
// different: a D-Bus service on Linux, a WinRT toast on Windows. Both are
// reached by running a command rather than by binding a library, so the
// dependency budget in research.md is untouched and a missing mechanism is an
// error to handle rather than a link failure at build time.
//
// **Absence is normal.** A minimal Linux desktop may ship no notification
// daemon at all, and a Windows install may have them switched off. FR-036 wants
// the user told, but a missing notifier must never cost anyone a transfer, so
// every failure here is reported and logged, never propagated into the transfer
// path. This is the same posture as the tray in tray.go, for the same reason.

// ErrNotificationsUnavailable reports that this system has no way to show one.
var ErrNotificationsUnavailable = errors.New("no desktop notification mechanism available")

// notifyTimeout bounds the helper process. A notification is worth a moment and
// not a second more: it is already too late to be useful if the user has walked
// away, and a hung helper must not accumulate.
const notifyTimeout = 5 * time.Second

// Notification is what the user sees.
type Notification struct {
	// Title is the one line they will actually read.
	Title string
	// Body is the detail underneath it. It may be empty.
	Body string
}

// Notify shows a desktop notification, or reports why it could not.
//
// It never carries file content and never carries a path outside the receive
// folder, both of which would end up in a notification history the user did not
// choose to keep.
func Notify(n Notification) error {
	if n.Title == "" {
		return errors.New("notification has no title")
	}

	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()

	return notify(ctx, n)
}
