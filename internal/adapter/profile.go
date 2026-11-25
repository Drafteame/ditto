package adapter

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"ditto/internal/jsonfile"
)

//go:embed defaults/*.json
var defaultAdapterProfilesFS embed.FS

const (
	adapterProfilesSeededFile       = ".seeded"
	maxAdapterProfileBytes    int64 = 1 << 20
)

var adapterProfileNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

var builtinAdapterNames = map[string]struct{}{
	"raw":     {},
	"appsync": {},
	"relay":   {},
}

// Profile configures a protocol without code: a built-in base adapter plus
// envelope templates, subprotocols and type aliases.
type Profile struct {
	ManifestVersion int               `json:"manifest_version"`
	Name            string            `json:"name"`
	BaseAdapter     string            `json:"base_adapter"`
	Subprotocols    []string          `json:"subprotocols,omitempty"`
	Envelope        Envelope          `json:"envelope"`
	TypeAliases     map[string]string `json:"type_aliases,omitempty"`
}

// Envelope holds a profile's templates for wrapping outgoing events.
type Envelope struct {
	Outer       string `json:"outer"`
	InnerBinary string `json:"inner_binary"`
	InnerJSON   string `json:"inner_json"`
}

// Summary is the part of a Profile the dashboard lists.
type Summary struct {
	Name         string            `json:"name"`
	BaseAdapter  string            `json:"base_adapter"`
	Subprotocols []string          `json:"subprotocols"`
	TypeAliases  map[string]string `json:"type_aliases"`
}

type profileAdapter struct {
	profile Profile
	base    Protocol
}

type adapterTemplateValue struct {
	value string
	raw   bool
}

// Registry resolves socket adapter names to the built-in protocol
// adapters or to adapter profiles loaded from disk. The zero value, and a nil
// *Registry, know only the built-in adapters.
type Registry struct {
	mu       sync.RWMutex
	profiles map[string]Profile
}

// ValidateProfile checks a profile's version, name, base adapter and
// envelope templates.
func ValidateProfile(profile Profile) error {
	if profile.ManifestVersion != 1 {
		return fmt.Errorf("manifest_version must be 1, got %d", profile.ManifestVersion)
	}
	name := Normalize(profile.Name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if name != profile.Name || !adapterProfileNamePattern.MatchString(name) {
		return fmt.Errorf("name %q must match [a-z0-9-]+", profile.Name)
	}
	if _, ok := builtinAdapterNames[name]; ok {
		return fmt.Errorf("name %q collides with a built-in adapter", name)
	}
	base := Normalize(profile.BaseAdapter)
	if _, ok := builtinAdapterNames[base]; !ok {
		return fmt.Errorf("base_adapter must be one of raw, appsync, relay, got %q", profile.BaseAdapter)
	}
	if profile.BaseAdapter != base {
		return fmt.Errorf("base_adapter must be lowercase, got %q", profile.BaseAdapter)
	}
	if strings.TrimSpace(profile.Envelope.Outer) == "" {
		return fmt.Errorf("envelope.outer is required")
	}
	if strings.TrimSpace(profile.Envelope.InnerBinary) == "" {
		return fmt.Errorf("envelope.inner_binary is required")
	}
	if strings.TrimSpace(profile.Envelope.InnerJSON) == "" {
		return fmt.Errorf("envelope.inner_json is required")
	}
	return nil
}

func newProfileAdapter(profile Profile) (profileAdapter, error) {
	if err := ValidateProfile(profile); err != nil {
		return profileAdapter{}, err
	}
	base, err := newBuiltin(profile.BaseAdapter)
	if err != nil {
		return profileAdapter{}, err
	}
	return profileAdapter{
		profile: cloneAdapterProfile(profile),
		base:    base,
	}, nil
}

func (a profileAdapter) ParseClientMessage(b []byte) (ClientMessage, error) {
	return a.base.ParseClientMessage(b)
}

func (a profileAdapter) EncodePayload(payload json.RawMessage) (Payload, error) {
	return a.base.EncodePayload(payload)
}

func (a profileAdapter) EncodeServerMessage(msg ServerMessage) (Message, error) {
	return a.base.EncodeServerMessage(msg)
}

func (a profileAdapter) Heartbeat() (Message, time.Duration) {
	return a.base.Heartbeat()
}

func (a profileAdapter) Subprotocols() []string {
	return append([]string(nil), a.profile.Subprotocols...)
}

func (a profileAdapter) GreetsOnConnect() bool {
	return GreetsOnConnect(a.base)
}

func (a profileAdapter) WrapData(payload Payload, subID, channel string) (Message, error) {
	var innerTemplate string
	var innerVars map[string]adapterTemplateValue
	var err error
	if payload.Kind == websocket.MessageBinary {
		innerTemplate = a.profile.Envelope.InnerBinary
		innerVars, err = a.innerBinaryVars(payload, channel)
	} else {
		innerTemplate = a.profile.Envelope.InnerJSON
		innerVars, err = a.innerJSONVars(payload, channel)
	}
	if err != nil {
		return Message{}, err
	}
	inner, err := renderAdapterJSONTemplate(innerTemplate, innerVars)
	if err != nil {
		return Message{}, fmt.Errorf("render inner envelope: %w", err)
	}

	innerString, err := json.Marshal(string(inner))
	if err != nil {
		return Message{}, err
	}
	outerVars := map[string]adapterTemplateValue{
		"sub_id":       {value: subID},
		"channel":      {value: channel},
		"inner_object": {value: string(inner), raw: true},
		"inner_string": {value: string(innerString), raw: true},
	}
	outer, err := renderAdapterJSONTemplate(a.profile.Envelope.Outer, outerVars)
	if err != nil {
		return Message{}, fmt.Errorf("render outer envelope: %w", err)
	}
	return Message{Data: outer, Kind: websocket.MessageText}, nil
}

func (a profileAdapter) innerBinaryVars(payload Payload, channel string) (map[string]adapterTemplateValue, error) {
	typeName := strings.TrimSpace(payload.TypeName)
	alias := typeName
	if mapped := a.profile.TypeAliases[typeName]; mapped != "" {
		alias = mapped
	}
	return map[string]adapterTemplateValue{
		"alias":     {value: alias},
		"type_name": {value: typeName},
		"channel":   {value: channel},
		"base64":    {value: base64.StdEncoding.EncodeToString(payload.Data)},
	}, nil
}

func (a profileAdapter) innerJSONVars(payload Payload, channel string) (map[string]adapterTemplateValue, error) {
	typeName := strings.TrimSpace(payload.TypeName)
	alias := typeName
	if mapped := a.profile.TypeAliases[typeName]; mapped != "" {
		alias = mapped
	}
	rawJSON, err := payloadJSONValue(payload)
	if err != nil {
		return nil, err
	}
	return map[string]adapterTemplateValue{
		"alias":     {value: alias},
		"type_name": {value: typeName},
		"channel":   {value: channel},
		"json":      {value: string(rawJSON), raw: true},
	}, nil
}

// payloadJSONValue is used only by the JSON-payload path. Binary callers must
// use the base64 variable instead; Data is considered here only when it already
// contains valid JSON bytes from a text payload.
func payloadJSONValue(payload Payload) ([]byte, error) {
	if len(payload.Data) > 0 && json.Valid(payload.Data) {
		return append([]byte(nil), payload.Data...), nil
	}
	if payload.Value != nil {
		data, err := json.Marshal(payload.Value)
		if err != nil {
			return nil, fmt.Errorf("json payload value: %w", err)
		}
		return data, nil
	}
	return []byte(`null`), nil
}

func renderAdapterJSONTemplate(tmpl string, vars map[string]adapterTemplateValue) ([]byte, error) {
	rendered, err := renderAdapterTemplate(tmpl, vars)
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(rendered)) {
		return nil, fmt.Errorf("rendered template is not valid JSON: %s", rendered)
	}
	return []byte(rendered), nil
}

