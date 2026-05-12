package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Config holds persistent user settings.
type Config struct {
	Port       int    `json:"port"`
	Target     string `json:"target"`
	LiveTarget string `json:"live_target,omitempty"`
}

// DefaultConfig returns the default settings.
func DefaultConfig() Config {
	return Config{
		Port:       8888,
		Target:     "",
		LiveTarget: "",
	}
}

// ConfigStore manages loading and saving the config file.
type ConfigStore struct {
	mu       sync.RWMutex
	config   Config
	filePath string
}

// NewConfigStore creates a store that reads/writes to the data directory.
func NewConfigStore() (*ConfigStore, error) {
	layout, err := DataDir()
	if err != nil {
		return nil, fmt.Errorf("resolving data dir: %w", err)
	}

	cs := &ConfigStore{
		config:   DefaultConfig(),
		filePath: layout.ConfigPath,
	}

	// Load existing config if present
	if data, err := os.ReadFile(cs.filePath); err == nil {
		var saved Config
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
func (cs *ConfigStore) Get() Config {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.config
}

// SetPort updates the port and saves.
func (cs *ConfigStore) SetPort(port int) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.config.Port = port
	return cs.save()
}

// SetTarget updates the target URL and saves.
func (cs *ConfigStore) SetTarget(target string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.config.Target = target
	return cs.save()
}

// SetLiveTarget updates the WebSocket upstream target and saves.
func (cs *ConfigStore) SetLiveTarget(target string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.config.LiveTarget = target
	return cs.save()
}

// Reset restores default settings and saves.
func (cs *ConfigStore) Reset() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.config = DefaultConfig()
	return cs.save()
}

func (cs *ConfigStore) save() error {
	data, err := json.MarshalIndent(cs.config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cs.filePath, data, 0o644)
}
