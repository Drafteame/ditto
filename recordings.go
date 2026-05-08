package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

const (
	recordingFrameQueueCapacity = 4096
	recordingManifestFlushEvery = 2 * time.Second
	recordingQueueSendTimeout   = 50 * time.Millisecond
	recordingStopTimeout        = 5 * time.Second
)

type RecorderOptions struct {
	FrameQueueCapacity int
	ManifestFlushEvery time.Duration
	QueueSendTimeout   time.Duration
	StopTimeout        time.Duration
}

type RecordingManifest struct {
	Version        int                        `json:"version"`
	ID             string                     `json:"id"`
	Name           string                     `json:"name"`
	Description    string                     `json:"description"`
	StartedAt      time.Time                  `json:"started_at"`
	StoppedAt      *time.Time                 `json:"stopped_at"`
	Channels       []RecordingChannelManifest `json:"channels"`
	AdapterProfile string                     `json:"adapter_profile,omitempty"`
	SchemaPackIDs  []string                   `json:"schema_pack_ids,omitempty"`
	Error          string                     `json:"error,omitempty"`
}

type RecordingChannelManifest struct {
	Channel        string                   `json:"channel"`
	Events         int                      `json:"events"`
	Dropped        int                      `json:"dropped"`
	QueueDropped   int                      `json:"queue_dropped,omitempty"`
	RateCapHz      int                      `json:"rate_cap_hz,omitempty"`
	AdapterProfile string                   `json:"adapter_profile,omitempty"`
	ProfileChanges []RecordingProfileChange `json:"profile_changes,omitempty"`
}

type RecordingProfileChange struct {
	TsMs    int64  `json:"ts_ms"`
	Profile string `json:"profile"`
}

type RecordedFrame struct {
	TsMs        int64         `json:"ts_ms"`
	Direction   string        `json:"direction"`
	Channel     string        `json:"channel"`
	FrameKind   string        `json:"frame_kind"`
	RawB64      string        `json:"raw_b64"`
	Decoded     *DecodedFrame `json:"decoded,omitempty"`
	DecodeError string        `json:"decode_error"`
}

type DecodedFrame struct {
	TypeName    string          `json:"type_name,omitempty"`
	PayloadJSON json.RawMessage `json:"payload_json,omitempty"`
	Alias       string          `json:"alias,omitempty"`
}

type RecordFrameInput struct {
	Channel   string
	Direction string
	Kind      string
	Data      []byte
	Adapter   string
	RateCapHz int
}

type Recorder struct {
	mu       sync.Mutex
	dir      string
	schemas  *SchemaRegistry
	modes    *ChannelModeRegistry
	bus      *EventBus
	jsonLogs bool
	active   *recordingSession
	options  RecorderOptions
}

type recordingSession struct {
	mu                sync.Mutex
	manifest          RecordingManifest
	dir               string
	start             time.Time
	frames            chan RecordedFrame
	closing           chan struct{}
	forceStop         chan struct{}
	done              chan struct{}
	producersDone     chan struct{}
	producersDoneOnce sync.Once
	forceStopOnce     sync.Once
	writers           map[string]*recordingWriter
	limits            map[string]*rateLimiter
	recorder          *Recorder
	dirty             bool
	options           RecorderOptions
	stopping          bool
	producers         int
}

type recordingWriter struct {
	file   *os.File
	writer *bufio.Writer
}

type rateLimiter struct {
	sec   int64
	count int
}

func NewRecorder(dir string, schemas *SchemaRegistry, modes *ChannelModeRegistry, bus *EventBus, jsonLogs bool) (*Recorder, error) {
	return NewRecorderWithOptions(dir, schemas, modes, bus, jsonLogs, RecorderOptions{})
}

