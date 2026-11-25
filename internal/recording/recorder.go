// Package recording captures socket traffic on channels in record or mixed
// mode into recordings on disk: a manifest plus one JSONL file of raw
// frames (with best-effort decoded payloads) per channel.
package recording

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ditto/internal/adapter"
	"ditto/internal/channelmode"
	"ditto/internal/eventlog"
	"ditto/internal/jsonfile"
	"ditto/internal/schema"
)

const (
	recordingFrameQueueCapacity = 4096
	recordingManifestFlushEvery = 2 * time.Second
	recordingQueueSendTimeout   = 50 * time.Millisecond
	recordingStopTimeout        = 5 * time.Second
)

// Errors returned by Recorder methods.
var (
	// ErrActive reports that a recording is active: another cannot start and
	// the active one cannot be deleted.
	ErrActive = errors.New("a recording is already active")
	// ErrNotActive reports a Stop for a recording that is not the active one.
	ErrNotActive = errors.New("recording is not active")
	// ErrNotFound reports an invalid recording ID.
	ErrNotFound = errors.New("recording not found")
)

// Options tunes a recorder's frame queue and flushing; zero fields use the
// defaults.
type Options struct {
	FrameQueueCapacity int
	ManifestFlushEvery time.Duration
	QueueSendTimeout   time.Duration
	StopTimeout        time.Duration
}

// Manifest describes one recording: its channels, counts and status.
type Manifest struct {
	Version        int               `json:"version"`
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	StartedAt      time.Time         `json:"started_at"`
	StoppedAt      *time.Time        `json:"stopped_at"`
	Channels       []ChannelManifest `json:"channels"`
	AdapterProfile string            `json:"adapter_profile,omitempty"`
	SchemaPackIDs  []string          `json:"schema_pack_ids,omitempty"`
	Error          string            `json:"error,omitempty"`
}

// ChannelManifest is one recorded channel's counters and profile history.
type ChannelManifest struct {
	Channel        string          `json:"channel"`
	Events         int             `json:"events"`
	Dropped        int             `json:"dropped"`
	QueueDropped   int             `json:"queue_dropped,omitempty"`
	RateCapHz      int             `json:"rate_cap_hz,omitempty"`
	AdapterProfile string          `json:"adapter_profile,omitempty"`
	ProfileChanges []ProfileChange `json:"profile_changes,omitempty"`
}

// ProfileChange notes when a channel's adapter profile changed mid-recording.
type ProfileChange struct {
	TsMs    int64  `json:"ts_ms"`
	Profile string `json:"profile"`
}

// Frame is one recorded frame as stored in a channel's JSONL file.
type Frame struct {
	TsMs        int64            `json:"ts_ms"`
	Direction   string           `json:"direction"`
	Channel     string           `json:"channel"`
	FrameKind   string           `json:"frame_kind"`
	RawB64      string           `json:"raw_b64"`
	Decoded     *adapter.Decoded `json:"decoded,omitempty"`
	DecodeError string           `json:"decode_error"`
}

// Input is a frame offered to the recorder by the socket hub or live bridge.
type Input struct {
	Channel   string
	Direction string
	Kind      string
	Data      []byte
	Adapter   string
	RateCapHz int
}

// Recorder records frames into at most one active recording at a time and
// lists, reads and deletes past recordings. It is safe for concurrent use.
type Recorder struct {
	mu       sync.Mutex
	dir      string
	schemas  *schema.Registry
	adapters *adapter.Registry
	modes    *channelmode.Registry
	bus      *eventlog.Bus
	active   *session
	options  Options
}

type session struct {
	mu                sync.Mutex
	manifest          Manifest
	dir               string
	start             time.Time
	frames            chan Frame
	closing           chan struct{}
	forceStop         chan struct{}
	done              chan struct{}
	producersDone     chan struct{}
	producersDoneOnce sync.Once
	forceStopOnce     sync.Once
	writers           map[string]*channelWriter
	limits            map[string]*rateLimiter
	recorder          *Recorder
	dirty             bool
	options           Options
	stopping          bool
	producers         int
}

type channelWriter struct {
	file   *os.File
	writer *bufio.Writer
}

type rateLimiter struct {
	sec   int64
	count int
}

// Config holds the recorder's collaborators. Dir is required; the
// other fields are optional.
type Config struct {
	Dir      string
	Schemas  *schema.Registry
	Adapters *adapter.Registry
	Modes    *channelmode.Registry
	Bus      *eventlog.Bus
	// Options tunes queueing and flushing; zero fields use the defaults.
	Options Options
}

// NewRecorder creates the recordings directory and marks any recording left
// active by a previous run as interrupted.
func NewRecorder(cfg Config) (*Recorder, error) {
	if strings.TrimSpace(cfg.Dir) == "" {
		return nil, fmt.Errorf("recordings dir is required")
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, err
	}
	r := &Recorder{dir: cfg.Dir, schemas: cfg.Schemas, adapters: cfg.Adapters, modes: cfg.Modes, bus: cfg.Bus,
		options: normalizeOptions(cfg.Options)}
	if err := r.recoverInterrupted(); err != nil {
		return nil, err
	}
	return r, nil
}

