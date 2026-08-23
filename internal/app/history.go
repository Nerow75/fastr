package app

import (
	"github.com/Nerow75/fastr/internal/store"
)

// History, per FR-037 and FR-039.
//
// A transfer that ended leaves a record saying what moved, with which device,
// when, and how it turned out. That is not bookkeeping for its own sake: the
// question this answers is "did last night's transfer actually finish", and
// without an answer the only way to find out is to go looking through folders.
//
// Two decisions are worth stating, because both are the opposite of what a
// logging habit would produce:
//
//   - **Only terminal transfers are recorded.** A history entry is written once,
//     when a transfer reaches completed, failed, or cancelled. Nothing here
//     tracks progress; that is what the transfer record is for, and duplicating
//     it would give the user two sources that could disagree.
//   - **The user can erase all of it** (FR-039), with no exception kept back
//     for diagnostics. A history nobody can clear is a record of someone's
//     files that they did not ask to keep.

// DefaultHistoryLimit is how many entries are returned when nobody says.
//
// A hundred is roughly a screen's worth of scrolling and covers weeks of
// ordinary use. The store returns them newest first, so a limit truncates the
// distant past rather than the part anyone is looking for.
const DefaultHistoryLimit = 100

// MaxHistoryLimit bounds what a caller may ask for, because the parameter comes
// from a peer and an unbounded read is an unbounded allocation.
const MaxHistoryLimit = 1000

// History returns recent entries, newest first.
func (t *Transfers) History(limit int) ([]store.HistoryEntry, error) {
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	if limit > MaxHistoryLimit {
		limit = MaxHistoryLimit
	}

	entries, err := t.Store.History(limit)
	if err != nil {
		return nil, Errorf(CodeInternal, err)
	}
	return entries, nil
}

// ClearHistory erases every entry. FR-039.
func (t *Transfers) ClearHistory() error {
	if err := t.Store.ClearHistory(); err != nil {
		return Errorf(CodeInternal, err)
	}
	t.Log.Info("history cleared")
	return nil
}
