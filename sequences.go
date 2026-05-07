package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxEventSequenceBodyBytes = 1 << 20
	maxSequenceStepDelay      = 24 * time.Hour
)

var ErrEventSequenceNotFound = errors.New("event sequence not found")

type EventSequence struct {
	Version     int                        `json:"version"`
	ID          string                     `json:"id"`
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Steps       []EventSequenceStep        `json:"steps"`
	Vars        map[string]json.RawMessage `json:"vars,omitempty"`
	OnEnd       string                     `json:"on_end"`
	CreatedAt   time.Time                  `json:"created_at"`
	UpdatedAt   time.Time                  `json:"updated_at"`
}

type EventSequenceStep struct {
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

type EventSequenceRegistry struct {
	mu        sync.RWMutex
	dir       string
	sequences map[string]EventSequence
	templates *EventTemplateRegistry
	schemas   *SchemaRegistry
}

type eventSequencesResponse struct {
	Sequences []EventSequence `json:"sequences"`
}

type sequencePlayRequest struct {
	Vars      map[string]json.RawMessage `json:"vars,omitempty"`
	StartStep *int                       `json:"start_step,omitempty"`
	Speed     *float64                   `json:"speed,omitempty"`
}

type sequenceSeekRequest struct {
	Step int `json:"step"`
}

type sequenceSpeedRequest struct {
	Speed float64 `json:"speed"`
}

func NewEventSequenceRegistry(dir string, templates *EventTemplateRegistry, schemas *SchemaRegistry) (*EventSequenceRegistry, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, err
	}
	reg := &EventSequenceRegistry{
		dir:       absDir,
		sequences: make(map[string]EventSequence),
		templates: templates,
		schemas:   schemas,
	}
	if err := reg.Load(); err != nil {
		return nil, err
	}
	return reg, nil
}

func (r *EventSequenceRegistry) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := make(map[string]EventSequence)
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !isSafeEventTemplateID(id) {
			log.Printf("event sequence %s skipped: unsafe id", entry.Name())
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			log.Printf("event sequence %s skipped: %v", entry.Name(), err)
			continue
		}
		var seq EventSequence
		if err := json.Unmarshal(data, &seq); err != nil {
			log.Printf("event sequence %s skipped: %v", entry.Name(), err)
			continue
		}
		if seq.ID == "" {
			seq.ID = id
		}
		fileID := seq.ID
		seq = normalizeEventSequence(seq)
		seq.ID = fileID
		if seq.ID != id || !isSafeEventTemplateID(seq.ID) {
			log.Printf("event sequence %s skipped: id mismatch", entry.Name())
			continue
		}
		if err := r.validate(seq); err != nil {
			log.Printf("event sequence %s skipped: %v", entry.Name(), err)
			continue
		}
		next[seq.ID] = cloneEventSequence(seq)
	}
	r.sequences = next
	return nil
}

func (r *EventSequenceRegistry) Sequences() []EventSequence {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sequences := make([]EventSequence, 0, len(r.sequences))
	for _, seq := range r.sequences {
		sequences = append(sequences, cloneEventSequence(seq))
	}
	sort.Slice(sequences, func(i, j int) bool {
		if sequences[i].Name == sequences[j].Name {
			return sequences[i].ID < sequences[j].ID
		}
		return sequences[i].Name < sequences[j].Name
	})
	return sequences
}

