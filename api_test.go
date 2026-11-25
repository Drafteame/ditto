package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ditto/internal/adapter"
	"ditto/internal/channelmode"
	"ditto/internal/eventlog"
	"ditto/internal/eventtemplate"
	"ditto/internal/schema"
	"ditto/internal/socket"
)

func TestSocketAdapterProfilesEndpointListsLoadedProfiles(t *testing.T) {
	// A directory that already holds a profile is user-owned, so Load does
	// not seed the bundled defaults next to it.
	dir := t.TempDir()
	profile, err := json.Marshal(adapter.Profile{
		ManifestVersion: 1, Name: "custom-profile", BaseAdapter: "appsync",
		Envelope: adapter.Envelope{
			Outer:       `{"id":"${sub_id}","type":"data","event":${inner_string}}`,
			InnerBinary: `{"t":"${alias}","e":"${base64}"}`,
			InnerJSON:   `{"t":"${alias}","e":${json}}`,
		},
		TypeAliases: map[string]string{"appsync.recovery.Recovery": "recovery"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "custom-profile.json"), profile, 0o644); err != nil {
		t.Fatal(err)
	}
	adapters := &adapter.Registry{}
	if err := adapters.Load(dir); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	RegisterSocketRoutes(mux, socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil), Adapters: adapters}), adapters)
	req := httptest.NewRequest(http.MethodGet, "/__ditto__/api/socket/adapter-profiles", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got []adapter.Summary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response JSON error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "custom-profile" || got[0].TypeAliases["appsync.recovery.Recovery"] != "recovery" {
		t.Fatalf("profiles response = %#v", got)
	}
}

func TestChannelModeRouteDelete(t *testing.T) {
	reg, err := channelmode.NewRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("channelmode.NewRegistry() error = %v", err)
	}
	mux := http.NewServeMux()
	RegisterChannelModeRoutes(mux, reg)

	put := httptest.NewRequest(http.MethodPut, "/__ditto__/api/channel-modes", strings.NewReader(`{"channel":"/saved","mode":"mock"}`))
	put.Header.Set("Content-Type", "application/json")
	put.Header.Set("Origin", "http://example.com")
	putRec := httptest.NewRecorder()
	mux.ServeHTTP(putRec, put)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body %q", putRec.Code, putRec.Body.String())
	}

	del := httptest.NewRequest(http.MethodDelete, "/__ditto__/api/channel-modes?channel=%2Fsaved", nil)
	del.Header.Set("Origin", "http://example.com")
	delRec := httptest.NewRecorder()
	mux.ServeHTTP(delRec, del)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, body %q", delRec.Code, delRec.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/__ditto__/api/channel-modes", nil)
	get.Header.Set("Origin", "http://example.com")
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body %q", getRec.Code, getRec.Body.String())
	}
	var body struct {
		Channels []channelmode.Config `json:"channels"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET body invalid JSON: %v", err)
	}
	if len(body.Channels) != 0 {
		t.Fatalf("GET channels = %#v, want empty", body.Channels)
	}
}

func TestEventTemplateRoutesRejectInvalidPayloadAndPathTraversal(t *testing.T) {
	reg, err := eventtemplate.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("eventtemplate.NewStore() error = %v", err)
	}
	mux := http.NewServeMux()
	RegisterEventTemplateRoutes(mux, reg, socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}))

	req := httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates", bytes.NewBufferString(`{"name":"Bad","channel":"x","payload":`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid payload status = %d, want 400", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/__ditto__/api/event-templates/..%2Ffoo", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("path traversal status = %d, want 404", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates", bytes.NewBufferString(`{"name":"Bad","channel":"x\nx","payload":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "channel cannot contain newlines") {
		t.Fatalf("newline channel status/body = %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates", bytes.NewBufferString(`{"name":"Bad Channel Var","channel":"/games/{{matchId}}","payload":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "channel variables are not supported") {
		t.Fatalf("channel vars status/body = %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates", strings.NewReader(`{"name":"Too Big","channel":"x","payload":"`+strings.Repeat("x", maxEventTemplateBodyBytes)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", rec.Code)
	}
}

func TestEventTemplateRoutesValidateSchemaAndNotFound(t *testing.T) {
	schemas, err := schema.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("schema.NewRegistry() error = %v", err)
	}
	reg, err := eventtemplate.NewStore(t.TempDir(), schemas, nil)
	if err != nil {
		t.Fatalf("eventtemplate.NewStore() error = %v", err)
	}
	mux := http.NewServeMux()
	RegisterEventTemplateRoutes(mux, reg, socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil), Schemas: schemas}))

	req := httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates", bytes.NewBufferString(`{"name":"Bad Type","channel":"x","type_name":"ditto.Missing","payload":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `schema type "ditto.Missing" is not loaded`) {
		t.Fatalf("missing type status/body = %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPut, "/__ditto__/api/event-templates/missing-template", bytes.NewBufferString(`{"name":"Missing","channel":"x","payload":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing update status = %d, want 404", rec.Code)
	}
}

