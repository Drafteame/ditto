package main

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The proxy must hand the log stream (and therefore "Save as mock") readable
// text, not the raw compressed bytes it streams to the client.
func TestProxyAsksUpstreamForIdentityEncoding(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hola":"mundo"}`))
	}))
	defer upstream.Close()

	pm := NewProxyManager(upstream.URL)
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder(), statusCode: 200}
	req := httptest.NewRequest("GET", "/api/thing", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	pm.ServeHTTP(capture, req)

	if seen != "identity" {
		t.Errorf("upstream saw Accept-Encoding %q, want identity", seen)
	}
	if got := capture.decodedBody(); got != `{"hola":"mundo"}` {
		t.Errorf("decodedBody() = %q", got)
	}
}

func TestDecodedBodyDecompressesGzip(t *testing.T) {
	rec := httptest.NewRecorder()
	capture := &responseCapture{ResponseWriter: rec, statusCode: 200}
	capture.Header().Set("Content-Type", "application/json")
	capture.Header().Set("Content-Encoding", "gzip")

	gz := gzip.NewWriter(capture)
	gz.Write([]byte(`{"hola":"mundo","n":123}`))
	gz.Close()

	if got := capture.decodedBody(); got != `{"hola":"mundo","n":123}` {
		t.Errorf("decodedBody() = %q, want the decompressed JSON", got)
	}
}

func TestDecodedBodySummarisesBinary(t *testing.T) {
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder(), statusCode: 200}
	capture.Header().Set("Content-Type", "image/png")
	capture.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe, 0xfd})

	got := capture.decodedBody()
	if !strings.HasPrefix(got, "<binary response:") {
		t.Errorf("decodedBody() = %q, want a binary summary", got)
	}
	if strings.ContainsRune(got, '�') {
		t.Errorf("decodedBody() leaked replacement characters: %q", got)
	}
}

func TestDecodedBodyPassesThroughPlainText(t *testing.T) {
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder(), statusCode: 200}
	capture.Header().Set("Content-Type", "application/json")
	capture.Write([]byte(`{"acentos":"ñ á é í ó ú"}`))

	if got := capture.decodedBody(); got != `{"acentos":"ñ á é í ó ú"}` {
		t.Errorf("decodedBody() = %q", got)
	}
}

// End-to-end: a gzip upstream (what the Draftea BFF sends a Flutter client)
// must reach the log stream — and therefore "Save as mock" — as clean JSON.
func TestProxiedGzipReachesLogAsCleanJSON(t *testing.T) {
	const payload = `[{"containerKey":"cat_top_recommendations:games","prediction":"↑ 8.5"}]`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			gz.Write([]byte(payload))
			gz.Close()
			return
		}
		w.Write([]byte(payload))
	}))
	defer upstream.Close()

	root := t.TempDir()
	layout, err := EnsureDataLayout(root)
	if err != nil {
		t.Fatalf("EnsureDataLayout: %v", err)
	}
	srv, err := NewServer(ServerConfig{
		Port:     0,
		Target:   upstream.URL,
		MocksDir: filepath.Join(root, "mocks"),
		Layout:   layout,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	events := srv.Bus.Subscribe()
	defer srv.Bus.Unsubscribe(events)

	req := httptest.NewRequest("GET", "/bff/osb/markets/top-recommendations", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	srv.Mux.ServeHTTP(httptest.NewRecorder(), req)

	select {
	case ev := <-events:
		if ev.Type != "PROXY" {
			t.Fatalf("got log type %q, want PROXY", ev.Type)
		}
		if ev.ResponseBody != payload {
			t.Errorf("ResponseBody = %q\nwant %q", ev.ResponseBody, payload)
		}
		var out any
		if err := json.Unmarshal([]byte(ev.ResponseBody), &out); err != nil {
			t.Errorf("logged body is not parseable JSON: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no log event published")
	}
}
