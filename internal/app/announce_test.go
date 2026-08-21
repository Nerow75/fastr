package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nerow75/fastr/internal/i18n"
	"github.com/Nerow75/fastr/internal/platform"
)

// The notification is one of the few surfaces with no browser to negotiate a
// language with, so what is checked here is that it is translated at all, that
// it says something different for one file than for several, and that a missing
// notification daemon is survivable.

func loadCatalogues(t *testing.T) *i18n.Bundle {
	t.Helper()

	b, err := i18n.Load()
	if err != nil {
		t.Fatalf("load catalogues: %v", err)
	}
	return b
}

func TestReceivedNotificationIsTranslated(t *testing.T) {
	bundle := loadCatalogues(t)

	for _, lang := range []string{"en", "fr"} {
		t.Run(lang, func(t *testing.T) {
			var shown []platform.Notification
			a := &DesktopAnnouncer{
				Bundle:   bundle,
				Language: lang,
				Show: func(n platform.Notification) error {
					shown = append(shown, n)
					return nil
				},
			}

			a.NotifyReceived(1, "holiday.mp4", "/home/me/Downloads")

			if len(shown) != 1 {
				t.Fatalf("raised %d notifications, want 1", len(shown))
			}

			n := shown[0]
			if n.Title == "" || n.Body == "" {
				t.Fatalf("empty notification: %+v", n)
			}
			// A raw key reaching the user is the failure this guards against.
			if strings.HasPrefix(n.Title, "notification.") || strings.HasPrefix(n.Body, "notification.") {
				t.Errorf("an untranslated key was shown: %+v", n)
			}
			if !strings.Contains(n.Title, "holiday.mp4") {
				t.Errorf("the title does not name the file: %q", n.Title)
			}
			if !strings.Contains(n.Body, "/home/me/Downloads") {
				t.Errorf("the body does not say where it went: %q", n.Body)
			}
		})
	}
}

func TestSeveralFilesGetADifferentMessage(t *testing.T) {
	var shown []platform.Notification
	a := &DesktopAnnouncer{
		Bundle: loadCatalogues(t),
		Show: func(n platform.Notification) error {
			shown = append(shown, n)
			return nil
		},
	}

	a.NotifyReceived(1, "one.jpg", "/tmp/received")
	a.NotifyReceived(4, "one.jpg", "/tmp/received")

	if len(shown) != 2 {
		t.Fatalf("raised %d notifications, want 2", len(shown))
	}
	if shown[0].Title == shown[1].Title {
		t.Errorf("one file and four files produced the same title: %q", shown[0].Title)
	}
	if !strings.Contains(shown[1].Title, "4") {
		t.Errorf("the title does not say how many: %q", shown[1].Title)
	}
}

// A desktop with no notification daemon must not cost anyone a transfer.
func TestAMissingNotifierIsSurvivable(t *testing.T) {
	a := &DesktopAnnouncer{
		Bundle: loadCatalogues(t),
		Show: func(platform.Notification) error {
			return platform.ErrNotificationsUnavailable
		},
	}

	// The contract is that this returns rather than panicking or propagating.
	a.NotifyReceived(1, "whatever.bin", "/tmp/received")
}

// Nothing is raised for an empty transfer, and a zero value is inert.
func TestNothingIsRaisedWithoutFiles(t *testing.T) {
	var raised bool
	a := &DesktopAnnouncer{
		Bundle: loadCatalogues(t),
		Show: func(platform.Notification) error {
			raised = true
			return errors.New("should not have been called")
		},
	}

	a.NotifyReceived(0, "", "/tmp/received")
	if raised {
		t.Error("a notification was raised for a transfer with no files")
	}

	var nilAnnouncer *DesktopAnnouncer
	nilAnnouncer.NotifyReceived(1, "x", "/tmp") // must not panic
}
