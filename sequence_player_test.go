package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSequencePlayerSpeedMaxPublishesStepAndCompleted(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "Fast",
		OnEnd: "reset",
		Steps: []EventSequenceStep{
			{DelayMs: 1_000, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
			{DelayMs: 1_000, Channel: "tickets", Payload: json.RawMessage(`{"n":2}`)},
			{DelayMs: 1_000, Channel: "tickets", Payload: json.RawMessage(`{"n":3}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	broadcaster := NewPlayerBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), broadcaster)
	defer player.Shutdown(t.Context())

	if _, err := player.Play(seq.ID, PlayOptions{Speed: 0, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}

	var steps int
	var completed PlayerState
	deadline := time.After(2 * time.Second)
	for completed.Status != PlayerCompleted {
		select {
		case event := <-ch:
			if event.Type == "step" {
				steps++
			}
			if event.Type == "completed" {
				completed = event.State
			}
		case <-deadline:
			t.Fatal("timed out waiting for completion")
		}
	}
	if steps != 3 {
		t.Fatalf("expected 3 step events, got %d", steps)
	}
	if completed.CurrentStep != 0 {
		t.Fatalf("reset on_end should return cursor to 0, got %d", completed.CurrentStep)
	}
}

func TestSequencePlayerLoopPublishesLoopedNotCompleted(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "Loop Event",
		OnEnd: "loop",
		Steps: []EventSequenceStep{
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewPlayerBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), broadcaster)
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 0, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	event := waitForPlayerEvent(t, ch, "looped", 500*time.Millisecond)
	if event.State.Status != PlayerPlaying {
		t.Fatalf("looped event should keep playing status, got %s", event.State.Status)
	}
	if _, err := player.Stop(seq.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSequencePlayerTerminalControlsReturnErrors(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "Terminal",
		OnEnd: "stay",
		Steps: []EventSequenceStep{
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"n":"{{missing}}"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewPlayerBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), broadcaster)
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 0, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "error", 500*time.Millisecond)
	if _, err := player.Pause(seq.ID); err == nil {
		t.Fatal("expected pause on errored runner to fail")
	}
	if _, err := player.Stop(seq.ID); err == nil {
		t.Fatal("expected stop on errored runner to fail")
	}
}

func TestSequenceRunnerCachesEncodedPayloadByStepAndPayload(t *testing.T) {
	root := t.TempDir()
	writeProto(t, filepath.Join(root, "pack"), "event.proto", `syntax = "proto3"; package ditto.cache; message Event { string id = 1; }`)
	schemas, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &sequenceRunner{
		player: &SequencePlayer{schemas: schemas},
		cache:  make(map[string]stepCacheEntry),
	}
	step := EventSequenceStep{ID: "step-cache"}
	rendered := RenderedDispatch{
		TypeName: "ditto.cache.Event",
		Payload:  json.RawMessage(`{"id":"a"}`),
	}
	first, err := runner.prepareRenderedStep(step, rendered)
	if err != nil {
		t.Fatal(err)
	}
	if first.EncodedPayload == nil || len(runner.cache) != 1 {
		t.Fatal("expected first encode to populate cache")
	}
	entry := runner.cache[step.ID]
	entry.encoded.Data = []byte("cached")
	runner.cache[step.ID] = entry
	second, err := runner.prepareRenderedStep(step, rendered)
	if err != nil {
		t.Fatal(err)
	}
	if second.EncodedPayload == nil || string(second.EncodedPayload.Data) != "cached" {
		t.Fatalf("expected cached payload, got %#v", second.EncodedPayload)
	}
	third, err := runner.prepareRenderedStep(step, RenderedDispatch{
		TypeName: "ditto.cache.Event",
		Payload:  json.RawMessage(`{"id":"b"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(third.EncodedPayload.Data) == "cached" {
		t.Fatal("expected payload change to invalidate cache")
	}
}

func TestSequencePlayerRejectsZeroStepSnapshot(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg.mu.Lock()
	reg.sequences["empty-00000000"] = EventSequence{
		Version: 1,
		ID:      "empty-00000000",
		Name:    "Empty",
		OnEnd:   "stay",
		Steps:   []EventSequenceStep{},
	}
	reg.mu.Unlock()
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), NewPlayerBroadcaster())
	if _, err := player.Play("empty-00000000", PlayOptions{}); err == nil {
		t.Fatal("expected zero-step sequence to be rejected")
	}
}

func TestSequencePlayerPauseSeekAndStop(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "Controls",
		OnEnd: "stay",
		Steps: []EventSequenceStep{
			{DelayMs: 10_000, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
			{DelayMs: 10_000, Channel: "tickets", Payload: json.RawMessage(`{"n":2}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), NewPlayerBroadcaster())
	defer player.Shutdown(t.Context())

	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	state, err := player.Pause(seq.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != PlayerPaused {
		t.Fatalf("expected paused, got %s", state.Status)
	}
	state, err = player.Seek(seq.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentStep != 1 {
		t.Fatalf("expected cursor 1, got %d", state.CurrentStep)
	}
	state, err = player.Stop(seq.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != PlayerStopped {
		t.Fatalf("expected stopped, got %s", state.Status)
	}
}

func TestSequencePlayerZeroDelayLoopCanStop(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "Loop",
		OnEnd: "loop",
		Steps: []EventSequenceStep{
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"n":2}`)},
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"n":3}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), NewPlayerBroadcaster())
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 0, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		state, err := player.Stop(seq.ID)
		if err == nil && state.Status != PlayerStopped {
			err = errors.New("expected stopped state")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("stop took too long: %s", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stop blocked on zero-delay loop")
	}
}

func TestSequencePlayerSpeedZeroDuringWaitDispatchesImmediately(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "Speed Zero",
		OnEnd: "stay",
		Steps: []EventSequenceStep{
			{DelayMs: 10_000, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewPlayerBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), broadcaster)
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := player.SetSpeed(seq.ID, 0); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "step", 300*time.Millisecond)
}

func TestSequencePlayerSpeedDuringPauseScalesRemaining(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "Paused Speed",
		OnEnd: "stay",
		Steps: []EventSequenceStep{
			{DelayMs: 500, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewPlayerBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), broadcaster)
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err := player.Pause(seq.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := player.SetSpeed(seq.ID, 10); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := player.Play(seq.ID, PlayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "step", 200*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("paused remaining was not scaled, elapsed %s", elapsed)
	}
}

func waitForPlayerEvent(t *testing.T, ch <-chan PlayerEvent, eventType string, timeout time.Duration) PlayerEvent {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event := <-ch:
			if event.Type == eventType {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s event", eventType)
		}
	}
}
