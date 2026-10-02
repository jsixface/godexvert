// Package config loads and saves the settings file (~/.godexvert.json), in the same format as the
// Kotlin app's ~/.codexvert.json.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	FileName = ".godexvert.json"
	// LegacyFileName is the Kotlin CodeXvert settings file, read when FileName does not exist yet.
	LegacyFileName = ".codexvert.json"
)

// Settings mirrors the Kotlin Settings data class.
type Settings struct {
	LibraryLocations  []string       `json:"libraryLocations"`
	WorkspaceLocation string         `json:"workspaceLocation"`
	VideoExtensions   []string       `json:"videoExtensions"`
	AutoConversion    AutoConversion `json:"autoConversion"`
	TakeBackups       bool           `json:"takeBackups"`
}

type AutoConversion struct {
	// Conversion maps a source codec name (e.g. "AC3") to a target codec name (e.g. "AAC").
	Conversion map[string]string `json:"conversion"`
}

type saved struct {
	Settings Settings `json:"settings"`
}

// Default returns the settings used when no file exists.
func Default() Settings {
	return Settings{
		LibraryLocations:  []string{},
		WorkspaceLocation: "/tmp/vid-con",
		VideoExtensions:   []string{"avi", "mp4", "mkv", "mpeg4"},
		AutoConversion:    AutoConversion{Conversion: map[string]string{"AAC": "AAC"}},
		TakeBackups:       true,
	}
}

// Store is a mutex-guarded settings holder persisted to a JSON file.
type Store struct {
	path string
	mu   sync.RWMutex
	cur  Settings
}

// DefaultPath returns $HOME/.godexvert.json (or ./.godexvert.json without HOME).
func DefaultPath() string { return filepath.Join(home(), FileName) }

// LegacyPath returns $HOME/.codexvert.json, the Kotlin app's settings file.
func LegacyPath() string { return filepath.Join(home(), LegacyFileName) }

func home() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return "."
}

// Load reads the file at path; a missing or blank file yields defaults.
// When path does not exist, the first existing fallback is read instead, while saves still go to
// path. Fields absent from the file keep their default values, as in the Kotlin app.
func Load(path string, fallbacks ...string) (*Store, error) {
	s := &Store{path: path, cur: Default()}
	data, err := os.ReadFile(path)
	for _, fb := range fallbacks {
		if !errors.Is(err, os.ErrNotExist) {
			break
		}
		data, err = os.ReadFile(fb)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if strings.TrimSpace(string(data)) != "" {
		sv := saved{Settings: Default()}
		if err := json.Unmarshal(data, &sv); err != nil {
			return nil, err
		}
		s.cur = sv.Settings
	}
	return s, nil
}

func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.cur)
}

// Save replaces the settings and writes them to disk.
func (s *Store) Save(v Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(saved{Settings: v}, "", "    ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return err
	}
	s.cur = clone(v)
	return nil
}

func clone(v Settings) Settings {
	v.LibraryLocations = append([]string{}, v.LibraryLocations...)
	v.VideoExtensions = append([]string{}, v.VideoExtensions...)
	m := make(map[string]string, len(v.AutoConversion.Conversion))
	for k, x := range v.AutoConversion.Conversion {
		m[k] = x
	}
	v.AutoConversion.Conversion = m
	return v
}
