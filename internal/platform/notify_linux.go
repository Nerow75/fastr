//go:build linux

package platform

import (
	"context"
	"fmt"
	"os/exec"
)

// Notifications on Linux go through notify-send, which talks to whatever
// notification daemon the desktop runs over D-Bus.
//
// Running a command rather than speaking D-Bus directly is deliberate. A D-Bus
// client would be a dependency, and research.md holds the project to a
// nine-dependency budget; notify-send ships with libnotify, which every desktop
// environment that has notifications at all already installs. On a machine that
// does not have it — a server, a bare window manager — the absence is detected
// here and reported, and the transfer is unaffected.

// notifySendBinary is the helper libnotify installs.
const notifySendBinary = "notify-send"

func notify(ctx context.Context, n Notification) error {
	path, err := exec.LookPath(notifySendBinary)
	if err != nil {
		return fmt.Errorf("%w: %s is not installed", ErrNotificationsUnavailable, notifySendBinary)
	}

	// Arguments are passed as an argv, never through a shell, so a filename
	// containing a quote or a semicolon is text rather than syntax.
	args := []string{
		"--app-name=fastr",
		// Normal, not critical: a finished transfer is good news, and news that
		// stays on screen until dismissed is an interruption rather than an
		// answer.
		"--urgency=normal",
		"--",
		n.Title,
	}
	if n.Body != "" {
		args = append(args, n.Body)
	}

	// path comes from LookPath, not from input, and the arguments are an argv
	// rather than a shell string, so a filename containing a quote or a
	// semicolon is text. The `--` above stops a name beginning with a dash from
	// being read as a flag.
	//nolint:gosec // fixed binary, argv arguments, no shell
	if out, err := exec.CommandContext(ctx, path, args...).CombinedOutput(); err != nil {
		// A daemon that is absent or refusing is the common case, and it looks
		// like a non-zero exit. Wrapping it keeps the caller's one check honest.
		return fmt.Errorf("%w: %s: %v: %s", ErrNotificationsUnavailable, notifySendBinary, err, out)
	}
	return nil
}
