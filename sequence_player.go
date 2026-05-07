package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrSequencePlayerActive = errors.New("sequence has an active player")

type PlayerStatus string

const (
	PlayerIdle      PlayerStatus = "idle"
	PlayerPlaying   PlayerStatus = "playing"
	PlayerPaused    PlayerStatus = "paused"
	PlayerCompleted PlayerStatus = "completed"
	PlayerStopped   PlayerStatus = "stopped"
	PlayerError     PlayerStatus = "error"
)

type PlayOptions struct {
	Vars      map[string]string
	StartStep int
	Speed     float64
	SpeedSet  bool
}

type PlayerState struct {
	SequenceID          string       `json:"sequence_id"`
	Status              PlayerStatus `json:"status"`
	CurrentStep         int          `json:"current_step"`
	TotalSteps          int          `json:"total_steps"`
	Speed               float64      `json:"speed"`
	StartedAt           *time.Time   `json:"started_at,omitempty"`
	UpdatedAt           time.Time    `json:"updated_at"`
	LastError           string       `json:"last_error,omitempty"`
	LastDispatchSummary string       `json:"last_dispatch_summary,omitempty"`
}

type PlayerEvent struct {
	Type            string      `json:"type"`
	State           PlayerState `json:"state"`
	SequenceID      string      `json:"sequence_id"`
	StepID          string      `json:"step_id,omitempty"`
	StepIndex       int         `json:"step_index,omitempty"`
	DispatchSummary string      `json:"dispatch_summary,omitempty"`
	Error           string      `json:"error,omitempty"`
	At              time.Time   `json:"at"`
}

type stepCacheEntry struct {
	payloadHash [32]byte
	encoded     EncodedPayload
}

type PlayerBroadcaster struct {
	mu      sync.Mutex
	clients map[chan PlayerEvent]struct{}
}

func NewPlayerBroadcaster() *PlayerBroadcaster {
	return &PlayerBroadcaster{clients: make(map[chan PlayerEvent]struct{})}
}

func (b *PlayerBroadcaster) Subscribe() chan PlayerEvent {
	ch := make(chan PlayerEvent, 64)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *PlayerBroadcaster) Unsubscribe(ch chan PlayerEvent) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
}

func (b *PlayerBroadcaster) Publish(event PlayerEvent) {
	if b == nil {
		return
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	b.mu.Lock()
	clients := make([]chan PlayerEvent, 0, len(b.clients))
	for ch := range b.clients {
		clients = append(clients, ch)
	}
	b.mu.Unlock()
	for _, ch := range clients {
		select {
		case ch <- event:
		default:
		}
	}
}

func (b *PlayerBroadcaster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	ctx := r.Context()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case event := <-ch:
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

type SequencePlayer struct {
	reg         *EventSequenceRegistry
	templates   *EventTemplateRegistry
	schemas     *SchemaRegistry
	hub         *SocketHub
	broadcaster *PlayerBroadcaster

	mu      sync.RWMutex
	runners map[string]*sequenceRunner
}

func NewSequencePlayer(reg *EventSequenceRegistry, templates *EventTemplateRegistry, schemas *SchemaRegistry, hub *SocketHub, broadcaster *PlayerBroadcaster) *SequencePlayer {
	return &SequencePlayer{
		reg:         reg,
		templates:   templates,
		schemas:     schemas,
		hub:         hub,
		broadcaster: broadcaster,
		runners:     make(map[string]*sequenceRunner),
	}
}

func (p *SequencePlayer) Play(id string, opts PlayOptions) (PlayerState, error) {
	if !isSafeEventTemplateID(id) {
		return PlayerState{}, fmt.Errorf("%w: %q", ErrEventSequenceNotFound, id)
	}
	opts = normalizePlayOptions(opts)

	p.mu.Lock()
	if runner := p.runners[id]; runner != nil {
		state := runner.State()
		if state.Status == PlayerPlaying {
			p.mu.Unlock()
			return state, nil
		}
		if state.Status == PlayerPaused {
			p.mu.Unlock()
			return runner.command(playerCommand{kind: playerCommandResume, opts: opts})
		}
		delete(p.runners, id)
		runner.shutdown()
	}
	seq, err := p.reg.Get(id)
	if err != nil {
		p.mu.Unlock()
		return PlayerState{}, err
	}
	if err := p.reg.validate(seq); err != nil {
		p.mu.Unlock()
		return PlayerState{}, err
	}
	runner := newSequenceRunner(p, seq, opts)
	p.runners[id] = runner
	p.mu.Unlock()

	runner.start()
	return runner.State(), nil
}

func (p *SequencePlayer) Pause(id string) (PlayerState, error) {
	runner, err := p.runner(id)
	if err != nil {
		return PlayerState{}, err
	}
	return runner.command(playerCommand{kind: playerCommandPause})
}

func (p *SequencePlayer) Stop(id string) (PlayerState, error) {
	runner, err := p.runner(id)
	if err != nil {
		return PlayerState{}, err
	}
	return runner.command(playerCommand{kind: playerCommandStop})
}

func (p *SequencePlayer) Seek(id string, step int) (PlayerState, error) {
	runner, err := p.runner(id)
	if err != nil {
		return PlayerState{}, err
	}
	return runner.command(playerCommand{kind: playerCommandSeek, step: step})
}

func (p *SequencePlayer) SetSpeed(id string, speed float64) (PlayerState, error) {
	if speed < 0 {
		return PlayerState{}, fmt.Errorf("speed must be >= 0")
	}
	runner, err := p.runner(id)
	if err != nil {
		return PlayerState{}, err
	}
	return runner.command(playerCommand{kind: playerCommandSpeed, speed: speed})
}

func (p *SequencePlayer) State(id string) (PlayerState, bool) {
	p.mu.RLock()
	runner := p.runners[id]
	p.mu.RUnlock()
	if runner == nil {
		return PlayerState{}, false
	}
	return runner.State(), true
}

func (p *SequencePlayer) States() []PlayerState {
	p.mu.RLock()
	runners := make([]*sequenceRunner, 0, len(p.runners))
	for _, runner := range p.runners {
		runners = append(runners, runner)
	}
	p.mu.RUnlock()
	states := make([]PlayerState, 0, len(runners))
	for _, runner := range runners {
		states = append(states, runner.State())
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].SequenceID < states[j].SequenceID
	})
	return states
}

