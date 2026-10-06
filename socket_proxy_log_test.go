package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

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

	bus := NewEventBus()
	hub := NewSocketHub(bus, false, nil)
	proxy := NewProxyManager(upstream.URL + "/base?from=target")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !proxy.ProxyWebSocket(w, r, hub) {
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
		for _, e := range bus.LogSummaries() {
			if e.Method == "FRAME" && e.Source == "websocket-proxy" {
				if e.Direction == "client_to_upstream" && e.FrameKind == "text" {
					foundText = true
				}
				if e.Direction == "upstream_to_client" && e.FrameKind == "binary" && e.Subprotocol == "ditto.test.v1" {
					foundBinary = true
				}
				if e.Direction == "client_to_upstream" && e.FrameKind == "binary" && e.RequestPayload != nil {
					detail, ok := bus.LogDetail(e.ID)
					foundTruncated = ok && detail.RequestPayload != nil && detail.RequestPayload.SizeBytes == int64(len(large)) &&
						detail.RequestPayload.CapturedBytes == MaxLogPayloadCaptureBytes && detail.RequestPayload.CaptureStatus == "truncated"
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
	bus := NewEventBus()
	hub := NewSocketHub(bus, false, nil)
	proxy := NewProxyManager(upstream.URL)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ProxyWebSocket(w, r, hub)
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
	for _, event := range bus.LogSummaries() {
		if event.Method == "HANDSHAKE_ERROR" && event.Source == "websocket-proxy" {
			detail, ok := bus.LogDetail(event.ID)
			if !ok {
				t.Fatal("handshake detail missing")
			}
			if detail.ResponsePayload == nil || detail.ResponsePayload.SizeBytes != int64(len(body)) || detail.ResponsePayload.CapturedBytes != MaxLogPayloadCaptureBytes {
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
	hub := NewSocketHub(NewEventBus(), false, nil)
	proxy := NewProxyManager(upstream.URL)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxy.ProxyWebSocket(w, r, hub) }))
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

func TestLiveBridgeLogsQueuedWrittenAndUpstreamFanoutWithCorrelatedIDs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if string(data) == `{"type":"subscribe","id":"sub","channel":"/live"}` {
				continue
			}
			_ = conn.Write(r.Context(), typ, data)
		}
	}))
	defer upstream.Close()
	bus := NewEventBus()
	modes, err := NewChannelModeRegistry(t.TempDir(), bus, false)
	if err != nil {
		t.Fatal(err)
	}
	hub := NewSocketHub(bus, false, modes)
	bridge := NewLiveBridge(NewLiveTargetManager(httpToWS(upstream.URL), nil), hub)
	hub.SetLiveBridge(bridge)
	if err := modes.Set(ChannelConfig{Channel: "/live", Mode: ModeLive}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, httpToWS(server.URL)+"/?adapter=raw", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(websocket.StatusNormalClosure, "")
	if err := client.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","id":"sub","channel":"/live"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Read(ctx); err != nil {
		t.Fatal(err)
	}
	frame := []byte(`{"type":"message","channel":"/live","payload":{"ok":true}}`)
	if err := client.Write(ctx, websocket.MessageText, frame); err != nil {
		t.Fatal(err)
	}
	if _, echoed, err := client.Read(ctx); err != nil || string(echoed) != string(frame) {
		t.Fatalf("echo=%s err=%v", echoed, err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var clientDispatchID, clientDeliveryID, liveDispatchID, upstreamDeliveryID string
		clientStates := map[string]bool{}
		ackStates := map[string]bool{}
		for _, summary := range bus.LogSummaries() {
			event, ok := bus.LogDetail(summary.ID)
			if !ok {
				continue
			}
			if event.Method == "FRAME" && event.Direction == "client_to_ditto" && event.RequestBody == string(frame) {
				clientDispatchID = event.DispatchID
			}
			if event.Method == "FRAME" && event.Direction == "client_to_upstream" && event.RequestBody == string(frame) {
				clientStates[event.DeliveryState] = true
				clientDeliveryID = event.DispatchID
			}
			if event.Method == "DISPATCH" && event.Channel == "/live" && event.Queued > 0 && event.Source == "live" {
				liveDispatchID = event.DispatchID
			}
			if event.Method == "FRAME" && event.ControlType == "subscribe_ack" {
				ackStates[event.DeliveryState] = true
			}
			if event.Method == "FRAME" && event.Direction == "upstream_to_client" && event.ResponseBody == string(frame) {
				upstreamDeliveryID = event.DispatchID
			}
		}
		if clientStates["queued"] && clientStates["written"] && ackStates["queued"] && ackStates["written"] &&
			clientDispatchID != "" && clientDispatchID == clientDeliveryID && liveDispatchID != "" && liveDispatchID == upstreamDeliveryID {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected correlated queued/written client and upstream delivery logs")
}

func TestDispatchWithoutSubscribersRetainsEncodedProtobufAndSuppression(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "test-pack")
	if err := os.MkdirAll(pack, 0o755); err != nil {
		t.Fatal(err)
	}
	protoSource := `syntax = "proto3"; package ditto.test; message Item { string name = 1; }`
	if err := os.WriteFile(filepath.Join(pack, "item.proto"), []byte(protoSource), 0o644); err != nil {
		t.Fatal(err)
	}
	schemas, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := schemas.Encode("ditto.test.Item", json.RawMessage(`{"name":"without-subscriber"}`))
	if err != nil {
		t.Fatal(err)
	}
	bus := NewEventBus()
	hub := NewSocketHub(bus, false, nil)
	hub.schemas = schemas
	result := hub.DispatchEncodedWithSourcePayload("/proto", encoded, "", "manual", json.RawMessage(`{"name":"without-subscriber"}`))
	if result.Queued != 0 || result.Delivered != 0 {
		t.Fatalf("dispatch result = %#v, want no recipients", result)
	}
	var detail LogEvent
	for _, summary := range bus.LogSummaries() {
		if summary.Method == "DISPATCH" {
			detail, _ = bus.LogDetail(summary.ID)
		}
	}
	if detail.ID == "" || detail.RequestPayload == nil {
		t.Fatalf("protobuf dispatch detail missing raw payload: %#v", detail)
	}
	if detail.RequestPayload.ContentType != "application/x-protobuf" || detail.RequestPayload.CapturedBytes != int64(len(encoded.Data)) {
		t.Fatalf("protobuf payload metadata = %#v", detail.RequestPayload)
	}
	if got := detail.RequestPayload.RawBase64; got != base64.StdEncoding.EncodeToString(encoded.Data) {
		t.Fatal("protobuf wire bytes were not retained")
	}
	if detail.DecodedPayload != `{"name":"without-subscriber"}` {
		t.Fatalf("decoded payload = %s", detail.DecodedPayload)
	}

	modes, err := NewChannelModeRegistry(t.TempDir(), bus, false)
	if err != nil {
		t.Fatal(err)
	}
	suppressedHub := NewSocketHub(bus, false, modes)
	if err := modes.Set(ChannelConfig{Channel: "/suppressed", Mode: ModeLive}); err != nil {
		t.Fatal(err)
	}
	_, err = dispatchRendered(suppressedHub, nil, RenderedDispatch{Channel: "/suppressed", Source: "sequence", Payload: json.RawMessage(`{"visible":true}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, summary := range bus.LogSummaries() {
		if summary.Method != "DISPATCH_SUPPRESSED" {
			continue
		}
		item, ok := bus.LogDetail(summary.ID)
		if ok && item.Source == "sequence" && item.RequestBody == `{"visible":true}` && item.DeliveryState == "suppressed" {
			found = true
		}
	}
	if !found {
		t.Fatal("suppressed dispatch detail did not retain its payload/source")
	}
}

func TestDispatchLogsPerClientQueueDrop(t *testing.T) {
	bus := NewEventBus()
	hub := NewSocketHub(bus, false, nil)
	client := testSocketClient("full")
	client.url, client.host, client.remoteAddr, client.protocolName = "ws://client.test/socket", "client.test", "127.0.0.1:5555", "HTTP/1.1"
	client.addSubscription("/drop", "sub-1")
	client.send <- EncodedServerMessage{Kind: websocket.MessageText, Data: []byte("already queued")}
	hub.addClient(client)
	hub.registry.Subscribe("/drop", client.id)
	result := hub.Dispatch("/drop", json.RawMessage(`{"drop":true}`), "")
	if result.Delivered != 0 || len(result.Dropped) != 1 {
		t.Fatalf("dispatch result = %#v", result)
	}
	for _, summary := range bus.LogSummaries() {
		event, ok := bus.LogDetail(summary.ID)
		if ok && event.Method == "FRAME" && event.ClientID == client.id && event.DeliveryState == "dropped" {
			if event.URL != client.url || event.Host != client.host || event.RemoteAddr != client.remoteAddr || event.Protocol != client.protocolName {
				t.Fatalf("delivery context = %#v", event)
			}
			return
		}
	}
	t.Fatal("dropped client frame was not retained")
}
