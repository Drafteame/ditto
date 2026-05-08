package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	_, _ = ConvertRecordingToSequence("recording-12345678", nil)
}