func (r *EventSequenceRegistry) Get(id string) (EventSequence, error) {
	if !isSafeEventTemplateID(id) {
		return EventSequence{}, fmt.Errorf("%w: %q", ErrEventSequenceNotFound, id)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	seq, ok := r.sequences[id]
	if !ok {
		return EventSequence{}, fmt.Errorf("%w: %q", ErrEventSequenceNotFound, id)
	}
	return cloneEventSequence(seq), nil
}

func (r *EventSequenceRegistry) Create(seq EventSequence) (EventSequence, error) {
	seq = normalizeEventSequence(seq)
	now := time.Now().UTC()
	seq.ID = ""
	seq.CreatedAt = now
	seq.UpdatedAt = now
	assignSequenceStepIDs(&seq)
	if err := r.validate(seq); err != nil {
		return EventSequence{}, err
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
		return EventSequence{}, err
	}
	r.sequences[seq.ID] = cloneEventSequence(seq)
	return cloneEventSequence(seq), nil
}

func (r *EventSequenceRegistry) Update(id string, seq EventSequence) (EventSequence, error) {
	if !isSafeEventTemplateID(id) {
		return EventSequence{}, fmt.Errorf("%w: %q", ErrEventSequenceNotFound, id)
	}
	seq = normalizeEventSequence(seq)
	assignSequenceStepIDs(&seq)
	if err := r.validate(seq); err != nil {
		return EventSequence{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.sequences[id]
	if !ok {
		return EventSequence{}, fmt.Errorf("%w: %q", ErrEventSequenceNotFound, id)
	}
	seq.ID = id
	seq.CreatedAt = existing.CreatedAt
	if seq.CreatedAt.IsZero() {
		seq.CreatedAt = time.Now().UTC()
	}
	seq.UpdatedAt = time.Now().UTC()
	if err := r.writeLocked(seq, true); err != nil {
		return EventSequence{}, err
	}
	r.sequences[id] = cloneEventSequence(seq)
	return cloneEventSequence(seq), nil
}

func (r *EventSequenceRegistry) Delete(id string) error {
	if !isSafeEventTemplateID(id) {
		return fmt.Errorf("%w: %q", ErrEventSequenceNotFound, id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sequences[id]; !ok {
		return fmt.Errorf("%w: %q", ErrEventSequenceNotFound, id)
	}
	if err := os.Remove(r.pathForIDLocked(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(r.sequences, id)
	return nil
}

func (r *EventSequenceRegistry) ResolveStep(seq EventSequence, step EventSequenceStep, runtimeVars map[string]string, index int) (RenderedDispatch, error) {
	vars, err := sequenceVariablesToStrings(seq.Vars, step.VarsOverride)
	if err != nil {
		return RenderedDispatch{}, err
	}
	for key, value := range runtimeVars {
		vars[key] = value
	}

	if step.TemplateRef != "" {
		tmpl, err := r.templates.Get(step.TemplateRef)
		if err != nil {
			return RenderedDispatch{}, err
		}
		rendered, err := r.templates.Render(step.TemplateRef, vars)
		if err != nil {
			return RenderedDispatch{}, err
		}
		if strings.TrimSpace(step.Channel) != "" {
			rendered.Channel = strings.TrimSpace(step.Channel)
		}
		if strings.TrimSpace(step.Adapter) != "" {
			rendered.Adapter = normalizeAdapter(step.Adapter)
		}
		if strings.TrimSpace(step.TypeName) != "" {
			rendered.TypeName = strings.TrimSpace(step.TypeName)
		}
		if len(step.Payload) > 0 {
			defaults := templateDefaults(tmpl)
			payload, missing, invalid, err := resolveTemplateDetailed(step.Payload, vars, defaults)
			if err != nil {
				return RenderedDispatch{}, err
			}
			rendered.Payload = payload
			rendered.Missing = missing
			rendered.InvalidCasts = invalid
		}
		rendered.Source = fmt.Sprintf("sequence:%s:step:%s", seq.ID, step.ID)
		return prepareRenderedSequenceStep(rendered, r.schemas)
	}

	payload, missing, invalid, err := resolveTemplateDetailed(step.Payload, vars, nil)
	if err != nil {
		return RenderedDispatch{}, err
	}
	rendered := RenderedDispatch{
		Channel:      strings.TrimSpace(step.Channel),
		Adapter:      normalizeAdapter(step.Adapter),
		TypeName:     strings.TrimSpace(step.TypeName),
		Payload:      payload,
		Missing:      missing,
		InvalidCasts: invalid,
		Source:       fmt.Sprintf("sequence:%s:step:%s", seq.ID, step.ID),
	}
	_ = index
	return prepareRenderedSequenceStep(rendered, r.schemas)
}

func prepareRenderedSequenceStep(rendered RenderedDispatch, schemas *SchemaRegistry) (RenderedDispatch, error) {
	if len(rendered.Missing) > 0 {
		return rendered, fmt.Errorf("missing variables: %s", strings.Join(rendered.Missing, ", "))
	}
	if len(rendered.InvalidCasts) > 0 {
		return rendered, fmt.Errorf("invalid template casts: %s", describeInvalidCasts(rendered.InvalidCasts))
	}
	typeName := strings.TrimSpace(rendered.TypeName)
	if typeName != "" {
		if schemas == nil {
			return rendered, fmt.Errorf("schema registry is not available")
		}
		encoded, err := schemas.Encode(typeName, rendered.Payload)
		if err != nil {
			return rendered, fmt.Errorf("protobuf encode failed: %w", err)
		}
		rendered.EncodedPayload = &encoded
	}
	return rendered, nil
}

func (r *EventSequenceRegistry) validate(seq EventSequence) error {
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
		if !isSafeEventTemplateID(step.ID) {
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
		if _, err := NewProtocolAdapter(step.Adapter); err != nil {
			return err
		}
		if len(step.Payload) > 0 {
			if !json.Valid(step.Payload) {
				return fmt.Errorf("step %d payload must be valid JSON", i+1)
			}
			if err := validateTemplateCasts(step.Payload); err != nil {
				return fmt.Errorf("step %d %w", i+1, err)
			}
		}
		if strings.TrimSpace(step.TypeName) != "" {
			if r.schemas == nil || r.schemas.Descriptor(strings.TrimSpace(step.TypeName)) == nil {
				return fmt.Errorf("schema type %q is not loaded", strings.TrimSpace(step.TypeName))
			}
		}
		if _, err := eventTemplateVariablesToStrings(step.VarsOverride); err != nil {
			return err
		}
	}
	if _, err := eventTemplateVariablesToStrings(seq.Vars); err != nil {
		return err
	}
	return nil
}

func (r *EventSequenceRegistry) nextIDLocked(name string) string {
	for attempt := 0; ; attempt++ {
		id := eventSequenceID(name, attempt)
		if _, exists := r.sequences[id]; !exists {
			if _, err := os.Stat(r.pathForIDLocked(id)); errors.Is(err, os.ErrNotExist) {
				return id
			}
		}
	}
}

func (r *EventSequenceRegistry) writeLocked(seq EventSequence, overwrite bool) error {
	data, err := json.MarshalIndent(seq, "", "  ")
	if err != nil {
		return err
	}
	path := r.pathForIDLocked(seq.ID)
	tmp := path + ".tmp"
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return os.ErrExist
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (r *EventSequenceRegistry) pathForIDLocked(id string) string {
	return filepath.Join(r.dir, id+".json")
}

func normalizeEventSequence(seq EventSequence) EventSequence {
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
		step.Adapter = normalizeAdapter(step.Adapter)
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

func assignSequenceStepIDs(seq *EventSequence) {
	seen := make(map[string]struct{}, len(seq.Steps))
	for i := range seq.Steps {
		id := strings.TrimSpace(seq.Steps[i].ID)
		if id != "" {
			if _, ok := seen[id]; !ok && isSafeEventTemplateID(id) {
				seen[id] = struct{}{}
				continue
			}
		}
		for attempt := 0; ; attempt++ {
			next := eventSequenceStepID(seq.Name, seq.Steps[i], i, attempt)
			if _, ok := seen[next]; !ok {
				seq.Steps[i].ID = next
				seen[next] = struct{}{}
				break
			}
		}
	}
}

func eventSequenceID(name string, attempt int) string {
	base := sanitizePackName(name)
	if base == "" {
		base = "event-sequence"
	}
	runes := []rune(base)
	if len(runes) > 48 {
		base = strings.Trim(string(runes[:48]), "-")
	}
	input := name
	if attempt > 0 {
		input = fmt.Sprintf("%s\n%d", name, attempt)
	}
	sum := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%s-%s", base, hex.EncodeToString(sum[:])[:8])
}

func eventSequenceStepID(name string, step EventSequenceStep, index int, attempt int) string {
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

func cloneEventSequence(seq EventSequence) EventSequence {
	if seq.Vars != nil {
		vars := make(map[string]json.RawMessage, len(seq.Vars))
		for key, value := range seq.Vars {
			vars[key] = append(json.RawMessage(nil), value...)
		}
		seq.Vars = vars
	}
	if seq.Steps != nil {
		steps := make([]EventSequenceStep, len(seq.Steps))
		for i, step := range seq.Steps {
			steps[i] = cloneEventSequenceStep(step)
		}
		seq.Steps = steps
	}
	return seq
}

func cloneEventSequenceStep(step EventSequenceStep) EventSequenceStep {
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

func templateDefaults(tmpl EventTemplate) map[string]*string {
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
		converted, err := eventTemplateVariablesToStrings(sequenceVars)
		if err != nil {
			return nil, err
		}
		for key, value := range converted {
			out[key] = value
		}
	}
	if len(stepVars) > 0 {
		converted, err := eventTemplateVariablesToStrings(stepVars)
		if err != nil {
			return nil, err
		}
		for key, value := range converted {
			out[key] = value
		}
	}
	return out, nil
}

func decodeEventSequenceJSON(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventSequenceBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "failed to read request body: "+err.Error(), http.StatusBadRequest)
		}
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		if allowEmpty {
			return true
		}
		http.Error(w, "request body is required", http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func splitSequencePath(rest string) (id string, action string, ok bool) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 1 && isSafeEventTemplateID(parts[0]) {
		return parts[0], "", true
	}
	if len(parts) == 2 && isSafeEventTemplateID(parts[0]) {
		switch parts[1] {
		case "play", "pause", "stop", "seek", "speed":
			return parts[0], parts[1], true
		}
	}
	return "", "", false
}

func RegisterSequenceRoutes(mux *http.ServeMux, registry *EventSequenceRegistry, player *SequencePlayer, broadcaster *PlayerBroadcaster) {
	if registry == nil || player == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/sequences", func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(eventSequencesResponse{Sequences: registry.Sequences()})
		case http.MethodPost:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var seq EventSequence
			if ok := decodeEventSequenceJSON(w, r, &seq, false); !ok {
				return
			}
			created, err := registry.Create(seq)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(created)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/__ditto__/api/sequences/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"states": player.States()})
	})
	mux.HandleFunc("/__ditto__/api/sequences/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if broadcaster == nil {
			http.Error(w, "player broadcaster is not available", http.StatusInternalServerError)
			return
		}
		broadcaster.ServeHTTP(w, r)
	})
	mux.HandleFunc("/__ditto__/api/sequences/", func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/__ditto__/api/sequences/")
		id, action, ok := splitSequencePath(rest)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if action != "" {
			handleSequencePlayerAction(w, r, player, id, action)
			return
		}
		switch r.Method {
		case http.MethodGet:
			seq, err := registry.Get(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(seq)
		case http.MethodPut:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var seq EventSequence
			if ok := decodeEventSequenceJSON(w, r, &seq, false); !ok {
				return
			}
			updated, err := registry.Update(id, seq)
			if err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, ErrEventSequenceNotFound) {
					status = http.StatusNotFound
				}
				http.Error(w, err.Error(), status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(updated)
		case http.MethodDelete:
			if err := player.DeleteWhenIdle(id, func() error {
				return registry.Delete(id)
			}); err != nil {
				status := http.StatusNotFound
				if errors.Is(err, ErrSequencePlayerActive) {
					status = http.StatusConflict
				}
				http.Error(w, err.Error(), status)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func handleSequencePlayerAction(w http.ResponseWriter, r *http.Request, player *SequencePlayer, id string, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Content-Type") != "" && !hasJSONContentType(r) {
		http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	var state PlayerState
	var err error
	switch action {
	case "play":
		var req sequencePlayRequest
		if ok := decodeEventSequenceJSON(w, r, &req, true); !ok {
			return
		}
		vars, varErr := eventTemplateVariablesToStrings(req.Vars)
		if varErr != nil {
			http.Error(w, varErr.Error(), http.StatusBadRequest)
			return
		}
		opts := PlayOptions{Vars: vars}
		if req.StartStep != nil {
			opts.StartStep = *req.StartStep
		}
		if req.Speed != nil {
			opts.Speed = *req.Speed
			opts.SpeedSet = true
		}
		state, err = player.Play(id, opts)
	case "pause":
		state, err = player.Pause(id)
	case "stop":
		state, err = player.Stop(id)
	case "seek":
		var req sequenceSeekRequest
		if ok := decodeEventSequenceJSON(w, r, &req, false); !ok {
			return
		}
		state, err = player.Seek(id, req.Step)
	case "speed":
		var req sequenceSpeedRequest
		if ok := decodeEventSequenceJSON(w, r, &req, false); !ok {
			return
		}
		state, err = player.SetSpeed(id, req.Speed)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrEventSequenceNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}