func (p *SequencePlayer) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	runners := make([]*sequenceRunner, 0, len(p.runners))
	for id, runner := range p.runners {
		runners = append(runners, runner)
		delete(p.runners, id)
	}
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, runner := range runners {
			runner.shutdown()
		}
		for _, runner := range runners {
			<-runner.done
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (p *SequencePlayer) DeleteWhenIdle(id string, deleteFn func() error) error {
	if !isSafeEventTemplateID(id) {
		return fmt.Errorf("%w: %q", ErrEventSequenceNotFound, id)
	}
	p.mu.Lock()
	runner := p.runners[id]
	if runner != nil {
		state := runner.State()
		if isActivePlayerStatus(state.Status) {
			p.mu.Unlock()
			return ErrSequencePlayerActive
		}
	}
	if err := deleteFn(); err != nil {
		p.mu.Unlock()
		return err
	}
	if runner != nil {
		delete(p.runners, id)
	}
	p.mu.Unlock()
	if runner != nil {
		runner.shutdown()
	}
	return nil
}

func (p *SequencePlayer) runner(id string) (*sequenceRunner, error) {
	if !isSafeEventTemplateID(id) {
		return nil, fmt.Errorf("%w: %q", ErrEventSequenceNotFound, id)
	}
	p.mu.RLock()
	runner := p.runners[id]
	p.mu.RUnlock()
	if runner == nil {
		return nil, fmt.Errorf("%w: %q", ErrEventSequenceNotFound, id)
	}
	return runner, nil
}

type playerCommandKind int

const (
	playerCommandResume playerCommandKind = iota
	playerCommandPause
	playerCommandStop
	playerCommandSeek
	playerCommandSpeed
	playerCommandShutdown
)

type playerCommand struct {
	kind  playerCommandKind
	opts  PlayOptions
	step  int
	speed float64
	reply chan playerCommandReply
}

type playerCommandReply struct {
	state PlayerState
	err   error
}

type sequenceRunner struct {
	player *SequencePlayer
	seq    EventSequence
	vars   map[string]string
	cache  map[string]stepCacheEntry

	commands chan playerCommand
	done     chan struct{}

	mu    sync.RWMutex
	state PlayerState
}

func newSequenceRunner(player *SequencePlayer, seq EventSequence, opts PlayOptions) *sequenceRunner {
	now := time.Now().UTC()
	current := clampStep(opts.StartStep, len(seq.Steps))
	state := PlayerState{
		SequenceID:  seq.ID,
		Status:      PlayerPlaying,
		CurrentStep: current,
		TotalSteps:  len(seq.Steps),
		Speed:       opts.Speed,
		StartedAt:   &now,
		UpdatedAt:   now,
	}
	return &sequenceRunner{
		player:   player,
		seq:      cloneEventSequence(seq),
		vars:     cloneStringMap(opts.Vars),
		cache:    make(map[string]stepCacheEntry),
		commands: make(chan playerCommand, 16),
		done:     make(chan struct{}),
		state:    state,
	}
}

func (r *sequenceRunner) start() {
	go r.loop()
	r.publishState("state")
}

func (r *sequenceRunner) State() PlayerState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return clonePlayerState(r.state)
}

