package sequence

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"ditto/internal/eventlog"
	"ditto/internal/eventtemplate"
	"ditto/internal/schema"
	"ditto/internal/socket"
)

func TestSequencePlayerSpeedMaxPublishesStepAndCompleted(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Fast",
		OnEnd: "reset",
		Steps: []Step{
			{DelayMs: 1_000, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
			{DelayMs: 1_000, Channel: "tickets", Payload: json.RawMessage(`{"n":2}`)},
			{DelayMs: 1_000, Channel: "tickets", Payload: json.RawMessage(`{"n":3}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster})
	defer player.Shutdown(t.Context())

	if _, err := player.Play(seq.ID, PlayOptions{Speed: 0, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}

	var steps int
	var completed State
	deadline := time.After(2 * time.Second)
	for completed.Status != StatusCompleted {
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
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Loop Event",
		OnEnd: "loop",
		Steps: []Step{
			{DelayMs: 10, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	clock := newFakeClock(testClockStart())
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster, Clock: clock})
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForRunnerArmed(t, ch, 500*time.Millisecond)
	clock.Advance(10 * time.Millisecond)
	event := waitForPlayerEvent(t, ch, "looped", 500*time.Millisecond)
	if event.State.Status != StatusPlaying {
		t.Fatalf("looped event should keep playing status, got %s", event.State.Status)
	}
	assertNoPlayerEvent(t, ch, "completed")
	if _, err := player.Stop(seq.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSequencePlayerTerminalControlsReturnErrors(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Terminal",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"n":"{{missing}}"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster})
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
	schemas, err := schema.NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &sequenceRunner{
		player: &Player{schemas: schemas},
		cache:  make(map[string]stepCacheEntry),
	}
	step := Step{ID: "step-cache"}
	rendered := socket.Dispatch{
		TypeName: "ditto.cache.Event",
		Payload:  json.RawMessage(`{"id":"a"}`),
	}
	first, err := runner.encodeStep(step, rendered)
	if err != nil {
		t.Fatal(err)
	}
	if first.Encoded == nil || len(runner.cache) != 1 {
		t.Fatal("expected first encode to populate cache")
	}
	entry := runner.cache[step.ID]
	entry.encoded.Data = []byte("cached")
	runner.cache[step.ID] = entry
	second, err := runner.encodeStep(step, rendered)
	if err != nil {
		t.Fatal(err)
	}
	if second.Encoded == nil || string(second.Encoded.Data) != "cached" {
		t.Fatalf("expected cached payload, got %#v", second.Encoded)
	}
	third, err := runner.encodeStep(step, socket.Dispatch{
		TypeName: "ditto.cache.Event",
		Payload:  json.RawMessage(`{"id":"b"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(third.Encoded.Data) == "cached" {
		t.Fatal("expected payload change to invalidate cache")
	}
}

func TestSequencePlayerRejectsZeroStepSnapshot(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg.mu.Lock()
	reg.sequences["empty-00000000"] = Sequence{
		Version: 1,
		ID:      "empty-00000000",
		Name:    "Empty",
		OnEnd:   "stay",
		Steps:   []Step{},
	}
	reg.mu.Unlock()
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: NewBroadcaster()})
	if _, err := player.Play("empty-00000000", PlayOptions{}); err == nil {
		t.Fatal("expected zero-step sequence to be rejected")
	}
}

func TestSequencePlayerPauseSeekAndStop(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Controls",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 10_000, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
			{DelayMs: 10_000, Channel: "tickets", Payload: json.RawMessage(`{"n":2}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	clock := newFakeClock(testClockStart())
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster, Clock: clock})
	defer player.Shutdown(t.Context())

	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForRunnerArmed(t, ch, 500*time.Millisecond)
	state, err := player.Pause(seq.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusPaused {
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
	if state.Status != StatusStopped {
		t.Fatalf("expected stopped, got %s", state.Status)
	}
}

func TestSequencePlayerZeroDelayLoopCanStop(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Loop",
		OnEnd: "loop",
		Steps: []Step{
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"n":2}`)},
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"n":3}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	clock := newFakeClock(testClockStart())
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster, Clock: clock})
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 0, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "looped", 500*time.Millisecond)

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		state, err := player.Stop(seq.ID)
		if err == nil && state.Status != StatusStopped {
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
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Speed Zero",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 10_000, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	clock := newFakeClock(testClockStart())
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster, Clock: clock})
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForRunnerArmed(t, ch, 500*time.Millisecond)
	if _, err := player.SetSpeed(seq.ID, 0); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "step", 300*time.Millisecond)
}

