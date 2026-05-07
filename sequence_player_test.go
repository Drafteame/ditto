package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
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
	var looped int
	var completed int
	deadline := time.After(50 * time.Millisecond)
	for {
		select {
		case event := <-ch:
			switch event.Type {
			case "looped":
				looped++
				if event.State.Status != PlayerPlaying {
					t.Fatalf("looped event should keep playing status, got %s", event.State.Status)
				}
			case "completed":
				completed++
			}
		case <-deadline:
			if looped == 0 {
				t.Fatal("expected at least one looped event")
			}
			if completed != 0 {
				t.Fatalf("loop emitted %d completed events", completed)
			}
			if _, err := player.Stop(seq.ID); err != nil {
				t.Fatal(err)
			}
			return
		}
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
	time.Sleep(5 * time.Millisecond)

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

func TestSequencePlayerResumeWithDifferentSpeedRescales(t *testing.T) {
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
		Name:  "Resume Speed",
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
	start := time.Now()
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 10, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "step", 120*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 120*time.Millisecond {
		t.Fatalf("resume speed did not rescale remaining, elapsed %s", elapsed)
	}
}

func TestSequencePlayerSpeedDuringWaitScalesRemaining(t *testing.T) {
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
		Name:  "Wait Speed",
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
	start := time.Now()
	if _, err := player.SetSpeed(seq.ID, 10); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "step", 200*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("wait remaining was not scaled, elapsed %s", elapsed)
	}
}

func TestSequencePlayerTemplateDeletedMidRunErrors(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := templates.Create(EventTemplate{
		Name:    "Gone",
		Channel: "tickets",
		Payload: json.RawMessage(`{"ok":true}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "Template Error",
		OnEnd: "stay",
		Steps: []EventSequenceStep{
			{DelayMs: 50, TemplateRef: tmpl.ID},
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
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	okSeq, err := reg.Create(EventSequence{
		Name:  "Broadcast OK",
		OnEnd: "stay",
		Steps: []EventSequenceStep{
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"ok":true}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	errSeq, err := reg.Create(EventSequence{
		Name:  "Broadcast Error",
		OnEnd: "stay",
		Steps: []EventSequenceStep{
			{DelayMs: 0, Channel: "tickets", Payload: json.RawMessage(`{"bad":"{{missing}}"}`)},
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

	if _, err := player.Play(okSeq.ID, PlayOptions{Speed: 0, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	waitForPlayerEvent(t, ch, "state", 500*time.Millisecond)
	waitForPlayerEvent(t, ch, "step", 500*time.Millisecond)
	waitForPlayerEvent(t, ch, "completed", 500*time.Millisecond)

	longSeq, err := reg.Create(EventSequence{
		Name:  "Broadcast Stopped",
		OnEnd: "stay",
		Steps: []EventSequenceStep{
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
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "Concurrent",
		OnEnd: "stay",
		Steps: []EventSequenceStep{
			{DelayMs: 10_000, Channel: "tickets", Payload: json.RawMessage(`{"ok":true}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), NewPlayerBroadcaster())
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
	if states := player.States(); len(states) != 1 || states[0].Status != PlayerPlaying {
		t.Fatalf("expected one playing runner, got %#v", states)
	}
}

func TestSequencePlayerShutdownPublishesStoppedAndReleasesRunner(t *testing.T) {
	before := runtime.NumGoroutine()
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
		Name:  "Shutdown",
		OnEnd: "stay",
		Steps: []EventSequenceStep{
			{DelayMs: 10_000, Channel: "tickets", Payload: json.RawMessage(`{"ok":true}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broadcaster := NewPlayerBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), broadcaster)
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
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "WS E2E",
		OnEnd: "stay",
		Steps: []EventSequenceStep{
			{DelayMs: 0, Channel: "/scores", Payload: json.RawMessage(`{"score":1}`)},
			{DelayMs: 0, Channel: "/scores", Payload: json.RawMessage(`{"score":2}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := NewSocketHub(NewEventBus(), false)
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
		if clients := hub.registry.Clients("/scores"); len(clients) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	player := NewSequencePlayer(reg, templates, nil, hub, NewPlayerBroadcaster())
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
