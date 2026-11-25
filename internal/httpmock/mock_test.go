package httpmock

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMatchPathSupportsWildcardWithinSegment(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{
			name:    "exact",
			pattern: "/tickets-bff/tkt_123",
			path:    "/tickets-bff/tkt_123",
			want:    true,
		},
		{
			name:    "whole segment wildcard",
			pattern: "/tickets-bff/*",
			path:    "/tickets-bff/tkt_8WNAsd8wsRnf0xC0m",
			want:    true,
		},
		{
			name:    "prefix wildcard within segment",
			pattern: "/tickets-bff/tkt_*",
			path:    "/tickets-bff/tkt_8WNAsd8wsRnf0xC0m",
			want:    true,
		},
		{
			name:    "prefix wildcard does not match other prefix",
			pattern: "/tickets-bff/tkt_*",
			path:    "/tickets-bff/usr_8WNAsd8wsRnf0xC0m",
			want:    false,
		},
		{
			name:    "different segment count",
			pattern: "/tickets-bff/tkt_*",
			path:    "/tickets-bff/tkt_123/history",
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchPath(tc.pattern, tc.path); got != tc.want {
				t.Fatalf("matchPath(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

func TestResolvePrefersExactPathOverWildcardPath(t *testing.T) {
	store := &Store{
		mocks: []Mock{
			{
				Method:  "GET",
				Path:    "/tickets-bff/tkt_*",
				Status:  200,
				Enabled: true,
			},
			{
				Method:  "GET",
				Path:    "/tickets-bff/tkt_8WNAsd8wsRnf0xC0m",
				Status:  201,
				Enabled: true,
			},
		},
	}

	req := httptest.NewRequest("GET", "/tickets-bff/tkt_8WNAsd8wsRnf0xC0m", nil)
	resolved := store.Resolve(req, nil)
	if resolved == nil {
		t.Fatal("expected a matching mock")
	}
	if resolved.Status != 201 {
		t.Fatalf("expected the exact mock (201) to win, got status %d", resolved.Status)
	}
}

func TestResolveSequenceProxyFallsThroughAfterLastStep(t *testing.T) {
	store := &Store{
		mocks: []Mock{
			{
				Method:       "GET",
				Path:         "/users",
				Status:       200,
				Enabled:      true,
				ResponseMode: "sequence",
				Sequence: &Sequence{
					OnEnd: "proxy",
					Steps: []Step{
						{Status: 201, Body: json.RawMessage(`{"mocked":true}`)},
					},
				},
			},
		},
	}

	req := httptest.NewRequest("GET", "/users", nil)

	first := store.Resolve(req, nil)
	if first == nil {
		t.Fatal("expected first sequence step to resolve")
	}
	if got := first.Status; got != 201 {
		t.Fatalf("expected status 201 on first call, got %d", got)
	}

	second := store.Resolve(req, nil)
	if second != nil {
		t.Fatal("expected exhausted proxy sequence to fall through")
	}

	if got := store.mocks[0].Sequence.CurrentStep; got != 1 {
		t.Fatalf("expected current step to stay exhausted at 1, got %d", got)
	}
}

func TestStoreAddressesMocksByFileName(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Create(Mock{Method: "GET", Path: "/a", Source: "a.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Mock{Method: "GET", Path: "/b", Source: "b.json"}); err != nil {
		t.Fatal(err)
	}

	// Deleting the first mock must not shift the second one's ID.
	if err := store.Delete("a.json"); err != nil {
		t.Fatal(err)
	}
	if found, _ := store.Toggle("b.json"); !found {
		t.Fatal("b.json not found after deleting a.json")
	}
	if err := store.Delete("a.json"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: err = %v, want ErrNotFound", err)
	}

	if _, err := store.Update("b.json", Mock{Method: "GET", Path: "/b", Source: "renamed.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.json")); !os.IsNotExist(err) {
		t.Fatalf("rename left the old file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "renamed.json")); err != nil {
		t.Fatalf("rename did not write the new file: %v", err)
	}
}

func TestStoreRejectsUnsafeOrDuplicateSources(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Create(Mock{Method: "GET", Path: "/a", Source: "a.json"}); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"../escape.json", "sub/dir.json", `sub\dir.json`, "notes.txt", ".json", "a.json"} {
		if _, err := store.Create(Mock{Method: "GET", Path: "/x", Source: src}); err == nil {
			t.Errorf("Create with source %q succeeded", src)
		}
	}
	if _, err := store.Update("a.json", Mock{Method: "GET", Path: "/a", Source: "../escape.json"}); err == nil {
		t.Error("Update renaming to ../escape.json succeeded")
	}
}
