package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventSequenceRegistryCRUDAndValidation(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}

	seq, err := reg.Create(EventSequence{
		Name:  " Ticket Flow ",
		OnEnd: "stay",
		Steps: []EventSequenceStep{{
			DelayMs: 0,
			Channel: "tickets",
			Payload: json.RawMessage(`{"id":"{{ticketId}}"}`),
		}},
		Vars: map[string]json.RawMessage{"ticketId": json.RawMessage(`"T-1"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(seq.ID, "ticket-flow-") {
		t.Fatalf("expected slug-hash id, got %q", seq.ID)
	}
	if seq.Steps[0].ID == "" {
		t.Fatal("expected generated step id")
	}

	got, err := reg.Get(seq.ID)
	if err != nil {
		t.Fatal(err)
	}
	got.Steps[0].DelayMs = -1
	if _, err := reg.Update(seq.ID, got); err == nil {
		t.Fatal("expected negative delay validation error")
	}

	got.Steps[0].DelayMs = 0
	got.OnEnd = "explode"
	if _, err := reg.Update(seq.ID, got); err == nil {
		t.Fatal("expected on_end validation error")
	}

	got.OnEnd = "stay"
	got.Steps[0].Channel = "/games/{{matchId}}"
	if _, err := reg.Update(seq.ID, got); err == nil {
		t.Fatal("expected channel templating validation error")
	}
}

func TestEventSequenceVarsAcceptTypedJSONValues(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(EventSequence{
		Name:  "Typed Vars",
		OnEnd: "stay",
		Vars: map[string]json.RawMessage{
			"matchId": json.RawMessage(`12345`),
		},
		Steps: []EventSequenceStep{{
			DelayMs: 0,
			Channel: "matches",
			Payload: json.RawMessage(`{"matchId":"{{int:matchId}}"}`),
			VarsOverride: map[string]json.RawMessage{
				"flag": json.RawMessage(`true`),
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := reg.ResolveStep(seq, seq.Steps[0], nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(rendered.Payload) != `{"matchId":12345}` {
		t.Fatalf("unexpected payload: %s", rendered.Payload)
	}
}

func TestRegisterSequenceRoutesRejectsDeleteWhileActive(t *testing.T) {
	dir := t.TempDir()
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
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
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), NewPlayerBroadcaster())
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
	templates, err := NewEventTemplateRegistry(filepath.Join(dir, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewEventSequenceRegistry(filepath.Join(dir, "sequences"), templates, nil)
	if err != nil {
		t.Fatal(err)
	}
	player := NewSequencePlayer(reg, templates, nil, NewSocketHub(NewEventBus(), false), NewPlayerBroadcaster())
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
