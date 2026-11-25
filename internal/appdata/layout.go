// Package appdata locates Ditto's per-user data directory and its fixed
// layout of artifact folders, and persists the user's settings.
package appdata

import (
	"os"
	"path/filepath"
	"runtime"
)

// Layout contains Ditto's bundle-compatible persistence paths.
type Layout struct {
	Root               string
	ConfigPath         string
	MocksDir           string
	DescriptorsDir     string
	EventTemplatesDir  string
	SequencesDir       string
	AdapterProfilesDir string
	RecordingsDir      string
	ChannelModesDir    string
	ScenariosDir       string
}

type dataSubdir struct {
	name   string
	assign func(*Layout, string)
}

var dataSubdirs = []dataSubdir{
	{"mocks", func(layout *Layout, path string) { layout.MocksDir = path }},
	{"descriptors", func(layout *Layout, path string) { layout.DescriptorsDir = path }},
	{"adapter_profiles", func(layout *Layout, path string) { layout.AdapterProfilesDir = path }},
	{"event_templates", func(layout *Layout, path string) { layout.EventTemplatesDir = path }},
	{"sequences", func(layout *Layout, path string) { layout.SequencesDir = path }},
	{"recordings", func(layout *Layout, path string) { layout.RecordingsDir = path }},
	{"channel_modes", func(layout *Layout, path string) { layout.ChannelModesDir = path }},
	{"scenarios", func(layout *Layout, path string) { layout.ScenariosDir = path }},
}

// NewLayout resolves every path in the persistent data layout from root.
func NewLayout(root string) Layout {
	layout := Layout{
		Root:       root,
		ConfigPath: filepath.Join(root, "config.json"),
	}
	for _, subdir := range dataSubdirs {
		subdir.assign(&layout, filepath.Join(root, subdir.name))
	}
	return layout
}

// ArtifactDirs returns the directories that can contain user-loadable artifacts.
func (layout Layout) ArtifactDirs() []string {
	dirs := make([]string, len(dataSubdirs))
	for i, subdir := range dataSubdirs {
		dirs[i] = filepath.Join(layout.Root, subdir.name)
	}
	return dirs
}

// Ensure creates Ditto's persistent root and known artifact folders.
func Ensure(root string) (Layout, error) {
	layout := NewLayout(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return layout, err
	}
	for _, dir := range layout.ArtifactDirs() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return layout, err
		}
	}
	return layout, nil
}

// Locate returns the platform-appropriate layout for persistent Ditto data.
// The directory and the standard artifact subdirectories are created if they
// don't exist.
//
//   - macOS:   ~/Library/Application Support/Ditto/
//   - Linux:   ~/.config/ditto/
//   - Windows: %APPDATA%\Ditto\
func Locate() (Layout, error) {
	var base string

	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return Layout{}, err
		}
		base = filepath.Join(home, "Library", "Application Support", "Ditto")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return Layout{}, err
			}
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		base = filepath.Join(appData, "Ditto")
	default: // linux and others
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if configDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return Layout{}, err
			}
			configDir = filepath.Join(home, ".config")
		}
		base = filepath.Join(configDir, "ditto")
	}

	return Ensure(base)
}

// DefaultMocksDir returns the persistent mocks directory inside Locate.
// Creates it with an example mock if it doesn't exist yet.
func DefaultMocksDir() (string, error) {
	layout, err := Locate()
	if err != nil {
		return "", err
	}

	mocksDir := layout.MocksDir

	// Seed with example mock on first run
	examplePath := filepath.Join(mocksDir, "example.json")
	if _, err := os.Stat(examplePath); os.IsNotExist(err) {
		example := `{
  "method": "GET",
  "path": "/api/v1/users",
  "status": 200,
  "headers": {
    "Content-Type": "application/json"
  },
  "body": {
    "users": [
      {"id": 1, "name": "John Doe", "email": "john@example.com"},
      {"id": 2, "name": "Jane Doe", "email": "jane@example.com"}
    ]
  },
  "delay_ms": 0
}`
		os.WriteFile(examplePath, []byte(example), 0o644)
	}

	return mocksDir, nil
}