func TestEventTemplateDispatchWithDeletedSchemaFailsUsefully(t *testing.T) {
	schemaRoot := t.TempDir()
	packDir := writeProto(t, filepath.Join(schemaRoot, "events"), "event.proto", `syntax = "proto3"; package ditto.events; message Ticket { string ticket_id = 1; }`)
	schemas, err := schema.NewRegistry(schemaRoot)
	if err != nil {
		t.Fatalf("schema.NewRegistry() error = %v", err)
	}
	reg, err := eventtemplate.NewStore(t.TempDir(), schemas, nil)
	if err != nil {
		t.Fatalf("eventtemplate.NewStore() error = %v", err)
	}
	tmpl, err := reg.Create(eventtemplate.Template{
		Name:     "Ticket",
		Channel:  " tickets ",
		TypeName: "ditto.events.Ticket",
		Payload:  json.RawMessage(`{"ticketId":"{{ticketId}}"}`),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := os.RemoveAll(packDir); err != nil {
		t.Fatal(err)
	}
	if err := schemas.Load(); err != nil {
		t.Fatalf("schemas.Load() error = %v", err)
	}

	mux := http.NewServeMux()
	RegisterEventTemplateRoutes(mux, reg, socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil), Schemas: schemas}))
	req := httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates/"+tmpl.ID+"/dispatch", bytes.NewBufferString(`{"variables":{"ticketId":"abc"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `schema type "ditto.events.Ticket" is not loaded`) {
		t.Fatalf("dispatch status/body = %d %s, want useful missing schema error", rec.Code, rec.Body.String())
	}
	if _, err := reg.Get(tmpl.ID); err != nil {
		t.Fatalf("template should remain valid after dispatch failure: %v", err)
	}
}

func TestEventTemplateDispatchMissingVariablesAndBuiltins(t *testing.T) {
	reg, err := eventtemplate.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("eventtemplate.NewStore() error = %v", err)
	}
	missing, err := reg.Create(eventtemplate.Template{
		Name:    "Missing",
		Channel: "tickets",
		Payload: json.RawMessage(`{"id":"{{ticketId}}"}`),
		Variables: []eventtemplate.Variable{
			{Name: "ticketId"},
		},
	})
	if err != nil {
		t.Fatalf("Create(missing) error = %v", err)
	}
	builtins, err := reg.Create(eventtemplate.Template{
		Name:    "Builtins",
		Channel: "tickets",
		Payload: json.RawMessage(`{"now":"{{now}}","uuid":"{{uuid}}"}`),
	})
	if err != nil {
		t.Fatalf("Create(builtins) error = %v", err)
	}
	mux := http.NewServeMux()
	RegisterEventTemplateRoutes(mux, reg, socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}))

	req := httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates/"+missing.ID+"/dispatch", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "missing_variables") {
		t.Fatalf("missing variable status/body = %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates/"+builtins.ID+"/dispatch", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("builtins dispatch status/body = %d %s", rec.Code, rec.Body.String())
	}
}

