package main

import (
	"bytes"
	"crypto/rand"
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxEventTemplateBodyBytes = 1 << 20
	maxTemplateResolveDepth   = 32
)

var (
	ErrEventTemplateNotFound = errors.New("event template not found")
	templateVariablePattern  = regexp.MustCompile(`\{\{\s*(?:(\w+):)?([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	templateNamePattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type EventTemplate struct {
	Version     int                     `json:"version"`
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Description string                  `json:"description,omitempty"`
	Channel     string                  `json:"channel"`
	Adapter     string                  `json:"adapter,omitempty"`
	TypeName    string                  `json:"type_name,omitempty"`
	Payload     json.RawMessage         `json:"payload"`
	Variables   []EventTemplateVariable `json:"variables,omitempty"`
	CreatedAt   time.Time               `json:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
}

type EventTemplateVariable struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Default     *string `json:"default,omitempty"`
}

type EventTemplateInvalidCast struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type EventTemplateRegistry struct {
	mu        sync.RWMutex
	dir       string
	templates map[string]EventTemplate
}

type eventTemplatesResponse struct {
	Templates []EventTemplate `json:"templates"`
}

type eventTemplateDispatchRequest struct {
	Variables       map[string]json.RawMessage `json:"variables,omitempty"`
	ChannelOverride string                     `json:"channel_override,omitempty"`
	AdapterOverride string                     `json:"adapter_override,omitempty"`
}

type eventTemplateDispatchResponse struct {
	SocketDispatchResult
	ResolvedPayload  json.RawMessage            `json:"resolved_payload"`
	MissingVariables []string                   `json:"missing_variables,omitempty"`
	InvalidCasts     []EventTemplateInvalidCast `json:"invalid_casts,omitempty"`
}

func NewEventTemplateRegistry(dir string) (*EventTemplateRegistry, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, err
	}
	reg := &EventTemplateRegistry{
		dir:       absDir,
		templates: make(map[string]EventTemplate),
	}
	if err := reg.Load(); err != nil {
		return nil, err
	}
	return reg, nil
}

func (r *EventTemplateRegistry) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := make(map[string]EventTemplate)
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
			log.Printf("event template %s skipped: unsafe id", entry.Name())
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			log.Printf("event template %s skipped: %v", entry.Name(), err)
			continue
		}
		var tmpl EventTemplate
		if err := json.Unmarshal(data, &tmpl); err != nil {
			log.Printf("event template %s skipped: %v", entry.Name(), err)
			continue
		}
		if tmpl.ID == "" {
			tmpl.ID = id
		}
		fileID := tmpl.ID
		tmpl = normalizeEventTemplate(tmpl)
		tmpl.ID = fileID
		if tmpl.ID != id || !isSafeEventTemplateID(tmpl.ID) {
			log.Printf("event template %s skipped: id mismatch", entry.Name())
			continue
		}
		if err := validateEventTemplate(tmpl, nil); err != nil {
			log.Printf("event template %s skipped: %v", entry.Name(), err)
			continue
		}
		next[tmpl.ID] = cloneEventTemplate(tmpl)
	}
	r.templates = next
	return nil
}

func (r *EventTemplateRegistry) Templates() []EventTemplate {
	r.mu.RLock()
	defer r.mu.RUnlock()

	templates := make([]EventTemplate, 0, len(r.templates))
	for _, tmpl := range r.templates {
		templates = append(templates, cloneEventTemplate(tmpl))
	}
	sort.Slice(templates, func(i, j int) bool {
		if templates[i].Name == templates[j].Name {
			return templates[i].ID < templates[j].ID
		}
		return templates[i].Name < templates[j].Name
	})
	return templates
}

