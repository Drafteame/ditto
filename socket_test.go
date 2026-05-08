package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

var captureStdoutMu sync.Mutex

func TestSubscriptionRegistryTracksClientsByChannel(t *testing.T) {
	registry := NewSubscriptionRegistry()

	registry.Subscribe("/scores", "client-b")
	registry.Subscribe("/scores", "client-a")
	registry.Subscribe("/chat", "client-c")

	got := registry.Clients("/scores")
	want := []string{"client-a", "client-b"}
	if len(got) != len(want) {
		t.Fatalf("Clients(/scores) len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Clients(/scores)[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	registry.Unsubscribe("/scores", "client-a")
	got = registry.Clients("/scores")
	if len(got) != 1 || got[0] != "client-b" {
		t.Fatalf("after unsubscribe Clients(/scores) = %v, want [client-b]", got)
	}

	registry.RemoveClient("client-b")
	if got := registry.Clients("/scores"); len(got) != 0 {
		t.Fatalf("after RemoveClient Clients(/scores) = %v, want empty", got)
	}
}

func TestRawAdapterSubscribeAndDispatchPayload(t *testing.T) {
	adapter := RawAdapter{}

	msg, err := adapter.ParseClientMessage([]byte(`{"type":"subscribe","id":"sub-1","channel":"/scores"}`))
	if err != nil {
		t.Fatalf("ParseClientMessage() error = %v", err)
	}
	if msg.Type != "subscribe" || msg.Channel != "/scores" || msg.SubscriptionID != "sub-1" {
		t.Fatalf("ParseClientMessage() = %#v, want subscribe /scores sub-1", msg)
	}

	payload := json.RawMessage(`{"score":7}`)
	encoded, err := adapter.EncodeServerMessage(ServerMsg{Type: "data", Channel: "/scores", Payload: payload})
	if err != nil {
		t.Fatalf("EncodeServerMessage() error = %v", err)
	}
	if string(encoded.Data) != string(payload) {
		t.Fatalf("EncodeServerMessage() = %s, want %s", encoded.Data, payload)
	}
}

func TestAppSyncAdapterEnvelope(t *testing.T) {
	adapter := AppSyncAdapter{}

	msg, err := adapter.ParseClientMessage([]byte(`{"type":"start","id":"sub-1","payload":{"channel":"/scores"}}`))
	if err != nil {
		t.Fatalf("ParseClientMessage() error = %v", err)
	}
	if msg.Type != "subscribe" || msg.Channel != "/scores" || msg.SubscriptionID != "sub-1" {
		t.Fatalf("ParseClientMessage() = %#v, want subscribe /scores sub-1", msg)
	}

	encoded, err := adapter.EncodeServerMessage(ServerMsg{
		Type:    "data",
		ID:      "sub-1",
		Channel: "/scores",
		Payload: json.RawMessage(`{"score":7}`),
	})
	if err != nil {
		t.Fatalf("EncodeServerMessage() error = %v", err)
	}

	var env struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Payload struct {
			Data map[string]any `json:"data"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(encoded.Data, &env); err != nil {
		t.Fatalf("encoded AppSync message is invalid JSON: %v", err)
	}
	if env.Type != "data" || env.ID != "sub-1" || env.Payload.Data["score"].(float64) != 7 {
		t.Fatalf("encoded AppSync message = %s", encoded.Data)
	}
}

func TestAppSyncAdapterErrorPayloadUsesJSONString(t *testing.T) {
	adapter := AppSyncAdapter{}

	encoded, err := adapter.EncodeServerMessage(ServerMsg{
		Type:    "error",
		ID:      "sub-1",
		Payload: json.RawMessage(`"plain error"`),
	})
	if err != nil {
		t.Fatalf("EncodeServerMessage() error = %v", err)
	}

	var env struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Payload struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(encoded.Data, &env); err != nil {
		t.Fatalf("encoded AppSync error is invalid JSON: %v", err)
	}
	if env.Type != "error" || env.ID != "sub-1" || len(env.Payload.Errors) != 1 || env.Payload.Errors[0].Message != "plain error" {
		t.Fatalf("encoded AppSync error = %s", encoded.Data)
	}
}

func TestSocketHubDispatchesToRawSubscriber(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if IsWebSocketRequest(r) {
			hub.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/?adapter=raw"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket.Dial() error = %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","channel":"/scores"}`)); err != nil {
		t.Fatalf("subscribe write error = %v", err)
	}
	_, ack, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("subscribe ack read error = %v", err)
	}
	if !strings.Contains(string(ack), "subscribe_ack") {
		t.Fatalf("subscribe ack = %s, want subscribe_ack", ack)
	}

	for i := 0; i < 20; i++ {
		if clients := hub.registry.Clients("/scores"); len(clients) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	result := hub.Dispatch("/scores", json.RawMessage(`{"score":7}`), "")
	if result.Delivered != 1 {
		t.Fatalf("Dispatch() delivered %d clients, want 1", result.Delivered)
	}

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("dispatch read error = %v", err)
	}
	if string(data) != `{"score":7}` {
		t.Fatalf("dispatch payload = %s, want {\"score\":7}", data)
	}
}