func TestSequencePlayerSpeedDuringPauseScalesRemaining(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Paused Speed",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 500, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	clock := newFakeClock(testClockStart())
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster, Clock: clock})
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForRunnerArmed(t, ch, 500*time.Millisecond)
	clock.Advance(100 * time.Millisecond)
	if _, err := player.Pause(seq.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := player.SetSpeed(seq.ID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := player.Play(seq.ID, PlayOptions{}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(39 * time.Millisecond)
	assertNoPlayerEvent(t, ch, "step")
	clock.Advance(time.Millisecond)
	waitForPlayerEvent(t, ch, "step", 200*time.Millisecond)
}

func TestSequencePlayerResumeWithDifferentSpeedRescales(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Resume Speed",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 500, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	clock := newFakeClock(testClockStart())
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster, Clock: clock})
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForRunnerArmed(t, ch, 500*time.Millisecond)
	clock.Advance(100 * time.Millisecond)
	if _, err := player.Pause(seq.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 10, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(39 * time.Millisecond)
	assertNoPlayerEvent(t, ch, "step")
	clock.Advance(time.Millisecond)
	waitForPlayerEvent(t, ch, "step", 120*time.Millisecond)
}

func TestSequencePlayerSpeedDuringWaitScalesRemaining(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Wait Speed",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 500, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	clock := newFakeClock(testClockStart())
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster, Clock: clock})
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForRunnerArmed(t, ch, 500*time.Millisecond)
	clock.Advance(100 * time.Millisecond)
	if _, err := player.SetSpeed(seq.ID, 10); err != nil {
		t.Fatal(err)
	}
	clock.Advance(39 * time.Millisecond)
	assertNoPlayerEvent(t, ch, "step")
	clock.Advance(time.Millisecond)
	waitForPlayerEvent(t, ch, "step", 200*time.Millisecond)
}

func TestSequencePlayerStopDuringWaitDoesNotDispatchPendingStep(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Stop During Wait",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 500, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	clock := newFakeClock(testClockStart())
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster, Clock: clock})
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForRunnerArmed(t, ch, 500*time.Millisecond)
	if _, err := player.Stop(seq.ID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	assertNoPlayerEvent(t, ch, "step")
}

func TestSequencePlayerEmitsWaitingEventBeforeStep(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Waiting",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 1_000, Channel: "tickets", Payload: json.RawMessage(`{"n":1}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	clock := newFakeClock(testClockStart())
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster, Clock: clock})
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}

	event := waitForPlayerEvent(t, ch, "waiting", 500*time.Millisecond)
	if event.StepID != seq.Steps[0].ID || event.StepIndex != 0 {
		t.Fatalf("unexpected waiting step: %#v", event)
	}
	if event.DelayMs != 1_000 {
		t.Fatalf("expected 1000ms delay, got %d", event.DelayMs)
	}
	assertNoPlayerEvent(t, ch, "step")
	clock.Advance(time.Second)
	waitForPlayerEvent(t, ch, "step", 500*time.Millisecond)
}

