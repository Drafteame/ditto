package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrChannelModeNotFound = errors.New("channel mode not found")

type ChannelMode string

const (
	ModeMock   ChannelMode = "mock"
	ModeLive   ChannelMode = "live"
	ModeRecord ChannelMode = "record"
	ModeMixed  ChannelMode = "mixed"
)

// DefaultChannelMode is applied to channels that do not yet have an explicit
// configuration. Mixed lets the dashboard dispatch locally while still
// forwarding client frames to the live target when one is configured.
const DefaultChannelMode = ModeMixed

type ChannelConfig struct {
	Channel     string      `json:"channel"`
	Mode        ChannelMode `json:"mode"`
	RecordingID string      `json:"recording_id,omitempty"`
	RateCapHz   int         `json:"rate_cap_hz,omitempty"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

type channelModeState struct {
	Channels []ChannelConfig `json:"channels"`
}

type ChannelModeRegistry struct {
	mu        sync.RWMutex
	path      string
	channels  map[string]ChannelConfig
	bus       *EventBus
	jsonLogs  bool
	listeners []func(ChannelConfig)
}

func NewChannelModeRegistry(dir string, bus *EventBus, jsonLogs bool) (*ChannelModeRegistry, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("channel modes dir is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r := &ChannelModeRegistry{
		path:     filepath.Join(dir, "state.json"),
		channels: make(map[string]ChannelConfig),
		bus:      bus,
		jsonLogs: jsonLogs,
	}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *ChannelModeRegistry) Get(channel string) ChannelConfig {
	channel = strings.TrimSpace(channel)
	r.mu.RLock()
	cfg, ok := r.channels[channel]
	r.mu.RUnlock()
	if ok {
		return cfg
	}
	return ChannelConfig{Channel: channel, Mode: DefaultChannelMode}
}

func (r *ChannelModeRegistry) Set(cfg ChannelConfig) error {
	cfg.Channel = strings.TrimSpace(cfg.Channel)
	if cfg.Channel == "" {
		return fmt.Errorf("channel is required")
	}
	if strings.ContainsAny(cfg.Channel, "\r\n") {
		return fmt.Errorf("channel cannot contain newlines")
	}
	if cfg.RateCapHz < 0 {
		return fmt.Errorf("rate_cap_hz cannot be negative")
	}
	if cfg.Mode != "" && !isChannelMode(cfg.Mode) {
		return fmt.Errorf("unsupported channel mode %q", cfg.Mode)
	}
	cfg.UpdatedAt = time.Now().UTC()

	r.mu.Lock()
	if existing, ok := r.channels[cfg.Channel]; ok {
		if cfg.Mode == "" {
			cfg.Mode = existing.Mode
		}
		if cfg.RecordingID == "" {
			cfg.RecordingID = existing.RecordingID
		}
	}
	if cfg.Mode == "" {
		cfg.Mode = DefaultChannelMode
	}
	r.channels[cfg.Channel] = cfg
	snapshot := r.snapshotLocked()
	listeners := append([]func(ChannelConfig){}, r.listeners...)
	r.mu.Unlock()

	if err := r.writeState(snapshot); err != nil {
		return err
	}
	r.publishModeEvent("SET", cfg)
	for _, listener := range listeners {
		listener(cfg)
	}
	return nil
}

func (r *ChannelModeRegistry) Delete(channel string) error {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return fmt.Errorf("channel is required")
	}
	if strings.ContainsAny(channel, "\r\n") {
		return fmt.Errorf("channel cannot contain newlines")
	}
	now := time.Now().UTC()

	r.mu.Lock()
	previous, ok := r.channels[channel]
	if !ok {
		r.mu.Unlock()
		return ErrChannelModeNotFound
	}
	delete(r.channels, channel)
	snapshot := r.snapshotLocked()
	listeners := append([]func(ChannelConfig){}, r.listeners...)
	r.mu.Unlock()

	listenerCfg := previous
	listenerCfg.Mode = DefaultChannelMode
	listenerCfg.UpdatedAt = now
	eventCfg := listenerCfg

	if err := r.writeState(snapshot); err != nil {
		return err
	}
	r.publishModeEvent(http.MethodDelete, eventCfg)
	for _, listener := range listeners {
		listener(listenerCfg)
	}
	return nil
}

func (r *ChannelModeRegistry) Snapshot() []ChannelConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshotLocked()
}

func (r *ChannelModeRegistry) AllowsLocalDispatch(channel string) bool {
	mode := r.Get(channel).Mode
	return mode == ModeMock || mode == ModeMixed
}

func (r *ChannelModeRegistry) OnChange(listener func(ChannelConfig)) {
	if listener == nil {
		return
	}
	r.mu.Lock()
	r.listeners = append(r.listeners, listener)
	r.mu.Unlock()
}

func (r *ChannelModeRegistry) snapshotLocked() []ChannelConfig {
	out := make([]ChannelConfig, 0, len(r.channels))
	for _, cfg := range r.channels {
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Channel < out[j].Channel })
	return out
}

func (r *ChannelModeRegistry) load() error {
	data, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state channelModeState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("load channel modes: %w", err)
	}
	for _, cfg := range state.Channels {
		cfg.Channel = strings.TrimSpace(cfg.Channel)
		if cfg.Channel == "" {
			continue
		}
		if cfg.Mode == "" {
			cfg.Mode = ModeMock
		}
		if !isChannelMode(cfg.Mode) {
			continue
		}
		r.channels[cfg.Channel] = cfg
	}
	return nil
}

func (r *ChannelModeRegistry) writeState(channels []ChannelConfig) error {
	return writeChannelModeState(r.path, channels)
}

func writeChannelModeState(path string, channels []ChannelConfig) error {
	data, err := json.MarshalIndent(channelModeState{Channels: channels}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0o644)
}

func (r *ChannelModeRegistry) publishModeEvent(method string, cfg ChannelConfig) {
	if r.bus == nil {
		return
	}
	body, _ := json.Marshal(cfg)
	event := LogEvent{
		Timestamp:    time.Now().Format("15:04:05"),
		Type:         "MODE",
		Method:       method,
		Path:         cfg.Channel,
		Status:       http.StatusOK,
		ResponseBody: string(body),
	}
	logRequest(r.jsonLogs, event)
	r.bus.Publish(event)
}

func isChannelMode(mode ChannelMode) bool {
	switch mode {
	case ModeMock, ModeLive, ModeRecord, ModeMixed:
		return true
	default:
		return false
	}
}

func RegisterChannelModeRoutes(mux *http.ServeMux, registry *ChannelModeRegistry) {
	if registry == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/channel-modes", func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"channels": registry.Snapshot()})
		case http.MethodPut:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var cfg ChannelConfig
			if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := registry.Set(cfg); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(registry.Get(cfg.Channel))
		case http.MethodDelete:
			channel := r.URL.Query().Get("channel")
			if err := registry.Delete(channel); err != nil {
				if errors.Is(err, ErrChannelModeNotFound) {
					http.Error(w, err.Error(), http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}
