package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"ditto/internal/capture"
	"ditto/internal/eventlog"
	"ditto/internal/httpmock"
	"ditto/internal/socket"
)

func newHTTPLogTestServer(t *testing.T, target string) *Server {
	t.Helper()
	layout, err := EnsureDataLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(ServerConfig{Port: 0, Target: target, MocksDir: layout.MocksDir, Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestHTTPLogCapturesBodiesForEveryMethodAndRequestForm(t *testing.T) {
	srv := newHTTPLogTestServer(t, "")
	cases := []struct {
		method, contentType, body string
		chunked                   bool
	}{
		{http.MethodPost, "application/json", `{"k":"v"}`, false},
		{http.MethodPatch, "text/plain", "plain body", false},
		{http.MethodPut, "application/x-www-form-urlencoded", "tag=one&tag=two", false},
		{http.MethodDelete, "application/json", `{"delete":true}`, false},
		{http.MethodGet, "text/plain", "get body", true},
		{http.MethodOptions, "application/json", `{"options":true}`, false},
	}
	for _, test := range cases {
		if _, err := srv.Store.Create(httpmock.Mock{Method: test.method, Path: "/capture", Status: http.StatusCreated,
			Body: json.RawMessage(`{"ok":true}`), Headers: map[string]string{"Content-Type": "application/json", "X-Reply": "mock"}}); err != nil {
			t.Fatal(err)
		}
	}

	for _, test := range cases {
		t.Run(test.method, func(t *testing.T) {
			ch := srv.Bus.Subscribe()
			defer srv.Bus.Unsubscribe(ch)
			req := httptest.NewRequest(test.method, "/capture?tag=one&tag=two", strings.NewReader(test.body))
			req.Host = "api.example.test"
			req.RemoteAddr = "192.0.2.4:5123"
			req.Header.Set("Content-Type", test.contentType)
			req.Header.Set("X-Client", "original")
			if test.chunked {
				req.ContentLength = -1
				req.TransferEncoding = []string{"chunked"}
			}
			rec := httptest.NewRecorder()
			srv.Mux.ServeHTTP(rec, req)
			event := <-ch
			if event.Type != "MOCK" || event.Method != test.method || event.Status != http.StatusCreated {
				t.Fatalf("unexpected event: %#v", event)
			}
			if event.RequestBody != test.body || event.RequestPayload.SizeBytes != int64(len(test.body)) || event.RequestPayload.CapturedBytes != int64(len(test.body)) {
				t.Fatalf("request capture mismatch: %#v", event.RequestPayload)
			}
			if event.URL != "http://api.example.test/capture?tag=one&tag=two" || event.Host != "api.example.test" || event.RemoteAddr != "192.0.2.4:5123" || event.Protocol != "HTTP/1.1" {
				t.Fatalf("request context mismatch: %#v", event)
			}
			if event.RequestHeaders["X-Client"][0] != "original" || event.ResponseHeaders["X-Reply"][0] != "mock" {
				t.Fatalf("headers missing: req=%v resp=%v", event.RequestHeaders, event.ResponseHeaders)
			}
			if test.method == http.MethodPut && !bytes.Equal([]byte(strings.Join(event.RequestFormFields["tag"], ",")), []byte("one,two")) {
				t.Fatalf("repeated form fields = %#v", event.RequestFormFields)
			}
		})
	}
}

func TestHTTPLogCapturesMultipartAndFileMetadata(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("name", "first")
	_ = writer.WriteField("name", "second")
	partHeader := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="upload"; filename="note.txt"`}, "Content-Type": {"text/plain"}}
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("file bytes"))
	_ = writer.Close()

	srv := newHTTPLogTestServer(t, "")
	_, err = srv.Store.Create(httpmock.Mock{Method: http.MethodPost, Path: "/upload", Status: http.StatusOK, Body: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	ch := srv.Bus.Subscribe()
	defer srv.Bus.Unsubscribe(ch)
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	srv.Mux.ServeHTTP(httptest.NewRecorder(), req)
	event := <-ch
	if strings.Join(event.RequestFormFields["name"], ",") != "first,second" {
		t.Fatalf("fields = %#v", event.RequestFormFields)
	}
	if len(event.RequestFiles) != 1 || event.RequestFiles[0].Name != "note.txt" || event.RequestFiles[0].SizeBytes != int64(len("file bytes")) || event.RequestFiles[0].CaptureStatus != "metadata_only" {
		t.Fatalf("files = %#v", event.RequestFiles)
	}
}

func TestHTTPProxyAndMissEventsCaptureBodiesHeadersAndTarget(t *testing.T) {
	const reqBody = `{"forward":true}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if string(got) != reqBody {
			t.Errorf("upstream body = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "yes")
		_, _ = w.Write([]byte(`{"from":"upstream"}`))
	}))
	defer upstream.Close()
	srv := newHTTPLogTestServer(t, upstream.URL)
	ch := srv.Bus.Subscribe()
	defer srv.Bus.Unsubscribe(ch)
	req := httptest.NewRequest(http.MethodPatch, "http://client.test/proxied?q=1", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client", "preserved")
	srv.Mux.ServeHTTP(httptest.NewRecorder(), req)
	event := <-ch
	if event.Type != "PROXY" || event.Target != upstream.URL || event.RequestBody != reqBody || event.ResponseBody != `{"from":"upstream"}` {
		t.Fatalf("proxy log missing payload or target: %#v", event)
	}
	if event.URL != "http://client.test/proxied?q=1" || event.Path != "/proxied?q=1" {
		t.Fatalf("original absolute request URL/path changed: url=%q path=%q", event.URL, event.Path)
	}
	if event.RequestHeaders["X-Client"][0] != "preserved" || event.ResponseHeaders["X-Upstream"][0] != "yes" {
		t.Fatalf("proxy headers missing: %#v %#v", event.RequestHeaders, event.ResponseHeaders)
	}

	missServer := newHTTPLogTestServer(t, "")
	missEvents := missServer.Bus.Subscribe()
	defer missServer.Bus.Unsubscribe(missEvents)
	missReq := httptest.NewRequest(http.MethodDelete, "/missing", strings.NewReader("raw"))
	missReq.Header.Set("Content-Type", "text/plain")
	missServer.Mux.ServeHTTP(httptest.NewRecorder(), missReq)
	miss := <-missEvents
	if miss.Type != "MISS" || miss.RequestBody != "raw" || miss.ResponsePayload.CaptureStatus != "captured" || miss.ResponseHeaders["Content-Type"][0] != "application/json" {
		t.Fatalf("miss log missing capture: %#v", miss)
	}
}

type failReadBody struct{ sent bool }

func (b *failReadBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, errors.New("read failed")
	}
	b.sent = true
	return copy(p, "partial"), errors.New("read failed")
}
func (*failReadBody) Close() error { return nil }

func TestHTTPRequestReadErrorReturns400WithoutMatchingPartialBody(t *testing.T) {
	srv := newHTTPLogTestServer(t, "")
	_, err := srv.Store.Create(httpmock.Mock{Method: http.MethodPost, Path: "/read-error", Status: http.StatusCreated, Body: json.RawMessage(`{"matched":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	ch := srv.Bus.Subscribe()
	defer srv.Bus.Unsubscribe(ch)
	req := httptest.NewRequest(http.MethodPost, "/read-error", nil)
	req.Body = &failReadBody{}
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	srv.Mux.ServeHTTP(rec, req)
	event := <-ch
	if rec.Code != http.StatusBadRequest || event.Status != http.StatusBadRequest || event.ResponseBody == `{"matched":true}` {
		t.Fatalf("partial body matched: status=%d event=%#v", rec.Code, event)
	}
	if event.RequestBody != "partial" || event.RequestPayload.CaptureStatus != "error" || !strings.Contains(event.Error, "read failed") {
		t.Fatalf("read error capture missing: %#v", event)
	}
}

func TestProxyAsksUpstreamForIdentityEncoding(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hola":"mundo"}`))
	}))
	defer upstream.Close()

	pm := NewProxyManager(upstream.URL)
	cw := capture.NewWriter(httptest.NewRecorder())
	req := httptest.NewRequest("GET", "/api/thing", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	pm.ServeHTTPWithTarget(cw, req)

	if seen != "identity" {
		t.Errorf("upstream saw Accept-Encoding %q, want identity", seen)
	}
	if got, _ := cw.Payload(); got != `{"hola":"mundo"}` {
		t.Errorf("captured body = %q", got)
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

func TestDirectWebSocketProxyStreamsAndLogsBidirectionalFrames(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/base/frames" || r.URL.Query().Get("from") != "target" || r.URL.Query().Get("client") != "yes" {
			http.Error(w, fmt.Sprintf("unexpected upstream URL %s", r.URL.String()), http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("Origin") != "https://client.example" {
			http.Error(w, "missing forwarded headers", http.StatusForbidden)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"ditto.test.v1"}, InsecureSkipVerify: true})
		if err != nil {
			return
		}
		conn.SetReadLimit(-1)
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	bus := eventlog.NewBus(nil)
	hub := socket.NewHub(socket.Config{Bus: bus})
	proxy := NewProxyManager(upstream.URL + "/base?from=target")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tunnelThrough(proxy, hub, w, r) {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, httpToWS(server.URL)+"/frames?client=yes", &websocket.DialOptions{
		Subprotocols: []string{"ditto.test.v1"},
		HTTPHeader:   http.Header{"Authorization": {"Bearer token"}, "Origin": {"https://client.example"}},
	})
	if err != nil {
		t.Fatalf("Dial proxy: %v", err)
	}
	defer client.Close(websocket.StatusNormalClosure, "")
	client.SetReadLimit(-1)
	if got := client.Subprotocol(); got != "ditto.test.v1" {
		t.Fatalf("subprotocol = %q", got)
	}
	for _, frame := range []struct {
		typ  websocket.MessageType
		data []byte
	}{
		{websocket.MessageText, []byte(`{"hello":"world"}`)},
		{websocket.MessageBinary, []byte{0, 1, 2, 255}},
	} {
		if err := client.Write(ctx, frame.typ, frame.data); err != nil {
			t.Fatalf("write frame: %v", err)
		}
		gotType, got, err := client.Read(ctx)
		if err != nil {
			t.Fatalf("read echo: %v", err)
		}
		if gotType != frame.typ || string(got) != string(frame.data) {
			t.Fatalf("echo=(%v,%v), want (%v,%v)", gotType, got, frame.typ, frame.data)
		}
	}
	large := bytes.Repeat([]byte{0, 1, 2, 255}, 300000)
	if err := client.Write(ctx, websocket.MessageBinary, large); err != nil {
		t.Fatalf("write large frame: %v", err)
	}
	gotType, gotLarge, err := client.Read(ctx)
	if err != nil {
		t.Fatalf("read large echo: %v", err)
	}
	if gotType != websocket.MessageBinary || !bytes.Equal(gotLarge, large) {
		t.Fatalf("large echo length/type = %d/%v, want %d/binary", len(gotLarge), gotType, len(large))
	}
	_ = client.Close(websocket.StatusNormalClosure, "done")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var foundText, foundBinary, foundTruncated bool
		for _, e := range bus.Summaries() {
			if e.Method == "FRAME" && e.Source == "websocket-proxy" {
				if e.Direction == "client_to_upstream" && e.FrameKind == "text" {
					foundText = true
				}
				if e.Direction == "upstream_to_client" && e.FrameKind == "binary" && e.Subprotocol == "ditto.test.v1" {
					foundBinary = true
				}
				if e.Direction == "client_to_upstream" && e.FrameKind == "binary" && e.RequestPayload != nil {
					detail, ok := bus.Detail(e.ID)
					foundTruncated = ok && detail.RequestPayload != nil && detail.RequestPayload.SizeBytes == int64(len(large)) &&
						detail.RequestPayload.CapturedBytes == capture.MaxBytes && detail.RequestPayload.CaptureStatus == "truncated"
				}
			}
		}
		if foundText && foundBinary && foundTruncated {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected bidirectional text and binary frame logs")
}

func TestDirectWebSocketProxyReplaysFullUpstreamRejectionBody(t *testing.T) {
	body := strings.Repeat("rejection-body-", 90000)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Rejection", "auth")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()
	bus := eventlog.NewBus(nil)
	hub := socket.NewHub(socket.Config{Bus: bus})
	proxy := NewProxyManager(upstream.URL)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tunnelThrough(proxy, hub, w, r)
	}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/reject")
	if err != nil {
		t.Fatalf("GET proxy: %v", err)
	}
	got, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if resp.StatusCode != http.StatusForbidden || string(got) != body {
		t.Fatalf("response status/body = %d/%d bytes, want 403/%d", resp.StatusCode, len(got), len(body))
	}
	if resp.Header.Get("X-Upstream-Rejection") != "auth" {
		t.Fatalf("upstream header not forwarded: %v", resp.Header)
	}
	for _, event := range bus.Summaries() {
		if event.Method == "HANDSHAKE_ERROR" && event.Source == "websocket-proxy" {
			detail, ok := bus.Detail(event.ID)
			if !ok {
				t.Fatal("handshake detail missing")
			}
			if detail.ResponsePayload == nil || detail.ResponsePayload.SizeBytes != int64(len(body)) || detail.ResponsePayload.CapturedBytes != capture.MaxBytes {
				t.Fatalf("rejection capture metadata = %#v", detail.ResponsePayload)
			}
			return
		}
	}
	t.Fatal("upstream handshake rejection was not logged")
}

func TestDirectWebSocketProxyPreservesUpstreamCloseCodeAndReason(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, _, _ = conn.Read(r.Context())
		_ = conn.Close(websocket.StatusPolicyViolation, "blocked by upstream")
	}))
	defer upstream.Close()
	hub := socket.NewHub(socket.Config{Bus: eventlog.NewBus(nil)})
	proxy := NewProxyManager(upstream.URL)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tunnelThrough(proxy, hub, w, r) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, httpToWS(server.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Write(ctx, websocket.MessageText, []byte("close me")); err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Read(ctx)
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("Read error = %v, want close error", err)
	}
	if closeErr.Code != websocket.StatusPolicyViolation || closeErr.Reason != "blocked by upstream" {
		t.Fatalf("close = %d %q, want 1008 %q", closeErr.Code, closeErr.Reason, "blocked by upstream")
	}
}

// tunnelThrough tunnels r to proxy's target, reporting false when no target is set.
func tunnelThrough(proxy *ProxyManager, hub *socket.Hub, w http.ResponseWriter, r *http.Request) bool {
	direct, target := proxy.Director()
	if direct == nil {
		return false
	}
	hub.Tunnel(w, r, target, direct)
	return true
}

func TestWebSocketProxyModeRejectsForbiddenOrigin(t *testing.T) {
	targetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	defer target.Close()
	layout, err := EnsureDataLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(ServerConfig{
		Port:     8888,
		Target:   target.URL,
		MocksDir: layout.MocksDir,
		Layout:   layout,
		ServeUI:  false,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:8888/events?__ditto_ws=live", nil)
	req.Host = "192.168.1.10:8888"
	req.RemoteAddr = "192.168.1.20:55555"
	req.Header.Set("Origin", "http://192.168.1.10:3000")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()

	srv.Mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("proxy websocket status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if targetHit {
		t.Fatalf("forbidden websocket proxy request reached target")
	}
}
