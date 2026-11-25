package sequence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ditto/internal/adapter"
	"ditto/internal/jsonfile"
	"ditto/internal/schema"
	"ditto/internal/socket"
)

// ErrActive is returned when deleting a sequence that is playing or paused.
var ErrActive = errors.New("sequence has an active player")

// Status is a player's state for one sequence.
type Status string

const (
	StatusIdle      Status = "idle"
	StatusPlaying   Status = "playing"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusStopped   Status = "stopped"
	StatusError     Status = "error"
)

// PlayOptions configures a run: runtime variables, the starting step and
// the speed (honoured only when SpeedSet, so 0 can mean "no delays").
type PlayOptions struct {
	Vars      map[string]string
	StartStep int
	Speed     float64
	SpeedSet  bool
}

// State is a sequence's playback state.
type State struct {
	SequenceID          string     `json:"sequence_id"`
	Status              Status     `json:"status"`
	CurrentStep         int        `json:"current_step"`
	TotalSteps          int        `json:"total_steps"`
	Speed               float64    `json:"speed"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
	LastError           string     `json:"last_error,omitempty"`
	LastDispatchSummary string     `json:"last_dispatch_summary,omitempty"`
}

// Event is a player notification sent to dashboard subscribers.
type Event struct {
	Type            string    `json:"type"`
	State           State     `json:"state"`
	SequenceID      string    `json:"sequence_id"`
	StepID          string    `json:"step_id,omitempty"`
	StepIndex       int       `json:"step_index,omitempty"`
	DelayMs         int64     `json:"delay_ms,omitempty"`
	DispatchSummary string    `json:"dispatch_summary,omitempty"`
	Error           string    `json:"error,omitempty"`
	At              time.Time `json:"at"`
}

type stepCacheEntry struct {
	payloadHash [32]byte
	encoded     adapter.Payload
}

// Broadcaster fans player events out to SSE subscribers.
type Broadcaster struct {
	mu      sync.Mutex
	clients map[chan Event]struct{}
}

// NewBroadcaster returns a broadcaster with no subscribers.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{clients: make(map[chan Event]struct{})}
}

// Subscribe returns a channel of later events.
func (b *Broadcaster) Subscribe() chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe stops broadcasting to ch.
//
// The channel is intentionally not closed: Publish snapshots subscribers under
// the lock and sends without the lock held, so a concurrent close could panic.
// Owning goroutines should exit on their own signal, usually r.Context().Done().
func (b *Broadcaster) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
}

// Publish sends event to every subscriber, dropping it for those whose
// buffer is full.
func (b *Broadcaster) Publish(event Event) {
	if b == nil {
		return
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	b.mu.Lock()
	clients := make([]chan Event, 0, len(b.clients))
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

// ServeHTTP streams player events as server-sent events.
func (b *Broadcaster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event := <-ch:
			data, _ := json.Marshal(event)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// Player runs sequences on the socket hub, one runner goroutine per
// sequence, and publishes their progress. It is safe for concurrent use.
type Player struct {
	reg         *Store
	schemas     *schema.Registry
	hub         *socket.Hub
	broadcaster *Broadcaster
	clock       Clock

	mu      sync.RWMutex
	runners map[string]*sequenceRunner
}

// PlayerConfig holds the player's collaborators. Sequences, Hub and
// Broadcaster are required; Schemas is needed only for protobuf steps and a
// nil Clock uses the system clock.
type PlayerConfig struct {
	Sequences   *Store
	Schemas     *schema.Registry
	Hub         *socket.Hub
	Broadcaster *Broadcaster
	Clock       Clock
}

// NewPlayer returns a player with no running sequences.
func NewPlayer(cfg PlayerConfig) *Player {
	clock := cfg.Clock
	if clock == nil {
		clock = systemClock
	}
	return &Player{
		reg:         cfg.Sequences,
		schemas:     cfg.Schemas,
		hub:         cfg.Hub,
		broadcaster: cfg.Broadcaster,
		clock:       clock,
		runners:     make(map[string]*sequenceRunner),
	}
}

// Play starts the sequence, resumes it when paused, or returns its state
// when it is already playing.
func (p *Player) Play(id string, opts PlayOptions) (State, error) {
	// TODO: M7 — support fire-and-forget runs for HTTP triggers so concurrent
	// trigger hits can launch independent snapshots of the same sequence ID.
	if !jsonfile.ValidID(id) {
		return State{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	opts = normalizePlayOptions(opts)

	p.mu.Lock()
	if runner := p.runners[id]; runner != nil {
		state := runner.State()
		if state.Status == StatusPlaying {
			p.mu.Unlock()
			return state, nil
		}
		if state.Status == StatusPaused {
			p.mu.Unlock()
			return runner.command(playerCommand{kind: playerCommandResume, opts: opts})
		}
		delete(p.runners, id)
		runner.shutdown()
	}
	seq, err := p.reg.Get(id)
	if err != nil {
		p.mu.Unlock()
		return State{}, err
	}
	if err := p.reg.validate(seq); err != nil {
		p.mu.Unlock()
		return State{}, err
	}
	runner := newSequenceRunner(p, seq, opts)
	p.runners[id] = runner
	p.mu.Unlock()

	runner.start()
	return runner.State(), nil
}

// Pause pauses a playing sequence, keeping the remaining delay.
func (p *Player) Pause(id string) (State, error) {
	runner, err := p.runner(id)
	if err != nil {
		return State{}, err
	}
	return runner.command(playerCommand{kind: playerCommandPause})
}

// Stop stops the sequence.
func (p *Player) Stop(id string) (State, error) {
	runner, err := p.runner(id)
	if err != nil {
		return State{}, err
	}
	return runner.command(playerCommand{kind: playerCommandStop})
}

// Seek moves the sequence to step.
func (p *Player) Seek(id string, step int) (State, error) {
	runner, err := p.runner(id)
	if err != nil {
		return State{}, err
	}
	return runner.command(playerCommand{kind: playerCommandSeek, step: step})
}

// SetSpeed changes the playback speed, rescaling any pending delay.
func (p *Player) SetSpeed(id string, speed float64) (State, error) {
	if speed < 0 {
		return State{}, fmt.Errorf("speed must be >= 0")
	}
	runner, err := p.runner(id)
	if err != nil {
		return State{}, err
	}
	return runner.command(playerCommand{kind: playerCommandSpeed, speed: speed})
}

// State returns the sequence's playback state, if it has a runner.
func (p *Player) State(id string) (State, bool) {
	p.mu.RLock()
	runner := p.runners[id]
	p.mu.RUnlock()
	if runner == nil {
		return State{}, false
	}
	return runner.State(), true
}

// States returns the playback state of every sequence with a runner.
func (p *Player) States() []State {
	p.mu.RLock()
	runners := make([]*sequenceRunner, 0, len(p.runners))
	for _, runner := range p.runners {
		runners = append(runners, runner)
	}
	p.mu.RUnlock()
	states := make([]State, 0, len(runners))
	for _, runner := range runners {
		states = append(states, runner.State())
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].SequenceID < states[j].SequenceID
	})
	return states
}

// Shutdown stops every runner, waiting until ctx is done.
func (p *Player) Shutdown(ctx context.Context) error {
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

// DeleteWhenIdle calls deleteFn unless the sequence is playing or paused,
// in which case it returns ErrActive.
func (p *Player) DeleteWhenIdle(id string, deleteFn func() error) error {
	if !jsonfile.ValidID(id) {
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	p.mu.Lock()
	runner := p.runners[id]
	if runner != nil {
		state := runner.State()
		if isActiveStatus(state.Status) {
			p.mu.Unlock()
			return ErrActive
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

func (p *Player) runner(id string) (*sequenceRunner, error) {
	if !jsonfile.ValidID(id) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	p.mu.RLock()
	runner := p.runners[id]
	p.mu.RUnlock()
	if runner == nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
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
	state State
	err   error
}

type sequenceRunner struct {
	player *Player
	seq    Sequence
	vars   map[string]string
	cache  map[string]stepCacheEntry

	commands chan playerCommand
	done     chan struct{}

	mu    sync.RWMutex
	state State
}

func newSequenceRunner(player *Player, seq Sequence, opts PlayOptions) *sequenceRunner {
	now := player.clock.Now().UTC()
	current := clampStep(opts.StartStep, len(seq.Steps))
	state := State{
		SequenceID:  seq.ID,
		Status:      StatusPlaying,
		CurrentStep: current,
		TotalSteps:  len(seq.Steps),
		Speed:       opts.Speed,
		StartedAt:   &now,
		UpdatedAt:   now,
	}
	return &sequenceRunner{
		player:   player,
		seq:      clone(seq),
		vars:     maps.Clone(opts.Vars),
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

func (r *sequenceRunner) State() State {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneState(r.state)
}

func (r *sequenceRunner) setState(mut func(*State)) State {
	r.mu.Lock()
	defer r.mu.Unlock()
	mut(&r.state)
	r.state.UpdatedAt = r.player.clock.Now().UTC()
	return cloneState(r.state)
}

func (r *sequenceRunner) command(cmd playerCommand) (State, error) {
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
	timer := r.player.clock.NewTimer(time.Hour)
	stopTimer(timer)

	var waiting bool
	var waitUntil time.Time
	var remaining time.Duration
	for {
		state := r.State()
		if state.Status == StatusPlaying && !waiting {
			if state.CurrentStep >= len(r.seq.Steps) {
				r.complete()
				state = r.State()
				if state.Status != StatusPlaying {
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
						waitUntil = r.player.clock.Now().Add(remaining)
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
			waitUntil = r.player.clock.Now().Add(delay)
			resetTimer(timer, delay)
			r.publishWaiting(state.CurrentStep, delay)
		}

		if waiting {
			select {
			case <-timer.C():
				waiting = false
				remaining = 0
				r.dispatchCurrentStep()
			case cmd := <-r.commands:
				nextWaiting, nextRemaining, exit := r.handleCommand(cmd, timer, waiting, waitUntil, remaining)
				waiting, remaining = nextWaiting, nextRemaining
				if waiting {
					waitUntil = r.player.clock.Now().Add(remaining)
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
			waitUntil = r.player.clock.Now().Add(remaining)
		}
		if exit {
			return
		}
	}
}

func (r *sequenceRunner) handleCommand(cmd playerCommand, timer Timer, waiting bool, waitUntil time.Time, remaining time.Duration) (bool, time.Duration, bool) {
	reply := func(state State, err error) {
		if cmd.reply != nil {
			cmd.reply <- playerCommandReply{state: state, err: err}
		}
	}
	switch cmd.kind {
	case playerCommandResume:
		current := r.State()
		if current.Status != StatusPaused {
			reply(current, fmt.Errorf("cannot resume: status is %s", current.Status))
			return waiting, remaining, false
		}
		opts := normalizePlayOptions(cmd.opts)
		nextRemaining := remaining
		if opts.SpeedSet && opts.Speed != current.Speed && nextRemaining > 0 {
			nextRemaining = rescaleRemaining(nextRemaining, current.Speed, opts.Speed)
		}
		if nextRemaining > 0 {
			resetTimer(timer, nextRemaining)
		}
		state := r.setState(func(s *State) {
			s.Status = StatusPlaying
			s.LastError = ""
			if opts.SpeedSet {
				s.Speed = opts.Speed
			}
		})
		r.publishState("state")
		if nextRemaining > 0 {
			r.publishWaiting(state.CurrentStep, nextRemaining)
		}
		reply(state, nil)
		return nextRemaining > 0, nextRemaining, false
	case playerCommandPause:
		current := r.State()
		if current.Status != StatusPlaying {
			reply(current, fmt.Errorf("cannot pause: status is %s", current.Status))
			return waiting, remaining, false
		}
		if waiting {
			stopTimer(timer)
			remaining = waitUntil.Sub(r.player.clock.Now())
			if remaining < 0 {
				remaining = 0
			}
		}
		state := r.setState(func(s *State) {
			if s.Status == StatusPlaying {
				s.Status = StatusPaused
			}
		})
		r.publishState("state")
		reply(state, nil)
		return false, remaining, false
	case playerCommandStop:
		current := r.State()
		if !isActiveStatus(current.Status) {
			reply(current, fmt.Errorf("cannot stop: status is %s", current.Status))
			return waiting, remaining, false
		}
		if waiting {
			stopTimer(timer)
		}
		state := r.setState(func(s *State) {
			s.Status = StatusStopped
		})
		r.publishState("stopped")
		reply(state, nil)
		return false, 0, false
	case playerCommandSeek:
		if waiting {
			stopTimer(timer)
		}
		step := clampStep(cmd.step, len(r.seq.Steps))
		state := r.setState(func(s *State) {
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
			nextRemaining = waitUntil.Sub(r.player.clock.Now())
			if nextRemaining < 0 {
				nextRemaining = 0
			}
			stopTimer(timer)
			nextRemaining = rescaleRemaining(nextRemaining, oldSpeed, cmd.speed)
			if nextRemaining > 0 {
				resetTimer(timer, nextRemaining)
			}
		} else if currentState.Status == StatusPaused && remaining > 0 {
			nextRemaining = rescaleRemaining(remaining, oldSpeed, cmd.speed)
		} else {
			nextRemaining = remaining
		}
		state := r.setState(func(s *State) {
			s.Speed = cmd.speed
		})
		r.publishState("state")
		if waiting && nextRemaining > 0 {
			r.publishWaiting(state.CurrentStep, nextRemaining)
		}
		reply(state, nil)
		return waiting && nextRemaining > 0, nextRemaining, false
	case playerCommandShutdown:
		if waiting {
			stopTimer(timer)
		}
		state := r.setState(func(s *State) {
			if isActiveStatus(s.Status) {
				s.Status = StatusStopped
			}
		})
		if state.Status == StatusStopped {
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
	if state.Status != StatusPlaying || state.CurrentStep >= len(r.seq.Steps) {
		return
	}
	index := state.CurrentStep
	step := r.seq.Steps[index]
	dispatch, err := r.player.reg.ResolveStep(r.seq, step, r.vars, index)
	if err != nil {
		r.fail(err)
		return
	}
	dispatch, err = r.encodeStep(step, dispatch)
	if err != nil {
		r.fail(err)
		return
	}
	result, err := r.player.hub.Dispatch(dispatch)
	if err != nil {
		r.fail(err)
		return
	}
	summary := result.Summary()
	r.setState(func(s *State) {
		s.CurrentStep = index + 1
		s.LastDispatchSummary = summary
	})
	r.publishStep(step.ID, index, summary)
}

// encodeStep pre-encodes a protobuf step, reusing the cached bytes while the
// step's type and payload are unchanged.
func (r *sequenceRunner) encodeStep(step Step, dispatch socket.Dispatch) (socket.Dispatch, error) {
	typeName := strings.TrimSpace(dispatch.TypeName)
	if typeName == "" {
		return dispatch, nil
	}
	if r.player.schemas == nil {
		return dispatch, fmt.Errorf("schema registry is not available")
	}
	hash := sha256.Sum256([]byte(typeName + "\x00" + string(dispatch.Payload)))
	if cached, ok := r.cache[step.ID]; ok && cached.payloadHash == hash {
		encoded := cached.encoded
		dispatch.Encoded = &encoded
		return dispatch, nil
	}
	data, err := r.player.schemas.Encode(typeName, dispatch.Payload)
	if err != nil {
		return dispatch, fmt.Errorf("protobuf encode failed: %w", err)
	}
	encoded := adapter.ProtobufPayload(typeName, data)
	r.cache[step.ID] = stepCacheEntry{payloadHash: hash, encoded: encoded}
	dispatch.Encoded = &encoded
	return dispatch, nil
}

func (r *sequenceRunner) fail(err error) {
	// TODO: M5 — make per-step error policy configurable.
	state := r.setState(func(s *State) {
		s.Status = StatusError
		s.LastError = err.Error()
	})
	r.publishError(state, err)
}

func (r *sequenceRunner) complete() {
	switch r.seq.OnEnd {
	case "loop":
		state := r.setState(func(s *State) {
			s.CurrentStep = 0
			s.Status = StatusPlaying
		})
		r.publishStateSnapshot("looped", state)
	case "reset":
		state := r.setState(func(s *State) {
			s.CurrentStep = 0
			s.Status = StatusCompleted
		})
		r.publishStateSnapshot("completed", state)
	default:
		state := r.setState(func(s *State) {
			s.CurrentStep = max(0, len(r.seq.Steps)-1)
			s.Status = StatusCompleted
		})
		r.publishStateSnapshot("completed", state)
	}
}

func (r *sequenceRunner) publishState(kind string) {
	r.publishStateSnapshot(kind, r.State())
}

func (r *sequenceRunner) publishStateSnapshot(kind string, state State) {
	r.player.broadcaster.Publish(Event{
		Type:       kind,
		State:      state,
		SequenceID: r.seq.ID,
		At:         r.player.clock.Now().UTC(),
	})
}

func (r *sequenceRunner) publishStep(stepID string, stepIndex int, summary string) {
	r.player.broadcaster.Publish(Event{
		Type:            "step",
		State:           r.State(),
		SequenceID:      r.seq.ID,
		StepID:          stepID,
		StepIndex:       stepIndex,
		DispatchSummary: summary,
		At:              r.player.clock.Now().UTC(),
	})
}

func (r *sequenceRunner) publishWaiting(stepIndex int, delay time.Duration) {
	if stepIndex < 0 || stepIndex >= len(r.seq.Steps) {
		return
	}
	step := r.seq.Steps[stepIndex]
	r.player.broadcaster.Publish(Event{
		Type:       "waiting",
		State:      r.State(),
		SequenceID: r.seq.ID,
		StepID:     step.ID,
		StepIndex:  stepIndex,
		DelayMs:    delay.Milliseconds(),
		At:         r.player.clock.Now().UTC(),
	})
}

func (r *sequenceRunner) publishError(state State, err error) {
	r.player.broadcaster.Publish(Event{
		Type:       "error",
		State:      state,
		SequenceID: r.seq.ID,
		Error:      err.Error(),
		At:         r.player.clock.Now().UTC(),
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

func stopTimer(timer Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
}

func resetTimer(timer Timer, d time.Duration) {
	stopTimer(timer)
	timer.Reset(d)
}

func cloneState(state State) State {
	if state.StartedAt != nil {
		started := *state.StartedAt
		state.StartedAt = &started
	}
	return state
}

func isActiveStatus(status Status) bool {
	return status == StatusPlaying || status == StatusPaused
}