func (r *EventTemplateRegistry) Get(id string) (EventTemplate, error) {
	if !isSafeEventTemplateID(id) {
		return EventTemplate{}, fmt.Errorf("%w: %q", ErrEventTemplateNotFound, id)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tmpl, ok := r.templates[id]
	if !ok {
		return EventTemplate{}, fmt.Errorf("%w: %q", ErrEventTemplateNotFound, id)
	}
	return cloneEventTemplate(tmpl), nil
}

func (r *EventTemplateRegistry) Create(tmpl EventTemplate, schemas *SchemaRegistry) (EventTemplate, error) {
	tmpl = normalizeEventTemplate(tmpl)
	now := time.Now().UTC()
	tmpl.ID = ""
	tmpl.CreatedAt = now
	tmpl.UpdatedAt = now
	if err := validateEventTemplate(tmpl, schemas); err != nil {
		return EventTemplate{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	tmpl.ID = r.nextIDLocked(tmpl.Name)
	err := r.writeLocked(tmpl, false)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			tmpl.ID = r.nextIDLocked(tmpl.Name)
			err = r.writeLocked(tmpl, false)
		}
	}
	if err != nil {
		return EventTemplate{}, err
	}
	r.templates[tmpl.ID] = cloneEventTemplate(tmpl)
	return cloneEventTemplate(tmpl), nil
}

func (r *EventTemplateRegistry) Update(id string, tmpl EventTemplate, schemas *SchemaRegistry) (EventTemplate, error) {
	if !isSafeEventTemplateID(id) {
		return EventTemplate{}, fmt.Errorf("%w: %q", ErrEventTemplateNotFound, id)
	}
	tmpl = normalizeEventTemplate(tmpl)
	if err := validateEventTemplate(tmpl, schemas); err != nil {
		return EventTemplate{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.templates[id]
	if !ok {
		return EventTemplate{}, fmt.Errorf("%w: %q", ErrEventTemplateNotFound, id)
	}
	tmpl.ID = id
	tmpl.CreatedAt = existing.CreatedAt
	if tmpl.CreatedAt.IsZero() {
		tmpl.CreatedAt = time.Now().UTC()
	}
	tmpl.UpdatedAt = time.Now().UTC()
	if err := r.writeLocked(tmpl, true); err != nil {
		return EventTemplate{}, err
	}
	r.templates[id] = cloneEventTemplate(tmpl)
	return cloneEventTemplate(tmpl), nil
}

func (r *EventTemplateRegistry) Delete(id string) error {
	if !isSafeEventTemplateID(id) {
		return fmt.Errorf("%w: %q", ErrEventTemplateNotFound, id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.templates[id]; !ok {
		return fmt.Errorf("%w: %q", ErrEventTemplateNotFound, id)
	}
	if err := os.Remove(r.pathForIDLocked(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(r.templates, id)
	return nil
}

func (r *EventTemplateRegistry) Render(id string, vars map[string]string) (RenderedDispatch, error) {
	tmpl, err := r.Get(id)
	if err != nil {
		return RenderedDispatch{}, err
	}
	defaults := make(map[string]*string)
	for _, variable := range tmpl.Variables {
		name := strings.TrimSpace(variable.Name)
		if name == "" {
			continue
		}
		if variable.Default != nil {
			defaults[name] = variable.Default
		}
	}
	payload, missing, invalidCasts, err := resolveTemplateDetailed(tmpl.Payload, vars, defaults)
	if err != nil {
		return RenderedDispatch{}, err
	}
	return RenderedDispatch{
		Channel:      strings.TrimSpace(tmpl.Channel),
		Adapter:      tmpl.Adapter,
		TypeName:     tmpl.TypeName,
		Payload:      payload,
		Missing:      missing,
		InvalidCasts: invalidCasts,
		Source:       "template:" + tmpl.ID,
	}, nil
}

func (r *EventTemplateRegistry) nextIDLocked(name string) string {
	for attempt := 0; ; attempt++ {
		id := eventTemplateID(name, attempt)
		if _, exists := r.templates[id]; !exists {
			if _, err := os.Stat(r.pathForIDLocked(id)); errors.Is(err, os.ErrNotExist) {
				return id
			}
		}
	}
}

func (r *EventTemplateRegistry) writeLocked(tmpl EventTemplate, overwrite bool) error {
	data, err := json.MarshalIndent(tmpl, "", "  ")
	if err != nil {
		return err
	}
	path := r.pathForIDLocked(tmpl.ID)
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

func (r *EventTemplateRegistry) pathForIDLocked(id string) string {
	return filepath.Join(r.dir, id+".json")
}

func ResolveTemplate(payload json.RawMessage, vars map[string]string) (json.RawMessage, []string, error) {
	resolved, missing, invalidCasts, err := resolveTemplateDetailed(payload, vars, nil)
	if err != nil {
		return resolved, missing, err
	}
	if len(invalidCasts) > 0 {
		return resolved, missing, fmt.Errorf("invalid template casts: %s", describeInvalidCasts(invalidCasts))
	}
	return resolved, missing, nil
}

func resolveTemplate(payload json.RawMessage, vars map[string]string, defaults map[string]*string) (json.RawMessage, []string, error) {
	resolved, missing, _, err := resolveTemplateDetailed(payload, vars, defaults)
	return resolved, missing, err
}

func resolveTemplateDetailed(payload json.RawMessage, vars map[string]string, defaults map[string]*string) (json.RawMessage, []string, []EventTemplateInvalidCast, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid template payload JSON: %w", err)
	}
	values := builtinTemplateValues()
	for name, value := range defaults {
		if value != nil {
			values[name] = *value
		}
	}
	for name, value := range vars {
		values[name] = value
	}
	state := &templateResolveState{
		missingSet: make(map[string]struct{}),
	}
	resolved, err := resolveTemplateValue(value, values, state, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	data, err := json.Marshal(resolved)
	if err != nil {
		return nil, nil, nil, err
	}
	return data, state.missing, state.invalidCasts, nil
}

type templateResolveState struct {
	missingSet   map[string]struct{}
	missing      []string
	invalidCasts []EventTemplateInvalidCast
}

func (s *templateResolveState) addMissing(name string) {
	if _, ok := s.missingSet[name]; ok {
		return
	}
	s.missingSet[name] = struct{}{}
	s.missing = append(s.missing, name)
}

func (s *templateResolveState) addInvalidCast(name, kind, value string) {
	s.invalidCasts = append(s.invalidCasts, EventTemplateInvalidCast{
		Name:  name,
		Kind:  kind,
		Value: value,
	})
}

func resolveTemplateValue(value any, values map[string]string, state *templateResolveState, depth int) (any, error) {
	if depth > maxTemplateResolveDepth {
		return nil, fmt.Errorf("template payload nesting exceeds depth limit %d", maxTemplateResolveDepth)
	}
	switch typed := value.(type) {
	case map[string]any:
		next := make(map[string]any, len(typed))
		for key, child := range typed {
			resolved, err := resolveTemplateValue(child, values, state, depth+1)
			if err != nil {
				return nil, err
			}
			next[key] = resolved
		}
		return next, nil
	case []any:
		next := make([]any, len(typed))
		for i, child := range typed {
			resolved, err := resolveTemplateValue(child, values, state, depth+1)
			if err != nil {
				return nil, err
			}
			next[i] = resolved
		}
		return next, nil
	case string:
		return resolveTemplateString(typed, values, state), nil
	default:
		return value, nil
	}
}

func resolveTemplateString(value string, values map[string]string, state *templateResolveState) any {
	matches := templateVariablePattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(value) {
		kind := templateMatchKind(value, matches[0])
		name := value[matches[0][4]:matches[0][5]]
		resolved, ok := values[name]
		if !ok {
			state.addMissing(name)
			return value
		}
		return typedTemplateValue(kind, name, resolved, state)
	}

	var b strings.Builder
	last := 0
	for _, match := range matches {
		b.WriteString(value[last:match[0]])
		name := value[match[4]:match[5]]
		resolved, ok := values[name]
		if !ok {
			state.addMissing(name)
			b.WriteString(value[match[0]:match[1]])
		} else {
			b.WriteString(resolved)
		}
		last = match[1]
	}
	b.WriteString(value[last:])
	return b.String()
}

func templateMatchKind(value string, match []int) string {
	if len(match) < 4 || match[2] < 0 || match[3] < 0 {
		return ""
	}
	return value[match[2]:match[3]]
}

func typedTemplateValue(kind, name, value string, state *templateResolveState) any {
	switch kind {
	case "", "str":
		return value
	case "json":
		var typed any
		if err := json.Unmarshal([]byte(value), &typed); err == nil {
			return typed
		}
	case "int":
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return parsed
		}
	case "float":
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	case "bool":
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	state.addInvalidCast(name, kind, value)
	return fmt.Sprintf("{{%s:%s}}", kind, name)
}

func builtinTemplateValues() map[string]string {
	now := time.Now().UTC()
	return map[string]string{
		"now":         now.Format(time.RFC3339),
		"now_unix":    strconv.FormatInt(now.Unix(), 10),
		"now_unix_ms": strconv.FormatInt(now.UnixMilli(), 10),
		"uuid":        newTemplateUUID(),
	}
}

func newTemplateUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func RegisterEventTemplateRoutes(mux *http.ServeMux, registry *EventTemplateRegistry, hub *SocketHub, schemas *SchemaRegistry) {
	if registry == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/event-templates", func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(eventTemplatesResponse{Templates: registry.Templates()})
		case http.MethodPost:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var tmpl EventTemplate
			if ok := decodeEventTemplateJSON(w, r, &tmpl, false); !ok {
				return
			}
			created, err := registry.Create(tmpl, schemas)
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
	mux.HandleFunc("/__ditto__/api/event-templates/", func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/__ditto__/api/event-templates/")
		id, action, ok := splitEventTemplatePath(rest)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if action == "dispatch" {
			handleEventTemplateDispatch(w, r, registry, hub, schemas, id)
			return
		}
		switch r.Method {
		case http.MethodGet:
			tmpl, err := registry.Get(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tmpl)
		case http.MethodPut:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var tmpl EventTemplate
			if ok := decodeEventTemplateJSON(w, r, &tmpl, false); !ok {
				return
			}
			updated, err := registry.Update(id, tmpl, schemas)
			if err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, ErrEventTemplateNotFound) {
					status = http.StatusNotFound
				}
				http.Error(w, err.Error(), status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(updated)
		case http.MethodDelete:
			if err := registry.Delete(id); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func handleEventTemplateDispatch(w http.ResponseWriter, r *http.Request, registry *EventTemplateRegistry, hub *SocketHub, schemas *SchemaRegistry, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Content-Type") != "" && !hasJSONContentType(r) {
		http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	var req eventTemplateDispatchRequest
	if ok := decodeEventTemplateJSON(w, r, &req, true); !ok {
		return
	}
	vars, err := eventTemplateVariablesToStrings(req.Variables)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rendered, err := registry.Render(id, vars)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrEventTemplateNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	if len(rendered.Missing) > 0 || len(rendered.InvalidCasts) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(eventTemplateDispatchResponse{
			ResolvedPayload:  rendered.Payload,
			MissingVariables: rendered.Missing,
			InvalidCasts:     rendered.InvalidCasts,
		})
		return
	}
	result, err := dispatchRendered(hub, schemas, rendered, &dispatchOverrides{
		Channel: req.ChannelOverride,
		Adapter: req.AdapterOverride,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(eventTemplateDispatchResponse{
		SocketDispatchResult: result,
		ResolvedPayload:      rendered.Payload,
	})
}

func eventTemplateVariablesToStrings(vars map[string]json.RawMessage) (map[string]string, error) {
	if len(vars) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(vars))
	for name, raw := range vars {
		name = strings.TrimSpace(name)
		if !templateNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid variable name %q", name)
		}
		if len(raw) == 0 {
			out[name] = ""
			continue
		}
		trimmed := bytes.TrimSpace(raw)
		if bytes.Equal(trimmed, []byte("null")) {
			out[name] = "null"
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			out[name] = text
			continue
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("variable %q must be valid JSON", name)
		}
		out[name] = string(raw)
	}
	return out, nil
}

func describeInvalidCasts(casts []EventTemplateInvalidCast) string {
	seen := make(map[string]struct{}, len(casts))
	parts := make([]string, 0, len(casts))
	for _, cast := range casts {
		part := fmt.Sprintf("%s:%s", cast.Kind, cast.Name)
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		parts = append(parts, part)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func splitEventTemplatePath(rest string) (id string, action string, ok bool) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 1 && isSafeEventTemplateID(parts[0]) {
		return parts[0], "", true
	}
	if len(parts) == 2 && parts[1] == "dispatch" && isSafeEventTemplateID(parts[0]) {
		return parts[0], parts[1], true
	}
	return "", "", false
}

func validateEventTemplate(tmpl EventTemplate, schemas *SchemaRegistry) error {
	if tmpl.Version != 1 {
		return fmt.Errorf("unsupported event template version %d", tmpl.Version)
	}
	if strings.TrimSpace(tmpl.Name) == "" {
		return fmt.Errorf("name is required")
	}
	channel := strings.TrimSpace(tmpl.Channel)
	if channel == "" {
		return fmt.Errorf("channel is required")
	}
	if strings.ContainsAny(channel, "\r\n") {
		return fmt.Errorf("channel cannot contain newlines")
	}
	if strings.Contains(channel, "{{") || strings.Contains(channel, "}}") {
		return fmt.Errorf("channel variables are not supported")
	}
	adapter := normalizeAdapter(tmpl.Adapter)
	if _, err := NewProtocolAdapter(adapter); err != nil {
		return err
	}
	if len(tmpl.Payload) == 0 {
		return fmt.Errorf("payload is required")
	}
	if !json.Valid(tmpl.Payload) {
		return fmt.Errorf("payload must be valid JSON")
	}
	if err := validateTemplateCasts(tmpl.Payload); err != nil {
		return err
	}
	typeName := strings.TrimSpace(tmpl.TypeName)
	if typeName != "" {
		if schemas == nil || schemas.Descriptor(typeName) == nil {
			return fmt.Errorf("schema type %q is not loaded", typeName)
		}
	}
	seen := make(map[string]struct{})
	for _, variable := range tmpl.Variables {
		name := strings.TrimSpace(variable.Name)
		if name == "" {
			return fmt.Errorf("variable name is required")
		}
		if !templateNamePattern.MatchString(name) {
			return fmt.Errorf("invalid variable name %q", name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate variable name %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateTemplateCasts(payload json.RawMessage) error {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return fmt.Errorf("payload must be valid JSON")
	}
	return validateTemplateCastsInValue(value)
}

func validateTemplateCastsInValue(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.Contains(key, "{{") || strings.Contains(key, "}}") {
				return fmt.Errorf("template variables in keys are not supported")
			}
			if err := validateTemplateCastsInValue(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateTemplateCastsInValue(child); err != nil {
				return err
			}
		}
	case string:
		matches := templateVariablePattern.FindAllStringSubmatchIndex(typed, -1)
		for _, match := range matches {
			kind := templateMatchKind(typed, match)
			if kind == "" {
				continue
			}
			if !isSupportedTemplateCast(kind) {
				return fmt.Errorf("unsupported template cast %q", kind)
			}
			if !(len(matches) == 1 && match[0] == 0 && match[1] == len(typed)) {
				name := typed[match[4]:match[5]]
				return fmt.Errorf("typed template cast %q for %q must occupy the whole string", kind, name)
			}
		}
	}
	return nil
}

func isSupportedTemplateCast(kind string) bool {
	switch kind {
	case "str", "json", "int", "float", "bool":
		return true
	default:
		return false
	}
}

func decodeEventTemplateJSON(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventTemplateBodyBytes))
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

func normalizeEventTemplate(tmpl EventTemplate) EventTemplate {
	if tmpl.Version == 0 {
		tmpl.Version = 1
	}
	tmpl.Name = strings.TrimSpace(tmpl.Name)
	tmpl.Description = strings.TrimSpace(tmpl.Description)
	tmpl.Channel = strings.TrimSpace(tmpl.Channel)
	tmpl.Adapter = normalizeAdapter(tmpl.Adapter)
	tmpl.TypeName = strings.TrimSpace(tmpl.TypeName)
	tmpl.Payload = append(json.RawMessage(nil), tmpl.Payload...)
	variables := make([]EventTemplateVariable, 0, len(tmpl.Variables))
	for _, variable := range tmpl.Variables {
		variable.Name = strings.TrimSpace(variable.Name)
		variable.Description = strings.TrimSpace(variable.Description)
		if variable.Default != nil {
			value := *variable.Default
			variable.Default = &value
		}
		variables = append(variables, variable)
	}
	tmpl.Variables = variables
	return tmpl
}

func eventTemplateID(name string, attempt int) string {
	base := sanitizePackName(name)
	if base == "" {
		base = "event-template"
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

func isSafeEventTemplateID(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" &&
		id != "." &&
		id != ".." &&
		!strings.Contains(id, "/") &&
		!strings.Contains(id, string(os.PathSeparator)) &&
		id == sanitizePackName(id)
}

func cloneEventTemplate(tmpl EventTemplate) EventTemplate {
	tmpl.Payload = append(json.RawMessage(nil), tmpl.Payload...)
	if tmpl.Variables != nil {
		variables := tmpl.Variables
		tmpl.Variables = make([]EventTemplateVariable, len(tmpl.Variables))
		for i, variable := range variables {
			if variable.Default != nil {
				value := *variable.Default
				variable.Default = &value
			}
			tmpl.Variables[i] = variable
		}
	}
	return tmpl
}
