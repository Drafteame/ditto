package jsonfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ditto/internal/jsonfile"
)

func TestSlugAndValidID(t *testing.T) {
	tests := []struct {
		in, slug string
	}{
		{"  Ticket Created!  ", "ticket-created"},
		{"a__b--c", "a-b-c"},
		{"Ñandú 2", "ñandú-2"},
		{"../etc", "etc"},
		{"***", ""},
	}
	for _, tt := range tests {
		if got := jsonfile.Slug(tt.in); got != tt.slug {
			t.Errorf("jsonfile.Slug(%q) = %q, want %q", tt.in, got, tt.slug)
		}
	}
	for id, want := range map[string]bool{"ticket-1": true, "Ticket": false, "a/b": false, "..": false, "": false} {
		if got := jsonfile.ValidID(id); got != want {
			t.Errorf("jsonfile.ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestHashedIDTruncatesAndFallsBack(t *testing.T) {
	long := jsonfile.HashedID(strings.Repeat("ab ", 40), "x", "in")
	base := long[:strings.LastIndex(long, "-")]
	if len([]rune(base)) > 48 || strings.HasSuffix(base, "-") {
		t.Errorf("hashedID base = %q, want at most 48 runes without a trailing dash", base)
	}
	if got := jsonfile.HashedID("!!!", "event-template", "in"); !strings.HasPrefix(got, "event-template-") || len(got) != len("event-template-")+8 {
		t.Errorf("hashedID fallback = %q", got)
	}
	if jsonfile.HashedID("n", "f", jsonfile.NameAttempt("n", 0)) == jsonfile.HashedID("n", "f", jsonfile.NameAttempt("n", 1)) {
		t.Error("attempts must produce different IDs")
	}
}

func TestWriteJSONFileRespectsOverwriteAndEndsWithNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "item.json")
	if err := jsonfile.Write(path, map[string]int{"v": 1}, false); err != nil {
		t.Fatal(err)
	}
	if err := jsonfile.Write(path, map[string]int{"v": 2}, false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second write without overwrite: err = %v, want os.ErrExist", err)
	}
	if err := jsonfile.Write(path, map[string]int{"v": 3}, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  \"v\": 3\n}\n" {
		t.Fatalf("file = %q", data)
	}
}