func TestSequencePlayerTemplateDeletedMidRunErrors(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := templates.Create(eventtemplate.Template{
		Name:    "Gone",
		Channel: "tickets",
		Payload: json.RawMessage(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Template Error",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 50, TemplateRef: tmpl.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster})
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	if err := templates.Delete(tmpl.ID); err != nil {
		t.Fatal(err)
	}
	event := waitForPlayerEvent(t, ch, "error", 500*time.Millisecond)
	if !strings.Contains(event.Error, "event template not found") {
		t.Fatalf("unexpected error event: %#v", event)
	}
}

func TestSequencePlayerBroadcastsStateStepCompletedStoppedAndError(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	okSeq, err := reg.Create(Sequence{
		Name:  "Broadcast OK",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"ok":true}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	errSeq, err := reg.Create(Sequence{
		Name:  "Broadcast Error",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"bad":"{{missing}}"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster})
	defer player.Shutdown(t.Context())

	if _, err := player.Play(okSeq.ID, PlayOptions{Speed: 0, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "state", 500*time.Millisecond)
	waitForPlayerEvent(t, ch, "step", 500*time.Millisecond)
	waitForPlayerEvent(t, ch, "completed", 500*time.Millisecond)

	longSeq, err := reg.Create(Sequence{
		Name:  "Broadcast Stopped",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 10_000, Channel: "tickets", Payload: json.RawMessage(`{"ok":true}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := player.Play(longSeq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := player.Stop(longSeq.ID); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "stopped", 500*time.Millisecond)

	if _, err := player.Play(errSeq.ID, PlayOptions{Speed: 0, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "error", 500*time.Millisecond)
}

func TestSequencePlayerConcurrentPlayIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Concurrent",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 10_000, Channel: "tickets", Payload: json.RawMessage(`{"ok":true}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: NewBroadcaster()})
	defer player.Shutdown(t.Context())

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if states := player.States(); len(states) != 1 || states[0].Status != StatusPlaying {
		t.Fatalf("expected one playing runner, got %#v", states)
	}
}

func TestSequencePlayerShutdownPublishesStoppedAndReleasesRunner(t *testing.T) {
	before := runtime.NumGoroutine()
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Shutdown",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 10_000, Channel: "tickets", Payload: json.RawMessage(`{"ok":true}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: broadcaster})
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	if err := player.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "stopped", 500*time.Millisecond)
	time.Sleep(25 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+8 {
		t.Fatalf("goroutines grew unexpectedly: before=%d after=%d", before, after)
	}
}

func TestSequencePlayerDispatchesToRawWebSocketClient(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "WS E2E",
		OnEnd: "stay",
		Steps: []Step{
			{DelayMs: 0, Channel: "/scores", Payload: json.RawMessage(`{"score":1}`)},
			{DelayMs: 0, Channel: "/scores", Payload: json.RawMessage(`{"score":2}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)})
	mux := http.NewServeMux()
	mux.HandleFunc("/", hub.ServeHTTP)
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/?adapter=raw", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","channel":"/scores"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if snapshot := hub.Snapshot(); len(snapshot) == 1 && slices.Contains(snapshot[0].Subscriptions, "/scores") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	player := NewPlayer(PlayerConfig{Sequences: reg, Hub: hub, Broadcaster: NewBroadcaster()})
	defer player.Shutdown(t.Context())
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 0, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	_, first, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), `"score":1`) || !strings.Contains(string(second), `"score":2`) {
		t.Fatalf("unexpected sequence payloads: %s / %s", first, second)
	}
}

func TestPlayerBroadcasterPublishUnsubscribeRace(t *testing.T) {
	b := NewBroadcaster()
	subs := make([]chan Event, 100)
	for i := range subs {
		subs[i] = b.Subscribe()
	}

	done := make(chan struct{})
	var drainWG sync.WaitGroup
	for _, ch := range subs {
		drainWG.Add(1)
		go func(ch chan Event) {
			defer drainWG.Done()
			for {
				select {
				case <-done:
					return
				case <-ch:
				}
			}
		}(ch)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			b.Publish(Event{Type: "step"})
			runtime.Gosched()
		}
	}()
	go func() {
		defer wg.Done()
		for _, ch := range subs {
			b.Unsubscribe(ch)
			runtime.Gosched()
		}
	}()
	wg.Wait()
	close(done)
	drainWG.Wait()
}

func waitForPlayerEvent(t *testing.T, ch <-chan Event, eventType string, timeout time.Duration) Event {
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

func waitForRunnerArmed(t *testing.T, ch <-chan Event, timeout time.Duration) Event {
	t.Helper()
	return waitForPlayerEvent(t, ch, "waiting", timeout)
}

func assertNoPlayerEvent(t *testing.T, ch <-chan Event, eventType string) {
	t.Helper()
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case event := <-ch:
			if event.Type == eventType {
				t.Fatalf("unexpected %s event: %#v", eventType, event)
			}
		case <-timer.C:
			return
		}
	}
}
