// Package eventtemplate stores reusable socket events: a channel, adapter,
// optional protobuf type and a JSON payload with {{var}} placeholders
// (optionally typed as {{int:var}}, {{bool:var}}, ...), and renders them
// with variable values, declared defaults and built-ins like {{uuid}}.
package eventtemplate

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ditto/internal/adapter"
	"ditto/internal/jsonfile"
	"ditto/internal/schema"
)

const (
	maxTemplateResolveDepth = 32
)

// ErrNotFound is returned for an unknown template ID.
var ErrNotFound = errors.New("event template not found")

var (
	templateVariablePattern = regexp.MustCompile(`\{\{\s*(?:(\w+):)?([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	templateNamePattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Template is a stored, reusable socket event.
type Template struct {
	Version     int             `json:"version"`
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Channel     string          `json:"channel"`
	Adapter     string          `json:"adapter,omitempty"`
	TypeName    string          `json:"type_name,omitempty"`
	Payload     json.RawMessage `json:"payload"`
	Variables   []Variable      `json:"variables,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Variable declares a template variable and its optional default.
type Variable struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Default     *string `json:"default,omitempty"`
}

// InvalidCast reports a typed placeholder whose value does not parse as
// its type.
type InvalidCast struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Store keeps templates as one JSON file each in a directory. It is safe
// for concurrent use.
type Store struct {
	mu        sync.RWMutex
	dir       string
	templates map[string]Template
	schemas   *schema.Registry
	adapters  *adapter.Registry
}

// NewStore loads the templates in dir. Templates naming an adapter or
// protobuf type that adapters or schemas do not know are skipped, so both
// must be loaded first.
func NewStore(dir string, schemas *schema.Registry, adapters *adapter.Registry) (*Store, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, err
	}
	reg := &Store{
		dir:       absDir,
		templates: make(map[string]Template),
		schemas:   schemas,
		adapters:  adapters,
	}
	if err := reg.Load(); err != nil {
		return nil, err
	}
	return reg, nil
}

// Load re-reads every template file, skipping invalid ones with a log line.
func (r *Store) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := make(map[string]Template)
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
			log.Printf("event template %s skipped: unsafe id", entry.Name())
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			log.Printf("event template %s skipped: %v", entry.Name(), err)
			continue
		}
		var tmpl Template
		if err := json.Unmarshal(data, &tmpl); err != nil {
			log.Printf("event template %s skipped: %v", entry.Name(), err)
			continue
		}
		if tmpl.ID == "" {
			tmpl.ID = id
		}
		fileID := tmpl.ID
		tmpl = normalize(tmpl)
		tmpl.ID = fileID
		if tmpl.ID != id || !jsonfile.ValidID(tmpl.ID) {
			log.Printf("event template %s skipped: id mismatch", entry.Name())
			continue
		}
		if err := validate(tmpl, r.schemas, r.adapters); err != nil {
			log.Printf("event template %s skipped: %v", entry.Name(), err)
			continue
		}
		next[tmpl.ID] = clone(tmpl)
	}
	r.templates = next
	return nil
}

// Templates returns every template sorted by name.
func (r *Store) Templates() []Template {
	r.mu.RLock()
	defer r.mu.RUnlock()

	templates := make([]Template, 0, len(r.templates))
	for _, tmpl := range r.templates {
		templates = append(templates, clone(tmpl))
	}
	sort.Slice(templates, func(i, j int) bool {
		if templates[i].Name == templates[j].Name {
			return templates[i].ID < templates[j].ID
		}
		return templates[i].Name < templates[j].Name
	})
	return templates
}

