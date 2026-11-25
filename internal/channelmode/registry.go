// Package channelmode stores each socket channel's mode: mock (Ditto
// dispatches), live (proxied to the upstream), record (proxied and
// recorded) or mixed (both). Modes persist to a state file and changes are
// published to the event log and to registered listeners.
package channelmode

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

	"ditto/internal/eventlog"
	"ditto/internal/jsonfile"
)

// ErrNotFound is returned when deleting a channel that has no stored mode.
var ErrNotFound = errors.New("channel mode not found")

// Mode decides whether Ditto or the upstream drives a channel.
type Mode string

const (
	Mock   Mode = "mock"
	Live   Mode = "live"
	Record Mode = "record"
	Mixed  Mode = "mixed"
)

// Default is applied to channels that do not yet have an explicit
// configuration. Mixed lets the dashboard dispatch locally while still
// forwarding client frames to the live target when one is configured.
const Default = Mixed

// Config is one channel's stored mode and settings.
type Config struct {
	Channel     string    `json:"channel"`
	Mode        Mode      `json:"mode"`
	RecordingID string    `json:"recording_id,omitempty"`
	RateCapHz   int       `json:"rate_cap_hz,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type state struct {
	Channels []Config `json:"channels"`
}

// Registry holds the per-channel modes and persists them to a state file.
// It is safe for concurrent use.
type Registry struct {
	mu        sync.RWMutex
	path      string
	channels  map[string]Config
	bus       *eventlog.Bus
	listeners []func(Config)
}

// NewRegistry loads the modes stored in dir. bus may be nil, which disables
// MODE log events.
func NewRegistry(dir string, bus *eventlog.Bus) (*Registry, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("channel modes dir is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r := &Registry{
		path:     filepath.Join(dir, "state.json"),
		channels: make(map[string]Config),
		bus:      bus,
	}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// Get returns channel's config, or the Default mode when none is stored.
func (r *Registry) Get(channel string) Config {
	channel = strings.TrimSpace(channel)
	r.mu.RLock()
	cfg, ok := r.channels[channel]
	r.mu.RUnlock()
	if ok {
		return cfg
	}
	return Config{Channel: channel, Mode: Default}
}

// Set validates, stores and persists cfg, then notifies listeners.
func (r *Registry) Set(cfg Config) error {
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
	if cfg.Mode != "" && !isValid(cfg.Mode) {
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
		cfg.Mode = Default
	}
	r.channels[cfg.Channel] = cfg
	snapshot := r.snapshotLocked()
	listeners := append([]func(Config){}, r.listeners...)
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

// Delete forgets channel's mode, returning it to Default.
func (r *Registry) Delete(channel string) error {
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
		return ErrNotFound
	}
	delete(r.channels, channel)
	snapshot := r.snapshotLocked()
	listeners := append([]func(Config){}, r.listeners...)
	r.mu.Unlock()

	listenerCfg := previous
	listenerCfg.Mode = Default
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

// Snapshot returns every stored config.
func (r *Registry) Snapshot() []Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshotLocked()
}

// AllowsLocalDispatch reports whether Ditto may dispatch its own events on
// channel (mock and mixed modes).
func (r *Registry) AllowsLocalDispatch(channel string) bool {
	mode := r.Get(channel).Mode
	return mode == Mock || mode == Mixed
}

// OnChange registers listener to run after every Set and Delete, in
// registration order.
func (r *Registry) OnChange(listener func(Config)) {
	if listener == nil {
		return
	}
	r.mu.Lock()
	r.listeners = append(r.listeners, listener)
	r.mu.Unlock()
}

func (r *Registry) snapshotLocked() []Config {
	out := make([]Config, 0, len(r.channels))
	for _, cfg := range r.channels {
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Channel < out[j].Channel })
	return out
}

func (r *Registry) load() error {
	data, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state state
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("load channel modes: %w", err)
	}
	for _, cfg := range state.Channels {
		cfg.Channel = strings.TrimSpace(cfg.Channel)
		if cfg.Channel == "" {
			continue
		}
		if cfg.Mode == "" {
			cfg.Mode = Mock
		}
		if !isValid(cfg.Mode) {
			continue
		}
		r.channels[cfg.Channel] = cfg
	}
	return nil
}

func (r *Registry) writeState(channels []Config) error {
	return writeStateFile(r.path, channels)
}

func writeStateFile(path string, channels []Config) error {
	data, err := json.MarshalIndent(state{Channels: channels}, "", "  ")
	if err != nil {
		return err
	}
	return jsonfile.WriteAtomic(path, data, 0o644)
}

func (r *Registry) publishModeEvent(method string, cfg Config) {
	if r.bus == nil {
		return
	}
	body, _ := json.Marshal(cfg)
	event := eventlog.Event{
		Type:         "MODE",
		Method:       method,
		Path:         cfg.Channel,
		Status:       http.StatusOK,
		ResponseBody: string(body),
	}
	r.bus.Publish(event)
}

func isValid(mode Mode) bool {
	switch mode {
	case Mock, Live, Record, Mixed:
		return true
	default:
		return false
	}
}