func TestSocketHubDispatchDuringDisconnectDoesNotPanic(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/", hub.ServeHTTP)
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/?adapter=raw", nil)
	if err != nil {
		t.Fatalf("websocket.Dial() error = %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","channel":"/scores"}`)); err != nil {
		t.Fatalf("subscribe write error = %v", err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("subscribe ack read error = %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			hub.Dispatch("/scores", json.RawMessage(`{"score":7}`), "")
		}
	}()

	_ = conn.Close(websocket.StatusNormalClosure, "")
	wg.Wait()
}

func TestSocketHubDispatchAdapterFilter(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false, nil)
	rawClient := &SocketClient{
		id:            "raw-1",
		adapter:       "raw",
		protocol:      RawAdapter{},
		control:       make(chan EncodedServerMessage, 1),
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		subscriptions: map[string]string{"/scores": "raw-sub"},
	}
	appSyncClient := &SocketClient{
		id:            "appsync-1",
		adapter:       "appsync",
		protocol:      AppSyncAdapter{},
		control:       make(chan EncodedServerMessage, 1),
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		subscriptions: map[string]string{"/scores": "appsync-sub"},
	}
	hub.addClient(rawClient)
	hub.addClient(appSyncClient)
	hub.registry.Subscribe("/scores", rawClient.id)
	hub.registry.Subscribe("/scores", appSyncClient.id)

	result := hub.Dispatch("/scores", json.RawMessage(`{"score":7}`), "appsync")
	if result.Delivered != 1 {
		t.Fatalf("Dispatch() delivered %d clients, want 1", result.Delivered)
	}
	if len(rawClient.send) != 0 {
		t.Fatalf("raw client received a frame despite appsync filter")
	}
	select {
	case msg := <-appSyncClient.send:
		if !strings.Contains(string(msg.Data), `"id":"appsync-sub"`) {
			t.Fatalf("appsync frame = %s, want subscription id", msg.Data)
		}
	default:
		t.Fatalf("appsync client did not receive a frame")
	}
}

func addRawTestSubscriber(hub *SocketHub, channel string) *SocketClient {
	client := &SocketClient{
		id:            "raw-1",
		adapter:       "raw",
		protocol:      RawAdapter{},
		control:       make(chan EncodedServerMessage, 1),
		send:          make(chan EncodedServerMessage, 8),
		done:          make(chan struct{}),
		subscriptions: map[string]string{channel: "sub-1"},
	}
	hub.addClient(client)
	hub.registry.Subscribe(channel, client.id)
	return client
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	captureStdoutMu.Lock()
	defer captureStdoutMu.Unlock()
	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = old }()
	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(data)
}

func TestDispatchLogBodyIncludesDecodedPayload(t *testing.T) {
	bus := NewEventBus()
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)
	hub := NewSocketHub(bus, false, nil)
	addRawTestSubscriber(hub, "/log")

	hub.Dispatch("/log", json.RawMessage(`{"score":7}`), "")

	event := waitForSocketEvent(t, events, time.Second)
	if event.Method != "DISPATCH" {
		t.Fatalf("event method = %s, want DISPATCH", event.Method)
	}
	var body DispatchLogBody
	if err := json.Unmarshal([]byte(event.ResponseBody), &body); err != nil {
		t.Fatalf("response body invalid JSON: %v", err)
	}
	if body.Delivered != 1 || body.Dropped != 0 || body.Errors != 0 {
		t.Fatalf("body counters = %#v", body)
	}
	var payload map[string]int
	if err := json.Unmarshal(body.Payload, &payload); err != nil {
		t.Fatalf("payload invalid JSON: %v", err)
	}
	if payload["score"] != 7 {
		t.Fatalf("payload = %#v, want score 7", payload)
	}
}

func TestDispatchLogBodyTruncatesLargePayloads(t *testing.T) {
	bus := NewEventBus()
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)
	hub := NewSocketHub(bus, false, nil)
	addRawTestSubscriber(hub, "/large")

	large := json.RawMessage(`{"data":"` + strings.Repeat("x", dispatchPayloadMaxBytes+128) + `"}`)
	hub.Dispatch("/large", large, "")

	event := waitForSocketEvent(t, events, time.Second)
	var body DispatchLogBody
	if err := json.Unmarshal([]byte(event.ResponseBody), &body); err != nil {
		t.Fatalf("response body invalid JSON: %v", err)
	}
	if !body.Truncated {
		t.Fatalf("body.Truncated = false, want true")
	}
	var payload string
	if err := json.Unmarshal(body.Payload, &payload); err != nil {
		t.Fatalf("truncated payload should be JSON string: %v (%s)", err, body.Payload)
	}
	if len(payload) != dispatchPayloadMaxBytes {
		t.Fatalf("truncated payload len = %d, want %d", len(payload), dispatchPayloadMaxBytes)
	}
}

func TestDispatchLogBodyTruncatesUTF8PayloadsAsValidJSONString(t *testing.T) {
	result := SocketDispatchResult{Delivered: 1}
	payload := json.RawMessage(`{"data":"` + strings.Repeat("ñ", dispatchPayloadMaxBytes) + `"}`)
	bodyText := buildDispatchLogBody(result, &DecodedFrame{PayloadJSON: payload}, "")
	var body DispatchLogBody
	if err := json.Unmarshal([]byte(bodyText), &body); err != nil {
		t.Fatalf("body invalid JSON: %v", err)
	}
	if !body.Truncated {
		t.Fatalf("body.Truncated = false, want true")
	}
	var truncated string
	if err := json.Unmarshal(body.Payload, &truncated); err != nil {
		t.Fatalf("truncated payload should remain valid JSON string: %v", err)
	}
	if truncated == "" {
		t.Fatalf("truncated payload is empty")
	}
}

func TestDispatchLogIncludesDecodedPayloadWithoutSSESubscribers(t *testing.T) {
	bus := NewEventBus()
	hub := NewSocketHub(bus, true, nil)
	addRawTestSubscriber(hub, "/decoded")

	output := captureStdout(t, func() {
		hub.Dispatch("/decoded", json.RawMessage(`{"score":7}`), "")
	})
	var event LogEvent
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var candidate LogEvent
		if err := json.Unmarshal([]byte(line), &candidate); err == nil && candidate.Path == "/decoded" {
			event = candidate
		}
	}
	if event.ResponseBody == "" {
		t.Fatalf("missing /decoded log event in %q", output)
	}
	var body DispatchLogBody
	if err := json.Unmarshal([]byte(event.ResponseBody), &body); err != nil {
		t.Fatalf("response body invalid JSON: %v", err)
	}
	if len(body.Payload) == 0 {
		t.Fatalf("body = %#v, want decoded payload even without SSE subscribers", body)
	}
	var payload map[string]int
	if err := json.Unmarshal(body.Payload, &payload); err != nil {
		t.Fatalf("payload invalid JSON: %v", err)
	}
	if payload["score"] != 7 {
		t.Fatalf("payload = %#v, want score 7", payload)
	}
}

func TestSocketAPIRejectsCrossOriginTextPlainDispatch(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false, nil)
	mux := http.NewServeMux()
	RegisterSocketRoutes(mux, hub)

	req := httptest.NewRequest(http.MethodPost, "/__ditto__/api/socket/dispatch", strings.NewReader(`{"channel":"/scores","payload":{}}`))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("dispatch status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestSocketOriginPolicy(t *testing.T) {
	cases := []struct {
		name       string
		origin     string
		host       string
		remoteAddr string
		want       bool
	}{
		{
			name:       "localhost cross port allowed on loopback",
			origin:     "http://localhost:3000",
			host:       "localhost:8888",
			remoteAddr: "127.0.0.1:55555",
			want:       true,
		},
		{
			name:       "wails localhost allowed on loopback",
			origin:     "wails://wails.localhost",
			host:       "localhost:8888",
			remoteAddr: "127.0.0.1:55555",
			want:       true,
		},
		{
			name:       "LAN cross port rejected",
			origin:     "http://192.168.1.10:3000",
			host:       "192.168.1.10:8888",
			remoteAddr: "192.168.1.20:55555",
			want:       false,
		},
		{
			name:       "LAN same origin allowed",
			origin:     "http://192.168.1.10:8888",
			host:       "192.168.1.10:8888",
			remoteAddr: "192.168.1.20:55555",
			want:       true,
		},
		{
			name:       "empty origin rejected from LAN",
			host:       "192.168.1.10:8888",
			remoteAddr: "192.168.1.20:55555",
			want:       false,
		},
		{
			name:       "empty origin allowed from loopback",
			host:       "localhost:8888",
			remoteAddr: "127.0.0.1:55555",
			want:       true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/__ditto__/api/socket/clients", nil)
			req.Host = tc.host
			req.RemoteAddr = tc.remoteAddr
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if got := isAllowedSocketAPIRequest(req); got != tc.want {
				t.Fatalf("isAllowedSocketAPIRequest() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSocketHubRejectsForbiddenOriginHandshake(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false, nil)
	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/?adapter=raw", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil {
		t.Fatalf("websocket.Dial() succeeded, want forbidden origin failure")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("handshake status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestSocketHubAllowsLocalhostCrossPortHandshake(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false, nil)
	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/?adapter=raw", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://localhost:3000"}},
	})
	if err != nil {
		t.Fatalf("websocket.Dial() error = %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
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

func TestShouldProxyWebSocket(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/events?__ditto_ws=live", nil)
	if !shouldProxyWebSocket(req) {
		t.Fatalf("shouldProxyWebSocket(query live) = false, want true")
	}
	req = httptest.NewRequest(http.MethodGet, "/events", nil)
	req.Header.Set("X-Ditto-WS-Mode", "proxy")
	if !shouldProxyWebSocket(req) {
		t.Fatalf("shouldProxyWebSocket(header proxy) = false, want true")
	}
	req = httptest.NewRequest(http.MethodGet, "/events", nil)
	if shouldProxyWebSocket(req) {
		t.Fatalf("shouldProxyWebSocket(default) = true, want false")
	}
}

func TestSocketHubSubscribeMissingChannelSendsError(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false, nil)
	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/?adapter=raw", nil)
	if err != nil {
		t.Fatalf("websocket.Dial() error = %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","id":"sub-1"}`)); err != nil {
		t.Fatalf("subscribe write error = %v", err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("error read = %v", err)
	}
	if !strings.Contains(string(data), `"type":"error"`) || !strings.Contains(string(data), "subscribe message missing channel") {
		t.Fatalf("missing channel response = %s, want error payload", data)
	}
}

