package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestRecorderStartStopAndJSONLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(dir, nil, nil, NewEventBus(), false)
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	manifest, err := rec.Start("Match Day Recording", "")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	rec.Record(RecordFrameInput{
		Channel: "/games/123", Direction: "upstream", Kind: "text", Data: []byte(`{"score":1}`),
	})
	stopped, err := rec.Stop(manifest.ID)
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if stopped.StoppedAt == nil || len(stopped.Channels) != 1 || stopped.Channels[0].Events != 1 {
		t.Fatalf("stopped manifest = %#v", stopped)
	}
	path := filepath.Join(dir, manifest.ID, channelFileName("/games/123"))
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open jsonl: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatalf("missing jsonl frame")
	}
	var frame RecordedFrame
	if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
		t.Fatalf("frame JSON invalid: %v", err)
	}
	if frame.RawB64 == "" || frame.Decoded == nil || frame.DecodeError != "" {
		t.Fatalf("frame = %#v, want raw and decoded JSON", frame)
	}
}

func TestRecorderRateCapDrops(t *testing.T) {
	rec, err := NewRecorder(t.TempDir(), nil, nil, nil, false)
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	manifest, err := rec.Start("Rate Cap", "")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	for i := 0; i < 5; i++ {
		rec.Record(RecordFrameInput{
			Channel: "/fast", Direction: "upstream", Kind: "text", Data: []byte(`{"n":1}`), RateCapHz: 1,
		})
	}
	stopped, err := rec.Stop(manifest.ID)
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if len(stopped.Channels) != 1 || stopped.Channels[0].Dropped == 0 {
		t.Fatalf("drops = %#v, want at least one drop", stopped.Channels)
	}
}

func TestRecorderRecordConcurrentWithStopDoesNotPanic(t *testing.T) {
	for i := 0; i < 1000; i++ {
		rec, err := NewRecorderWithOptions(t.TempDir(), nil, nil, nil, false, RecorderOptions{
			FrameQueueCapacity: 8,
			QueueSendTimeout:   time.Millisecond,
		})
		if err != nil {
			t.Fatalf("NewRecorderWithOptions() error = %v", err)
		}
		manifest, err := rec.Start("Concurrent Stop", "")
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		var wg sync.WaitGroup
		for worker := 0; worker < 4; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 25; j++ {
					rec.Record(RecordFrameInput{
						Channel: "/race", Direction: "upstream", Kind: "text", Data: []byte(`{"n":1}`),
					})
				}
			}()
		}
		if _, err := rec.Stop(manifest.ID); err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
		wg.Wait()
	}
}

