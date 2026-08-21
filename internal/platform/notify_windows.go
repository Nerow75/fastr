//go:build windows

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Notifications on Windows are WinRT toasts, raised through PowerShell.
//
// The alternative was a balloon from the tray icon, which is deprecated and on
// Windows 10 and later is silently converted into a toast anyway, and would tie
// notifications to a tray that may not have attached. Calling WinRT directly
// would mean a COM binding, which research.md's dependency budget does not have
// room for. PowerShell is present on every supported Windows and can reach the
// same API in a dozen lines.
//
// The text is passed through the environment rather than interpolated into the
// script. A filename is attacker-influenced — it arrives from a paired device —
// and a name containing a quote would otherwise end a string literal and run as
// PowerShell. Reading $env: makes the values data with no escaping to get wrong.

const (
	envToastTitle = "FASTR_TOAST_TITLE"
	envToastBody  = "FASTR_TOAST_BODY"

	// toastAppID is the identifier the toast is shown under. It must be a
	// registered AUMID or nothing appears; PowerShell's own is used because it
	// is the process actually raising it, and registering one of our own would
	// mean writing to the registry at install time for a cosmetic gain.
	toastAppID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`
)

// toastScript builds and shows a two-line toast. It reads its text from the
// environment; see the note above on why.
const toastScript = `
$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] > $null
[Windows.UI.Notifications.ToastNotification, Windows.UI.Notifications, ContentType=WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom, ContentType=WindowsRuntime] > $null

$template = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent(
    [Windows.UI.Notifications.ToastTemplateType]::ToastText02)

$texts = $template.GetElementsByTagName('text')
$texts.Item(0).AppendChild($template.CreateTextNode($env:FASTR_TOAST_TITLE)) > $null
if ($env:FASTR_TOAST_BODY) {
    $texts.Item(1).AppendChild($template.CreateTextNode($env:FASTR_TOAST_BODY)) > $null
}

$toast = [Windows.UI.Notifications.ToastNotification]::new($template)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($env:FASTR_TOAST_APPID).Show($toast)
`

const envToastAppID = "FASTR_TOAST_APPID"

func notify(ctx context.Context, n Notification) error {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		return fmt.Errorf("%w: powershell is not available", ErrNotificationsUnavailable)
	}

	cmd := exec.CommandContext(ctx, path,
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-ExecutionPolicy", "Bypass", "-Command", toastScript)

	cmd.Env = append(os.Environ(),
		envToastTitle+"="+n.Title,
		envToastBody+"="+n.Body,
		envToastAppID+"="+toastAppID,
	)

	if out, err := cmd.CombinedOutput(); err != nil {
		// Notifications switched off in Focus Assist, or a policy blocking the
		// script, both surface as a non-zero exit. Neither is worth failing a
		// transfer over.
		return fmt.Errorf("%w: powershell: %v: %s", ErrNotificationsUnavailable, err, out)
	}
	return nil
}
