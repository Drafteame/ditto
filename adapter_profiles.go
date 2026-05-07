package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

//go:embed defaults/adapter_profiles/*.json
var defaultAdapterProfilesFS embed.FS

const adapterProfilesDirName = "adapter_profiles"

var adapterProfileNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

var builtinAdapterNames = map[string]struct{}{
	"raw":     {},
	"appsync": {},
}

type AdapterProfile struct {
	ManifestVersion int                    `json:"manifest_version"`
	Name            string                 `json:"name"`
	BaseAdapter     string                 `json:"base_adapter"`
	Subprotocols    []string               `json:"subprotocols,omitempty"`
	Envelope        AdapterProfileEnvelope `json:"envelope"`
	TypeAliases     map[string]string      `json:"type_aliases,omitempty"`
}

type AdapterProfileEnvelope struct {
	Outer       string `json:"outer"`
	InnerBinary string `json:"inner_binary"`
	InnerJSON   string `json:"inner_json"`
}

type AdapterProfileSummary struct {
	Name         string            `json:"name"`
	BaseAdapter  string            `json:"base_adapter"`
	Subprotocols []string          `json:"subprotocols"`
	TypeAliases  map[string]string `json:"type_aliases"`
}

type ProfileAdapter struct {
	profile AdapterProfile
	base    ProtocolAdapter
}

type adapterTemplateValue struct {
	value string
	raw   bool
}

var adapterProfileRegistry = struct {
	sync.RWMutex
	profiles map[string]AdapterProfile
}{profiles: make(map[string]AdapterProfile)}

