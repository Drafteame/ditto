package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
