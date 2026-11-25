// Package sequence stores timed event sequences (steps of templates or
// inline payloads with delays) and plays them on the socket hub with
// play, pause, seek, speed and loop controls.
package sequence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ditto/internal/adapter"
	"ditto/internal/eventtemplate"
	"ditto/internal/jsonfile"
	"ditto/internal/schema"
	"ditto/internal/socket"
)

const maxSequenceStepDelay = 24 * time.Hour

// ErrNotFound is returned for an unknown sequence ID.
var ErrNotFound = errors.New("event sequence not found")

// Sequence is a timeline of steps played on the socket hub.
type Sequence struct {
	Version     int                        `json:"version"`
	ID          string                     `json:"id"`
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Steps       []Step                     `json:"steps"`
	Vars        map[string]json.RawMessage `json:"vars,omitempty"`
	OnEnd       string                     `json:"on_end"`
	CreatedAt   time.Time                  `json:"created_at"`
	UpdatedAt   time.Time                  `json:"updated_at"`
}

// Step dispatches a template (with optional overrides) or an inline
// payload after DelayMs.
type Step struct {
	ID           string                     `json:"id"`
	Name         string                     `json:"name,omitempty"`
	DelayMs      int64                      `json:"delay_ms"`
	TemplateRef  string                     `json:"template_ref,omitempty"`
	Channel      string                     `json:"channel,omitempty"`
	Adapter      string                     `json:"adapter,omitempty"`
	TypeName     string                     `json:"type_name,omitempty"`
	Payload      json.RawMessage            `json:"payload,omitempty"`
	VarsOverride map[string]json.RawMessage `json:"vars_override,omitempty"`
}

// Store keeps sequences as one JSON file each in a directory. It is safe
// for concurrent use.
type Store struct {
	mu        sync.RWMutex
	dir       string
	sequences map[string]Sequence
	templates *eventtemplate.Store
	schemas   *schema.Registry
	adapters  *adapter.Registry
}

// NewStore loads the sequences in dir. Sequences referencing unknown
// templates, adapters or protobuf types are skipped, so those must be
// loaded first.
func NewStore(dir string, templates *eventtemplate.Store, schemas *schema.Registry, adapters *adapter.Registry) (*Store, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, err
	}
	reg := &Store{
		dir:       absDir,
		sequences: make(map[string]Sequence),
		templates: templates,
		schemas:   schemas,
		adapters:  adapters,
	}
	if err := reg.Load(); err != nil {
		return nil, err
	}
	return reg, nil
}

// Load re-reads every sequence file, skipping invalid ones with a log line.
func (r *Store) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := make(map[string]Sequence)
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !jsonfile.ValidID(id) {
			slog.Warn("event sequence skipped", "file", entry.Name(), "reason", "unsafe id")
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			slog.Warn("event sequence skipped", "file", entry.Name(), "err", err)
			continue
		}
		var seq Sequence
		if err := json.Unmarshal(data, &seq); err != nil {
			slog.Warn("event sequence skipped", "file", entry.Name(), "err", err)
			continue
		}
		if seq.ID == "" {
			seq.ID = id
		}
		fileID := seq.ID
		if err := validateSequenceVariableKeyCollisions(seq); err != nil {
			slog.Warn("event sequence skipped", "file", entry.Name(), "err", err)
			continue
		}
		seq = normalize(seq)
		seq.ID = fileID
		if seq.ID != id || !jsonfile.ValidID(seq.ID) {
			slog.Warn("event sequence skipped", "file", entry.Name(), "reason", "id mismatch")
			continue
		}
		if err := r.validate(seq); err != nil {
			slog.Warn("event sequence skipped", "file", entry.Name(), "err", err)
			continue
		}
		next[seq.ID] = clone(seq)
	}
	r.sequences = next
	return nil
}

// Sequences returns every sequence sorted by name.
func (r *Store) Sequences() []Sequence {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sequences := make([]Sequence, 0, len(r.sequences))
	for _, seq := range r.sequences {
		sequences = append(sequences, clone(seq))
	}
	sort.Slice(sequences, func(i, j int) bool {
		if sequences[i].Name == sequences[j].Name {
			return sequences[i].ID < sequences[j].ID
		}
		return sequences[i].Name < sequences[j].Name
	})
	return sequences
}

