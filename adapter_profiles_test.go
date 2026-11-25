package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log"
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

func TestAdapterRegistryLoadRegistersValidFilesAndSkipsInvalidFiles(t *testing.T) {
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

	adapters := &AdapterRegistry{}
	if err := adapters.Load(dir); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := adapters.Protocol("custom-profile"); err != nil {
		t.Fatalf("custom profile was not registered: %v", err)
	}
	for _, name := range []string{"bad-version", "missing-name"} {
		if _, ok := adapters.Profile(name); ok {
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

func TestAppSyncWrapsBinaryPayloadAsBase64(t *testing.T) {
	payload := EncodedPayload{
		Data:        []byte{8, 7},
		Kind:        websocket.MessageBinary,
		ContentType: "application/x-protobuf",
		TypeName:    "ditto.test.ScoreEvent",
	}
	msg, err := AppSyncAdapter{}.WrapData(payload, "sub-1", "/test")
	if err != nil {
		t.Fatalf("WrapData() error = %v", err)
	}
	var env struct {
		Payload struct {
			Data struct {
				Base64      string `json:"base64"`
				ContentType string `json:"content_type"`
				TypeName    string `json:"type_name"`
			} `json:"data"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		t.Fatalf("WrapData() JSON error = %v", err)
	}
	if env.Payload.Data.Base64 != base64.StdEncoding.EncodeToString([]byte{8, 7}) {
		t.Fatalf("base64 = %q", env.Payload.Data.Base64)
	}
	if env.Payload.Data.ContentType != "application/x-protobuf" || env.Payload.Data.TypeName != "ditto.test.ScoreEvent" {
		t.Fatalf("metadata = %#v", env.Payload.Data)
	}
}

func TestDecodeFrameReadsRelayEnvelope(t *testing.T) {
	adapters := &AdapterRegistry{}
	if err := adapters.Load(t.TempDir()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	frame := []byte(`{"type":"data","channel":"/osb/s/l/*","event":"{\"t\":\"betInfo\",\"e\":\"AQID\"}"}`)
	decoded, decodeErr := adapters.DecodeFrame(nil, "text", frame, "relay-draftea")
	if decodeErr != "" {
		t.Fatalf("DecodeFrame() error = %s", decodeErr)
	}
	if decoded == nil || decoded.Alias != "betInfo" {
		t.Fatalf("decoded = %#v, want alias betInfo", decoded)
	}
}

func TestAppSyncDecoderKeepsBuiltInAliasWhenSchemaMissing(t *testing.T) {
	rec, err := NewRecorder(RecorderConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	decoded, decodeErr := rec.decodeAppSyncProfile(AdapterProfile{BaseAdapter: "appsync"}, []byte(`{
		"type":"data",
		"payload":{"data":{"t":"example.Event","e":"AAAA"}}
	}`))
	if decodeErr != "" {
		t.Fatalf("decodeErr = %q, want empty fallback", decodeErr)
	}
	if decoded == nil || decoded.Alias != "example.Event" || decoded.TypeName != "" {
		t.Fatalf("decoded = %#v, want alias-only fallback", decoded)
	}
}

func TestAppSyncDecoderReportsMissingPayload(t *testing.T) {
	rec, err := NewRecorder(RecorderConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	decoded, decodeErr := rec.decodeAppSyncProfile(AdapterProfile{BaseAdapter: "appsync"}, []byte(`{
		"type":"data",
		"payload":{"data":{"t":"example.Event"}}
	}`))
	if decoded != nil || decodeErr != "appsync payload missing" {
		t.Fatalf("decoded=%#v decodeErr=%q, want missing payload error", decoded, decodeErr)
	}
}

func TestAppSyncDecoderTreatsControlFramesAsBenign(t *testing.T) {
	rec, err := NewRecorder(RecorderConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	// AppSync sends keep-alives and subscription-lifecycle frames that carry
	// no payload; they must not be reported as decode errors.
	for _, typ := range []string{"ka", "connection_ack", "subscribe", "subscribe_success", "complete"} {
		decoded, decodeErr := rec.decodeAppSyncProfile(
			AdapterProfile{BaseAdapter: "appsync"},
			[]byte(`{"type":"`+typ+`"}`),
		)
		if decodeErr != "" {
			t.Fatalf("type %q: decodeErr = %q, want empty (control frame)", typ, decodeErr)
		}
		if decoded == nil || decoded.Alias != typ || decoded.TypeName != "" || decoded.PayloadJSON != nil {
			t.Fatalf("type %q: decoded = %#v, want control frame labelled by type", typ, decoded)
		}
	}
}

func TestRawAdapterSubscribeAndDispatchPayload(t *testing.T) {
	adapter := RawAdapter{}

	msg, err := adapter.ParseClientMessage([]byte(`{"type":"subscribe","id":"sub-1","channel":"/scores"}`))
	if err != nil {
		t.Fatalf("ParseClientMessage() error = %v", err)
	}
	if msg.Type != "subscribe" || msg.Channel != "/scores" || msg.SubscriptionID != "sub-1" {
		t.Fatalf("ParseClientMessage() = %#v, want subscribe /scores sub-1", msg)
	}

	payload := json.RawMessage(`{"score":7}`)
	encoded, err := adapter.EncodeServerMessage(ServerMsg{Type: "data", Channel: "/scores", Payload: payload})
	if err != nil {
		t.Fatalf("EncodeServerMessage() error = %v", err)
	}
	if string(encoded.Data) != string(payload) {
		t.Fatalf("EncodeServerMessage() = %s, want %s", encoded.Data, payload)
	}
}

func TestAppSyncAdapterEnvelope(t *testing.T) {
	adapter := AppSyncAdapter{}

	msg, err := adapter.ParseClientMessage([]byte(`{"type":"start","id":"sub-1","payload":{"channel":"/scores"}}`))
	if err != nil {
		t.Fatalf("ParseClientMessage() error = %v", err)
	}
	if msg.Type != "subscribe" || msg.Channel != "/scores" || msg.SubscriptionID != "sub-1" {
		t.Fatalf("ParseClientMessage() = %#v, want subscribe /scores sub-1", msg)
	}

	encoded, err := adapter.EncodeServerMessage(ServerMsg{
		Type:    "data",
		ID:      "sub-1",
		Channel: "/scores",
		Payload: json.RawMessage(`{"score":7}`),
	})
	if err != nil {
		t.Fatalf("EncodeServerMessage() error = %v", err)
	}

	var env struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Payload struct {
			Data map[string]any `json:"data"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(encoded.Data, &env); err != nil {
		t.Fatalf("encoded AppSync message is invalid JSON: %v", err)
	}
	if env.Type != "data" || env.ID != "sub-1" || env.Payload.Data["score"].(float64) != 7 {
		t.Fatalf("encoded AppSync message = %s", encoded.Data)
	}
}

func TestRelayAdapterControlFrames(t *testing.T) {
	adapter := RelayAdapter{}

	if !adapter.GreetsOnConnect() {
		t.Fatalf("GreetsOnConnect() = false, want true")
	}
	if len(adapter.Subprotocols()) != 0 {
		t.Fatalf("Subprotocols() = %v, want none", adapter.Subprotocols())
	}

	msg, err := adapter.ParseClientMessage([]byte(`{"type":"subscribe","id":"/osb/s/l/*","channel":"/osb/s/l/*","authorization":{"Authorization":"Basic token"}}`))
	if err != nil {
		t.Fatalf("ParseClientMessage() error = %v", err)
	}
	if msg.Type != "subscribe" || msg.Channel != "/osb/s/l/*" || msg.SubscriptionID != "/osb/s/l/*" {
		t.Fatalf("ParseClientMessage() = %#v, want subscribe /osb/s/l/*", msg)
	}

	ack, err := adapter.EncodeServerMessage(ServerMsg{Type: "connection_ack"})
	if err != nil {
		t.Fatalf("EncodeServerMessage(connection_ack) error = %v", err)
	}
	if string(ack.Data) != `{"type":"connection_ack"}` {
		t.Fatalf("connection_ack = %s", ack.Data)
	}

	subAck, err := adapter.EncodeServerMessage(ServerMsg{Type: "subscribe_ack", ID: "/scores", Channel: "/scores"})
	if err != nil {
		t.Fatalf("EncodeServerMessage(subscribe_ack) error = %v", err)
	}
	var subEnv struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
	}
	if err := json.Unmarshal(subAck.Data, &subEnv); err != nil {
		t.Fatalf("subscribe ack is invalid JSON: %v", err)
	}
	if subEnv.Type != "subscribe_success" || subEnv.Channel != "/scores" {
		t.Fatalf("subscribe ack = %s, want subscribe_success with channel", subAck.Data)
	}

	data, err := adapter.EncodeServerMessage(ServerMsg{Type: "data", Channel: "/scores", Payload: json.RawMessage(`{"score":7}`)})
	if err != nil {
		t.Fatalf("EncodeServerMessage(data) error = %v", err)
	}
	var dataEnv struct {
		Type    string         `json:"type"`
		Channel string         `json:"channel"`
		Event   map[string]any `json:"event"`
	}
	if err := json.Unmarshal(data.Data, &dataEnv); err != nil {
		t.Fatalf("data frame is invalid JSON: %v", err)
	}
	if dataEnv.Type != "data" || dataEnv.Channel != "/scores" || dataEnv.Event["score"].(float64) != 7 {
		t.Fatalf("data frame = %s", data.Data)
	}
}

func TestAppSyncAdapterErrorPayloadUsesJSONString(t *testing.T) {
	adapter := AppSyncAdapter{}

	encoded, err := adapter.EncodeServerMessage(ServerMsg{
		Type:    "error",
		ID:      "sub-1",
		Payload: json.RawMessage(`"plain error"`),
	})
	if err != nil {
		t.Fatalf("EncodeServerMessage() error = %v", err)
	}

	var env struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Payload struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(encoded.Data, &env); err != nil {
		t.Fatalf("encoded AppSync error is invalid JSON: %v", err)
	}
	if env.Type != "error" || env.ID != "sub-1" || len(env.Payload.Errors) != 1 || env.Payload.Errors[0].Message != "plain error" {
		t.Fatalf("encoded AppSync error = %s", encoded.Data)
	}
}

func TestProtobufPayloadDescribesBinaryProtobuf(t *testing.T) {
	got := ProtobufPayload("ditto.test.Item", []byte{0x0a, 0x01, 'x'})
	if got.Kind != websocket.MessageBinary || got.ContentType != "application/x-protobuf" ||
		got.TypeName != "ditto.test.Item" || got.Value != "CgF4" {
		t.Fatalf("ProtobufPayload() = %#v", got)
	}
}
