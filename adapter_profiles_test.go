package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nhooyr.io/websocket"
)

func TestAdapterTemplateStringSubstitutionEscapesJSONStringContent(t *testing.T) {
	rendered, err := renderAdapterJSONTemplate(`{"v":"${value}"}`, map[string]adapterTemplateValue{
		"value": {value: "quote \" slash \\ newline \n"},
	})
	if err != nil {
		t.Fatalf("renderAdapterJSONTemplate() error = %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(rendered, &got); err != nil {
		t.Fatalf("rendered JSON did not unmarshal: %v", err)
	}
	if got["v"] != "quote \" slash \\ newline \n" {
		t.Fatalf("v = %q, want escaped string round trip", got["v"])
	}
}

func TestAdapterTemplateRawJSONSubstitutionInsertsVerbatim(t *testing.T) {
	rendered, err := renderAdapterJSONTemplate(`{"e":${json}}`, map[string]adapterTemplateValue{
		"json": {value: `{"score":7}`, raw: true},
	})
	if err != nil {
		t.Fatalf("renderAdapterJSONTemplate() error = %v", err)
	}
	if string(rendered) != `{"e":{"score":7}}` {
		t.Fatalf("rendered = %s, want raw JSON inserted", rendered)
	}
}

func TestAdapterTemplateMissingVariableErrors(t *testing.T) {
	_, err := renderAdapterJSONTemplate(`{"v":"${missing}"}`, nil)
	if err == nil || !strings.Contains(err.Error(), `missing variable "missing"`) {
		t.Fatalf("error = %v, want missing variable", err)
	}
}

func TestAdapterTemplateRenderedOutputMustBeJSON(t *testing.T) {
	_, err := renderAdapterJSONTemplate(`{"v":${value}}`, map[string]adapterTemplateValue{
		"value": {value: "not-json"},
	})
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("error = %v, want invalid JSON", err)
	}
}

func TestProfileAdapterWrapDataBinaryPayloadUsesAlias(t *testing.T) {
	adapter := newTestProfileAdapter(t, map[string]string{
		"appsync.recovery.Recovery": "recovery",
	})
	payload := EncodedPayload{
		Data:     []byte{1, 2, 3},
		Kind:     websocket.MessageBinary,
		TypeName: "appsync.recovery.Recovery",
	}

	encoded, err := adapter.WrapData(payload, "sub-1", "/test")
	if err != nil {
		t.Fatalf("WrapData() error = %v", err)
	}

	env, inner := decodeProfileEnvelope(t, encoded.Data)
	if env.ID != "sub-1" || env.Type != "data" {
		t.Fatalf("outer = %#v, want data for sub-1", env)
	}
	if inner.T != "recovery" || inner.E != base64.StdEncoding.EncodeToString(payload.Data) {
		t.Fatalf("inner = %#v, want alias and base64 payload", inner)
	}
}

func TestProfileAdapterWrapDataBinaryPayloadFallsBackToFQN(t *testing.T) {
	adapter := newTestProfileAdapter(t, nil)
	payload := EncodedPayload{
		Data:     []byte{4, 5, 6},
		Kind:     websocket.MessageBinary,
		TypeName: "appsync.unknown.Event",
	}

	encoded, err := adapter.WrapData(payload, "sub-1", "/test")
	if err != nil {
		t.Fatalf("WrapData() error = %v", err)
	}

	_, inner := decodeProfileEnvelope(t, encoded.Data)
	if inner.T != "appsync.unknown.Event" {
		t.Fatalf("inner.t = %q, want FQN fallback", inner.T)
	}
}

func TestProfileAdapterWrapDataJSONPayloadUsesRawJSONValue(t *testing.T) {
	adapter := newTestProfileAdapter(t, map[string]string{
		"appsync.gameinfo.GameEventDto": "gameInfo",
	})
	payload := EncodedPayload{
		Kind:     websocket.MessageText,
		Value:    map[string]any{"score": float64(7)},
		TypeName: "appsync.gameinfo.GameEventDto",
	}

	encoded, err := adapter.WrapData(payload, "sub-1", "/test")
	if err != nil {
		t.Fatalf("WrapData() error = %v", err)
	}

	_, inner := decodeProfileEnvelope(t, encoded.Data)
	if inner.T != "gameInfo" {
		t.Fatalf("inner.t = %q, want gameInfo", inner.T)
	}
	e, ok := inner.E.(map[string]any)
	if !ok || e["score"].(float64) != 7 {
		t.Fatalf("inner.e = %#v, want raw JSON object", inner.E)
	}
}