func ValidateAdapterProfile(profile AdapterProfile) error {
	if profile.ManifestVersion != 1 {
		return fmt.Errorf("manifest_version must be 1")
	}
	name := normalizeAdapter(profile.Name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if name != profile.Name || !adapterProfileNamePattern.MatchString(name) {
		return fmt.Errorf("name must match [a-z0-9-]+")
	}
	if _, ok := builtinAdapterNames[name]; ok {
		return fmt.Errorf("name %q collides with a built-in adapter", name)
	}
	base := normalizeAdapter(profile.BaseAdapter)
	if _, ok := builtinAdapterNames[base]; !ok {
		return fmt.Errorf("base_adapter must be one of raw, appsync")
	}
	if profile.BaseAdapter != base {
		return fmt.Errorf("base_adapter must be lowercase")
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

func NewProfileAdapter(profile AdapterProfile) (ProfileAdapter, error) {
	if err := ValidateAdapterProfile(profile); err != nil {
		return ProfileAdapter{}, err
	}
	base, err := newBuiltinProtocolAdapter(profile.BaseAdapter)
	if err != nil {
		return ProfileAdapter{}, err
	}
	return ProfileAdapter{
		profile: cloneAdapterProfile(profile),
		base:    base,
	}, nil
}

func (a ProfileAdapter) ParseClientMessage(b []byte) (ClientMsg, error) {
	return a.base.ParseClientMessage(b)
}

func (a ProfileAdapter) EncodePayload(payload json.RawMessage) (EncodedPayload, error) {
	return a.base.EncodePayload(payload)
}

func (a ProfileAdapter) EncodeServerMessage(msg ServerMsg) (EncodedServerMessage, error) {
	return a.base.EncodeServerMessage(msg)
}

func (a ProfileAdapter) Heartbeat() (EncodedServerMessage, time.Duration) {
	return a.base.Heartbeat()
}

func (a ProfileAdapter) Subprotocols() []string {
	return append([]string(nil), a.profile.Subprotocols...)
}

func (a ProfileAdapter) WrapData(payload EncodedPayload, subID string) (EncodedServerMessage, error) {
	innerTemplate := a.profile.Envelope.InnerJSON
	innerVars, err := a.innerJSONVars(payload)
	if payload.Kind == websocket.MessageBinary {
		innerTemplate = a.profile.Envelope.InnerBinary
		innerVars, err = a.innerBinaryVars(payload)
	}
	if err != nil {
		return EncodedServerMessage{}, err
	}
	inner, err := renderAdapterJSONTemplate(innerTemplate, innerVars)
	if err != nil {
		return EncodedServerMessage{}, fmt.Errorf("render inner envelope: %w", err)
	}

	innerString, err := json.Marshal(string(inner))
	if err != nil {
		return EncodedServerMessage{}, err
	}
	outerVars := map[string]adapterTemplateValue{
		"sub_id":            {value: subID},
		"inner_string":      {value: string(innerString), raw: true},
		"inner_json":        {value: string(inner), raw: true},
		"inner_json_string": {value: string(inner)},
	}
	outer, err := renderAdapterJSONTemplate(a.profile.Envelope.Outer, outerVars)
	if err != nil {
		return EncodedServerMessage{}, fmt.Errorf("render outer envelope: %w", err)
	}
	return EncodedServerMessage{Data: outer, Kind: websocket.MessageText}, nil
}

func (a ProfileAdapter) innerBinaryVars(payload EncodedPayload) (map[string]adapterTemplateValue, error) {
	typeName := strings.TrimSpace(payload.TypeName)
	alias := typeName
	if mapped := a.profile.TypeAliases[typeName]; mapped != "" {
		alias = mapped
	}
	return map[string]adapterTemplateValue{
		"alias":     {value: alias},
		"type_name": {value: typeName},
		"base64":    {value: base64.StdEncoding.EncodeToString(payload.Data)},
	}, nil
}

func (a ProfileAdapter) innerJSONVars(payload EncodedPayload) (map[string]adapterTemplateValue, error) {
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
		"json":      {value: string(rawJSON), raw: true},
	}, nil
}

func payloadJSONValue(payload EncodedPayload) ([]byte, error) {
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

func SeedDefaultAdapterProfiles(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fs.WalkDir(defaultAdapterProfilesFS, "defaults/adapter_profiles", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		target := filepath.Join(dir, entry.Name())
		if _, err := os.Stat(target); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := defaultAdapterProfilesFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func LoadAdapterProfiles(dir string) error {
	if err := SeedDefaultAdapterProfiles(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	profiles := make(map[string]AdapterProfile)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		profile, err := readAdapterProfile(path)
		if err != nil {
			log.Printf("adapter profile %s skipped: %v", entry.Name(), err)
			continue
		}
		if _, exists := profiles[profile.Name]; exists {
			log.Printf("adapter profile %s skipped: duplicate profile name %q", entry.Name(), profile.Name)
			continue
		}
		profiles[profile.Name] = profile
	}
	setAdapterProfiles(profiles)
	return nil
}

func readAdapterProfile(path string) (AdapterProfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AdapterProfile{}, err
	}
	var profile AdapterProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return AdapterProfile{}, err
	}
	if err := ValidateAdapterProfile(profile); err != nil {
		return AdapterProfile{}, err
	}
	return cloneAdapterProfile(profile), nil
}

func AdapterProfileSummaries() []AdapterProfileSummary {
	profiles := snapshotAdapterProfiles()
	summaries := make([]AdapterProfileSummary, 0, len(profiles))
	for _, profile := range profiles {
		typeAliases := cloneStringMap(profile.TypeAliases)
		if typeAliases == nil {
			typeAliases = map[string]string{}
		}
		subprotocols := append([]string(nil), profile.Subprotocols...)
		if subprotocols == nil {
			subprotocols = []string{}
		}
		summaries = append(summaries, AdapterProfileSummary{
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

func setAdapterProfiles(profiles map[string]AdapterProfile) {
	next := make(map[string]AdapterProfile, len(profiles))
	for name, profile := range profiles {
		next[normalizeAdapter(name)] = cloneAdapterProfile(profile)
	}
	adapterProfileRegistry.Lock()
	adapterProfileRegistry.profiles = next
	adapterProfileRegistry.Unlock()
}

func snapshotAdapterProfiles() map[string]AdapterProfile {
	adapterProfileRegistry.RLock()
	defer adapterProfileRegistry.RUnlock()
	profiles := make(map[string]AdapterProfile, len(adapterProfileRegistry.profiles))
	for name, profile := range adapterProfileRegistry.profiles {
		profiles[name] = cloneAdapterProfile(profile)
	}
	return profiles
}

func adapterProfile(name string) (AdapterProfile, bool) {
	adapterProfileRegistry.RLock()
	defer adapterProfileRegistry.RUnlock()
	profile, ok := adapterProfileRegistry.profiles[normalizeAdapter(name)]
	return cloneAdapterProfile(profile), ok
}

func cloneAdapterProfile(profile AdapterProfile) AdapterProfile {
	profile.Subprotocols = append([]string(nil), profile.Subprotocols...)
	profile.TypeAliases = cloneStringMap(profile.TypeAliases)
	return profile
}