func TestRecordingBurstKeepsAllFramesAndCoalescesLog(t *testing.T) {
	bus := NewEventBus()
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)
	modes, err := NewChannelModeRegistry(t.TempDir(), bus, false)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	if err := modes.Set(ChannelConfig{Channel: "/burst-record", Mode: ModeRecord}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	rec, err := NewRecorder(t.TempDir(), nil, modes, bus, false)
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	hub := NewSocketHub(bus, false, modes)
	hub.SetRecorder(rec)
	manifest, err := rec.Start("Burst Recording", "")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	for i := 0; i < 200; i++ {
		hub.forwardFromUpstream("/burst-record", websocket.MessageText, []byte(`{"n":1}`))
	}
	if got := waitForBurstSummary(t, events, "/burst-record"); got != 200 {
		t.Fatalf("burst total_frames = %d, want 200", got)
	}
	if _, err := rec.Stop(manifest.ID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	frames, err := rec.Frames(manifest.ID, "/burst-record", 0, 500)
	if err != nil {
		t.Fatalf("Frames() error = %v", err)
	}
	if len(frames) != 200 {
		t.Fatalf("frames len = %d, want 200", len(frames))
	}
}

func waitForBurstSummary(t *testing.T, events <-chan LogEvent, channel string) int {
	t.Helper()
	deadline := time.After(1500 * time.Millisecond)
	for {
		select {
		case event := <-events:
			if event.Type != "SOCKET" || event.Method != "DISPATCH_BURST" || event.Path != channel {
				continue
			}
			var body struct {
				TotalFrames int `json:"total_frames"`
			}
			if err := json.Unmarshal([]byte(event.ResponseBody), &body); err != nil {
				t.Fatalf("summary body invalid JSON: %v", err)
			}
			return body.TotalFrames
		case <-deadline:
			t.Fatalf("timed out waiting for DISPATCH_BURST")
		}
	}
}

func TestRecorderQueueDropsAreSeparateFromRateCapDrops(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorderWithOptions(dir, nil, nil, nil, false, RecorderOptions{
		FrameQueueCapacity: 1,
		QueueSendTimeout:   time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("NewRecorderWithOptions() error = %v", err)
	}
	id := "queue-drop-12345678"
	session := &recordingSession{
		manifest: RecordingManifest{
			Version: 1, ID: id, Name: "Queue Drop", StartedAt: time.Now().UTC(), Channels: []RecordingChannelManifest{},
		},
		dir:     filepath.Join(dir, id),
		start:   time.Now().UTC(),
		frames:  make(chan RecordedFrame, 1),
		done:    make(chan struct{}),
		writers: make(map[string]*recordingWriter),
		limits:  make(map[string]*rateLimiter),
		options: normalizeRecorderOptions(RecorderOptions{FrameQueueCapacity: 1, QueueSendTimeout: time.Nanosecond}),
	}
	if err := os.MkdirAll(session.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	session.frames <- RecordedFrame{Channel: "/full", FrameKind: "text", RawB64: "e30="}
	rec.active = session

	rec.Record(RecordFrameInput{Channel: "/full", Direction: "upstream", Kind: "text", Data: []byte(`{"n":1}`)})

	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.manifest.Channels) != 1 {
		t.Fatalf("channels = %#v", session.manifest.Channels)
	}
	got := session.manifest.Channels[0]
	if got.Dropped != 0 || got.QueueDropped != 1 {
		t.Fatalf("channel counters = %#v, want dropped=0 queue_dropped=1", got)
	}
}

func TestAppSyncDecoderKeepsBuiltInAliasWhenSchemaMissing(t *testing.T) {
	rec, err := NewRecorder(t.TempDir(), nil, nil, nil, false)
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	decoded, decodeErr := rec.decodeAppSyncProfile(AdapterProfile{BaseAdapter: "appsync"}, []byte(`{
		"type":"data",
		"payload":{"data":{"t":"example.Event","e":"AAAA"}}
	}`))
	if decodeErr != "" {
		t.Fatalf("decodeErr = %q, want empty fallback", decodeErr)
	}
	if decoded == nil || decoded.Alias != "example.Event" || decoded.TypeName != "" {
		t.Fatalf("decoded = %#v, want alias-only fallback", decoded)
	}
}

func TestAppSyncDecoderReportsMissingPayload(t *testing.T) {
	rec, err := NewRecorder(t.TempDir(), nil, nil, nil, false)
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	decoded, decodeErr := rec.decodeAppSyncProfile(AdapterProfile{BaseAdapter: "appsync"}, []byte(`{
		"type":"data",
		"payload":{"data":{"t":"example.Event"}}
	}`))
	if decoded != nil || decodeErr != "appsync payload missing" {
		t.Fatalf("decoded=%#v decodeErr=%q, want missing payload error", decoded, decodeErr)
	}
}

func TestMixedModeLocalDispatchIsRecorded(t *testing.T) {
	bus := NewEventBus()
	modes, err := NewChannelModeRegistry(t.TempDir(), bus, false)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	if err := modes.Set(ChannelConfig{Channel: "/mixed", Mode: ModeMixed}); err != nil {
		t.Fatalf("Set mixed mode: %v", err)
	}
	recDir := t.TempDir()
	rec, err := NewRecorder(recDir, nil, modes, bus, false)
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	hub := NewSocketHub(bus, false, modes)
	hub.SetRecorder(rec)
	client := &SocketClient{
		id:            "raw-1",
		adapter:       "raw",
		protocol:      RawAdapter{},
		send:          make(chan EncodedServerMessage, 4),
		done:          make(chan struct{}),
		connected:     time.Now(),
		subscriptions: map[string]string{"/mixed": "sub-1"},
	}
	hub.addClient(client)
	hub.registry.Subscribe("/mixed", client.id)

	manifest, err := rec.Start("Mixed Local", "")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	payload := json.RawMessage(`{"local":true}`)
	result, err := dispatchRendered(hub, nil, RenderedDispatch{Channel: "/mixed", Payload: payload}, nil)
	if err != nil {
		t.Fatalf("dispatchRendered() error = %v", err)
	}
	if result.Delivered != 1 {
		t.Fatalf("delivered = %d, want 1", result.Delivered)
	}
	if _, err := rec.Stop(manifest.ID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	frames, err := rec.Frames(manifest.ID, "/mixed", 0, 10)
	if err != nil {
		t.Fatalf("Frames() error = %v", err)
	}
	if len(frames) != 1 || frames[0].Direction != "local" {
		t.Fatalf("frames = %#v, want one local frame", frames)
	}
	raw, err := base64.StdEncoding.DecodeString(frames[0].RawB64)
	if err != nil {
		t.Fatalf("raw_b64 invalid: %v", err)
	}
	if string(raw) != string(payload) {
		t.Fatalf("recorded raw = %s, want wrapped payload %s", raw, payload)
	}
}

func TestRecorderMarksInterruptedManifest(t *testing.T) {
	dir := t.TempDir()
	id := "interrupted-12345678"
	recDir := filepath.Join(dir, id)
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute)
	if err := writeRecordingManifest(recDir, RecordingManifest{
		Version: 1, ID: id, Name: "Interrupted", StartedAt: started, Channels: []RecordingChannelManifest{},
	}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if _, err := NewRecorder(dir, nil, nil, nil, false); err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	manifest, err := readRecordingManifest(recDir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.StoppedAt == nil || manifest.Error != "interrupted" {
		t.Fatalf("manifest = %#v, want interrupted", manifest)
	}
}

func TestConvertRecordingToSequenceM6(t *testing.T) {
	t.Skip("M6")
	rec, _ := NewRecorder(t.TempDir(), nil, nil, nil, false)
	_, _ = rec.ConvertRecordingToSequence("recording-12345678", nil)
}
