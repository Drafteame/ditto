package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ditto/internal/eventtemplate"
	"ditto/internal/sequence"
)

// writeProto writes a .proto file into dir, creating it, and returns dir.
func writeProto(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func httpToWS(raw string) string {
	return "ws" + strings.TrimPrefix(raw, "http")
}

// newTestSequenceRegistry returns an empty sequence store backed by an empty
// template store in a temporary directory.
func newTestSequenceRegistry(t *testing.T) *sequence.Store {
	t.Helper()
	dir := t.TempDir()
	templates, err := eventtemplate.NewStore(filepath.Join(dir, "templates"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := sequence.NewStore(filepath.Join(dir, "sequences"), templates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