func TestProfileAdapterSubprotocolsOverrideBase(t *testing.T) {
	adapter := newTestProfileAdapter(t, nil)
	got := adapter.Subprotocols()
	if len(got) != 1 || got[0] != "custom-protocol" {
		t.Fatalf("Subprotocols() = %v, want profile list", got)
	}
}

func TestProfileAdapterWrapDataExposesChannelVariable(t *testing.T) {
	profile := testAdapterProfile("custom-profile", nil)
	profile.Envelope.Outer = `{"id":"${sub_id}","channel":"${channel}","event":${inner_string}}`
	adapter, err := NewProfileAdapter(profile)
	if err != nil {
		t.Fatalf("NewProfileAdapter() error = %v", err)
	}

	encoded, err := adapter.WrapData(EncodedPayload{
		Data:     []byte{1},
		Kind:     websocket.MessageBinary,
		TypeName: "appsync.recovery.Recovery",
	}, "sub-1", `/odd"channel\name`)
	if err != nil {
		t.Fatalf("WrapData() error = %v", err)
	}

	var env struct {
		Channel string `json:"channel"`
	}
	if err := json.Unmarshal(encoded.Data, &env); err != nil {
		t.Fatalf("outer JSON error = %v", err)
	}
	if env.Channel != `/odd"channel\name` {
		t.Fatalf("channel = %q, want escaped channel value", env.Channel)
	}
}

func TestProfileAdapterWrapDataExposesChannelVariableInsideInnerTemplate(t *testing.T) {
	profile := testAdapterProfile("custom-profile", nil)
	profile.Envelope.InnerBinary = `{"t":"${alias}","channel":"${channel}","e":"${base64}"}`
	adapter, err := NewProfileAdapter(profile)
	if err != nil {
		t.Fatalf("NewProfileAdapter() error = %v", err)
	}

	encoded, err := adapter.WrapData(EncodedPayload{
		Data:     []byte{1},
		Kind:     websocket.MessageBinary,
		TypeName: "appsync.recovery.Recovery",
	}, "sub-1", `/inner"channel\name`)
	if err != nil {
		t.Fatalf("WrapData() error = %v", err)
	}

	_, inner := decodeProfileEnvelope(t, encoded.Data)
	if inner.Channel != `/inner"channel\name` {
		t.Fatalf("inner.channel = %q, want escaped channel value", inner.Channel)
	}
}

func TestProfileAdapterWrapDataCanInsertInnerObjectRaw(t *testing.T) {
	profile := testAdapterProfile("custom-profile", map[string]string{
		"appsync.gameinfo.GameEventDto": "gameInfo",
	})
	profile.Envelope.Outer = `{"id":"${sub_id}","event":${inner_object}}`
	adapter, err := NewProfileAdapter(profile)
	if err != nil {
		t.Fatalf("NewProfileAdapter() error = %v", err)
	}

	encoded, err := adapter.WrapData(EncodedPayload{
		Kind:     websocket.MessageText,
		Value:    map[string]any{"score": float64(7)},
		TypeName: "appsync.gameinfo.GameEventDto",
	}, "sub-1", "/test")
	if err != nil {
		t.Fatalf("WrapData() error = %v", err)
	}

	var env struct {
		Event struct {
			T string         `json:"t"`
			E map[string]any `json:"e"`
		} `json:"event"`
	}
	if err := json.Unmarshal(encoded.Data, &env); err != nil {
		t.Fatalf("outer JSON error = %v", err)
	}
	if env.Event.T != "gameInfo" || env.Event.E["score"].(float64) != 7 {
		t.Fatalf("event = %#v, want raw inner object", env.Event)
	}
}

func TestProfileAdapterWrapDataDoesNotEvaluateJSONVarsForBinaryPayload(t *testing.T) {
	adapter := newTestProfileAdapter(t, map[string]string{
		"appsync.recovery.Recovery": "recovery",
	})

	_, err := adapter.WrapData(EncodedPayload{
		Data:     []byte{0xff, 0xfe},
		Kind:     websocket.MessageBinary,
		Value:    func() {},
		TypeName: "appsync.recovery.Recovery",
	}, "sub-1", "/test")
	if err != nil {
		t.Fatalf("WrapData() error = %v, binary path should not marshal Value", err)
	}
}