func TestEventTemplateDispatchReportsInvalidCastsAndAcceptsJSONVariables(t *testing.T) {
	reg, err := eventtemplate.NewStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("eventtemplate.NewStore() error = %v", err)
	}
	tmpl, err := reg.Create(eventtemplate.Template{
		Name:    "Typed",
		Channel: "tickets",
		Payload: json.RawMessage(`{"age":"{{int:age}}","obj":"{{json:obj}}","maybe":"{{json:maybe}}"}`),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	mux := http.NewServeMux()
	bus := eventlog.NewBus(nil)
	hub := socket.NewHub(socket.Config{Bus: bus})
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)
	RegisterEventTemplateRoutes(mux, reg, hub)

	req := httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates/"+tmpl.ID+"/dispatch", bytes.NewBufferString(`{"variables":{"age":"abc","obj":{"score":7},"maybe":null}}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_casts") || strings.Contains(rec.Body.String(), "missing_variables") {
		t.Fatalf("invalid cast status/body = %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates/"+tmpl.ID+"/dispatch", bytes.NewBufferString(`{"variables":{"age":42,"obj":{"score":7},"maybe":null}}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("typed JSON variables status/body = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"score":7`) || !strings.Contains(rec.Body.String(), `"age":42`) || !strings.Contains(rec.Body.String(), `"maybe":null`) {
		t.Fatalf("typed JSON variables response = %s", rec.Body.String())
	}
	select {
	case event := <-events:
		if event.Source != "template:"+tmpl.ID {
			t.Fatalf("dispatch event source = %q, want template:%s", event.Source, tmpl.ID)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for template dispatch event")
	}
}

func TestLogSummaryAndDetailRoutes(t *testing.T) {
	bus := eventlog.NewBus(nil)
	bus.Publish(eventlog.Event{Type: "MOCK", Method: "GET", Path: "/item", ResponseBody: `{"ok":true}`})
	id := bus.Summaries()[0].ID
	mux := http.NewServeMux()
	RegisterUI(mux, nil, bus, nil, nil, ServerInfo{}, false)

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/__ditto__/api/logs", nil))
	var summaries []eventlog.Event
	if err := json.Unmarshal(list.Body.Bytes(), &summaries); err != nil {
		t.Fatal(err)
	}
	if list.Code != http.StatusOK || len(summaries) != 1 || summaries[0].ID != id || summaries[0].ResponseBody != "" {
		t.Fatalf("unexpected summary response: status=%d body=%s", list.Code, list.Body.String())
	}

	detail := httptest.NewRecorder()
	mux.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/__ditto__/api/logs/"+id, nil))
	var event eventlog.Event
	if err := json.Unmarshal(detail.Body.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if detail.Code != http.StatusOK || event.ID != id || event.ResponseBody != `{"ok":true}` {
		t.Fatalf("unexpected detail response: status=%d body=%s", detail.Code, detail.Body.String())
	}
}

func TestSSEStartsImmediatelyAndReplaysSummariesAfterReset(t *testing.T) {
	bus := eventlog.NewBus(nil)
	bus.Publish(eventlog.Event{Type: "PROXY", Method: "POST", Path: "/secret", RequestBody: "request secret", ResponseBody: "response secret",
		RequestPayload: &eventlog.Payload{SizeBytes: 7, CapturedBytes: 7, CaptureStatus: "captured", RawBase64: "c2VjcmV0"}})
	mux := http.NewServeMux()
	RegisterUI(mux, nil, bus, nil, nil, ServerInfo{}, false)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/__ditto__/events", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "old-session:99")
	writer := &cancelOnFlushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	started := time.Now()
	mux.ServeHTTP(writer, req)
	if time.Since(started) > time.Second || !writer.flushed || writer.Code != http.StatusOK {
		t.Fatalf("SSE did not flush headers immediately: elapsed=%s flushed=%t status=%d", time.Since(started), writer.flushed, writer.Code)
	}
	body := writer.Body.String()
	if !strings.Contains(body, `"gap_reason":"server_reset"`) || !strings.Contains(body, `"method":"POST"`) {
		t.Fatalf("reset replay missing gap or event: %s", body)
	}
	if strings.Contains(body, "request secret") || strings.Contains(body, "response secret") || strings.Contains(body, "c2VjcmV0") {
		t.Fatalf("SSE replay contained payload details: %s", body)
	}
	if strings.HasPrefix(body, "id:") {
		t.Fatalf("GAP marker incorrectly advanced SSE ID: %s", body)
	}
}

func TestRegisterSequenceRoutesRejectsDeleteWhileActive(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "Active",
		OnEnd: "stay",
		Steps: []EventSequenceStep{{
			DelayMs: 10_000,
			Channel: "tickets",
			Payload: json.RawMessage(`{"ok":true}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	player := NewSequencePlayer(SequencePlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: NewPlayerBroadcaster()})
	if _, err := player.Play(seq.ID, PlayOptions{Speed: 1, SpeedSet: true}); err != nil {
		t.Fatal(err)
	}
	defer player.Shutdown(t.Context())

	mux := http.NewServeMux()
	RegisterSequenceRoutes(mux, reg, player, NewPlayerBroadcaster())
	req := httptest.NewRequest(http.MethodDelete, "/__ditto__/api/sequences/"+seq.ID, nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterSequenceRoutesBodyLimitAndPathTraversal(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	player := NewSequencePlayer(SequencePlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: NewPlayerBroadcaster()})
	mux := http.NewServeMux()
	RegisterSequenceRoutes(mux, reg, player, NewPlayerBroadcaster())

	req := httptest.NewRequest(http.MethodGet, "/__ditto__/api/sequences/%2e%2e/nope", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently {
		t.Fatalf("expected safe rejection, got %d", rec.Code)
	}

	body := bytes.Repeat([]byte("x"), maxEventSequenceBodyBytes+1)
	req = httptest.NewRequest(http.MethodPost, "/__ditto__/api/sequences", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
}

func TestRegisterSequenceRoutesRejectsBodyWithoutContentType(t *testing.T) {
	reg := newTestSequenceRegistry(t)
	seq, err := reg.Create(EventSequence{
		Name:  "HTTP",
		OnEnd: "stay",
		Steps: []EventSequenceStep{{
			DelayMs: 0,
			Channel: "tickets",
			Payload: json.RawMessage(`{"ok":true}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	player := NewSequencePlayer(SequencePlayerConfig{Sequences: reg, Hub: socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)}), Broadcaster: NewPlayerBroadcaster()})
	mux := http.NewServeMux()
	RegisterSequenceRoutes(mux, reg, player, NewPlayerBroadcaster())

	req := httptest.NewRequest(http.MethodPost, "/__ditto__/api/sequences/"+seq.ID+"/seek", strings.NewReader(`{"step":0}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSocketAPIRejectsCrossOriginTextPlainDispatch(t *testing.T) {
	hub := socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)})
	mux := http.NewServeMux()
	RegisterSocketRoutes(mux, hub, nil)

	req := httptest.NewRequest(http.MethodPost, "/__ditto__/api/socket/dispatch", strings.NewReader(`{"channel":"/scores","payload":{}}`))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("dispatch status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

type cancelOnFlushRecorder struct {
	*httptest.ResponseRecorder
	cancel  context.CancelFunc
	flushed bool
}

func (w *cancelOnFlushRecorder) Flush() {
	w.ResponseRecorder.Flush()
	if !w.flushed {
		w.flushed = true
		w.cancel()
	}
}
