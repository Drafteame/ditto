package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestMatchAndResolveSequenceProxyFallsThroughAfterLastStep(t *testing.T) {
	store := &MockStore{
		mocks: []Mock{
			{
				Method:       "GET",
				Path:         "/users",
				Status:       200,
				Enabled:      true,
				ResponseMode: "sequence",
				Sequence: &Sequence{
					OnEnd: "proxy",
					Steps: []SequenceStep{
						{Status: 201, Body: json.RawMessage(`{"mocked":true}`)},
					},
				},
			},
		},
	}

	req := httptest.NewRequest("GET", "/users", nil)

	first := store.MatchAndResolve(req, nil)
	if first == nil {
		t.Fatal("expected first sequence step to resolve")
	}
	if got := first.Status; got != 201 {
		t.Fatalf("expected status 201 on first call, got %d", got)
	}

	second := store.MatchAndResolve(req, nil)
	if second != nil {
		t.Fatal("expected exhausted proxy sequence to fall through")
	}

	if got := store.mocks[0].Sequence.CurrentStep; got != 1 {
		t.Fatalf("expected current step to stay exhausted at 1, got %d", got)
	}
}
