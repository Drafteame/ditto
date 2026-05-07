package main

import (
	"os"
	"path/filepath"
	"runtime"
)

var dataSubdirs = []string{
	"mocks",
	"descriptors",
	"event_templates",
	"sequences",
	"recordings",
	"scenarios",
}

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

// NewDataLayout resolves every path in the persistent data layout from root.
func NewDataLayout(root string) DataLayout {
	return DataLayout{
		Root:              root,
		ConfigPath:        filepath.Join(root, "config.json"),
		MocksDir:          filepath.Join(root, "mocks"),
		DescriptorsDir:    filepath.Join(root, "descriptors"),
		EventTemplatesDir: filepath.Join(root, "event_templates"),
		SequencesDir:      filepath.Join(root, "sequences"),
		RecordingsDir:     filepath.Join(root, "recordings"),
		ScenariosDir:      filepath.Join(root, "scenarios"),
	}
}

// EnsureDataLayout creates Ditto's persistent root and known artifact folders.
func EnsureDataLayout(root string) (DataLayout, error) {
	layout := NewDataLayout(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return layout, err
	}
	for _, dir := range dataSubdirs {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return layout, err
		}
	}
	return layout, nil
}

// DataDir returns the platform-appropriate directory for persistent Ditto data.
// The directory and the standard artifact subdirectories are created if they
// don't exist.
//
//   - macOS:   ~/Library/Application Support/Ditto/
//   - Linux:   ~/.config/ditto/
//   - Windows: %APPDATA%\Ditto\
func DataDir() (string, error) {
	var base string

	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, "Library", "Application Support", "Ditto")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		base = filepath.Join(appData, "Ditto")
	default: // linux and others
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if configDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			configDir = filepath.Join(home, ".config")
		}
		base = filepath.Join(configDir, "ditto")
	}

	if _, err := EnsureDataLayout(base); err != nil {
		return "", err
	}
	return base, nil
}

// DefaultMocksDir returns the persistent mocks directory inside DataDir.
// Creates it with an example mock if it doesn't exist yet.
func DefaultMocksDir() (string, error) {
	dataDir, err := DataDir()
	if err != nil {
		return "", err
	}

	mocksDir := NewDataLayout(dataDir).MocksDir

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