// Get returns the sequence with id, or ErrNotFound.
func (r *Store) Get(id string) (Sequence, error) {
	if !jsonfile.ValidID(id) {
		return Sequence{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	seq, ok := r.sequences[id]
	if !ok {
		return Sequence{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return clone(seq), nil
}

// Create validates seq, assigns IDs to it and its steps and saves it.
func (r *Store) Create(seq Sequence) (Sequence, error) {
	if err := validateSequenceVariableKeyCollisions(seq); err != nil {
		return Sequence{}, err
	}
	seq = normalize(seq)
	now := time.Now().UTC()
	seq.ID = ""
	seq.CreatedAt = now
	seq.UpdatedAt = now
	assignSequenceStepIDs(&seq)
	if err := r.validate(seq); err != nil {
		return Sequence{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	seq.ID = r.nextIDLocked(seq.Name)
	err := r.writeLocked(seq, false)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			seq.ID = r.nextIDLocked(seq.Name)
			err = r.writeLocked(seq, false)
		}
	}
	if err != nil {
		return Sequence{}, err
	}
	r.sequences[seq.ID] = clone(seq)
	return clone(seq), nil
}

// Update validates and replaces the sequence with id, keeping step IDs
// that were sent and generating missing ones.
func (r *Store) Update(id string, seq Sequence) (Sequence, error) {
	if !jsonfile.ValidID(id) {
		return Sequence{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err := validateSequenceVariableKeyCollisions(seq); err != nil {
		return Sequence{}, err
	}
	seq = normalize(seq)
	assignSequenceStepIDs(&seq)
	if err := r.validate(seq); err != nil {
		return Sequence{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.sequences[id]
	if !ok {
		return Sequence{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	seq.ID = id
	seq.CreatedAt = existing.CreatedAt
	if seq.CreatedAt.IsZero() {
		seq.CreatedAt = time.Now().UTC()
	}
	seq.UpdatedAt = time.Now().UTC()
	if err := r.writeLocked(seq, true); err != nil {
		return Sequence{}, err
	}
	r.sequences[id] = clone(seq)
	return clone(seq), nil
}

// Delete removes the sequence with id. Use Player.DeleteWhenIdle to avoid
// deleting a sequence that is playing.
func (r *Store) Delete(id string) error {
	if !jsonfile.ValidID(id) {
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sequences[id]; !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err := os.Remove(r.pathForIDLocked(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(r.sequences, id)
	return nil
}

// ResolveStep renders step with the sequence, step and runtime variables
// (later ones win) and returns it ready to dispatch. A step that references a
// template starts from the template's render and applies the step overrides.
func (r *Store) ResolveStep(seq Sequence, step Step, runtimeVars map[string]string, index int) (socket.Dispatch, error) {
	vars, err := sequenceVariablesToStrings(seq.Vars, step.VarsOverride)
	if err != nil {
		return socket.Dispatch{}, err
	}
	for key, value := range runtimeVars {
		vars[key] = value
	}

	var rendered eventtemplate.Rendered
	if step.TemplateRef != "" {
		tmpl, err := r.templates.Get(step.TemplateRef)
		if err != nil {
			return socket.Dispatch{}, err
		}
		rendered, err = r.templates.Render(step.TemplateRef, vars)
		if err != nil {
			return socket.Dispatch{}, err
		}
		if strings.TrimSpace(step.Channel) != "" {
			rendered.Channel = strings.TrimSpace(step.Channel)
		}
		if strings.TrimSpace(step.Adapter) != "" {
			rendered.Adapter = adapter.Normalize(step.Adapter)
		}
		if strings.TrimSpace(step.TypeName) != "" {
			rendered.TypeName = strings.TrimSpace(step.TypeName)
		}
		if len(step.Payload) > 0 {
			defaults := templateDefaults(tmpl)
			payload, missing, invalid, err := eventtemplate.Resolve(step.Payload, vars, defaults)
			if err != nil {
				return socket.Dispatch{}, err
			}
			rendered.Payload = payload
			rendered.Missing = missing
			rendered.InvalidCasts = invalid
		}
	} else {
		payload, missing, invalid, err := eventtemplate.Resolve(step.Payload, vars, nil)
		if err != nil {
			return socket.Dispatch{}, err
		}
		rendered = eventtemplate.Rendered{
			Channel:      strings.TrimSpace(step.Channel),
			Adapter:      adapter.Normalize(step.Adapter),
			TypeName:     strings.TrimSpace(step.TypeName),
			Payload:      payload,
			Missing:      missing,
			InvalidCasts: invalid,
		}
	}
	if len(rendered.Missing) > 0 {
		return socket.Dispatch{}, fmt.Errorf("missing variables: %s", strings.Join(rendered.Missing, ", "))
	}
	if len(rendered.InvalidCasts) > 0 {
		return socket.Dispatch{}, fmt.Errorf("invalid template casts: %s", eventtemplate.DescribeInvalidCasts(rendered.InvalidCasts))
	}
	return socket.Dispatch{
		Channel:  rendered.Channel,
		Adapter:  rendered.Adapter,
		TypeName: rendered.TypeName,
		Payload:  rendered.Payload,
		Source:   fmt.Sprintf("sequence:%s:step:%s", seq.ID, step.ID),
	}, nil
}

func (r *Store) validate(seq Sequence) error {
	if seq.Version != 1 {
		return fmt.Errorf("unsupported event sequence version %d", seq.Version)
	}
	if strings.TrimSpace(seq.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if !isSequenceOnEnd(seq.OnEnd) {
		return fmt.Errorf("on_end must be one of loop, stay, reset")
	}
	if len(seq.Steps) == 0 {
		return fmt.Errorf("at least one step is required")
	}
	seen := make(map[string]struct{}, len(seq.Steps))
	for i, step := range seq.Steps {
		if !jsonfile.ValidID(step.ID) {
			return fmt.Errorf("step %d has unsafe id", i+1)
		}
		if _, ok := seen[step.ID]; ok {
			return fmt.Errorf("duplicate step id %q", step.ID)
		}
		seen[step.ID] = struct{}{}
		if step.DelayMs < 0 {
			return fmt.Errorf("step %d delay_ms must be >= 0", i+1)
		}
		if time.Duration(step.DelayMs)*time.Millisecond > maxSequenceStepDelay {
			return fmt.Errorf("step %d delay_ms exceeds 24h", i+1)
		}
		if step.TemplateRef != "" {
			if r.templates == nil {
				return fmt.Errorf("event template registry is not available")
			}
			if _, err := r.templates.Get(step.TemplateRef); err != nil {
				return fmt.Errorf("step %d template_ref %q is not loaded", i+1, step.TemplateRef)
			}
		} else {
			if strings.TrimSpace(step.Channel) == "" {
				return fmt.Errorf("step %d channel is required without template_ref", i+1)
			}
			if len(step.Payload) == 0 {
				return fmt.Errorf("step %d payload is required without template_ref", i+1)
			}
		}
		if strings.ContainsAny(step.Channel, "\r\n") {
			return fmt.Errorf("step %d channel cannot contain newlines", i+1)
		}
		if strings.Contains(step.Channel, "{{") || strings.Contains(step.Channel, "}}") {
			// TODO: M7 — support channel templating for triggers.
			return fmt.Errorf("step %d channel variables are not supported", i+1)
		}
		if _, err := r.adapters.Protocol(step.Adapter); err != nil {
			return err
		}
		if len(step.Payload) > 0 {
			if !json.Valid(step.Payload) {
				return fmt.Errorf("step %d payload must be valid JSON", i+1)
			}
			if err := eventtemplate.ValidateCasts(step.Payload); err != nil {
				return fmt.Errorf("step %d %w", i+1, err)
			}
		}
		if strings.TrimSpace(step.TypeName) != "" {
			if r.schemas == nil || r.schemas.Descriptor(strings.TrimSpace(step.TypeName)) == nil {
				return fmt.Errorf("schema type %q is not loaded", strings.TrimSpace(step.TypeName))
			}
		}
		if _, err := eventtemplate.VarsFromJSON(step.VarsOverride); err != nil {
			return err
		}
	}
	if _, err := eventtemplate.VarsFromJSON(seq.Vars); err != nil {
		return err
	}
	return nil
}

func (r *Store) nextIDLocked(name string) string {
	for attempt := 0; ; attempt++ {
		id := sequenceID(name, attempt)
		if _, exists := r.sequences[id]; !exists {
			if _, err := os.Stat(r.pathForIDLocked(id)); errors.Is(err, os.ErrNotExist) {
				return id
			}
		}
	}
}

func (r *Store) writeLocked(seq Sequence, overwrite bool) error {
	return jsonfile.Write(r.pathForIDLocked(seq.ID), seq, overwrite)
}

func (r *Store) pathForIDLocked(id string) string {
	return filepath.Join(r.dir, id+".json")
}

func normalize(seq Sequence) Sequence {
	if seq.Version == 0 {
		seq.Version = 1
	}
	seq.Name = strings.TrimSpace(seq.Name)
	seq.Description = strings.TrimSpace(seq.Description)
	seq.ID = strings.TrimSpace(seq.ID)
	if strings.TrimSpace(seq.OnEnd) == "" {
		seq.OnEnd = "stay"
	} else {
		seq.OnEnd = strings.ToLower(strings.TrimSpace(seq.OnEnd))
	}
	if seq.Vars != nil {
		vars := make(map[string]json.RawMessage, len(seq.Vars))
		for key, value := range seq.Vars {
			vars[strings.TrimSpace(key)] = append(json.RawMessage(nil), value...)
		}
		seq.Vars = vars
	}
	for i := range seq.Steps {
		step := &seq.Steps[i]
		step.ID = strings.TrimSpace(step.ID)
		step.Name = strings.TrimSpace(step.Name)
		step.TemplateRef = strings.TrimSpace(step.TemplateRef)
		step.Channel = strings.TrimSpace(step.Channel)
		step.Adapter = adapter.Normalize(step.Adapter)
		step.TypeName = strings.TrimSpace(step.TypeName)
		step.Payload = append(json.RawMessage(nil), step.Payload...)
		if step.VarsOverride != nil {
			vars := make(map[string]json.RawMessage, len(step.VarsOverride))
			for key, value := range step.VarsOverride {
				vars[strings.TrimSpace(key)] = append(json.RawMessage(nil), value...)
			}
			step.VarsOverride = vars
		}
	}
	return seq
}

func assignSequenceStepIDs(seq *Sequence) {
	seen := make(map[string]struct{}, len(seq.Steps))
	for i := range seq.Steps {
		id := strings.TrimSpace(seq.Steps[i].ID)
		if id != "" {
			if _, ok := seen[id]; !ok && jsonfile.ValidID(id) {
				seen[id] = struct{}{}
				continue
			}
		}
		for attempt := 0; ; attempt++ {
			next := stepID(seq.Name, seq.Steps[i], i, attempt)
			if _, ok := seen[next]; !ok {
				seq.Steps[i].ID = next
				seen[next] = struct{}{}
				break
			}
		}
	}
}

func sequenceID(name string, attempt int) string {
	return jsonfile.HashedID(name, "event-sequence", jsonfile.NameAttempt(name, attempt))
}

func stepID(name string, step Step, index int, attempt int) string {
	input := fmt.Sprintf("%s\n%d\n%s\n%s\n%s\n%s\n%d", name, index, step.Name, step.TemplateRef, step.Channel, string(step.Payload), attempt)
	sum := sha256.Sum256([]byte(input))
	return "step-" + hex.EncodeToString(sum[:])[:8]
}

func isSequenceOnEnd(onEnd string) bool {
	switch strings.ToLower(strings.TrimSpace(onEnd)) {
	case "loop", "stay", "reset":
		return true
	default:
		return false
	}
}

func clone(seq Sequence) Sequence {
	if seq.Vars != nil {
		vars := make(map[string]json.RawMessage, len(seq.Vars))
		for key, value := range seq.Vars {
			vars[key] = append(json.RawMessage(nil), value...)
		}
		seq.Vars = vars
	}
	if seq.Steps != nil {
		steps := make([]Step, len(seq.Steps))
		for i, step := range seq.Steps {
			steps[i] = cloneStep(step)
		}
		seq.Steps = steps
	}
	return seq
}

func cloneStep(step Step) Step {
	step.Payload = append(json.RawMessage(nil), step.Payload...)
	if step.VarsOverride != nil {
		vars := make(map[string]json.RawMessage, len(step.VarsOverride))
		for key, value := range step.VarsOverride {
			vars[key] = append(json.RawMessage(nil), value...)
		}
		step.VarsOverride = vars
	}
	return step
}

func templateDefaults(tmpl eventtemplate.Template) map[string]*string {
	defaults := make(map[string]*string)
	for _, variable := range tmpl.Variables {
		name := strings.TrimSpace(variable.Name)
		if name == "" || variable.Default == nil {
			continue
		}
		value := *variable.Default
		defaults[name] = &value
	}
	return defaults
}

func sequenceVariablesToStrings(sequenceVars, stepVars map[string]json.RawMessage) (map[string]string, error) {
	out := make(map[string]string)
	if len(sequenceVars) > 0 {
		converted, err := eventtemplate.VarsFromJSON(sequenceVars)
		if err != nil {
			return nil, err
		}
		for key, value := range converted {
			out[key] = value
		}
	}
	if len(stepVars) > 0 {
		converted, err := eventtemplate.VarsFromJSON(stepVars)
		if err != nil {
			return nil, err
		}
		for key, value := range converted {
			out[key] = value
		}
	}
	return out, nil
}

func validateSequenceVariableKeys(vars map[string]json.RawMessage, label string) error {
	if len(vars) == 0 {
		return nil
	}
	seen := make(map[string]string, len(vars))
	for key := range vars {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			return fmt.Errorf("%s contains an empty variable name", label)
		}
		if prior, ok := seen[trimmed]; ok && prior != key {
			return fmt.Errorf("%s contains duplicate variable name %q after trimming", label, trimmed)
		}
		seen[trimmed] = key
	}
	return nil
}

// validateSequenceVariableKeyCollisions runs against raw input before
// normalize trims keys, so "foo" and " foo " are caught.
func validateSequenceVariableKeyCollisions(seq Sequence) error {
	if err := validateSequenceVariableKeys(seq.Vars, "sequence vars"); err != nil {
		return err
	}
	for i, step := range seq.Steps {
		if err := validateSequenceVariableKeys(step.VarsOverride, fmt.Sprintf("step %d vars_override", i+1)); err != nil {
			return err
		}
	}
	return nil
}