func NewRecorderWithOptions(dir string, schemas *SchemaRegistry, modes *ChannelModeRegistry, bus *EventBus, jsonLogs bool, options RecorderOptions) (*Recorder, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("recordings dir is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	options = normalizeRecorderOptions(options)
	r := &Recorder{dir: dir, schemas: schemas, modes: modes, bus: bus, jsonLogs: jsonLogs, options: options}
	if err := r.recoverInterrupted(); err != nil {
		return nil, err
	}
	return r, nil
}

func normalizeRecorderOptions(options RecorderOptions) RecorderOptions {
	if options.FrameQueueCapacity <= 0 {
		options.FrameQueueCapacity = recordingFrameQueueCapacity
	}
	if options.ManifestFlushEvery <= 0 {
		options.ManifestFlushEvery = recordingManifestFlushEvery
	}
	if options.QueueSendTimeout <= 0 {
		options.QueueSendTimeout = recordingQueueSendTimeout
	}
	if options.StopTimeout <= 0 {
		options.StopTimeout = recordingStopTimeout
	}
	return options
}

func (r *Recorder) Start(name, description string) (RecordingManifest, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Recording"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil {
		return RecordingManifest{}, fmt.Errorf("recording %q is already active", r.active.manifest.ID)
	}
	id := recordingID(name, time.Now().UTC())
	dir := filepath.Join(r.dir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return RecordingManifest{}, err
	}
	now := time.Now().UTC()
	manifest := RecordingManifest{
		Version:       1,
		ID:            id,
		Name:          name,
		Description:   strings.TrimSpace(description),
		StartedAt:     now,
		Channels:      []RecordingChannelManifest{},
		SchemaPackIDs: schemaPackIDs(r.schemas),
	}
	session := &recordingSession{
		manifest:      manifest,
		dir:           dir,
		start:         now,
		frames:        make(chan RecordedFrame, r.options.FrameQueueCapacity),
		closing:       make(chan struct{}),
		forceStop:     make(chan struct{}),
		done:          make(chan struct{}),
		producersDone: make(chan struct{}),
		writers:       make(map[string]*recordingWriter),
		limits:        make(map[string]*rateLimiter),
		recorder:      r,
		options:       r.options,
	}
	r.active = session
	if r.modes != nil {
		for _, cfg := range r.modes.Snapshot() {
			if cfg.Mode == ModeRecord || cfg.Mode == ModeMixed {
				session.ensureChannel(cfg.Channel, cfg.RateCapHz, "")
			}
		}
	}
	if err := writeRecordingManifest(dir, session.manifest); err != nil {
		r.active = nil
		return RecordingManifest{}, err
	}
	go session.runSafe()
	r.publish("RECORD_START", id, http.StatusCreated, "")
	return session.manifest, nil
}

func (r *Recorder) Stop(id string) (RecordingManifest, error) {
	r.mu.Lock()
	session := r.active
	if session == nil || session.manifest.ID != id {
		r.mu.Unlock()
		return RecordingManifest{}, fmt.Errorf("active recording %q not found", id)
	}
	r.active = nil
	session.beginClosing()
	r.mu.Unlock()

	select {
	case <-session.done:
	case <-time.After(session.options.StopTimeout):
		session.setError("stop timed out while draining frames")
		session.forceStopDrain()
		<-session.done
	}
	now := time.Now().UTC()
	session.mu.Lock()
	session.manifest.StoppedAt = &now
	manifest := cloneRecordingManifest(session.manifest)
	session.mu.Unlock()
	if err := writeRecordingManifest(session.dir, manifest); err != nil {
		return RecordingManifest{}, err
	}
	r.publish("RECORD_STOP", id, http.StatusOK, "")
	return manifest, nil
}

func (r *Recorder) ActiveID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		return ""
	}
	return r.active.manifest.ID
}

func (r *Recorder) clearActive(session *recordingSession) {
	r.mu.Lock()
	if r.active == session {
		r.active = nil
	}
	r.mu.Unlock()
}

func (r *Recorder) Record(input RecordFrameInput) {
	r.mu.Lock()
	session := r.active
	r.mu.Unlock()
	if session == nil {
		return
	}
	if !session.beginRecord() {
		return
	}
	defer session.endRecord()
	channel := strings.TrimSpace(input.Channel)
	if channel == "" {
		return
	}
	if session.ensureChannel(channel, input.RateCapHz, input.Adapter) {
		_ = session.flushManifest()
	}
	if !session.allow(channel, input.RateCapHz) {
		session.addRateDrop(channel, input.RateCapHz)
		return
	}
	frame := RecordedFrame{
		TsMs:      time.Since(session.start).Milliseconds(),
		Direction: firstNonEmpty(input.Direction, "upstream"),
		Channel:   channel,
		FrameKind: input.Kind,
		RawB64:    base64.StdEncoding.EncodeToString(input.Data),
	}
	frame.Decoded, frame.DecodeError = r.decodeFrame(channel, input.Kind, input.Data, input.Adapter)
	select {
	case <-session.closing:
		return
	case session.frames <- frame:
		return
	default:
	}
	if input.RateCapHz > 0 {
		session.addQueueDrop(channel, input.RateCapHz)
		return
	}
	timer := time.NewTimer(session.options.QueueSendTimeout)
	defer timer.Stop()
	select {
	case <-session.closing:
	case session.frames <- frame:
	case <-timer.C:
		session.addQueueDrop(channel, input.RateCapHz)
	}
}