func TestEnqueueControlPublishesErrorWhenClientClosed(t *testing.T) {
	bus := NewEventBus()
	hub := NewSocketHub(bus, false, nil)
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)

	client := &SocketClient{
		id:            "raw-1",
		adapter:       "raw",
		protocol:      RawAdapter{},
		control:       make(chan EncodedServerMessage, 1),
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		subscriptions: map[string]string{},
	}
	client.close()

	hub.enqueueControl(client, ServerMsg{Type: "subscribe_ack", Channel: "/scores"})

	select {
	case event := <-events:
		if event.Type != "SOCKET" || event.Method != "ERROR" {
			t.Fatalf("event = %#v, want SOCKET ERROR", event)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for enqueueControl error event")
	}
}

func TestSocketClientEnqueueRejectsEmptyMessageKind(t *testing.T) {
	client := &SocketClient{
		send: make(chan EncodedServerMessage, 1),
		done: make(chan struct{}),
	}
	if client.enqueue(EncodedServerMessage{Data: []byte(`{}`)}, 0) {
		t.Fatalf("enqueue() accepted empty websocket message type")
	}
}

func TestSocketClientEnqueueRejectsAfterClose(t *testing.T) {
	client := &SocketClient{
		send: make(chan EncodedServerMessage, 1),
		done: make(chan struct{}),
	}
	client.close()

	if client.enqueue(textMessage([]byte(`{"ok":true}`)), 0) {
		t.Fatalf("enqueue() accepted message after close")
	}
	if len(client.send) != 0 {
		t.Fatalf("enqueue() buffered a message after close")
	}
}