func TestLoadAdapterProfilesRegistersValidFilesAndSkipsInvalidFiles(t *testing.T) {
	restore := preserveAdapterProfiles()
	defer restore()

	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldOutput)

	dir := t.TempDir()
	writeProfileFile(t, dir, "valid.json", testAdapterProfile("custom-profile", nil))
	writeProfileFile(t, dir, "bad-version.json", AdapterProfile{
		ManifestVersion: 2,
		Name:            "bad-version",
		BaseAdapter:     "appsync",
		Envelope:        testProfileEnvelope(),
	})
	writeProfileFile(t, dir, "missing-name.json", AdapterProfile{
		ManifestVersion: 1,
		BaseAdapter:     "appsync",
		Envelope:        testProfileEnvelope(),
	})
	writeProfileFile(t, dir, "collision.json", AdapterProfile{
		ManifestVersion: 1,
		Name:            "raw",
		BaseAdapter:     "appsync",
		Envelope:        testProfileEnvelope(),
	})

	if err := LoadAdapterProfiles(dir); err != nil {
		t.Fatalf("LoadAdapterProfiles() error = %v", err)
	}
	if _, err := NewProtocolAdapter("custom-profile"); err != nil {
		t.Fatalf("custom profile was not registered: %v", err)
	}
	for _, name := range []string{"bad-version", "missing-name"} {
		if _, ok := adapterProfile(name); ok {
			t.Fatalf("invalid profile %q was registered", name)
		}
	}
	if !strings.Contains(logs.String(), "bad-version.json skipped") ||
		!strings.Contains(logs.String(), "missing-name.json skipped") ||
		!strings.Contains(logs.String(), "collision.json skipped") {
		t.Fatalf("logs = %q, want warnings for invalid profiles", logs.String())
	}
}

func TestSeedDefaultAdapterProfilesCopiesEmbeddedDefaultOnce(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "appsync-draftea.json")

	if err := SeedDefaultAdapterProfiles(dir); err != nil {
		t.Fatalf("SeedDefaultAdapterProfiles() error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("seeded profile missing: %v", err)
	}
	want, err := defaultAdapterProfilesFS.ReadFile("defaults/adapter_profiles/appsync-draftea.json")
	if err != nil {
		t.Fatalf("ReadFile(default) error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("seeded default does not match embedded content")
	}

	edited := []byte(`{"edited":true}`)
	if err := os.WriteFile(target, edited, 0o644); err != nil {
		t.Fatalf("edit seeded profile: %v", err)
	}
	if err := SeedDefaultAdapterProfiles(dir); err != nil {
		t.Fatalf("second SeedDefaultAdapterProfiles() error = %v", err)
	}
	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("read edited profile: %v", err)
	}
	if !bytes.Equal(got, edited) {
		t.Fatalf("second seed overwrote edited profile")
	}
}

func TestSeedDefaultAdapterProfilesDoesNotReseedRenamedDefault(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "appsync-draftea.json")
	renamed := filepath.Join(dir, "my-draftea.json")

	if err := SeedDefaultAdapterProfiles(dir); err != nil {
		t.Fatalf("SeedDefaultAdapterProfiles() error = %v", err)
	}
	if err := os.Rename(target, renamed); err != nil {
		t.Fatalf("rename seeded profile: %v", err)
	}
	if err := SeedDefaultAdapterProfiles(dir); err != nil {
		t.Fatalf("second SeedDefaultAdapterProfiles() error = %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("default profile was re-seeded after rename, stat err=%v", err)
	}
	if _, err := os.Stat(renamed); err != nil {
		t.Fatalf("renamed profile missing: %v", err)
	}
}

func TestSeedDefaultAdapterProfilesTreatsExistingProfilesAsUserOwned(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "my-draftea.json", testAdapterProfile("my-draftea", nil))

	if err := SeedDefaultAdapterProfiles(dir); err != nil {
		t.Fatalf("SeedDefaultAdapterProfiles() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "appsync-draftea.json")); !os.IsNotExist(err) {
		t.Fatalf("default profile was seeded into a user-owned profile dir, stat err=%v", err)
	}
}

func TestReadAdapterProfileRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.json")
	data := bytes.Repeat([]byte(" "), int(maxAdapterProfileBytes)+1)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := readAdapterProfile(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds 1 MB") {
		t.Fatalf("readAdapterProfile() error = %v, want size cap error", err)
	}
}