func (r *Recorder) HandleModeChange(cfg ChannelConfig) {
	if cfg.Mode != ModeRecord && cfg.Mode != ModeMixed {
		return
	}
	r.mu.Lock()
	session := r.active
	r.mu.Unlock()
	if session == nil {
		return
	}
	if session.ensureChannel(cfg.Channel, cfg.RateCapHz, "") {
		_ = session.flushManifest()
	}
}

func (r *Recorder) List() ([]RecordingManifest, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	out := make([]RecordingManifest, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := readRecordingManifest(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			r.publish("WARN", entry.Name(), http.StatusOK, "invalid recording manifest")
			continue
		}
		out = append(out, manifest)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

func (r *Recorder) Manifest(id string) (RecordingManifest, error) {
	if !isSafeEventTemplateID(id) {
		return RecordingManifest{}, fmt.Errorf("invalid recording id")
	}
	return readRecordingManifest(filepath.Join(r.dir, id))
}

func (r *Recorder) Delete(id string) error {
	if !isSafeEventTemplateID(id) {
		return fmt.Errorf("invalid recording id")
	}
	if active := r.ActiveID(); active == id {
		return fmt.Errorf("cannot delete active recording")
	}
	path := filepath.Join(r.dir, id)
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func (r *Recorder) Frames(id, channel string, offset, limit int) ([]RecordedFrame, error) {
	if !isSafeEventTemplateID(id) {
		return nil, fmt.Errorf("invalid recording id")
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// TODO(M6): add a sidecar .idx file with byte offsets so large recordings
	// can seek directly instead of decoding from the beginning for each page.
	path := filepath.Join(r.dir, id, channelFileName(channel))
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	dec := json.NewDecoder(file)
	out := make([]RecordedFrame, 0)
	index := 0
	for {
		var frame RecordedFrame
		if err := dec.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if index >= offset {
			out = append(out, frame)
			if len(out) >= limit {
				break
			}
		}
		index++
	}
	return out, nil
}

func (r *Recorder) recoverInterrupted() error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			continue
		}
		dir := filepath.Join(r.dir, entry.Name())
		manifest, err := readRecordingManifest(dir)
		if err != nil {
			r.publish("WARN", entry.Name(), http.StatusOK, "invalid recording manifest")
			continue
		}
		if manifest.StoppedAt == nil && manifest.Error != "interrupted" {
			info, err := os.Stat(filepath.Join(dir, "manifest.json"))
			stopped := time.Now().UTC()
			if err == nil {
				stopped = info.ModTime().UTC()
			}
			manifest.StoppedAt = &stopped
			manifest.Error = "interrupted"
			if err := writeRecordingManifest(dir, manifest); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Recorder) decodeFrame(channel, kind string, data []byte, adapter string) (*DecodedFrame, string) {
	// See docs/adr/0004-recording-decoder-strategy.md: M5 records raw frames
	// first and treats decoder output as optional metadata.
	if kind == "binary" {
		return nil, "binary frame has no envelope decoder"
	}
	profileName := normalizeAdapter(adapter)
	if profileName == "" {
		profileName = "raw"
	}
	if profile, ok := adapterProfile(profileName); ok && profile.BaseAdapter == "appsync" {
		return r.decodeAppSyncProfile(profile, data)
	}
	if profileName == "appsync" {
		return r.decodeAppSyncProfile(AdapterProfile{BaseAdapter: "appsync"}, data)
	}
	var payload json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err.Error()
	}
	return &DecodedFrame{PayloadJSON: payload}, ""
}

func (r *Recorder) decodeAppSyncProfile(profile AdapterProfile, data []byte) (*DecodedFrame, string) {
	var outer map[string]any
	if err := json.Unmarshal(data, &outer); err != nil {
		return nil, err.Error()
	}
	value := outer["event"]
	if value == nil {
		if payload, ok := outer["payload"].(map[string]any); ok {
			if dataObj, ok := payload["data"].(map[string]any); ok {
				value = dataObj
			} else {
				value = payload["data"]
			}
		}
	}
	if text, ok := value.(string); ok {
		var inner map[string]any
		if err := json.Unmarshal([]byte(text), &inner); err != nil {
			return nil, err.Error()
		}
		value = inner
	}
	inner, ok := value.(map[string]any)
	if !ok {
		return nil, "appsync envelope not found"
	}
	alias, _ := inner["t"].(string)
	if alias == "" {
		return nil, "appsync alias missing"
	}
	rawValue, exists := inner["e"]
	if !exists {
		return nil, "appsync payload missing"
	}
	encoded, ok := rawValue.(string)
	if !ok {
		payload, err := json.Marshal(rawValue)
		if err != nil {
			return nil, err.Error()
		}
		return &DecodedFrame{Alias: alias, PayloadJSON: payload}, ""
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err.Error()
	}
	typeName := reverseAlias(profile.TypeAliases, alias)
	if typeName == "" {
		typeName = alias
	}
	if r.schemas == nil || r.schemas.Descriptor(typeName) == nil {
		return &DecodedFrame{Alias: alias}, ""
	}
	payload, err := r.schemas.Decode(typeName, raw)
	if err != nil {
		return nil, err.Error()
	}
	return &DecodedFrame{TypeName: typeName, PayloadJSON: payload, Alias: alias}, ""
}

func (s *recordingSession) run() {
	defer close(s.done)
	defer s.closeWriters()
	ticker := time.NewTicker(s.options.ManifestFlushEvery)
	defer ticker.Stop()
	for {
		select {
		case frame := <-s.frames:
			if !s.writeFrame(frame) {
				return
			}
		case <-s.closing:
			s.drainAndStop()
			return
		case <-ticker.C:
			_ = s.flushManifest()
		}
	}
}

func (s *recordingSession) drainAndStop() {
	for {
		select {
		case frame := <-s.frames:
			if !s.writeFrame(frame) {
				return
			}
		case <-s.producersDone:
			s.drainBuffered()
			return
		case <-s.forceStop:
			return
		}
	}
}

func (s *recordingSession) drainBuffered() {
	for {
		select {
		case frame := <-s.frames:
			if !s.writeFrame(frame) {
				return
			}
		default:
			_ = s.flushManifest()
			return
		}
	}
}

func (s *recordingSession) writeFrame(frame RecordedFrame) bool {
	if err := s.write(frame); err != nil {
		s.setError(err.Error())
		if s.recorder != nil {
			s.recorder.publish("ERROR", s.manifest.ID, http.StatusInsufficientStorage, err.Error())
			s.recorder.clearActive(s)
		}
		return false
	}
	return true
}

func (s *recordingSession) runSafe() {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.setError(fmt.Sprintf("recording writer panic: %v", recovered))
			if s.recorder != nil {
				s.recorder.publish("ERROR", s.manifest.ID, http.StatusInternalServerError, "recording writer panic")
				s.recorder.clearActive(s)
			}
		}
	}()
	s.run()
}