func TestSocketClientDropCounterInSnapshot(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false, nil)
	client := &SocketClient{
		id:            "raw-1",
		adapter:       "raw",
		protocol:      RawAdapter{},
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		connected:     time.Now(),
		subscriptions: map[string]string{},
	}
	hub.addClient(client)
	client.send <- textMessage([]byte(`{"queued":true}`))
	if client.enqueue(textMessage([]byte(`{"dropped":true}`)), 0) {
		t.Fatalf("enqueue() succeeded with full send queue")
	}
	snap := hub.Snapshot()
	if len(snap) != 1 || snap[0].DroppedToClient != 1 {
		t.Fatalf("snapshot = %#v, want one dropped_to_client", snap)
	}
}

func TestCoalescingPublisherEmitsBurstSummary(t *testing.T) {
	bus := NewEventBus()
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)
	pub := NewCoalescingPublisher(bus, false)
	for i := 0; i < SocketLogCoalesceThresholdPerSecond+5; i++ {
		pub.Publish(LogEvent{Type: "SOCKET", Method: "DISPATCH", Path: "/burst", Status: http.StatusOK})
	}
	deadline := time.After(1500 * time.Millisecond)
	summaries := 0
	for {
		select {
		case event := <-events:
			if event.Method == "DISPATCH_BURST" {
				summaries++
			}
		case <-deadline:
			if summaries != 1 {
				t.Fatalf("burst summaries = %d, want 1", summaries)
			}
			return
		}
	}
}

