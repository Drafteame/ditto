package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// localRequest is a body-less request from a loopback address, which the
// API's origin check accepts.
func localRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "127.0.0.1:5555"
	return req
}