func (s *recordingSession) closeWriters() {
	for _, writer := range s.writers {
		_ = writer.writer.Flush()
		_ = writer.file.Sync()
		_ = writer.file.Close()
	}
}

func (s *recordingSession) beginClosing() {
	s.mu.Lock()
	if !s.stopping {
		s.stopping = true
		close(s.closing)
		if s.producers == 0 {
			s.producersDoneOnce.Do(func() { close(s.producersDone) })
		}
	}
	s.mu.Unlock()
}

func (s *recordingSession) forceStopDrain() {
	s.forceStopOnce.Do(func() { close(s.forceStop) })
}

func (s *recordingSession) beginRecord() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return false
	}
	s.producers++
	return true
}

func (s *recordingSession) endRecord() {
	s.mu.Lock()
	s.producers--
	if s.stopping && s.producers == 0 {
		s.producersDoneOnce.Do(func() { close(s.producersDone) })
	}
	s.mu.Unlock()
}

func (s *recordingSession) write(frame RecordedFrame) error {
	s.mu.Lock()
	name := channelFileName(frame.Channel)
	writer := s.writers[name]
	if writer == nil {
		file, err := os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		writer = &recordingWriter{file: file, writer: bufio.NewWriter(file)}
		s.writers[name] = writer
	}
	data, err := json.Marshal(frame)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if _, err := writer.writer.Write(append(data, '\n')); err != nil {
		s.mu.Unlock()
		return err
	}
	for i := range s.manifest.Channels {
		if s.manifest.Channels[i].Channel == frame.Channel {
			s.manifest.Channels[i].Events++
			s.dirty = true
			break
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *recordingSession) ensureChannel(channel string, rateCapHz int, profile string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureChannelLocked(channel, rateCapHz, profile)
}

func (s *recordingSession) ensureChannelLocked(channel string, rateCapHz int, profile string) bool {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return false
	}
	for i := range s.manifest.Channels {
		if s.manifest.Channels[i].Channel == channel {
			changed := false
			if rateCapHz > 0 {
				changed = changed || s.manifest.Channels[i].RateCapHz != rateCapHz
				s.manifest.Channels[i].RateCapHz = rateCapHz
			}
			if profile != "" && s.manifest.Channels[i].AdapterProfile != "" && s.manifest.Channels[i].AdapterProfile != profile {
				s.manifest.Channels[i].ProfileChanges = append(s.manifest.Channels[i].ProfileChanges, RecordingProfileChange{
					TsMs: time.Since(s.start).Milliseconds(), Profile: profile,
				})
				changed = true
			}
			if profile != "" && s.manifest.Channels[i].AdapterProfile == "" {
				s.manifest.Channels[i].AdapterProfile = profile
				changed = true
			}
			s.dirty = s.dirty || changed
			return changed
		}
	}
	s.manifest.Channels = append(s.manifest.Channels, RecordingChannelManifest{
		Channel: channel, RateCapHz: rateCapHz, AdapterProfile: profile,
	})
	sort.Slice(s.manifest.Channels, func(i, j int) bool {
		return s.manifest.Channels[i].Channel < s.manifest.Channels[j].Channel
	})
	s.dirty = true
	return true
}

func (s *recordingSession) addRateDrop(channel string, rateCapHz int) {
	s.mu.Lock()
	s.ensureChannelLocked(channel, rateCapHz, "")
	for i := range s.manifest.Channels {
		if s.manifest.Channels[i].Channel == channel {
			s.manifest.Channels[i].Dropped++
			break
		}
	}
	s.dirty = true
	s.mu.Unlock()
}

func (s *recordingSession) addQueueDrop(channel string, rateCapHz int) {
	s.mu.Lock()
	s.ensureChannelLocked(channel, rateCapHz, "")
	for i := range s.manifest.Channels {
		if s.manifest.Channels[i].Channel == channel {
			s.manifest.Channels[i].QueueDropped++
			break
		}
	}
	s.dirty = true
	s.mu.Unlock()
}

func (s *recordingSession) allow(channel string, capHz int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if capHz <= 0 {
		return true
	}
	now := time.Now().Unix()
	limiter := s.limits[channel]
	if limiter == nil || limiter.sec != now {
		s.limits[channel] = &rateLimiter{sec: now, count: 1}
		return true
	}
	if limiter.count >= capHz {
		return false
	}
	limiter.count++
	return true
}

func (s *recordingSession) setError(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	s.manifest.StoppedAt = &now
	s.manifest.Error = message
	manifest := cloneRecordingManifest(s.manifest)
	s.mu.Unlock()
	_ = writeRecordingManifest(s.dir, manifest)
}

func (s *recordingSession) flushManifest() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	manifest := cloneRecordingManifest(s.manifest)
	s.dirty = false
	s.mu.Unlock()
	return writeRecordingManifest(s.dir, manifest)
}

