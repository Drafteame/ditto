package main

import (
	"encoding/json"
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
