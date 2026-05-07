package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

var templateVariablePattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

type EventTemplate struct {
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
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
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
	Variables       map[string]string `json:"variables,omitempty"`
	ChannelOverride string            `json:"channel_override,omitempty"`
	AdapterOverride string            `json:"adapter_override,omitempty"`
}

type eventTemplateDispatchResponse struct {
	SocketDispatchResult
	ResolvedPayload  json.RawMessage `json:"resolved_payload"`
	MissingVariables []string        `json:"missing_variables,omitempty"`
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
		return EventTemplate{}, fmt.Errorf("event template %q not found", id)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tmpl, ok := r.templates[id]
	if !ok {
		return EventTemplate{}, fmt.Errorf("event template %q not found", id)
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
	if err := r.writeLocked(tmpl); err != nil {
		return EventTemplate{}, err
	}
	r.templates[tmpl.ID] = cloneEventTemplate(tmpl)
	return cloneEventTemplate(tmpl), nil
}

func (r *EventTemplateRegistry) Update(id string, tmpl EventTemplate, schemas *SchemaRegistry) (EventTemplate, error) {
	if !isSafeEventTemplateID(id) {
		return EventTemplate{}, fmt.Errorf("event template %q not found", id)
	}
	tmpl = normalizeEventTemplate(tmpl)
	if err := validateEventTemplate(tmpl, schemas); err != nil {
		return EventTemplate{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.templates[id]
	if !ok {
		return EventTemplate{}, fmt.Errorf("event template %q not found", id)
	}
	tmpl.ID = id
	tmpl.CreatedAt = existing.CreatedAt
	if tmpl.CreatedAt.IsZero() {
		tmpl.CreatedAt = time.Now().UTC()
	}
	tmpl.UpdatedAt = time.Now().UTC()
	if err := r.writeLocked(tmpl); err != nil {
		return EventTemplate{}, err
	}
	r.templates[id] = cloneEventTemplate(tmpl)
	return cloneEventTemplate(tmpl), nil
}

func (r *EventTemplateRegistry) Delete(id string) error {
	if !isSafeEventTemplateID(id) {
		return fmt.Errorf("event template %q not found", id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.templates[id]; !ok {
		return fmt.Errorf("event template %q not found", id)
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
	defaults := make(map[string]string)
	for _, variable := range tmpl.Variables {
		name := strings.TrimSpace(variable.Name)
		if name == "" {
			continue
		}
		defaults[name] = variable.Default
	}
	payload, missing, err := resolveTemplate(tmpl.Payload, vars, defaults)
	if err != nil {
		return RenderedDispatch{}, err
	}
	return RenderedDispatch{
		Channel:  strings.TrimSpace(tmpl.Channel),
		Adapter:  tmpl.Adapter,
		TypeName: tmpl.TypeName,
		Payload:  payload,
		Missing:  missing,
	}, nil
}

func (r *EventTemplateRegistry) nextIDLocked(name string) string {
	for attempt := 0; ; attempt++ {
		id := eventTemplateID(name, attempt)
		if _, exists := r.templates[id]; !exists {
			return id
		}
	}
}

func (r *EventTemplateRegistry) writeLocked(tmpl EventTemplate) error {
	data, err := json.MarshalIndent(tmpl, "", "  ")
	if err != nil {
		return err
	}
	path := r.pathForIDLocked(tmpl.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (r *EventTemplateRegistry) pathForIDLocked(id string) string {
	return filepath.Join(r.dir, id+".json")
}

func ResolveTemplate(payload json.RawMessage, vars map[string]string) (json.RawMessage, []string, error) {
	return resolveTemplate(payload, vars, nil)
}

func resolveTemplate(payload json.RawMessage, vars map[string]string, defaults map[string]string) (json.RawMessage, []string, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, nil, fmt.Errorf("invalid template payload JSON: %w", err)
	}
	values := builtinTemplateValues()
	for name, value := range defaults {
		values[name] = value
	}
	for name, value := range vars {
		values[name] = value
	}
	missingSet := make(map[string]struct{})
	var missing []string
	addMissing := func(name string) {
		if _, ok := missingSet[name]; ok {
			return
		}
		missingSet[name] = struct{}{}
		missing = append(missing, name)
	}
	resolved, err := resolveTemplateValue(value, values, addMissing, 0)
	if err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(resolved)
	if err != nil {
		return nil, nil, err
	}
	return data, missing, nil
}

func resolveTemplateValue(value any, values map[string]string, addMissing func(string), depth int) (any, error) {
	if depth > maxTemplateResolveDepth {
		return nil, fmt.Errorf("template payload nesting exceeds depth limit %d", maxTemplateResolveDepth)
	}
	switch typed := value.(type) {
	case map[string]any:
		next := make(map[string]any, len(typed))
		for key, child := range typed {
			resolved, err := resolveTemplateValue(child, values, addMissing, depth+1)
			if err != nil {
				return nil, err
			}
			next[key] = resolved
		}
		return next, nil
	case []any:
		next := make([]any, len(typed))
		for i, child := range typed {
			resolved, err := resolveTemplateValue(child, values, addMissing, depth+1)
			if err != nil {
				return nil, err
			}
			next[i] = resolved
		}
		return next, nil
	case string:
		return resolveTemplateString(typed, values, addMissing), nil
	default:
		return value, nil
	}
}

func resolveTemplateString(value string, values map[string]string, addMissing func(string)) any {
	matches := templateVariablePattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(value) {
		name := value[matches[0][2]:matches[0][3]]
		resolved, ok := values[name]
		if !ok {
			addMissing(name)
			return value
		}
		var typed any
		if err := json.Unmarshal([]byte(resolved), &typed); err == nil {
			return typed
		}
		return resolved
	}

	var b strings.Builder
	last := 0
	for _, match := range matches {
		b.WriteString(value[last:match[0]])
		name := value[match[2]:match[3]]
		resolved, ok := values[name]
		if !ok {
			addMissing(name)
			b.WriteString(value[match[0]:match[1]])
		} else {
			b.WriteString(resolved)
		}
		last = match[1]
	}
	b.WriteString(value[last:])
	return b.String()
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
			r.Body = http.MaxBytesReader(w, r.Body, maxEventTemplateBodyBytes)
			var tmpl EventTemplate
			if err := json.NewDecoder(r.Body).Decode(&tmpl); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
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
			r.Body = http.MaxBytesReader(w, r.Body, maxEventTemplateBodyBytes)
			var tmpl EventTemplate
			if err := json.NewDecoder(r.Body).Decode(&tmpl); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			updated, err := registry.Update(id, tmpl, schemas)
			if err != nil {
				status := http.StatusBadRequest
				if strings.Contains(err.Error(), "not found") {
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
	if !hasJSONContentType(r) {
		http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEventTemplateBodyBytes)
	var req eventTemplateDispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	rendered, err := registry.Render(id, req.Variables)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	if len(rendered.Missing) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(eventTemplateDispatchResponse{
			ResolvedPayload:  rendered.Payload,
			MissingVariables: rendered.Missing,
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
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(name) {
			return fmt.Errorf("invalid variable name %q", name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate variable name %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func normalizeEventTemplate(tmpl EventTemplate) EventTemplate {
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
	if len(base) > 48 {
		base = strings.Trim(base[:48], "-")
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
		tmpl.Variables = append([]EventTemplateVariable(nil), tmpl.Variables...)
	}
	return tmpl
}