// Get returns the template with id, or ErrNotFound.
func (r *Store) Get(id string) (Template, error) {
	if !jsonfile.ValidID(id) {
		return Template{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tmpl, ok := r.templates[id]
	if !ok {
		return Template{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return clone(tmpl), nil
}

// Create validates tmpl, assigns it an ID derived from its name and saves it.
func (r *Store) Create(tmpl Template) (Template, error) {
	tmpl = normalize(tmpl)
	now := time.Now().UTC()
	tmpl.ID = ""
	tmpl.CreatedAt = now
	tmpl.UpdatedAt = now
	if err := validate(tmpl, r.schemas, r.adapters); err != nil {
		return Template{}, err
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
		return Template{}, err
	}
	r.templates[tmpl.ID] = clone(tmpl)
	return clone(tmpl), nil
}

// Update validates and replaces the template with id.
func (r *Store) Update(id string, tmpl Template) (Template, error) {
	if !jsonfile.ValidID(id) {
		return Template{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	tmpl = normalize(tmpl)
	if err := validate(tmpl, r.schemas, r.adapters); err != nil {
		return Template{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.templates[id]
	if !ok {
		return Template{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	tmpl.ID = id
	tmpl.CreatedAt = existing.CreatedAt
	if tmpl.CreatedAt.IsZero() {
		tmpl.CreatedAt = time.Now().UTC()
	}
	tmpl.UpdatedAt = time.Now().UTC()
	if err := r.writeLocked(tmpl, true); err != nil {
		return Template{}, err
	}
	r.templates[id] = clone(tmpl)
	return clone(tmpl), nil
}

// Delete removes the template with id.
func (r *Store) Delete(id string) error {
	if !jsonfile.ValidID(id) {
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.templates[id]; !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err := os.Remove(r.pathForIDLocked(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(r.templates, id)
	return nil
}

// Rendered is a template with its variables substituted. Missing and
// InvalidCasts report variables that could not be resolved; callers must not
// dispatch a render that has either.
type Rendered struct {
	Channel      string
	Adapter      string
	TypeName     string
	Payload      json.RawMessage
	Missing      []string
	InvalidCasts []InvalidCast
}

// Render substitutes vars, the template's defaults and built-ins into the
// template's payload. Unresolved or badly typed variables are reported in
// the result, not as an error.
func (r *Store) Render(id string, vars map[string]string) (Rendered, error) {
	tmpl, err := r.Get(id)
	if err != nil {
		return Rendered{}, err
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
	payload, missing, invalidCasts, err := Resolve(tmpl.Payload, vars, defaults)
	if err != nil {
		return Rendered{}, err
	}
	return Rendered{
		Channel:      strings.TrimSpace(tmpl.Channel),
		Adapter:      tmpl.Adapter,
		TypeName:     tmpl.TypeName,
		Payload:      payload,
		Missing:      missing,
		InvalidCasts: invalidCasts,
	}, nil
}

func (r *Store) nextIDLocked(name string) string {
	for attempt := 0; ; attempt++ {
		id := templateID(name, attempt)
		if _, exists := r.templates[id]; !exists {
			if _, err := os.Stat(r.pathForIDLocked(id)); errors.Is(err, os.ErrNotExist) {
				return id
			}
		}
	}
}

func (r *Store) writeLocked(tmpl Template, overwrite bool) error {
	return jsonfile.Write(r.pathForIDLocked(tmpl.ID), tmpl, overwrite)
}

func (r *Store) pathForIDLocked(id string) string {
	return filepath.Join(r.dir, id+".json")
}

// Resolve substitutes placeholders in payload, returning the result, the
// names of variables with no value, and typed placeholders whose value did
// not parse. defaults supplies values for variables missing from vars.
func Resolve(payload json.RawMessage, vars map[string]string, defaults map[string]*string) (json.RawMessage, []string, []InvalidCast, error) {
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
	invalidCasts []InvalidCast
}

func (s *templateResolveState) addMissing(name string) {
	if _, ok := s.missingSet[name]; ok {
		return
	}
	s.missingSet[name] = struct{}{}
	s.missing = append(s.missing, name)
}

func (s *templateResolveState) addInvalidCast(name, kind, value string) {
	s.invalidCasts = append(s.invalidCasts, InvalidCast{
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

// VarsFromJSON converts request variables given as JSON values (strings,
// numbers, booleans) to the strings Resolve takes.
func VarsFromJSON(vars map[string]json.RawMessage) (map[string]string, error) {
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

// DescribeInvalidCasts formats casts as one sorted, de-duplicated message.
func DescribeInvalidCasts(casts []InvalidCast) string {
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

func validate(tmpl Template, schemas *schema.Registry, adapters *adapter.Registry) error {
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
	adapterName := adapter.Normalize(tmpl.Adapter)
	if _, err := adapters.Protocol(adapterName); err != nil {
		return err
	}
	if len(tmpl.Payload) == 0 {
		return fmt.Errorf("payload is required")
	}
	if !json.Valid(tmpl.Payload) {
		return fmt.Errorf("payload must be valid JSON")
	}
	if err := ValidateCasts(tmpl.Payload); err != nil {
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

// ValidateCasts rejects typed placeholders with an unsupported type or used
// inside a longer string or as an object key.
func ValidateCasts(payload json.RawMessage) error {
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

func normalize(tmpl Template) Template {
	if tmpl.Version == 0 {
		tmpl.Version = 1
	}
	tmpl.Name = strings.TrimSpace(tmpl.Name)
	tmpl.Description = strings.TrimSpace(tmpl.Description)
	tmpl.Channel = strings.TrimSpace(tmpl.Channel)
	tmpl.Adapter = adapter.Normalize(tmpl.Adapter)
	tmpl.TypeName = strings.TrimSpace(tmpl.TypeName)
	tmpl.Payload = append(json.RawMessage(nil), tmpl.Payload...)
	variables := make([]Variable, 0, len(tmpl.Variables))
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

func templateID(name string, attempt int) string {
	return jsonfile.HashedID(name, "event-template", jsonfile.NameAttempt(name, attempt))
}

func clone(tmpl Template) Template {
	tmpl.Payload = append(json.RawMessage(nil), tmpl.Payload...)
	if tmpl.Variables != nil {
		variables := tmpl.Variables
		tmpl.Variables = make([]Variable, len(tmpl.Variables))
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