func renderAdapterTemplate(tmpl string, vars map[string]adapterTemplateValue) (string, error) {
	var out strings.Builder
	for i := 0; i < len(tmpl); {
		start := strings.Index(tmpl[i:], "${")
		if start < 0 {
			out.WriteString(tmpl[i:])
			break
		}
		start += i
		out.WriteString(tmpl[i:start])
		end := strings.IndexByte(tmpl[start+2:], '}')
		if end < 0 {
			return "", fmt.Errorf("unclosed variable at byte %d", start)
		}
		end += start + 2
		name := tmpl[start+2 : end]
		value, ok := vars[name]
		if !ok {
			return "", fmt.Errorf("missing variable %q", name)
		}
		if value.raw {
			out.WriteString(value.value)
		} else {
			escaped, err := jsonStringContent(value.value)
			if err != nil {
				return "", err
			}
			out.WriteString(escaped)
		}
		i = end + 1
	}
	return out.String(), nil
}

func jsonStringContent(value string) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) < 2 {
		return "", fmt.Errorf("failed to encode JSON string")
	}
	return string(data[1 : len(data)-1]), nil
}

func seedDefaults(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	seeded, err := readSeededAdapterProfiles(dir)
	if err != nil {
		return err
	}
	if len(seeded) == 0 {
		hasProfiles, err := hasAdapterProfileFiles(dir)
		if err != nil {
			return err
		}
		if hasProfiles {
			if err := markEmbeddedAdapterProfilesSeeded(dir, seeded); err != nil {
				return err
			}
			return writeSeededAdapterProfiles(dir, seeded)
		}
	}
	changed := false
	if err := fs.WalkDir(defaultAdapterProfilesFS, "defaults", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		if seeded[entry.Name()] {
			return nil
		}
		target := filepath.Join(dir, entry.Name())
		if _, err := os.Stat(target); err == nil {
			seeded[entry.Name()] = true
			changed = true
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := readEmbeddedAdapterProfile(path)
		if err != nil {
			return err
		}
		if err := jsonfile.WriteAtomic(target, data, 0o644); err != nil {
			return err
		}
		seeded[entry.Name()] = true
		changed = true
		return nil
	}); err != nil {
		return err
	}
	if changed {
		return writeSeededAdapterProfiles(dir, seeded)
	}
	return nil
}

