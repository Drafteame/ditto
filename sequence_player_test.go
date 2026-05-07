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