func normalizeOptions(options Options) Options {
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

// Start begins a new recording. Only one can be active.
func (r *Recorder) Start(name, description string) (Manifest, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Recording"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil {
		return Manifest{}, fmt.Errorf("%w: %q", ErrActive, r.active.manifest.ID)
	}
	id := recordingID(name, time.Now().UTC())
	dir := filepath.Join(r.dir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Manifest{}, err
	}
	now := time.Now().UTC()
	manifest := Manifest{
		Version:       1,
		ID:            id,
		Name:          name,
		Description:   strings.TrimSpace(description),
		StartedAt:     now,
		Channels:      []ChannelManifest{},
		SchemaPackIDs: schemaPackIDs(r.schemas),
	}
	session := &session{
		manifest:      manifest,
		dir:           dir,
		start:         now,
		frames:        make(chan Frame, r.options.FrameQueueCapacity),
		closing:       make(chan struct{}),
		forceStop:     make(chan struct{}),
		done:          make(chan struct{}),
		producersDone: make(chan struct{}),
		writers:       make(map[string]*channelWriter),
		limits:        make(map[string]*rateLimiter),
		recorder:      r,
		options:       r.options,
	}
	r.active = session
	if r.modes != nil {
		for _, cfg := range r.modes.Snapshot() {
			if cfg.Mode == channelmode.Record || cfg.Mode == channelmode.Mixed {
				session.ensureChannel(cfg.Channel, cfg.RateCapHz, "")
			}
		}
	}
	if err := writeManifest(dir, session.manifest); err != nil {
		r.active = nil
		return Manifest{}, err
	}
	go session.runSafe()
	r.publish("RECORD_START", id, http.StatusCreated, "")
	return session.manifest, nil
}

// Stop drains the active recording's queue, closes its files and returns
// its final manifest.
func (r *Recorder) Stop(id string) (Manifest, error) {
	r.mu.Lock()
	session := r.active
	if session == nil || session.manifest.ID != id {
		r.mu.Unlock()
		return Manifest{}, fmt.Errorf("%w: %q", ErrNotActive, id)
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
	manifest := cloneManifest(session.manifest)
	session.mu.Unlock()
	if err := writeManifest(session.dir, manifest); err != nil {
		return Manifest{}, err
	}
	r.publish("RECORD_STOP", id, http.StatusOK, "")
	return manifest, nil
}

// ActiveID returns the active recording's ID, or "".
func (r *Recorder) ActiveID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		return ""
	}
	return r.active.manifest.ID
}

func (r *Recorder) clearActive(session *session) {
	r.mu.Lock()
	if r.active == session {
		r.active = nil
	}
	r.mu.Unlock()
}

