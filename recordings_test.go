package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRecorderStartStopAndJSONLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(RecorderConfig{Dir: dir, Bus: NewEventBus(nil)})
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

func TestRecorderListReturnsEmptySlice(t *testing.T) {
	rec, err := NewRecorder(RecorderConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	recordings, err := rec.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if recordings == nil {
		t.Fatalf("List() returned nil, want empty slice")
	}
	if len(recordings) != 0 {
		t.Fatalf("List() returned %d recordings, want 0", len(recordings))
	}
}

func TestRecorderRateCapDrops(t *testing.T) {
	rec, err := NewRecorder(RecorderConfig{Dir: t.TempDir()})
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
		rec, err := NewRecorder(RecorderConfig{Dir: t.TempDir(), Options: RecorderOptions{
			FrameQueueCapacity: 8,
			QueueSendTimeout:   time.Millisecond,
		}})
		if err != nil {
			t.Fatalf("NewRecorder() error = %v", err)
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

func TestRecorderStopTimeoutForceStopsDrain(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(RecorderConfig{Dir: dir, Options: RecorderOptions{
		StopTimeout: 10 * time.Millisecond,
	}})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	id := "stuck-drain-12345678"
	sessionDir := filepath.Join(dir, id)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	session := &recordingSession{
		manifest: RecordingManifest{
			Version: 1, ID: id, Name: "Stuck Drain", StartedAt: time.Now().UTC(), Channels: []RecordingChannelManifest{},
		},
		dir:           sessionDir,
		start:         time.Now().UTC(),
		frames:        make(chan RecordedFrame, 1),
		closing:       make(chan struct{}),
		forceStop:     make(chan struct{}),
		done:          make(chan struct{}),
		producersDone: make(chan struct{}),
		writers:       make(map[string]*recordingWriter),
		limits:        make(map[string]*rateLimiter),
		recorder:      rec,
		options:       rec.options,
		producers:     1,
	}
	rec.mu.Lock()
	rec.active = session
	rec.mu.Unlock()
	go session.runSafe()

	started := time.Now()
	stopped, err := rec.Stop(id)
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Stop() took %v, want force stop before 1s", elapsed)
	}
	if stopped.Error != "stop timed out while draining frames" {
		t.Fatalf("stopped error = %q", stopped.Error)
	}
	if stopped.StoppedAt == nil {
		t.Fatalf("stopped_at was not set")
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
	rec, err := NewRecorder(RecorderConfig{Dir: dir, Options: RecorderOptions{
		FrameQueueCapacity: 1,
		QueueSendTimeout:   time.Nanosecond,
	}})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
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
	if _, err := NewRecorder(RecorderConfig{Dir: dir}); err != nil {
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