func hasAdapterProfileFiles(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			return true, nil
		}
	}
	return false, nil
}

func markEmbeddedAdapterProfilesSeeded(dir string, seeded map[string]bool) error {
	return fs.WalkDir(defaultAdapterProfilesFS, "defaults", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		seeded[entry.Name()] = true
		return nil
	})
}

func readSeededAdapterProfiles(dir string) (map[string]bool, error) {
	seeded := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(dir, adapterProfilesSeededFile))
	if os.IsNotExist(err) {
		return seeded, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			seeded[name] = true
		}
	}
	return seeded, nil
}

func writeSeededAdapterProfiles(dir string, seeded map[string]bool) error {
	names := make([]string, 0, len(seeded))
	for name := range seeded {
		names = append(names, name)
	}
	sort.Strings(names)
	return jsonfile.WriteAtomic(filepath.Join(dir, adapterProfilesSeededFile), []byte(strings.Join(names, "\n")+"\n"), 0o644)
}

func readEmbeddedAdapterProfile(path string) ([]byte, error) {
	file, err := defaultAdapterProfilesFS.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimitedAdapterProfile(file)
}

func readLimitedAdapterProfile(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxAdapterProfileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxAdapterProfileBytes {
		return nil, fmt.Errorf("adapter profile exceeds 1 MB")
	}
	return data, nil
}

func readAdapterProfileFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimitedAdapterProfile(file)
}

// Load seeds the bundled default profiles into dir, then replaces the
// registry's profiles with the valid ones found there. Invalid or duplicate
// files are skipped with a log line.
func (r *Registry) Load(dir string) error {
	if err := seedDefaults(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	profiles := make(map[string]Profile)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		profile, err := readAdapterProfile(path)
		if err != nil {
			slog.Warn("adapter profile skipped", "file", entry.Name(), "err", err)
			continue
		}
		if _, exists := profiles[profile.Name]; exists {
			slog.Warn("adapter profile skipped", "file", entry.Name(), "reason", "duplicate profile name", "name", profile.Name)
			continue
		}
		profiles[profile.Name] = profile
	}
	r.setProfiles(profiles)
	return nil
}

func readAdapterProfile(path string) (Profile, error) {
	data, err := readAdapterProfileFile(path)
	if err != nil {
		return Profile{}, err
	}
	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return Profile{}, err
	}
	if err := ValidateProfile(profile); err != nil {
		return Profile{}, err
	}
	return cloneAdapterProfile(profile), nil
}

// Summaries lists the loaded profiles sorted by name.
func (r *Registry) Summaries() []Summary {
	profiles := r.snapshot()
	summaries := make([]Summary, 0, len(profiles))
	for _, profile := range profiles {
		typeAliases := maps.Clone(profile.TypeAliases)
		if typeAliases == nil {
			typeAliases = map[string]string{}
		}
		subprotocols := append([]string(nil), profile.Subprotocols...)
		if subprotocols == nil {
			subprotocols = []string{}
		}
		summaries = append(summaries, Summary{
			Name:         profile.Name,
			BaseAdapter:  profile.BaseAdapter,
			Subprotocols: subprotocols,
			TypeAliases:  typeAliases,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Name < summaries[j].Name
	})
	return summaries
}

// Profile returns a copy of the named profile.
func (r *Registry) Profile(name string) (Profile, bool) {
	if r == nil {
		return Profile{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.profiles[Normalize(name)]
	return cloneAdapterProfile(profile), ok
}

// Protocol returns the adapter for name: a loaded profile if one matches,
// otherwise a built-in adapter. An empty name selects the raw adapter.
func (r *Registry) Protocol(name string) (Protocol, error) {
	adapterName := Normalize(name)
	if adapterName == "" {
		adapterName = "raw"
	}
	if profile, ok := r.Profile(adapterName); ok {
		return newProfileAdapter(profile)
	}
	return newBuiltin(adapterName)
}

func (r *Registry) setProfiles(profiles map[string]Profile) {
	next := make(map[string]Profile, len(profiles))
	for name, profile := range profiles {
		next[Normalize(name)] = cloneAdapterProfile(profile)
	}
	r.mu.Lock()
	r.profiles = next
	r.mu.Unlock()
}

func (r *Registry) snapshot() map[string]Profile {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	profiles := make(map[string]Profile, len(r.profiles))
	for name, profile := range r.profiles {
		profiles[name] = cloneAdapterProfile(profile)
	}
	return profiles
}

func cloneAdapterProfile(profile Profile) Profile {
	profile.Subprotocols = append([]string(nil), profile.Subprotocols...)
	profile.TypeAliases = maps.Clone(profile.TypeAliases)
	return profile
}