func (r *sequenceRunner) setState(mut func(*PlayerState)) PlayerState {
	r.mu.Lock()
	defer r.mu.Unlock()
	mut(&r.state)
	r.state.UpdatedAt = time.Now().UTC()
	return clonePlayerState(r.state)
}

func (r *sequenceRunner) command(cmd playerCommand) (PlayerState, error) {
	cmd.reply = make(chan playerCommandReply, 1)
	select {
	case r.commands <- cmd:
	case <-r.done:
		return r.State(), nil
	}
	select {
	case reply := <-cmd.reply:
		return reply.state, reply.err
	case <-r.done:
		return r.State(), nil
	}
}

func (r *sequenceRunner) shutdown() {
	cmd := playerCommand{kind: playerCommandShutdown, reply: make(chan playerCommandReply, 1)}
	select {
	case r.commands <- cmd:
	case <-r.done:
	}
}

func (r *sequenceRunner) loop() {
	defer close(r.done)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}

	var waiting bool
	var waitUntil time.Time
	var remaining time.Duration
	for {
		state := r.State()
		if state.Status == PlayerPlaying && !waiting {
			if state.CurrentStep >= len(r.seq.Steps) {
				r.complete()
				state = r.State()
				if state.Status != PlayerPlaying {
					continue
				}
			}
			delay := r.effectiveDelay(r.seq.Steps[state.CurrentStep].DelayMs, state.Speed)
			if delay <= 0 {
				select {
				case cmd := <-r.commands:
					nextWaiting, nextRemaining, exit := r.handleCommand(cmd, timer, false, time.Time{}, remaining)
					waiting, remaining = nextWaiting, nextRemaining
					if waiting {
						waitUntil = time.Now().Add(remaining)
					}
					if exit {
						return
					}
				default:
					r.dispatchCurrentStep()
				}
				continue
			}
			waiting = true
			remaining = delay
			waitUntil = time.Now().Add(delay)
			resetTimer(timer, delay)
		}

		if waiting {
			select {
			case <-timer.C:
				waiting = false
				remaining = 0
				r.dispatchCurrentStep()
			case cmd := <-r.commands:
				nextWaiting, nextRemaining, exit := r.handleCommand(cmd, timer, waiting, waitUntil, remaining)
				waiting, remaining = nextWaiting, nextRemaining
				if waiting {
					waitUntil = time.Now().Add(remaining)
				}
				if exit {
					return
				}
			}
			continue
		}

		cmd := <-r.commands
		nextWaiting, nextRemaining, exit := r.handleCommand(cmd, timer, false, waitUntil, remaining)
		waiting, remaining = nextWaiting, nextRemaining
		if waiting {
			waitUntil = time.Now().Add(remaining)
		}
		if exit {
			return
		}
	}
}

