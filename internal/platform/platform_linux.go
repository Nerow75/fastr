//go:build linux

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type linuxPlatform struct{}

// Current returns the running platform.
func Current() Platform { return linuxPlatform{} }

func (linuxPlatform) OS() OS { return Linux }

// ConfigDir follows the XDG base directory specification, so settings land
// where a Linux user expects and never in the install directory.
func (linuxPlatform) ConfigDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "fastr"), nil
}

func (linuxPlatform) DataDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "fastr"), nil
}

// DefaultReceiveDir prefers the user's Downloads folder, honouring a localized
// name from user-dirs.dirs when present, and falls back to ~/fastr.
func (p linuxPlatform) DefaultReceiveDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if d := xdgUserDir(home, "XDG_DOWNLOAD_DIR"); d != "" {
		return filepath.Join(d, "fastr"), nil
	}
	return filepath.Join(home, "fastr"), nil
}

// xdgUserDir reads a single entry out of user-dirs.dirs. A missing or malformed
// file is not an error; the caller falls back.
func xdgUserDir(home, key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	data, err := os.ReadFile(filepath.Join(cfg, "user-dirs.dirs"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, key+"=") {
			continue
		}
		v := strings.Trim(strings.TrimPrefix(line, key+"="), `"`)
		v = strings.Replace(v, "$HOME", home, 1)
		if v != "" && v != home {
			return v
		}
	}
	return ""
}

// FreeSpace reports bytes available to an unprivileged user, which is Bavail
// rather than Bfree: the difference is the reserved blocks only root can use,
// and promising those to a transfer would fail it partway.
func (linuxPlatform) FreeSpace(path string) (uint64, error) {
	var st unix.Statfs_t
	target := existingAncestor(path)
	if err := unix.Statfs(target, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", target, err)
	}
	//nolint:gosec // Bsize is a filesystem block size; it is never negative.
	return st.Bavail * uint64(st.Bsize), nil
}

// existingAncestor walks up until it finds a path that exists, so free space
// can be checked for a receive folder that has not been created yet.
func existingAncestor(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

const autostartEntry = "fastr.desktop"

// SetAutostart writes a freedesktop autostart entry. FR-049: off by default and
// always removable, so the user is never silently opted in.
func (p linuxPlatform) SetAutostart(enabled bool, executable string) error {
	dir, err := autostartDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, autostartEntry)

	if !enabled {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entry := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=fastr\n" +
		"Comment=Local network file transfer\n" +
		"Exec=" + executable + " --background\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	return os.WriteFile(path, []byte(entry), 0o644)
}

func (p linuxPlatform) AutostartEnabled() (bool, error) {
	dir, err := autostartDir()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(dir, autostartEntry))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func autostartDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "autostart"), nil
}