func TestEnqueueControlUsesControlChannelWhenDataFull(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false, nil)
	client := &SocketClient{
		id:            "raw-1",
		adapter:       "raw",
		protocol:      RawAdapter{},
		control:       make(chan EncodedServerMessage, 1),
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		subscriptions: map[string]string{},
	}
	client.send <- textMessage([]byte(`{"data":true}`))

	hub.enqueueControl(client, ServerMsg{Type: "subscribe_ack", Channel: "/scores"})

	if len(client.control) != 1 {
		t.Fatalf("control channel len = %d, want 1", len(client.control))
	}
	if len(client.send) != 1 {
		t.Fatalf("data channel len = %d, want unchanged full buffer", len(client.send))
	}
}

func TestDispatchEncodesPayloadOncePerAdapter(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false, nil)
	adapter := &countingAdapter{}
	clientA := &SocketClient{
		id:            "client-a",
		adapter:       "counting",
		protocol:      adapter,
		control:       make(chan EncodedServerMessage, 1),
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		subscriptions: map[string]string{"/scores": "sub-a"},
	}
	clientB := &SocketClient{
		id:            "client-b",
		adapter:       "counting",
		protocol:      adapter,
		control:       make(chan EncodedServerMessage, 1),
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		subscriptions: map[string]string{"/scores": "sub-b"},
	}
	hub.addClient(clientA)
	hub.addClient(clientB)
	hub.registry.Subscribe("/scores", clientA.id)
	hub.registry.Subscribe("/scores", clientB.id)

	result := hub.Dispatch("/scores", json.RawMessage(`{"score":7}`), "")
	if result.Delivered != 2 {
		t.Fatalf("Dispatch() delivered %d clients, want 2", result.Delivered)
	}
	if adapter.encodePayloadCalls != 1 {
		t.Fatalf("EncodePayload called %d times, want 1", adapter.encodePayloadCalls)
	}
}

type countingAdapter struct {
	encodePayloadCalls int
}

func (a *countingAdapter) ParseClientMessage(b []byte) (ClientMsg, error) {
	return ClientMsg{}, nil
}

func (a *countingAdapter) EncodePayload(payload json.RawMessage) (EncodedPayload, error) {
	a.encodePayloadCalls++
	return EncodedPayload{Data: append([]byte(nil), payload...), Kind: websocket.MessageText}, nil
}

func (a *countingAdapter) WrapData(payload EncodedPayload, subID, channel string) (EncodedServerMessage, error) {
	return textMessage([]byte(fmt.Sprintf(`{"id":%q,"data":%s}`, subID, payload.Data))), nil
}

func (a *countingAdapter) EncodeServerMessage(msg ServerMsg) (EncodedServerMessage, error) {
	return textMessage([]byte(`{}`)), nil
}

func (a *countingAdapter) Heartbeat() (EncodedServerMessage, time.Duration) {
	return EncodedServerMessage{}, 0
}

func (a *countingAdapter) Subprotocols() []string {
	return nil
}