func (r *sequenceRunner) handleCommand(cmd playerCommand, timer *time.Timer, waiting bool, waitUntil time.Time, remaining time.Duration) (bool, time.Duration, bool) {
	reply := func(state PlayerState, err error) {
		if cmd.reply != nil {
			cmd.reply <- playerCommandReply{state: state, err: err}
		}
	}
	switch cmd.kind {
	case playerCommandResume:
		current := r.State()
		if current.Status != PlayerPaused {
			reply(current, fmt.Errorf("cannot resume: status is %s", current.Status))
			return waiting, remaining, false
		}
		opts := normalizePlayOptions(cmd.opts)
		nextRemaining := remaining
		if nextRemaining > 0 {
			resetTimer(timer, nextRemaining)
		}
		state := r.setState(func(s *PlayerState) {
			s.Status = PlayerPlaying
			s.LastError = ""
			if opts.SpeedSet {
				s.Speed = opts.Speed
			}
		})
		r.publishState("state")
		reply(state, nil)
		return nextRemaining > 0, nextRemaining, false
	case playerCommandPause:
		current := r.State()
		if current.Status != PlayerPlaying {
			reply(current, fmt.Errorf("cannot pause: status is %s", current.Status))
			return waiting, remaining, false
		}
		if waiting {
			stopTimer(timer)
			remaining = time.Until(waitUntil)
			if remaining < 0 {
				remaining = 0
			}
		}
		state := r.setState(func(s *PlayerState) {
			if s.Status == PlayerPlaying {
				s.Status = PlayerPaused
			}
		})
		r.publishState("state")
		reply(state, nil)
		return false, remaining, false
	case playerCommandStop:
		current := r.State()
		if !isActivePlayerStatus(current.Status) {
			reply(current, fmt.Errorf("cannot stop: status is %s", current.Status))
			return waiting, remaining, false
		}
		if waiting {
			stopTimer(timer)
		}
		state := r.setState(func(s *PlayerState) {
			s.Status = PlayerStopped
		})
		r.publishState("stopped")
		reply(state, nil)
		return false, 0, false
	case playerCommandSeek:
		if waiting {
			stopTimer(timer)
		}
		step := clampStep(cmd.step, len(r.seq.Steps))
		state := r.setState(func(s *PlayerState) {
			s.CurrentStep = step
			s.LastError = ""
		})
		r.publishState("state")
		reply(state, nil)
		return false, 0, false
	case playerCommandSpeed:
		if cmd.speed < 0 {
			reply(r.State(), fmt.Errorf("speed must be >= 0"))
			return waiting, remaining, false
		}
		currentState := r.State()
		oldSpeed := currentState.Speed
		var nextRemaining time.Duration
		if waiting {
			nextRemaining = time.Until(waitUntil)
			if nextRemaining < 0 {
				nextRemaining = 0
			}
			stopTimer(timer)
			nextRemaining = rescaleRemaining(nextRemaining, oldSpeed, cmd.speed)
			if nextRemaining > 0 {
				resetTimer(timer, nextRemaining)
			}
		} else if currentState.Status == PlayerPaused && remaining > 0 {
			nextRemaining = rescaleRemaining(remaining, oldSpeed, cmd.speed)
		} else {
			nextRemaining = remaining
		}
		state := r.setState(func(s *PlayerState) {
			s.Speed = cmd.speed
		})
		r.publishState("state")
		reply(state, nil)
		return waiting && nextRemaining > 0, nextRemaining, false
	case playerCommandShutdown:
		if waiting {
			stopTimer(timer)
		}
		state := r.setState(func(s *PlayerState) {
			if isActivePlayerStatus(s.Status) {
				s.Status = PlayerStopped
			}
		})
		if state.Status == PlayerStopped {
			r.publishState("stopped")
		}
		reply(state, nil)
		return false, 0, true
	default:
		reply(r.State(), fmt.Errorf("unknown player command"))
		return waiting, remaining, false
	}
}

func (r *sequenceRunner) dispatchCurrentStep() {
	state := r.State()
	if state.Status != PlayerPlaying || state.CurrentStep >= len(r.seq.Steps) {
		return
	}
	index := state.CurrentStep
	step := r.seq.Steps[index]
	rendered, err := r.player.reg.ResolveStep(r.seq, step, r.vars, index)
	if err != nil {
		r.fail(err)
		return
	}
	rendered, err = r.prepareRenderedStep(step, rendered)
	if err != nil {
		r.fail(err)
		return
	}
	result, err := dispatchRendered(r.player.hub, r.player.schemas, rendered, nil)
	if err != nil {
		r.fail(err)
		return
	}
	summary := dispatchSummary(result)
	next := r.setState(func(s *PlayerState) {
		s.CurrentStep = index + 1
		s.LastDispatchSummary = summary
	})
	r.publishStep(step.ID, index, summary)
	_ = next
}