func readRecordingManifest(dir string) (RecordingManifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return RecordingManifest{}, err
	}
	var manifest RecordingManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return RecordingManifest{}, err
	}
	if manifest.Version != 1 || !isSafeEventTemplateID(manifest.ID) {
		return RecordingManifest{}, fmt.Errorf("invalid recording manifest")
	}
	if manifest.Channels == nil {
		manifest.Channels = make([]RecordingChannelManifest, 0)
	}
	if manifest.SchemaPackIDs == nil {
		manifest.SchemaPackIDs = make([]string, 0)
	}
	return manifest, nil
}

func cloneRecordingManifest(manifest RecordingManifest) RecordingManifest {
	manifest.SchemaPackIDs = append([]string(nil), manifest.SchemaPackIDs...)
	if manifest.Channels != nil {
		channels := manifest.Channels
		manifest.Channels = make([]RecordingChannelManifest, len(channels))
		for i, channel := range channels {
			channel.ProfileChanges = append([]RecordingProfileChange(nil), channel.ProfileChanges...)
			manifest.Channels[i] = channel
		}
	}
	return manifest
}

func writeRecordingManifest(dir string, manifest RecordingManifest) error {
	manifest = cloneRecordingManifest(manifest)
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(dir, "manifest.json"), data, 0o644)
}

