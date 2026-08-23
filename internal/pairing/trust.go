package pairing

import (
	"time"

	"github.com/Nerow75/fastr/internal/store"
)

// The acceptance decision, per FR-016a to FR-016d.
//
// Pairing answers "may this device talk to me at all". Trust answers a
// narrower question that comes up on every single transfer: **may this device
// write to my disk without anyone looking?**
//
// The two are deliberately separate. A pairing lasts a year, and over a year
// the answer to the second question changes: a phone that was mine becomes a
// phone I lent to someone, a laptop I paired at a friend's house is still
// paired the next time I am on their network. So the trust mode is a per-device
// switch the user can flip at any time, it is visible wherever devices are
// listed (FR-016c), and it takes effect on the next transfer rather than on the
// next pairing.
//
// Nothing here decides anything about *reading*. A device can never fetch what
// it was not sent, in any trust mode; this is only about what arrives unasked.

// AcceptanceWindow is how long an ask-mode transfer waits for a human.
//
// Two minutes, which is roughly how long it takes to walk to another room and
// look at a screen. FR-016d requires a bound rather than an indefinite wait:
// a transfer that sits in the queue forever holds the sender's attention and,
// worse, holds a place in a queue that only runs one thing at a time.
const AcceptanceWindow = 2 * time.Minute

// Decision is what should happen to an incoming transfer before it may run.
type Decision int

const (
	// Start means the transfer may begin immediately.
	Start Decision = iota
	// Ask means a human on this device has to say yes first.
	Ask
	// Refuse means the device may not send at all.
	Refuse
)

// Decide answers whether an incoming transfer may start, must wait for a human,
// or must be refused outright.
//
// now is a parameter so expiry is decided against one instant rather than a
// clock read twice, and so tests can exercise a year of inactivity.
//
// The default when anything is unclear is Refuse. A missing pairing, a revoked
// one, an expired one: all of them mean this device has no standing here, and
// the safe answer to "should these bytes land on the user's disk" is no.
func Decide(p store.Pairing, err error, now time.Time) Decision {
	if err != nil || p.DeviceID == "" {
		return Refuse
	}
	if !p.Active(now) {
		return Refuse
	}
	if p.AcceptsAutomatically() {
		return Start
	}
	return Ask
}

// AcceptanceExpired reports whether an ask-mode transfer has waited too long.
//
// Measured from when it was queued, because that is when the person on the
// other device started waiting for an answer.
func AcceptanceExpired(queuedAt, now time.Time) bool {
	return now.Sub(queuedAt) >= AcceptanceWindow
}
