package app

import (
	"errors"
	"log/slog"

	"github.com/Nerow75/fastr/internal/i18n"
	"github.com/Nerow75/fastr/internal/platform"
)

// Telling the user that files arrived, per FR-036.
//
// This is one of the few surfaces the browser cannot reach. A transfer that
// completes while every page is closed would otherwise finish in silence, and
// the user would find the files only by going to look.
//
// It is translated through the same catalogue the web application uses, so a
// translator edits one file and both follow (FR-039a). The language is the
// user's setting, or English: there is no browser to negotiate with here, which
// is exactly why this package exists rather than the message being assembled in
// a handler.

// DesktopAnnouncer shows native notifications.
type DesktopAnnouncer struct {
	Bundle *i18n.Bundle
	// Language is the user's setting. Empty falls back to English, because a
	// native surface has no Accept-Language to negotiate from.
	Language string
	Log      *slog.Logger

	// Show is the mechanism, injected so tests do not raise real notifications
	// and so a headless run can silence them. Nil means platform.Notify.
	Show func(platform.Notification) error
}

// NotifyReceived says that a transfer landed.
//
// folder is the receive folder, which the user configured and already knows;
// no other path is ever included, and file content never is.
func (a *DesktopAnnouncer) NotifyReceived(itemCount int, firstName, folder string) {
	if a == nil || a.Bundle == nil || itemCount <= 0 {
		return
	}

	lang := a.Language
	if lang == "" {
		lang = i18n.DefaultLanguage
	}

	title := a.Bundle.T(lang, "notification.received.one",
		map[string]any{"name": firstName})
	if itemCount > 1 {
		title = a.Bundle.T(lang, "notification.received.many",
			map[string]any{"count": itemCount})
	}

	a.show(platform.Notification{
		Title: title,
		Body:  a.Bundle.T(lang, "notification.received.body", map[string]any{"folder": folder}),
	})
}

// NotifySwept says that the retention sweep removed something.
//
// FR-034 requires the user to be told, and the sweep runs at startup, which is
// exactly when no page is open to be told through. It carries a count and the
// reason, and nothing else: a sweep can take a dozen unrelated things, and the
// interface has the list. The byte total is deliberately absent, because
// formatting it here would mean a second size formatter that disagrees with the
// browser's on the same number.
func (a *DesktopAnnouncer) NotifySwept(count int) {
	if a == nil || a.Bundle == nil || count <= 0 {
		return
	}

	lang := a.Language
	if lang == "" {
		lang = i18n.DefaultLanguage
	}

	a.show(platform.Notification{
		Title: a.Bundle.T(lang, "notification.swept.title", map[string]any{"count": count}),
		Body:  a.Bundle.T(lang, "notification.swept.body", nil),
	})
}

func (a *DesktopAnnouncer) show(n platform.Notification) {
	show := a.Show
	if show == nil {
		show = platform.Notify
	}

	if err := show(n); err != nil {
		// A desktop with no notification daemon, or notifications switched off.
		// The transfer succeeded either way, and saying so louder than debug
		// would turn a cosmetic gap into an apparent failure.
		if a.Log == nil {
			return
		}
		if errors.Is(err, platform.ErrNotificationsUnavailable) {
			a.Log.Debug("desktop notification", "error", err)
			return
		}
		a.Log.Warn("desktop notification", "error", err)
	}
}
