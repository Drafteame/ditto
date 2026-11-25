package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"ditto/internal/appdata"
	"ditto/internal/golden"
)

// TestRouteTableCharacterization pins method, path and origin handling for
// every route, so the move to Go 1.22 ServeMux patterns cannot silently change
// a status code or an error body.
func TestRouteTableCharacterization(t *testing.T) {
	paths := []string{
		"/__ditto__/",
		"/__ditto__/unknown",
		"/__ditto__/events",
		"/__ditto__/socket",
		"/__ditto__/ws",
		"/__ditto__/api/channel-modes",
		"/__ditto__/api/config/reset",
		"/__ditto__/api/event-templates",
		"/__ditto__/api/event-templates/missing",
		"/__ditto__/api/event-templates/missing/",
		"/__ditto__/api/event-templates/missing/dispatch",
		"/__ditto__/api/event-templates/..%2Ffoo",
		"/__ditto__/api/logs",
		"/__ditto__/api/logs/history",
		"/__ditto__/api/logs/missing",
		"/__ditto__/api/logs/a/b",
		"/__ditto__/api/mocks",
		"/__ditto__/api/mocks/reload",
		"/__ditto__/api/mocks/0",
		"/__ditto__/api/mocks/0/toggle",
		"/__ditto__/api/mocks/0/sequence/reset",
		"/__ditto__/api/mocks/abc",
		"/__ditto__/api/open-browser",
		"/__ditto__/api/open-url",
		"/__ditto__/api/port",
		"/__ditto__/api/port/check",
		"/__ditto__/api/qr",
		"/__ditto__/api/recordings",
		"/__ditto__/api/recordings/missing",
		"/__ditto__/api/recordings/missing/stop",
		"/__ditto__/api/recordings/missing/frames",
		"/__ditto__/api/schemas/packs",
		"/__ditto__/api/schemas/packs/missing",
		"/__ditto__/api/schemas/types",
		"/__ditto__/api/sequences",
		"/__ditto__/api/sequences/state",
		"/__ditto__/api/sequences/events",
		"/__ditto__/api/sequences/missing",
		"/__ditto__/api/sequences/missing/play",
		"/__ditto__/api/sequences/missing/seek",
		"/__ditto__/api/socket/adapter-profiles",
		"/__ditto__/api/socket/clients",
		"/__ditto__/api/socket/dispatch",
		"/__ditto__/api/socket/live-target",
		"/__ditto__/api/target",
		"/__ditto__/api/target/save",
		"/favicon.ico",
		"/some/app/path",
	}
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch}
	callers := []struct {
		name, remoteAddr, origin string
	}{
		{"loopback", "127.0.0.1:5555", ""},
		{"local-dev", "127.0.0.1:5555", "http://localhost:5173"},
		{"lan-cross", "192.168.1.50:5555", "http://evil.example"},
	}
	// POST open-browser launches a real browser, so it is not characterized.
	// /api/update-check calls GitHub and is left out for the same reason.
	skip := map[string]bool{
		"POST /__ditto__/api/open-browser": true,
	}

	var out strings.Builder
	for _, path := range paths {
		for _, method := range methods {
			if skip[method+" "+path] {
				continue
			}
			for _, caller := range callers {
				status, body := serveCharacterizationRequest(t, method, path, caller.remoteAddr, caller.origin)
				fmt.Fprintf(&out, "%-6s %-48s %-9s %d", method, path, caller.name, status)
				if status >= 400 {
					fmt.Fprintf(&out, " %q", body)
				}
				out.WriteByte('\n')
			}
		}
	}
	golden.Assert(t, "routes.golden", []byte(out.String()))
}

// tempPathPattern matches the per-test data directory that leaks into some
// filesystem error messages.
var tempPathPattern = regexp.MustCompile(`/\S*TestRouteTableCharacterization\S*`)

// serveCharacterizationRequest runs one empty-body request against a fresh
// server, wired like main.go, so that no request can change the outcome of
// another. The request context is already cancelled so SSE endpoints return
// after their headers.
func serveCharacterizationRequest(t *testing.T, method, path, remoteAddr, origin string) (int, string) {
	t.Helper()
	layout, err := appdata.Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{MocksDir: layout.MocksDir, Layout: layout, UI: testDashboard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, method, path, nil)
	req.RemoteAddr = remoteAddr
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	srv.Mux.ServeHTTP(rec, req)

	body := tempPathPattern.ReplaceAllString(strings.TrimSpace(rec.Body.String()), "<tmp>")
	if len(body) > 80 {
		body = body[:80]
	}
	return rec.Code, body
}

// testDashboard stands in for the built dashboard: the route table only
// records status codes for static paths.
var testDashboard = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Ditto</title>")}}