func TestBundledDrafteaProfileDispatchesStringifiedRealtimeEnvelope(t *testing.T) {
	restore := preserveAdapterProfiles()
	defer restore()

	dir := t.TempDir()
	if err := LoadAdapterProfiles(dir); err != nil {
		t.Fatalf("LoadAdapterProfiles() error = %v", err)
	}
	protocol, err := NewProtocolAdapter("appsync-draftea")
	if err != nil {
		t.Fatalf("NewProtocolAdapter(appsync-draftea) error = %v", err)
	}
	hub := NewSocketHub(NewEventBus(), false, nil)
	client := &SocketClient{
		id:            "appsync-draftea-1",
		adapter:       "appsync-draftea",
		protocol:      protocol,
		control:       make(chan EncodedServerMessage, 1),
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		subscriptions: map[string]string{"/test": "sub-1"},
	}
	hub.addClient(client)
	hub.registry.Subscribe("/test", client.id)

	encodedPayload := EncodedPayload{
		Data:     []byte{1, 2, 3},
		Kind:     websocket.MessageBinary,
		TypeName: "appsync.recovery.Recovery",
	}
	result, err := dispatchRendered(hub, &SchemaRegistry{}, RenderedDispatch{
		Channel:        "/test",
		Adapter:        "appsync-draftea",
		TypeName:       "appsync.recovery.Recovery",
		Payload:        json.RawMessage(`{"recoveryId":"r1"}`),
		EncodedPayload: &encodedPayload,
	}, nil)
	if err != nil {
		t.Fatalf("dispatchRendered() error = %v", err)
	}
	if result.Delivered != 1 {
		t.Fatalf("delivered = %d, want 1; errors=%v", result.Delivered, result.Errors)
	}

	select {
	case msg := <-client.send:
		env, inner := decodeProfileEnvelope(t, msg.Data)
		if env.ID != "sub-1" || env.Type != "data" {
			t.Fatalf("outer = %#v, want Draftea data envelope", env)
		}
		if inner.T != "recovery" || inner.E != base64.StdEncoding.EncodeToString(encodedPayload.Data) {
			t.Fatalf("inner = %#v, want recovery base64 envelope", inner)
		}
	default:
		t.Fatalf("client did not receive dispatch frame")
	}
}

func TestSocketAdapterProfilesEndpointListsLoadedProfiles(t *testing.T) {
	restore := preserveAdapterProfiles()
	defer restore()
	setAdapterProfiles(map[string]AdapterProfile{
		"custom-profile": testAdapterProfile("custom-profile", map[string]string{
			"appsync.recovery.Recovery": "recovery",
		}),
	})

	mux := http.NewServeMux()
	RegisterSocketRoutes(mux, NewSocketHub(NewEventBus(), false, nil))
	req := httptest.NewRequest(http.MethodGet, "/__ditto__/api/socket/adapter-profiles", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got []AdapterProfileSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response JSON error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "custom-profile" || got[0].TypeAliases["appsync.recovery.Recovery"] != "recovery" {
		t.Fatalf("profiles response = %#v", got)
	}
}

func newTestProfileAdapter(t *testing.T, aliases map[string]string) ProfileAdapter {
	t.Helper()
	adapter, err := NewProfileAdapter(testAdapterProfile("custom-profile", aliases))
	if err != nil {
		t.Fatalf("NewProfileAdapter() error = %v", err)
	}
	return adapter
}

func testAdapterProfile(name string, aliases map[string]string) AdapterProfile {
	return AdapterProfile{
		ManifestVersion: 1,
		Name:            name,
		BaseAdapter:     "appsync",
		Subprotocols:    []string{"custom-protocol"},
		Envelope:        testProfileEnvelope(),
		TypeAliases:     aliases,
	}
}

func testProfileEnvelope() AdapterProfileEnvelope {
	return AdapterProfileEnvelope{
		Outer:       `{"id":"${sub_id}","type":"data","event":${inner_string}}`,
		InnerBinary: `{"t":"${alias}","e":"${base64}"}`,
		InnerJSON:   `{"t":"${alias}","e":${json}}`,
	}
}

func decodeProfileEnvelope(t *testing.T, data []byte) (struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Event string `json:"event"`
}, struct {
	T       string `json:"t"`
	Channel string `json:"channel,omitempty"`
	E       any    `json:"e"`
}) {
	t.Helper()
	var env struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Event string `json:"event"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("outer JSON error: %v; data=%s", err, data)
	}
	var inner struct {
		T       string `json:"t"`
		Channel string `json:"channel,omitempty"`
		E       any    `json:"e"`
	}
	if err := json.Unmarshal([]byte(env.Event), &inner); err != nil {
		t.Fatalf("inner JSON error: %v; event=%s", err, env.Event)
	}
	return env, inner
}

func writeProfileFile(t *testing.T, dir, name string, profile AdapterProfile) {
	t.Helper()
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("Marshal(%s) error = %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}

func preserveAdapterProfiles() func() {
	// Adapter profile tests mutate a package-global registry; keep them serial.
	snapshot := snapshotAdapterProfiles()
	return func() {
		setAdapterProfiles(snapshot)
	}
}
