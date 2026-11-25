package channelmode

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
	reg, err := NewRegistry(dir, eventlog.NewBus(nil))
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if got := reg.Get("/scores").Mode; got != Default {
		t.Fatalf("default mode = %s, want %s", got, Default)
	}
	if err := reg.Set(Config{Channel: "/scores", Mode: Mixed, RateCapHz: 25}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got := reg.Get("/scores"); got.Mode != Mixed || got.RateCapHz != 25 {
		t.Fatalf("Get() = %#v, want mixed cap 25", got)
	}
	if got := reg.Snapshot(); len(got) != 1 || got[0].Channel != "/scores" {
		t.Fatalf("Snapshot() = %#v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("state.json missing: %v", err)
	}
	var state state
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("state.json invalid JSON: %v", err)
	}
	reloaded, err := NewRegistry(dir, nil)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if got := reloaded.Get("/scores").Mode; got != Mixed {
		t.Fatalf("reloaded mode = %s, want mixed", got)
	}
}

func TestChannelModeRegistryConcurrentSetGet(t *testing.T) {
	reg, err := NewRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.Set(Config{Channel: "/race", Mode: Live})
			_ = reg.Get("/race")
			_ = reg.Snapshot()
		}()
	}
	wg.Wait()
}

func TestChannelModeRegistrySetPreservesRecordingID(t *testing.T) {
	reg, err := NewRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if err := reg.Set(Config{Channel: "/recorded", Mode: Mixed, RecordingID: "rec-12345678", RateCapHz: 10}); err != nil {
		t.Fatalf("initial Set() error = %v", err)
	}
	if err := reg.Set(Config{Channel: "/recorded", Mode: Live, RateCapHz: 25}); err != nil {
		t.Fatalf("update Set() error = %v", err)
	}
	got := reg.Get("/recorded")
	if got.RecordingID != "rec-12345678" || got.Mode != Live || got.RateCapHz != 25 {
		t.Fatalf("Get() = %#v, want recording id preserved with updated mode/rate", got)
	}
}

func TestChannelModeRegistryPersistsExplicitMockMode(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(dir, nil)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if err := reg.Set(Config{Channel: "/known", Mode: Mock}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got := reg.Snapshot(); len(got) != 1 || got[0].Channel != "/known" || got[0].Mode != Mock {
		t.Fatalf("Snapshot() = %#v, want explicit mock channel", got)
	}
	reloaded, err := NewRegistry(dir, nil)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if got := reloaded.Snapshot(); len(got) != 1 || got[0].Channel != "/known" || got[0].Mode != Mock {
		t.Fatalf("reloaded Snapshot() = %#v, want explicit mock channel", got)
	}
}

func TestChannelModeRegistryDelete(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(dir, nil)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if err := reg.Set(Config{Channel: "/gone", Mode: Live}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := reg.Delete("/gone"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got := reg.Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot() = %#v, want empty", got)
	}
	reloaded, err := NewRegistry(dir, nil)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if got := reloaded.Snapshot(); len(got) != 0 {
		t.Fatalf("reloaded Snapshot() = %#v, want empty", got)
	}
}

func TestChannelModeRegistryDeleteNotFound(t *testing.T) {
	reg, err := NewRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if err := reg.Delete("/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete() error = %v, want ErrNotFound", err)
	}
}
