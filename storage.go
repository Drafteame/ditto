package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// DataDir returns the platform-appropriate directory for persistent Ditto data.
// The directory is created if it doesn't exist.
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

	if err := os.MkdirAll(base, 0o755); err != nil {
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

	mocksDir := filepath.Join(dataDir, "mocks")
	if err := os.MkdirAll(mocksDir, 0o755); err != nil {
		return "", err
	}

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
