package sequence

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ditto/internal/eventtemplate"
)

func TestEventSequenceRegistryCRUDAndValidation(t *testing.T) {
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	seq, err := reg.Create(Sequence{
		Name:  " Ticket Flow ",
		OnEnd: "stay",
		Steps: []Step{{
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
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := reg.Create(Sequence{
		Name:  "Typed Vars",
		OnEnd: "stay",
		Vars: map[string]json.RawMessage{
			"matchId": json.RawMessage(`12345`),
		},
		Steps: []Step{{
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

func TestEventSequenceUpdatePreservesStepIDsWhenSent(t *testing.T) {
	reg := newTestSequenceRegistry(t)
	seq, err := reg.Create(Sequence{
		Name:  "IDs",
		OnEnd: "stay",
		Steps: []Step{{
			DelayMs: 0,
			Channel: "tickets",
			Payload: json.RawMessage(`{"ok":true}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stepID := seq.Steps[0].ID
	seq.Steps[0].Name = "Renamed"
	updated, err := reg.Update(seq.ID, seq)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Steps[0].ID != stepID {
		t.Fatalf("step id changed from %q to %q", stepID, updated.Steps[0].ID)
	}
}

func TestEventSequenceUpdateRegeneratesMissingStepIDs(t *testing.T) {
	reg := newTestSequenceRegistry(t)
	seq, err := reg.Create(Sequence{
		Name:  "Missing IDs",
		OnEnd: "stay",
		Steps: []Step{{
			DelayMs: 0,
			Channel: "tickets",
			Payload: json.RawMessage(`{"ok":true}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	seq.Steps[0].ID = ""
	updated, err := reg.Update(seq.ID, seq)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Steps[0].ID == "" {
		t.Fatal("expected regenerated step id")
	}
}

func TestEventSequenceRejectsTrimmedVariableKeyCollision(t *testing.T) {
	reg := newTestSequenceRegistry(t)
	_, err := reg.Create(Sequence{
		Name:  "Collision",
		OnEnd: "stay",
		Vars: map[string]json.RawMessage{
			"foo":   json.RawMessage(`"a"`),
			" foo ": json.RawMessage(`"b"`),
		},
		Steps: []Step{{
			DelayMs: 0,
			Channel: "tickets",
			Payload: json.RawMessage(`{"ok":true}`),
		}},
	})
	if err == nil {
		t.Fatal("expected trimmed variable key collision")
	}
}

func newTestSequenceRegistry(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
