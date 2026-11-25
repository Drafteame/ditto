package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"ditto/internal/eventlog"
)

func TestChannelModeRegistryGetSetSnapshotAndPersistence(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewChannelModeRegistry(dir, eventlog.NewBus(nil))
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	if got := reg.Get("/scores").Mode; got != DefaultChannelMode {
		t.Fatalf("default mode = %s, want %s", got, DefaultChannelMode)
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
	reloaded, err := NewChannelModeRegistry(dir, nil)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if got := reloaded.Get("/scores").Mode; got != ModeMixed {
		t.Fatalf("reloaded mode = %s, want mixed", got)
	}
}

func TestChannelModeRegistryConcurrentSetGet(t *testing.T) {
	reg, err := NewChannelModeRegistry(t.TempDir(), nil)
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
	reg, err := NewChannelModeRegistry(t.TempDir(), nil)
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

func TestChannelModeRegistryPersistsExplicitMockMode(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewChannelModeRegistry(dir, nil)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	if err := reg.Set(ChannelConfig{Channel: "/known", Mode: ModeMock}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got := reg.Snapshot(); len(got) != 1 || got[0].Channel != "/known" || got[0].Mode != ModeMock {
		t.Fatalf("Snapshot() = %#v, want explicit mock channel", got)
	}
	reloaded, err := NewChannelModeRegistry(dir, nil)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if got := reloaded.Snapshot(); len(got) != 1 || got[0].Channel != "/known" || got[0].Mode != ModeMock {
		t.Fatalf("reloaded Snapshot() = %#v, want explicit mock channel", got)
	}
}

func TestChannelModeRegistryDelete(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewChannelModeRegistry(dir, nil)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	if err := reg.Set(ChannelConfig{Channel: "/gone", Mode: ModeLive}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := reg.Delete("/gone"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got := reg.Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot() = %#v, want empty", got)
	}
	reloaded, err := NewChannelModeRegistry(dir, nil)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if got := reloaded.Snapshot(); len(got) != 0 {
		t.Fatalf("reloaded Snapshot() = %#v, want empty", got)
	}
}

func TestChannelModeRegistryDeleteNotFound(t *testing.T) {
	reg, err := NewChannelModeRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	if err := reg.Delete("/missing"); !errors.Is(err, ErrChannelModeNotFound) {
		t.Fatalf("Delete() error = %v, want ErrChannelModeNotFound", err)
	}
}