// Record queues a frame for the active recording, if any, applying the
// channel's rate cap. It never blocks the caller for long: frames that do
// not fit the queue are counted as dropped.
func (r *Recorder) Record(input Input) {
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
	direction := input.Direction
	if strings.TrimSpace(direction) == "" {
		direction = "upstream"
	}
	frame := Frame{
		TsMs:      time.Since(session.start).Milliseconds(),
		Direction: direction,
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

// HandleModeChange adds a channel switched to record or mixed mode to the
// active recording. Register it with channelmode.Registry.OnChange.
func (r *Recorder) HandleModeChange(cfg channelmode.Config) {
	if cfg.Mode != channelmode.Record && cfg.Mode != channelmode.Mixed {
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

// List returns every recording's manifest.
func (r *Recorder) List() ([]Manifest, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	out := make([]Manifest, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := readManifest(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			r.publish("WARN", entry.Name(), http.StatusOK, "invalid recording manifest")
			continue
		}
		out = append(out, manifest)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// Manifest returns one recording's manifest.
func (r *Recorder) Manifest(id string) (Manifest, error) {
	if !jsonfile.ValidID(id) {
		return Manifest{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return readManifest(filepath.Join(r.dir, id))
}

// Delete removes a recording that is not active.
func (r *Recorder) Delete(id string) error {
	if !jsonfile.ValidID(id) {
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if active := r.ActiveID(); active == id {
		return fmt.Errorf("cannot delete %q: %w", id, ErrActive)
	}
	path := filepath.Join(r.dir, id)
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

// Frames returns a page of one channel's recorded frames.
func (r *Recorder) Frames(id, channel string, offset, limit int) ([]Frame, error) {
	if !jsonfile.ValidID(id) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
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
	defer func() { _ = file.Close() }() // read-only
	dec := json.NewDecoder(file)
	out := make([]Frame, 0)
	index := 0
	for {
		var frame Frame
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
		manifest, err := readManifest(dir)
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
			if err := writeManifest(dir, manifest); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Recorder) decodeFrame(channel, kind string, data []byte, adapterName string) (*adapter.Decoded, string) {
	// See docs/adr/0004-recording-decoder-strategy.md: M5 records raw frames
	// first and treats decoder output as optional metadata.
	return r.adapters.DecodeFrame(r.schemas, kind, data, adapterName)
}

func (s *session) run() {
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

func (s *session) drainAndStop() {
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

func (s *session) drainBuffered() {
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

func (s *session) writeFrame(frame Frame) bool {
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

func (s *session) runSafe() {
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

func (s *session) closeWriters() {
	for _, writer := range s.writers {
		_ = writer.writer.Flush()
		_ = writer.file.Sync()
		_ = writer.file.Close()
	}
}

func (s *session) beginClosing() {
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

func (s *session) forceStopDrain() {
	s.forceStopOnce.Do(func() { close(s.forceStop) })
}

func (s *session) beginRecord() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return false
	}
	s.producers++
	return true
}

func (s *session) endRecord() {
	s.mu.Lock()
	s.producers--
	if s.stopping && s.producers == 0 {
		s.producersDoneOnce.Do(func() { close(s.producersDone) })
	}
	s.mu.Unlock()
}

func (s *session) write(frame Frame) error {
	s.mu.Lock()
	name := channelFileName(frame.Channel)
	writer := s.writers[name]
	if writer == nil {
		file, err := os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		writer = &channelWriter{file: file, writer: bufio.NewWriter(file)}
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

func (s *session) ensureChannel(channel string, rateCapHz int, profile string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureChannelLocked(channel, rateCapHz, profile)
}

func (s *session) ensureChannelLocked(channel string, rateCapHz int, profile string) bool {
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
				s.manifest.Channels[i].ProfileChanges = append(s.manifest.Channels[i].ProfileChanges, ProfileChange{
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
	s.manifest.Channels = append(s.manifest.Channels, ChannelManifest{
		Channel: channel, RateCapHz: rateCapHz, AdapterProfile: profile,
	})
	sort.Slice(s.manifest.Channels, func(i, j int) bool {
		return s.manifest.Channels[i].Channel < s.manifest.Channels[j].Channel
	})
	s.dirty = true
	return true
}

func (s *session) addRateDrop(channel string, rateCapHz int) {
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

func (s *session) addQueueDrop(channel string, rateCapHz int) {
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

func (s *session) allow(channel string, capHz int) bool {
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

func (s *session) setError(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	s.manifest.StoppedAt = &now
	s.manifest.Error = message
	manifest := cloneManifest(s.manifest)
	s.mu.Unlock()
	_ = writeManifest(s.dir, manifest)
}

func (s *session) flushManifest() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	manifest := cloneManifest(s.manifest)
	s.dirty = false
	s.mu.Unlock()
	return writeManifest(s.dir, manifest)
}

func readManifest(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.Version != 1 || !jsonfile.ValidID(manifest.ID) {
		return Manifest{}, fmt.Errorf("invalid recording manifest")
	}
	if manifest.Channels == nil {
		manifest.Channels = make([]ChannelManifest, 0)
	}
	if manifest.SchemaPackIDs == nil {
		manifest.SchemaPackIDs = make([]string, 0)
	}
	return manifest, nil
}

func cloneManifest(manifest Manifest) Manifest {
	manifest.SchemaPackIDs = append([]string(nil), manifest.SchemaPackIDs...)
	if manifest.Channels != nil {
		channels := manifest.Channels
		manifest.Channels = make([]ChannelManifest, len(channels))
		for i, channel := range channels {
			channel.ProfileChanges = append([]ProfileChange(nil), channel.ProfileChanges...)
			manifest.Channels[i] = channel
		}
	}
	return manifest
}

func writeManifest(dir string, manifest Manifest) error {
	manifest = cloneManifest(manifest)
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return jsonfile.WriteAtomic(filepath.Join(dir, "manifest.json"), data, 0o644)
}

func (r *Recorder) publish(method, path string, status int, body string) {
	if r.bus == nil {
		return
	}
	event := eventlog.Event{
		Type:         "RECORD",
		Method:       method,
		Path:         path,
		Status:       status,
		ResponseBody: body,
	}
	r.bus.Publish(event)
}

func recordingID(name string, at time.Time) string {
	return jsonfile.HashedID(name, "recording", fmt.Sprintf("%s\n%s", name, at.Format(time.RFC3339Nano)))
}

func channelFileName(channel string) string {
	base := jsonfile.Slug(channel)
	if base == "" {
		base = "channel"
	}
	sum := sha256.Sum256([]byte(channel))
	return fmt.Sprintf("%s-%s.jsonl", base, hex.EncodeToString(sum[:])[:8])
}

func schemaPackIDs(schemas *schema.Registry) []string {
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
