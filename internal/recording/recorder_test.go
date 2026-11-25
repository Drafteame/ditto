package recording

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ditto/internal/eventlog"
)

func TestRecorderStartStopAndJSONLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Config{Dir: dir, Bus: eventlog.NewBus(nil)})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	manifest, err := rec.Start("Match Day Recording", "")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	rec.Record(Input{
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
	var frame Frame
	if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
		t.Fatalf("frame JSON invalid: %v", err)
	}
	if frame.RawB64 == "" || frame.Decoded == nil || frame.DecodeError != "" {
		t.Fatalf("frame = %#v, want raw and decoded JSON", frame)
	}
}

func TestRecorderListReturnsEmptySlice(t *testing.T) {
	rec, err := NewRecorder(Config{Dir: t.TempDir()})
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
	rec, err := NewRecorder(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	manifest, err := rec.Start("Rate Cap", "")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	for i := 0; i < 5; i++ {
		rec.Record(Input{
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
		rec, err := NewRecorder(Config{Dir: t.TempDir(), Options: Options{
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
					rec.Record(Input{
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
	rec, err := NewRecorder(Config{Dir: dir, Options: Options{
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
	session := &session{
		manifest: Manifest{
			Version: 1, ID: id, Name: "Stuck Drain", StartedAt: time.Now().UTC(), Channels: []ChannelManifest{},
		},
		dir:           sessionDir,
		start:         time.Now().UTC(),
		frames:        make(chan Frame, 1),
		closing:       make(chan struct{}),
		forceStop:     make(chan struct{}),
		done:          make(chan struct{}),
		producersDone: make(chan struct{}),
		writers:       make(map[string]*channelWriter),
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

func TestRecorderQueueDropsAreSeparateFromRateCapDrops(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Config{Dir: dir, Options: Options{
		FrameQueueCapacity: 1,
		QueueSendTimeout:   time.Nanosecond,
	}})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	id := "queue-drop-12345678"
	session := &session{
		manifest: Manifest{
			Version: 1, ID: id, Name: "Queue Drop", StartedAt: time.Now().UTC(), Channels: []ChannelManifest{},
		},
		dir:     filepath.Join(dir, id),
		start:   time.Now().UTC(),
		frames:  make(chan Frame, 1),
		done:    make(chan struct{}),
		writers: make(map[string]*channelWriter),
		limits:  make(map[string]*rateLimiter),
		options: normalizeOptions(Options{FrameQueueCapacity: 1, QueueSendTimeout: time.Nanosecond}),
	}
	if err := os.MkdirAll(session.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	session.frames <- Frame{Channel: "/full", FrameKind: "text", RawB64: "e30="}
	rec.active = session

	rec.Record(Input{Channel: "/full", Direction: "upstream", Kind: "text", Data: []byte(`{"n":1}`)})

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
	if err := writeManifest(recDir, Manifest{
		Version: 1, ID: id, Name: "Interrupted", StartedAt: started, Channels: []ChannelManifest{},
	}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if _, err := NewRecorder(Config{Dir: dir}); err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	manifest, err := readManifest(recDir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.StoppedAt == nil || manifest.Error != "interrupted" {
		t.Fatalf("manifest = %#v, want interrupted", manifest)
	}
}
