package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"ditto/internal/capture"
	"ditto/internal/eventlog"
	"ditto/internal/socket"
)

func TestProxyAsksUpstreamForIdentityEncoding(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hola":"mundo"}`))
	}))
	defer upstream.Close()

	pm := NewUpstream(upstream.URL)
	cw := capture.NewWriter(httptest.NewRecorder())
	req := httptest.NewRequest("GET", "/api/thing", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	pm.Serve(cw, req)

	if seen != "identity" {
		t.Errorf("upstream saw Accept-Encoding %q, want identity", seen)
	}
	if got, _ := cw.Payload(); got != `{"hola":"mundo"}` {
		t.Errorf("captured body = %q", got)
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
	rp := NewUpstream(upstream.URL + "/base?from=target")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tunnelThrough(rp, hub, w, r) {
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
	rp := NewUpstream(upstream.URL)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tunnelThrough(rp, hub, w, r)
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
	rp := NewUpstream(upstream.URL)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tunnelThrough(rp, hub, w, r) }))
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
func tunnelThrough(rp *Upstream, hub *socket.Hub, w http.ResponseWriter, r *http.Request) bool {
	direct, target := rp.Director()
	if direct == nil {
		return false
	}
	hub.Tunnel(w, r, target, direct)
	return true
}

func httpToWS(raw string) string {
	return "ws" + strings.TrimPrefix(raw, "http")
}
