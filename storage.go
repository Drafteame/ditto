package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// DataLayout contains Ditto's bundle-compatible persistence paths.
type DataLayout struct {
	Root              string
	ConfigPath        string
	MocksDir          string
	DescriptorsDir    string
	EventTemplatesDir string
	SequencesDir      string
	RecordingsDir     string
	ScenariosDir      string
}

type dataSubdir struct {
	name   string
	assign func(*DataLayout, string)
}

var dataSubdirs = []dataSubdir{
	{"mocks", func(layout *DataLayout, path string) { layout.MocksDir = path }},
	{"descriptors", func(layout *DataLayout, path string) { layout.DescriptorsDir = path }},
	{"event_templates", func(layout *DataLayout, path string) { layout.EventTemplatesDir = path }},
	{"sequences", func(layout *DataLayout, path string) { layout.SequencesDir = path }},
	{"recordings", func(layout *DataLayout, path string) { layout.RecordingsDir = path }},
	{"scenarios", func(layout *DataLayout, path string) { layout.ScenariosDir = path }},
}

// NewDataLayout resolves every path in the persistent data layout from root.
func NewDataLayout(root string) DataLayout {
	layout := DataLayout{
		Root:       root,
		ConfigPath: filepath.Join(root, "config.json"),
	}
	for _, subdir := range dataSubdirs {
		subdir.assign(&layout, filepath.Join(root, subdir.name))
	}
	return layout
}

// ArtifactDirs returns the directories that can contain user-loadable artifacts.
func (layout DataLayout) ArtifactDirs() []string {
	dirs := make([]string, len(dataSubdirs))
	for i, subdir := range dataSubdirs {
		dirs[i] = filepath.Join(layout.Root, subdir.name)
	}
	return dirs
}

// EnsureDataLayout creates Ditto's persistent root and known artifact folders.
func EnsureDataLayout(root string) (DataLayout, error) {
	layout := NewDataLayout(root)
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

// DataDir returns the platform-appropriate layout for persistent Ditto data.
// The directory and the standard artifact subdirectories are created if they
// don't exist.
//
//   - macOS:   ~/Library/Application Support/Ditto/
//   - Linux:   ~/.config/ditto/
//   - Windows: %APPDATA%\Ditto\
func DataDir() (DataLayout, error) {
	var base string

	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return DataLayout{}, err
		}
		base = filepath.Join(home, "Library", "Application Support", "Ditto")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return DataLayout{}, err
			}
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		base = filepath.Join(appData, "Ditto")
	default: // linux and others
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if configDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return DataLayout{}, err
			}
			configDir = filepath.Join(home, ".config")
		}
		base = filepath.Join(configDir, "ditto")
	}

	return EnsureDataLayout(base)
}

// DefaultMocksDir returns the persistent mocks directory inside DataDir.
// Creates it with an example mock if it doesn't exist yet.
func DefaultMocksDir() (string, error) {
	layout, err := DataDir()
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
