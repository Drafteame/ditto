package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestChannelModeRegistryGetSetSnapshotAndPersistence(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewChannelModeRegistry(dir, NewEventBus(), false)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	if got := reg.Get("/scores").Mode; got != ModeMock {
		t.Fatalf("default mode = %s, want mock", got)
	}
	if err := reg.Set(ChannelConfig{Channel: "/scores", Mode: ModeMixed, RateCapHz: 25}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got := reg.Get("/scores"); got.Mode != ModeMixed || got.RateCapHz != 25 {
		t.Fatalf("Get() = %#v, want mixed cap 25", got)
	}
	if got := reg.Snapshot(); len(got) != 1 || got[0].Channel != "/scores" {
		t.Fatalf("Snapshot() = %#v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("state.json missing: %v", err)
	}
	var state channelModeState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("state.json invalid JSON: %v", err)
	}
	reloaded, err := NewChannelModeRegistry(dir, nil, false)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if got := reloaded.Get("/scores").Mode; got != ModeMixed {
		t.Fatalf("reloaded mode = %s, want mixed", got)
	}
}

func TestChannelModeRegistryConcurrentSetGet(t *testing.T) {
	reg, err := NewChannelModeRegistry(t.TempDir(), nil, false)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.Set(ChannelConfig{Channel: "/race", Mode: ModeLive})
			_ = reg.Get("/race")
			_ = reg.Snapshot()
		}()
	}
	wg.Wait()
}

func TestChannelModeRegistrySetPreservesRecordingID(t *testing.T) {
	reg, err := NewChannelModeRegistry(t.TempDir(), nil, false)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	if err := reg.Set(ChannelConfig{Channel: "/recorded", Mode: ModeMixed, RecordingID: "rec-12345678", RateCapHz: 10}); err != nil {
		t.Fatalf("initial Set() error = %v", err)
	}
	if err := reg.Set(ChannelConfig{Channel: "/recorded", Mode: ModeLive, RateCapHz: 25}); err != nil {
		t.Fatalf("update Set() error = %v", err)
	}
	got := reg.Get("/recorded")
	if got.RecordingID != "rec-12345678" || got.Mode != ModeLive || got.RateCapHz != 25 {
		t.Fatalf("Get() = %#v, want recording id preserved with updated mode/rate", got)
	}
}

func TestDispatchRenderedSuppressedByMode(t *testing.T) {
	reg, err := NewChannelModeRegistry(t.TempDir(), nil, false)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	hub := NewSocketHub(NewEventBus(), false, reg)
	for _, mode := range []ChannelMode{ModeLive, ModeRecord} {
		if err := reg.Set(ChannelConfig{Channel: "/blocked", Mode: mode}); err != nil {
			t.Fatalf("Set(%s) error = %v", mode, err)
		}
		result, err := dispatchRendered(hub, nil, RenderedDispatch{
			Channel: "/blocked",
			Payload: json.RawMessage(`{"ok":true}`),
		}, nil)
		if err != nil {
			t.Fatalf("dispatchRendered(%s) fatal error = %v", mode, err)
		}
		if len(result.Errors) != 1 {
			t.Fatalf("dispatchRendered(%s) errors = %#v, want suppression", mode, result.Errors)
		}
	}
	for _, mode := range []ChannelMode{ModeMock, ModeMixed} {
		if err := reg.Set(ChannelConfig{Channel: "/allowed", Mode: mode}); err != nil {
			t.Fatalf("Set(%s) error = %v", mode, err)
		}
		result, err := dispatchRendered(hub, nil, RenderedDispatch{
			Channel: "/allowed",
			Payload: json.RawMessage(`{"ok":true}`),
		}, nil)
		if err != nil {
			t.Fatalf("dispatchRendered(%s) error = %v", mode, err)
		}
		if len(result.Errors) != 0 {
			t.Fatalf("dispatchRendered(%s) errors = %#v, want none", mode, result.Errors)
		}
	}
}
