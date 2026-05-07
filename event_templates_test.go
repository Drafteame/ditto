package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestEventTemplateRegistryLoadSkipsMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewEventTemplateRegistry(dir)
	if err != nil {
		t.Fatalf("NewEventTemplateRegistry() error = %v", err)
	}
	created, err := reg.Create(EventTemplate{
		Name:    "Ticket Created",
		Channel: "tickets",
		Adapter: "raw",
		Payload: json.RawMessage(`{"ticketId":"{{ticketId}}"}`),
	}, nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"name":`), 0o644); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewEventTemplateRegistry(dir)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	templates := reloaded.Templates()
	if len(templates) != 1 || templates[0].ID != created.ID {
		t.Fatalf("Templates() = %#v, want only %s", templates, created.ID)
	}
}

func TestEventTemplateRegistryCRUDAndCollisionIDs(t *testing.T) {
	reg, err := NewEventTemplateRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewEventTemplateRegistry() error = %v", err)
	}
	first, err := reg.Create(EventTemplate{Name: "Ticket Created", Channel: " tickets ", Payload: json.RawMessage(`{"id":1}`)}, nil)
	if err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("Version = %d, want 1", first.Version)
	}
	second, err := reg.Create(EventTemplate{Name: "Ticket Created", Channel: "tickets", Payload: json.RawMessage(`{"id":2}`)}, nil)
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("collision reused id %q", first.ID)
	}
	if !strings.HasPrefix(first.ID, "ticket-created-") || !strings.HasPrefix(second.ID, "ticket-created-") {
		t.Fatalf("ids = %q %q, want slug-derived ids", first.ID, second.ID)
	}

	updated, err := reg.Update(first.ID, EventTemplate{Name: "Ticket Updated", Channel: "updates", Payload: json.RawMessage(`{"ok":true}`)}, nil)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.ID != first.ID || updated.CreatedAt.IsZero() || !updated.UpdatedAt.After(first.CreatedAt) {
		t.Fatalf("updated template timestamps/id = %#v", updated)
	}
	if err := reg.Delete(second.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := reg.Get(second.ID); err == nil {
		t.Fatalf("Get(deleted) unexpectedly succeeded")
	}
}

func TestEventTemplateRegistryCreateAvoidsOrphanedFileCollision(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewEventTemplateRegistry(dir)
	if err != nil {
		t.Fatalf("NewEventTemplateRegistry() error = %v", err)
	}
	orphanID := eventTemplateID("Ticket Created", 0)
	if err := os.WriteFile(filepath.Join(dir, orphanID+".json"), []byte(`{"stale":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	created, err := reg.Create(EventTemplate{Name: "Ticket Created", Channel: "tickets", Payload: json.RawMessage(`{"id":"x"}`)}, nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == orphanID {
		t.Fatalf("Create() reused orphaned file id %q", orphanID)
	}
	data, err := os.ReadFile(filepath.Join(dir, orphanID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"stale":true}` {
		t.Fatalf("orphaned file was overwritten: %s", data)
	}
}

func TestEventTemplateRegistryGetReturnsSnapshot(t *testing.T) {
	defaultValue := "fallback"
	reg, err := NewEventTemplateRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewEventTemplateRegistry() error = %v", err)
	}
	tmpl, err := reg.Create(EventTemplate{
		Name:    "Snapshot",
		Channel: "tickets",
		Payload: json.RawMessage(`{"id":"{{ticketId}}"}`),
		Variables: []EventTemplateVariable{{
			Name:    "ticketId",
			Default: &defaultValue,
		}},
	}, nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := reg.Get(tmpl.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Variables[0].Default == nil {
		t.Fatalf("default missing in snapshot: %#v", got.Variables)
	}
	got.Payload[0] = '['
	*got.Variables[0].Default = "mutated"

	again, err := reg.Get(tmpl.ID)
	if err != nil {
		t.Fatalf("Get() again error = %v", err)
	}
	if string(again.Payload) != `{"id":"{{ticketId}}"}` || *again.Variables[0].Default != "fallback" {
		t.Fatalf("Get() did not return a snapshot: %#v payload=%s", again.Variables, again.Payload)
	}
}

func TestResolveTemplateValuesDefaultsBuiltinsAndMissing(t *testing.T) {
	payload := json.RawMessage(`{
		"id": "{{ticketId}}",
		"typedId": "{{int:ticketId}}",
		"message": "hello {{name}}",
		"empty": "{{empty}}",
		"createdAt": "{{now}}",
		"createdMs": "{{now_unix_ms}}",
		"key {{name}}": "not in key",
		"nested": [{"flag": "{{bool:flag}}", "obj": "{{json:obj}}", "unresolved": "{{missing}}"}],
		"noRecursive": "{{chain}}"
	}`)
	emptyDefault := ""
	resolved, missing, err := resolveTemplate(payload, map[string]string{
		"ticketId": "123",
		"name":     `Ada "quoted"`,
		"flag":     "true",
		"obj":      `{"score":7}`,
		"chain":    "{{other}}",
	}, map[string]*string{
		"empty": &emptyDefault,
	})
	if err != nil {
		t.Fatalf("resolveTemplate() error = %v", err)
	}
	if len(missing) != 1 || missing[0] != "missing" {
		t.Fatalf("missing = %#v, want [missing]", missing)
	}

	var got map[string]any
	if err := json.Unmarshal(resolved, &got); err != nil {
		t.Fatalf("resolved payload invalid JSON: %v", err)
	}
	if got["id"] != "123" {
		t.Fatalf("id = %#v, want string 123", got["id"])
	}
	if got["typedId"].(float64) != 123 {
		t.Fatalf("typedId = %#v, want typed number 123", got["typedId"])
	}
	if got["message"] != `hello Ada "quoted"` {
		t.Fatalf("message = %#v", got["message"])
	}
	if got["empty"] != "" {
		t.Fatalf("empty default = %#v, want empty string", got["empty"])
	}
	if _, ok := got["key {{name}}"]; !ok {
		t.Fatalf("template variables in keys should not be resolved: %#v", got)
	}
	if got["noRecursive"] != "{{other}}" {
		t.Fatalf("noRecursive = %#v, want one-pass value", got["noRecursive"])
	}
	nested := got["nested"].([]any)[0].(map[string]any)
	if nested["flag"] != true {
		t.Fatalf("flag = %#v, want typed bool", nested["flag"])
	}
	if nested["obj"].(map[string]any)["score"].(float64) != 7 {
		t.Fatalf("obj = %#v, want typed object", nested["obj"])
	}
	if got["createdAt"] == "" || got["createdMs"] == "" {
		t.Fatalf("builtins not resolved: %#v", got)
	}
}

func TestResolveTemplateRejectsInvalidJSONAndDeepPayload(t *testing.T) {
	if _, _, err := ResolveTemplate(json.RawMessage(`{"x":`), nil); err == nil {
		t.Fatalf("ResolveTemplate(invalid JSON) unexpectedly succeeded")
	}

	value := `"{{x}}"`
	for i := 0; i < maxTemplateResolveDepth+2; i++ {
		value = `{"x":` + value + `}`
	}
	if _, _, err := ResolveTemplate(json.RawMessage(value), map[string]string{"x": "1"}); err == nil {
		t.Fatalf("ResolveTemplate(deep payload) unexpectedly succeeded")
	}
}

func TestResolveTemplateReportsInvalidCasts(t *testing.T) {
	payload := json.RawMessage(`{"age":"{{int:age}}","future":"{{date:dob}}"}`)
	resolved, missing, invalid, err := resolveTemplateDetailed(payload, map[string]string{"age": "abc", "dob": "2026-05-07"}, nil)
	if err != nil {
		t.Fatalf("resolveTemplateDetailed() error = %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %#v, want none", missing)
	}
	if len(invalid) != 2 || invalid[0].Name != "age" || invalid[0].Kind != "int" || invalid[1].Kind != "date" {
		t.Fatalf("invalid casts = %#v", invalid)
	}
	if !strings.Contains(string(resolved), "{{int:age}}") || !strings.Contains(string(resolved), "{{date:dob}}") {
		t.Fatalf("resolved = %s, want invalid cast placeholders preserved", resolved)
	}
}

func TestValidateTemplateRejectsUnsupportedAndInlineCasts(t *testing.T) {
	for name, payload := range map[string]json.RawMessage{
		"unsupported": json.RawMessage(`{"dob":"{{date:dob}}"}`),
		"inline":      json.RawMessage(`{"label":"age {{int:age}}"}`),
	} {
		t.Run(name, func(t *testing.T) {
			err := validateEventTemplate(EventTemplate{
				Version: 1,
				Name:    name,
				Channel: "tickets",
				Payload: payload,
			}, nil)
			if err == nil {
				t.Fatalf("validateEventTemplate() unexpectedly succeeded")
			}
		})
	}
}

func TestEventTemplateRenderKeepsPlainPlaceholderStringForProtobuf(t *testing.T) {
	schemaRoot := t.TempDir()
	writeProto(t, filepath.Join(schemaRoot, "events"), "event.proto", `syntax = "proto3"; package ditto.events; message Ticket { string ticket_id = 1; int32 attempt = 2; }`)
	schemas, err := NewSchemaRegistry(schemaRoot)
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}
	reg, err := NewEventTemplateRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewEventTemplateRegistry() error = %v", err)
	}
	tmpl, err := reg.Create(EventTemplate{
		Name:     "Ticket",
		Channel:  "tickets",
		TypeName: "ditto.events.Ticket",
		Payload:  json.RawMessage(`{"ticketId":"{{ticketId}}","attempt":"{{int:attempt}}"}`),
	}, schemas)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	rendered, err := reg.Render(tmpl.ID, map[string]string{"ticketId": "123", "attempt": "2"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if _, err := schemas.Encode(rendered.TypeName, rendered.Payload); err != nil {
		t.Fatalf("Encode() error = %v; payload=%s", err, rendered.Payload)
	}
	var got map[string]any
	if err := json.Unmarshal(rendered.Payload, &got); err != nil {
		t.Fatalf("rendered payload invalid JSON: %v", err)
	}
	if got["ticketId"] != "123" || got["attempt"].(float64) != 2 {
		t.Fatalf("rendered payload = %#v, want string ticketId and typed attempt", got)
	}
}

func TestEventTemplateRoutesRejectInvalidPayloadAndPathTraversal(t *testing.T) {
	reg, err := NewEventTemplateRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewEventTemplateRegistry() error = %v", err)
	}
	mux := http.NewServeMux()
	RegisterEventTemplateRoutes(mux, reg, NewSocketHub(NewEventBus(), false), nil)

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
	schemas, err := NewSchemaRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}
	reg, err := NewEventTemplateRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewEventTemplateRegistry() error = %v", err)
	}
	mux := http.NewServeMux()
	RegisterEventTemplateRoutes(mux, reg, NewSocketHub(NewEventBus(), false), schemas)

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
	schemas, err := NewSchemaRegistry(schemaRoot)
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}
	reg, err := NewEventTemplateRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewEventTemplateRegistry() error = %v", err)
	}
	tmpl, err := reg.Create(EventTemplate{
		Name:     "Ticket",
		Channel:  " tickets ",
		TypeName: "ditto.events.Ticket",
		Payload:  json.RawMessage(`{"ticketId":"{{ticketId}}"}`),
	}, schemas)
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
	RegisterEventTemplateRoutes(mux, reg, NewSocketHub(NewEventBus(), false), schemas)
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
	reg, err := NewEventTemplateRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewEventTemplateRegistry() error = %v", err)
	}
	missing, err := reg.Create(EventTemplate{
		Name:    "Missing",
		Channel: "tickets",
		Payload: json.RawMessage(`{"id":"{{ticketId}}"}`),
		Variables: []EventTemplateVariable{
			{Name: "ticketId"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Create(missing) error = %v", err)
	}
	builtins, err := reg.Create(EventTemplate{
		Name:    "Builtins",
		Channel: "tickets",
		Payload: json.RawMessage(`{"now":"{{now}}","uuid":"{{uuid}}"}`),
	}, nil)
	if err != nil {
		t.Fatalf("Create(builtins) error = %v", err)
	}
	mux := http.NewServeMux()
	RegisterEventTemplateRoutes(mux, reg, NewSocketHub(NewEventBus(), false), nil)

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
	reg, err := NewEventTemplateRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewEventTemplateRegistry() error = %v", err)
	}
	tmpl, err := reg.Create(EventTemplate{
		Name:    "Typed",
		Channel: "tickets",
		Payload: json.RawMessage(`{"age":"{{int:age}}","obj":"{{json:obj}}"}`),
	}, nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	mux := http.NewServeMux()
	RegisterEventTemplateRoutes(mux, reg, NewSocketHub(NewEventBus(), false), nil)

	req := httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates/"+tmpl.ID+"/dispatch", bytes.NewBufferString(`{"variables":{"age":"abc","obj":{"score":7}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_casts") || strings.Contains(rec.Body.String(), "missing_variables") {
		t.Fatalf("invalid cast status/body = %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/__ditto__/api/event-templates/"+tmpl.ID+"/dispatch", bytes.NewBufferString(`{"variables":{"age":42,"obj":{"score":7}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("typed JSON variables status/body = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"score":7`) || !strings.Contains(rec.Body.String(), `"age":42`) {
		t.Fatalf("typed JSON variables response = %s", rec.Body.String())
	}
}

func TestEventTemplateRegistryConcurrentUpdatesLastWriterWins(t *testing.T) {
	reg, err := NewEventTemplateRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewEventTemplateRegistry() error = %v", err)
	}
	tmpl, err := reg.Create(EventTemplate{Name: "Race", Channel: "tickets", Payload: json.RawMessage(`{"v":0}`)}, nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	var wg sync.WaitGroup
	for i := 1; i <= 32; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := reg.Update(tmpl.ID, EventTemplate{
				Name:    "Race",
				Channel: "tickets",
				Payload: json.RawMessage(`{"v":` + strconv.Itoa(i) + `}`),
			}, nil)
			if err != nil {
				t.Errorf("Update(%d) error = %v", i, err)
			}
		}()
	}
	wg.Wait()

	got, err := reg.Get(tmpl.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var parsed struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(got.Payload, &parsed); err != nil {
		t.Fatalf("final payload invalid JSON: %v", err)
	}
	if parsed.V < 1 || parsed.V > 32 {
		t.Fatalf("payload = %s, want one of the concurrent updates", got.Payload)
	}
}