func updateManifestAtomic(root, id string, fn func(*RecordingManifest) error) error {
	if !isSafeEventTemplateID(id) {
		return fmt.Errorf("invalid recording id")
	}
	dir := filepath.Join(root, id)
	manifest, err := readRecordingManifest(dir)
	if err != nil {
		return err
	}
	if err := fn(&manifest); err != nil {
		return err
	}
	return writeRecordingManifest(dir, manifest)
}

func (r *Recorder) publish(method, path string, status int, body string) {
	if r.bus == nil {
		return
	}
	event := LogEvent{
		Timestamp:    time.Now().Format("15:04:05"),
		Type:         "RECORD",
		Method:       method,
		Path:         path,
		Status:       status,
		ResponseBody: body,
	}
	logRequest(r.jsonLogs, event)
	r.bus.Publish(event)
}

func recordingID(name string, at time.Time) string {
	base := sanitizePackName(name)
	if base == "" {
		base = "recording"
	}
	runes := []rune(base)
	if len(runes) > 48 {
		base = strings.Trim(string(runes[:48]), "-")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s", name, at.Format(time.RFC3339Nano))))
	return fmt.Sprintf("%s-%s", base, hex.EncodeToString(sum[:])[:8])
}

func channelFileName(channel string) string {
	base := sanitizePackName(channel)
	if base == "" {
		base = "channel"
	}
	sum := sha256.Sum256([]byte(channel))
	return fmt.Sprintf("%s-%s.jsonl", base, hex.EncodeToString(sum[:])[:8])
}

func schemaPackIDs(schemas *SchemaRegistry) []string {
	if schemas == nil {
		return []string{}
	}
	packs := schemas.Packs()
	ids := make([]string, 0, len(packs))
	for _, pack := range packs {
		ids = append(ids, pack.ID)
	}
	sort.Strings(ids)
	return ids
}

func reverseAlias(aliases map[string]string, alias string) string {
	for typeName, value := range aliases {
		if value == alias {
			return typeName
		}
	}
	return ""
}

func frameKind(typ websocket.MessageType) string {
	if typ == websocket.MessageBinary {
		return "binary"
	}
	return "text"
}

func RegisterRecordingRoutes(mux *http.ServeMux, recorder *Recorder) {
	if recorder == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/recordings", func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			items, err := recorder.List()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"recordings": items, "active_id": recorder.ActiveID()})
		case http.MethodPost:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var req struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			manifest, err := recorder.Start(req.Name, req.Description)
			if err != nil {
				status := http.StatusBadRequest
				if strings.Contains(err.Error(), "already active") {
					status = http.StatusConflict
				}
				http.Error(w, err.Error(), status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(manifest)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/__ditto__/api/recordings/", func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		rel := strings.Trim(strings.TrimPrefix(r.URL.Path, "/__ditto__/api/recordings/"), "/")
		parts := strings.Split(rel, "/")
		if len(parts) == 0 || !isSafeEventTemplateID(parts[0]) {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		if len(parts) == 2 && parts[1] == "stop" && r.Method == http.MethodPost {
			manifest, err := recorder.Stop(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(manifest)
			return
		}
		if len(parts) == 2 && parts[1] == "frames" && r.Method == http.MethodGet {
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			frames, err := recorder.Frames(id, r.URL.Query().Get("channel"), offset, limit)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"frames": frames, "offset": offset, "limit": limit})
			return
		}
		if len(parts) == 1 && r.Method == http.MethodGet {
			manifest, err := recorder.Manifest(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(manifest)
			return
		}
		if len(parts) == 1 && r.Method == http.MethodDelete {
			if err := recorder.Delete(id); err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, fs.ErrNotExist) {
					status = http.StatusNotFound
				}
				http.Error(w, err.Error(), status)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})
}

func (r *Recorder) ConvertRecordingToSequence(recordingID string, channels []string) (EventSequence, error) {
	return EventSequence{}, errors.New("not implemented (M6)")
}
