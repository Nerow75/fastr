// Package config owns user settings: their defaults, their validation, and
// their persistence.
//
// Settings live in a JSON file rather than in the bbolt store, unlike every
// other record in data-model.md. The reason is that a user who has bound fastr
// to the wrong interface, or pointed the receive folder somewhere unfortunate,
// must be able to fix it with a text editor while the application will not
// start. A binary store would make that a support conversation.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Nerow75/fastr/internal/platform"
)

// Settings is the complete user-configurable state.
type Settings struct {
	// DeviceName is what other devices display. Defaults to the hostname.
	DeviceName string `json:"device_name"`

	// ReceiveFolder is where incoming files are written. FR-023: user
	// configurable, defaulting outside system folders.
	ReceiveFolder string `json:"receive_folder"`

	// StagingFolder holds partial and relayed data. Never equal to
	// ReceiveFolder: FR-055 requires relayed bytes to be structurally
	// incapable of appearing as received files.
	StagingFolder string `json:"staging_folder"`

	// Language forces an interface language. Empty means negotiate from the
	// browser, per FR-039b.
	Language string `json:"language,omitempty"`

	// Autostart starts fastr with the user's session. FR-049: off by default,
	// never enabled without the user choosing it.
	Autostart bool `json:"autostart"`

	// BoundInterfaces lists the interfaces the server may listen on. Empty
	// means every non-loopback interface on the local network. FR-001 is
	// enforced elsewhere: nothing listens until the user starts the server.
	BoundInterfaces []string `json:"bound_interfaces,omitempty"`

	// Port is the listening port. Zero asks the operating system to choose.
	Port int `json:"port"`
}

const (
	// MaxDeviceNameLen matches the Device rules in data-model.md.
	MaxDeviceNameLen = 64
	settingsFile     = "settings.json"
)

// ErrNotConfigured is returned by Load when no settings file exists yet.
var ErrNotConfigured = errors.New("no settings file")

// Defaults builds the initial settings for this machine.
func Defaults(p platform.Platform) (Settings, error) {
	receive, err := p.DefaultReceiveDir()
	if err != nil {
		return Settings{}, fmt.Errorf("default receive folder: %w", err)
	}

	data, err := p.DataDir()
	if err != nil {
		return Settings{}, fmt.Errorf("data directory: %w", err)
	}

	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		name = "fastr"
	}

	return Settings{
		DeviceName: truncateName(name),
		// Staging sits under the data directory, not under the receive folder.
		// Keeping them on separate paths is what makes "relayed data never
		// appears as a received file" a structural property rather than a
		// matter of remembering to clean up.
		ReceiveFolder: receive,
		StagingFolder: filepath.Join(data, "staging"),
		Autostart:     false,
		Port:          0,
	}, nil
}

// Validate reports why the settings are unusable, or nil.
func (s Settings) Validate() error {
	name := strings.TrimSpace(s.DeviceName)
	if name == "" {
		return errors.New("device name is empty")
	}
	if len(name) > MaxDeviceNameLen {
		return fmt.Errorf("device name exceeds %d bytes", MaxDeviceNameLen)
	}

	if s.ReceiveFolder == "" {
		return errors.New("receive folder is empty")
	}
	if s.StagingFolder == "" {
		return errors.New("staging folder is empty")
	}
	if !filepath.IsAbs(s.ReceiveFolder) {
		return fmt.Errorf("receive folder must be absolute: %q", s.ReceiveFolder)
	}
	if !filepath.IsAbs(s.StagingFolder) {
		return fmt.Errorf("staging folder must be absolute: %q", s.StagingFolder)
	}

	receive := filepath.Clean(s.ReceiveFolder)
	staging := filepath.Clean(s.StagingFolder)
	if platform.EqualNames(receive, staging, platform.Rules()) {
		return errors.New("staging folder must differ from the receive folder")
	}
	// Nesting either inside the other would let a relayed file surface in the
	// receive folder, or a received file be swept as abandoned staging data.
	if within(staging, receive) {
		return errors.New("staging folder must not sit inside the receive folder")
	}
	if within(receive, staging) {
		return errors.New("receive folder must not sit inside the staging folder")
	}

	if s.Port < 0 || s.Port > 65535 {
		return fmt.Errorf("port out of range: %d", s.Port)
	}
	return nil
}

// within reports whether child is at or below parent.
func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func truncateName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) <= MaxDeviceNameLen {
		return name
	}
	runes := []rune(name)
	for len(string(runes)) > MaxDeviceNameLen {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

// Store loads and saves settings, guarding concurrent access.
type Store struct {
	path string
	mu   sync.RWMutex
	cur  Settings
}

// NewStore returns a store backed by settings.json in the platform's config
// directory.
func NewStore(p platform.Platform) (*Store, error) {
	dir, err := p.ConfigDir()
	if err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, settingsFile)}, nil
}

// Path is where settings are read from and written to.
func (st *Store) Path() string { return st.path }

// Load reads settings from disk. It returns ErrNotConfigured when the file does
// not exist, so the caller can write defaults on first run.
func (st *Store) Load() (Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	data, err := os.ReadFile(st.path)
	if os.IsNotExist(err) {
		return Settings{}, ErrNotConfigured
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}

	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("parse %s: %w", st.path, err)
	}
	if err := s.Validate(); err != nil {
		return Settings{}, fmt.Errorf("invalid settings in %s: %w", st.path, err)
	}

	st.cur = s
	return s, nil
}

// LoadOrInit returns existing settings, or writes and returns defaults.
func (st *Store) LoadOrInit(p platform.Platform) (Settings, error) {
	s, err := st.Load()
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, ErrNotConfigured) {
		return Settings{}, err
	}

	def, err := Defaults(p)
	if err != nil {
		return Settings{}, err
	}
	if err := st.Save(def); err != nil {
		return Settings{}, err
	}
	return def, nil
}

// Save validates and writes settings atomically, so an interrupted write can
// never leave a half-written file that refuses to parse on next start.
func (st *Store) Save(s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(st.path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best effort: the temporary file is only reachable from here, and a
	// failure to remove it after a successful rename means nothing was renamed.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, st.path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}

	st.cur = s
	return nil
}

// Current returns the last loaded or saved settings.
func (st *Store) Current() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.cur
}

// EnsureFolders creates the receive and staging folders.
//
// Changing the receive folder leaves previously received files untouched: this
// only creates the new destination, and never moves or deletes anything.
func (s Settings) EnsureFolders() error {
	// 0755, not 0700: this is the user's own folder, alongside Downloads, and a
	// private receive folder would surprise every file manager they open it in.
	if err := os.MkdirAll(s.ReceiveFolder, 0o755); err != nil { //nolint:gosec // the user's folder, deliberately theirs to see
		return fmt.Errorf("create receive folder: %w", err)
	}
	// Staging holds other people's data in transit, including relayed content
	// that belongs to neither party on this machine. Keep it private.
	if err := os.MkdirAll(s.StagingFolder, 0o700); err != nil {
		return fmt.Errorf("create staging folder: %w", err)
	}
	return nil
}
