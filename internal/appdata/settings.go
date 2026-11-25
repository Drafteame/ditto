package appdata

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Settings holds persistent user settings.
type Settings struct {
	Port       int    `json:"port"`
	Target     string `json:"target"`
	LiveTarget string `json:"live_target,omitempty"`
}

// DefaultSettings returns the default settings.
func DefaultSettings() Settings {
	return Settings{
		Port:       8888,
		Target:     "",
		LiveTarget: "",
	}
}

// SettingsStore manages loading and saving the config file.
type SettingsStore struct {
	mu       sync.RWMutex
	config   Settings
	filePath string
}

// NewSettingsStore creates a store that reads/writes to the data directory.
func NewSettingsStore() (*SettingsStore, error) {
	layout, err := Locate()
	if err != nil {
		return nil, fmt.Errorf("resolving data dir: %w", err)
	}

	cs := &SettingsStore{
		config:   DefaultSettings(),
		filePath: layout.ConfigPath,
	}

	// Load existing config if present
	if data, err := os.ReadFile(cs.filePath); err == nil {
		var saved Settings
		if err := json.Unmarshal(data, &saved); err == nil {
			if saved.Port > 0 {
				cs.config.Port = saved.Port
			}
			cs.config.Target = saved.Target
			cs.config.LiveTarget = saved.LiveTarget
		}
	}

	return cs, nil
}

// Get returns the current config.
func (cs *SettingsStore) Get() Settings {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.config
}

// SetPort updates the port and saves.
func (cs *SettingsStore) SetPort(port int) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.config.Port = port
	return cs.save()
}

// SetTarget updates the target URL and saves.
func (cs *SettingsStore) SetTarget(target string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.config.Target = target
	return cs.save()
}

// SetLiveTarget updates the WebSocket upstream target and saves.
func (cs *SettingsStore) SetLiveTarget(target string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.config.LiveTarget = target
	return cs.save()
}

// Reset restores default settings and saves.
func (cs *SettingsStore) Reset() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.config = DefaultSettings()
	return cs.save()
}

func (cs *SettingsStore) save() error {
	data, err := json.MarshalIndent(cs.config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cs.filePath, data, 0o644)
}
