//go:build windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type windowsPlatform struct{}

// Current returns the running platform.
func Current() Platform { return windowsPlatform{} }

func (windowsPlatform) OS() OS { return Windows }

// ConfigDir and DataDir both sit under LOCALAPPDATA. Roaming is deliberately
// avoided: the store holds pairings and staging data bound to this machine, and
// syncing them to another would be both wrong and slow.
func (windowsPlatform) ConfigDir() (string, error) {
	base, err := localAppData()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "fastr", "config"), nil
}

func (windowsPlatform) DataDir() (string, error) {
	base, err := localAppData()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "fastr", "data"), nil
}

func localAppData() (string, error) {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "AppData", "Local"), nil
}

// DefaultReceiveDir uses the Downloads known folder, which respects a user who
// has moved it, and falls back to the profile root.
func (windowsPlatform) DefaultReceiveDir() (string, error) {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0); err == nil && p != "" {
		return filepath.Join(p, "fastr"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "fastr"), nil
}

// FreeSpace reports bytes available to the calling user, which respects disk
// quotas, rather than total free bytes on the volume.
func (windowsPlatform) FreeSpace(path string) (uint64, error) {
	target := existingAncestor(path)
	p, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return 0, err
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &totalFree); err != nil {
		return 0, fmt.Errorf("GetDiskFreeSpaceEx %s: %w", target, err)
	}
	return freeToCaller, nil
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

const (
	runKey       = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "fastr"
)

// SetAutostart writes a per-user Run entry. HKCU rather than HKLM: this is the
// user's choice for their own session, and it needs no elevation.
func (windowsPlatform) SetAutostart(enabled bool, executable string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = k.Close() }()

	if !enabled {
		err := k.DeleteValue(runValueName)
		if err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	return k.SetStringValue(runValueName, `"`+executable+`" --background`)
}

func (windowsPlatform) AutostartEnabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = k.Close() }()

	_, _, err = k.GetStringValue(runValueName)
	if err == registry.ErrNotExist {
		return false, nil
	}
	return err == nil, err
}