func (r *sequenceRunner) prepareRenderedStep(step EventSequenceStep, rendered RenderedDispatch) (RenderedDispatch, error) {
	typeName := strings.TrimSpace(rendered.TypeName)
	if typeName == "" {
		return rendered, nil
	}
	if r.player.schemas == nil {
		return rendered, fmt.Errorf("schema registry is not available")
	}
	hash := sha256.Sum256([]byte(typeName + "\x00" + string(rendered.Payload)))
	if cached, ok := r.cache[step.ID]; ok && cached.payloadHash == hash {
		encoded := cached.encoded
		rendered.EncodedPayload = &encoded
		return rendered, nil
	}
	encoded, err := r.player.schemas.Encode(typeName, rendered.Payload)
	if err != nil {
		return rendered, fmt.Errorf("protobuf encode failed: %w", err)
	}
	r.cache[step.ID] = stepCacheEntry{payloadHash: hash, encoded: encoded}
	rendered.EncodedPayload = &encoded
	return rendered, nil
}

func (r *sequenceRunner) fail(err error) {
	// TODO: M5 — make per-step error policy configurable.
	state := r.setState(func(s *PlayerState) {
		s.Status = PlayerError
		s.LastError = err.Error()
	})
	r.publishError(state, err)
}

func (r *sequenceRunner) complete() {
	switch r.seq.OnEnd {
	case "loop":
		state := r.setState(func(s *PlayerState) {
			s.CurrentStep = 0
			s.Status = PlayerPlaying
		})
		r.publishStateSnapshot("looped", state)
	case "reset":
		state := r.setState(func(s *PlayerState) {
			s.CurrentStep = 0
			s.Status = PlayerCompleted
		})
		r.publishStateSnapshot("completed", state)
	default:
		state := r.setState(func(s *PlayerState) {
			s.CurrentStep = maxInt(0, len(r.seq.Steps)-1)
			s.Status = PlayerCompleted
		})
		r.publishStateSnapshot("completed", state)
	}
}

func (r *sequenceRunner) publishState(kind string) {
	r.publishStateSnapshot(kind, r.State())
}

func (r *sequenceRunner) publishStateSnapshot(kind string, state PlayerState) {
	r.player.broadcaster.Publish(PlayerEvent{
		Type:       kind,
		State:      state,
		SequenceID: r.seq.ID,
		At:         time.Now().UTC(),
	})
}

func (r *sequenceRunner) publishStep(stepID string, stepIndex int, summary string) {
	r.player.broadcaster.Publish(PlayerEvent{
		Type:            "step",
		State:           r.State(),
		SequenceID:      r.seq.ID,
		StepID:          stepID,
		StepIndex:       stepIndex,
		DispatchSummary: summary,
		At:              time.Now().UTC(),
	})
}

func (r *sequenceRunner) publishError(state PlayerState, err error) {
	r.player.broadcaster.Publish(PlayerEvent{
		Type:       "error",
		State:      state,
		SequenceID: r.seq.ID,
		Error:      err.Error(),
		At:         time.Now().UTC(),
	})
}

func (r *sequenceRunner) effectiveDelay(delayMs int64, speed float64) time.Duration {
	if delayMs <= 0 || speed == 0 {
		return 0
	}
	if speed < 0 {
		speed = 1
	}
	return time.Duration(float64(time.Duration(delayMs)*time.Millisecond) / speed)
}

func normalizePlayOptions(opts PlayOptions) PlayOptions {
	if opts.Speed < 0 {
		opts.Speed = 1
	}
	if opts.Speed == 0 && !opts.SpeedSet {
		opts.Speed = 1
	}
	return opts
}

func clampStep(step, total int) int {
	if step < 0 {
		return 0
	}
	if step > total {
		return total
	}
	return step
}

func rescaleRemaining(remaining time.Duration, oldSpeed, newSpeed float64) time.Duration {
	if remaining <= 0 || newSpeed == 0 {
		return 0
	}
	if oldSpeed <= 0 {
		oldSpeed = 1
	}
	scaled := time.Duration(float64(remaining) * oldSpeed / newSpeed)
	if scaled < 0 {
		return 0
	}
	return scaled
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func resetTimer(timer *time.Timer, d time.Duration) {
	stopTimer(timer)
	timer.Reset(d)
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func clonePlayerState(state PlayerState) PlayerState {
	if state.StartedAt != nil {
		started := *state.StartedAt
		state.StartedAt = &started
	}
	return state
}

func isActivePlayerStatus(status PlayerStatus) bool {
	return status == PlayerPlaying || status == PlayerPaused
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
