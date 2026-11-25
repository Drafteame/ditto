package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"ditto/internal/eventlog"
)

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

func TestRelayAdapterAcksConnectionWithoutInit(t *testing.T) {
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil)})
	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/?adapter=relay", nil)
	if err != nil {
		t.Fatalf("websocket.Dial() error = %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	_, greeting, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("connection ack read error = %v", err)
	}
	if string(greeting) != `{"type":"connection_ack"}` {
		t.Fatalf("greeting = %s, want connection_ack", greeting)
	}

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","id":"/scores","channel":"/scores","authorization":{"Authorization":"Basic token"}}`)); err != nil {
		t.Fatalf("subscribe write error = %v", err)
	}
	_, ack, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("subscribe ack read error = %v", err)
	}
	if !strings.Contains(string(ack), `"type":"subscribe_success"`) || !strings.Contains(string(ack), `"channel":"/scores"`) {
		t.Fatalf("subscribe ack = %s, want subscribe_success with channel", ack)
	}

	for i := 0; i < 20; i++ {
		if clients := hub.registry.Clients("/scores"); len(clients) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if result := hub.dispatchJSON("/scores", json.RawMessage(`{"score":7}`), "", ""); result.Delivered != 1 {
		t.Fatalf("Dispatch() delivered %d clients, want 1", result.Delivered)
	}

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("dispatch read error = %v", err)
	}
	if !strings.Contains(string(data), `"type":"data"`) || !strings.Contains(string(data), `"channel":"/scores"`) {
		t.Fatalf("dispatch frame = %s", data)
	}
}

func TestSocketHubDispatchesToRawSubscriber(t *testing.T) {
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil)})
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

	result := hub.dispatchJSON("/scores", json.RawMessage(`{"score":7}`), "", "")
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
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil)})
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
			hub.dispatchJSON("/scores", json.RawMessage(`{"score":7}`), "", "")
		}
	}()

	_ = conn.Close(websocket.StatusNormalClosure, "")
	wg.Wait()
}

func TestSocketHubDispatchAdapterFilter(t *testing.T) {
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil)})
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

	result := hub.dispatchJSON("/scores", json.RawMessage(`{"score":7}`), "appsync", "")
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

func TestDispatchLogBodyIncludesDecodedPayload(t *testing.T) {
	bus := eventlog.NewBus(nil)
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)
	hub := NewSocketHub(SocketHubConfig{Bus: bus})
	addRawTestSubscriber(hub, "/log")

	hub.dispatchJSON("/log", json.RawMessage(`{"score":7}`), "", "")

	event := waitForSocketMethod(t, events, "DISPATCH", "/log", time.Second)
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
	bus := eventlog.NewBus(nil)
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)
	hub := NewSocketHub(SocketHubConfig{Bus: bus})
	addRawTestSubscriber(hub, "/large")

	large := json.RawMessage(`{"data":"` + strings.Repeat("x", dispatchPayloadMaxBytes+128) + `"}`)
	hub.dispatchJSON("/large", large, "", "")

	event := waitForSocketMethod(t, events, "DISPATCH", "/large", time.Second)
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
	var out bytes.Buffer
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(eventlog.NewPrinter(&out, true))})
	addRawTestSubscriber(hub, "/decoded")

	hub.dispatchJSON("/decoded", json.RawMessage(`{"score":7}`), "", "")
	output := out.String()
	var event eventlog.Event
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var candidate eventlog.Event
		if err := json.Unmarshal([]byte(line), &candidate); err == nil && candidate.Type == "SOCKET" && candidate.Method == "DISPATCH" && candidate.Path == "/decoded" {
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
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil)})
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
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil)})
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
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil)})
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
	bus := eventlog.NewBus(nil)
	hub := NewSocketHub(SocketHubConfig{Bus: bus})
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
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil)})
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

func TestEnqueueControlUsesControlChannelWhenDataFull(t *testing.T) {
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil)})
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
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil)})
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

	result := hub.dispatchJSON("/scores", json.RawMessage(`{"score":7}`), "", "")
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

func TestBundledDrafteaProfileDispatchesStringifiedRealtimeEnvelope(t *testing.T) {
	adapters := &AdapterRegistry{}
	if err := adapters.Load(t.TempDir()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	protocol, err := adapters.Protocol("appsync-draftea")
	if err != nil {
		t.Fatalf("Protocol(appsync-draftea) error = %v", err)
	}
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil), Schemas: &SchemaRegistry{}, Adapters: adapters})
	client := &SocketClient{
		id:            "appsync-draftea-1",
		adapter:       "appsync-draftea",
		protocol:      protocol,
		control:       make(chan EncodedServerMessage, 1),
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		subscriptions: map[string]string{"/test": "sub-1"},
	}
	hub.addClient(client)
	hub.registry.Subscribe("/test", client.id)

	encodedPayload := EncodedPayload{
		Data:     []byte{1, 2, 3},
		Kind:     websocket.MessageBinary,
		TypeName: "appsync.recovery.Recovery",
	}
	result, err := hub.Dispatch(SocketDispatch{
		Channel:  "/test",
		Adapter:  "appsync-draftea",
		TypeName: "appsync.recovery.Recovery",
		Payload:  json.RawMessage(`{"recoveryId":"r1"}`),
		Encoded:  &encodedPayload,
	})
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if result.Delivered != 1 {
		t.Fatalf("delivered = %d, want 1; errors=%v", result.Delivered, result.Errors)
	}

	select {
	case msg := <-client.send:
		env, inner := decodeProfileEnvelope(t, msg.Data)
		if env.ID != "sub-1" || env.Type != "data" {
			t.Fatalf("outer = %#v, want Draftea data envelope", env)
		}
		if inner.T != "recovery" || inner.E != base64.StdEncoding.EncodeToString(encodedPayload.Data) {
			t.Fatalf("inner = %#v, want recovery base64 envelope", inner)
		}
	default:
		t.Fatalf("client did not receive dispatch frame")
	}
}

func TestBundledRelayProfileDispatchesRelayEnvelope(t *testing.T) {
	adapters := &AdapterRegistry{}
	if err := adapters.Load(t.TempDir()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	protocol, err := adapters.Protocol("relay-draftea")
	if err != nil {
		t.Fatalf("Protocol(relay-draftea) error = %v", err)
	}
	if !adapterGreetsOnConnect(protocol) {
		t.Fatalf("relay-draftea does not greet on connect")
	}
	if len(protocol.Subprotocols()) != 0 {
		t.Fatalf("Subprotocols() = %v, want none", protocol.Subprotocols())
	}

	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil), Schemas: &SchemaRegistry{}, Adapters: adapters})
	client := &SocketClient{
		id:            "relay-draftea-1",
		adapter:       "relay-draftea",
		protocol:      protocol,
		control:       make(chan EncodedServerMessage, 1),
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		subscriptions: map[string]string{"/osb/sport/league/*": "/osb/sport/league/*"},
	}
	hub.addClient(client)
	hub.registry.Subscribe("/osb/sport/league/*", client.id)

	encodedPayload := EncodedPayload{
		Data:     []byte{1, 2, 3},
		Kind:     websocket.MessageBinary,
		TypeName: "appsync.betinfo.BetEventDto",
	}
	result, err := hub.Dispatch(SocketDispatch{
		Channel:  "/osb/sport/league/*",
		Adapter:  "relay-draftea",
		TypeName: "appsync.betinfo.BetEventDto",
		Payload:  json.RawMessage(`{"id":"bet-1"}`),
		Encoded:  &encodedPayload,
	})
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if result.Delivered != 1 {
		t.Fatalf("delivered = %d, want 1; errors=%v", result.Delivered, result.Errors)
	}

	select {
	case msg := <-client.send:
		var env struct {
			Type    string `json:"type"`
			Channel string `json:"channel"`
			Event   string `json:"event"`
		}
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			t.Fatalf("outer JSON error: %v; data=%s", err, msg.Data)
		}
		if env.Type != "data" || env.Channel != "/osb/sport/league/*" {
			t.Fatalf("outer = %#v, want relay data envelope", env)
		}
		var inner struct {
			T string `json:"t"`
			E string `json:"e"`
		}
		if err := json.Unmarshal([]byte(env.Event), &inner); err != nil {
			t.Fatalf("inner JSON error: %v; event=%s", err, env.Event)
		}
		if inner.T != "betInfo" || inner.E != base64.StdEncoding.EncodeToString(encodedPayload.Data) {
			t.Fatalf("inner = %#v, want betInfo base64 envelope", inner)
		}
	default:
		t.Fatalf("client did not receive dispatch frame")
	}
}

func TestHubDispatchSuppressedByMode(t *testing.T) {
	reg, err := NewChannelModeRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	hub := NewSocketHub(SocketHubConfig{Bus: eventlog.NewBus(nil), Modes: reg})
	for _, mode := range []ChannelMode{ModeLive, ModeRecord} {
		if err := reg.Set(ChannelConfig{Channel: "/blocked", Mode: mode}); err != nil {
			t.Fatalf("Set(%s) error = %v", mode, err)
		}
		result, err := hub.Dispatch(SocketDispatch{
			Channel: "/blocked",
			Payload: json.RawMessage(`{"ok":true}`),
		})
		if err != nil {
			t.Fatalf("Dispatch(%s) fatal error = %v", mode, err)
		}
		if len(result.Errors) != 1 {
			t.Fatalf("Dispatch(%s) errors = %#v, want suppression", mode, result.Errors)
		}
	}
	for _, mode := range []ChannelMode{ModeMock, ModeMixed} {
		if err := reg.Set(ChannelConfig{Channel: "/allowed", Mode: mode}); err != nil {
			t.Fatalf("Set(%s) error = %v", mode, err)
		}
		result, err := hub.Dispatch(SocketDispatch{
			Channel: "/allowed",
			Payload: json.RawMessage(`{"ok":true}`),
		})
		if err != nil {
			t.Fatalf("Dispatch(%s) error = %v", mode, err)
		}
		if len(result.Errors) != 0 {
			t.Fatalf("Dispatch(%s) errors = %#v, want none", mode, result.Errors)
		}
	}
}

func TestRecordingBurstKeepsAllFramesAndCoalescesLog(t *testing.T) {
	bus := eventlog.NewBus(nil)
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)
	modes, err := NewChannelModeRegistry(t.TempDir(), bus)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	if err := modes.Set(ChannelConfig{Channel: "/burst-record", Mode: ModeRecord}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	rec, err := NewRecorder(RecorderConfig{Dir: t.TempDir(), Modes: modes, Bus: bus})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	hub := NewSocketHub(SocketHubConfig{Bus: bus, Modes: modes, Recorder: rec})
	manifest, err := rec.Start("Burst Recording", "")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	for i := 0; i < 200; i++ {
		hub.forwardFromUpstream("/burst-record", websocket.MessageText, []byte(`{"n":1}`))
	}
	if got := waitForBurstSummary(t, events, "/burst-record"); got != 200 {
		t.Fatalf("burst total_frames = %d, want 200", got)
	}
	if _, err := rec.Stop(manifest.ID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	frames, err := rec.Frames(manifest.ID, "/burst-record", 0, 500)
	if err != nil {
		t.Fatalf("Frames() error = %v", err)
	}
	if len(frames) != 200 {
		t.Fatalf("frames len = %d, want 200", len(frames))
	}
}

func TestSocketFrameDecodeKeepsWireErrorsSeparateFromTransportErrors(t *testing.T) {
	root := t.TempDir()
	writeProto(t, filepath.Join(root, "events"), "event.proto", `syntax = "proto3"; package ditto.frame; message Event { string message = 1; }`)
	schemas, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := schemas.Encode("ditto.frame.Event", json.RawMessage(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded := ProtobufPayload("ditto.frame.Event", data)
	envelope, err := json.Marshal(map[string]any{"type": "data", "payload": map[string]any{"data": map[string]any{
		"base64": base64.StdEncoding.EncodeToString(encoded.Data), "content_type": encoded.ContentType, "type_name": encoded.TypeName,
	}}})
	if err != nil {
		t.Fatal(err)
	}

	bus := eventlog.NewBus(nil)
	hub := NewSocketHub(SocketHubConfig{Bus: bus})
	hub.schemas = schemas
	hub.logSocketFrame(eventlog.Event{Status: 200, Adapter: "appsync"}, envelope, websocket.MessageText, encoded.TypeName)
	detail, ok := bus.Detail(bus.Summaries()[0].ID)
	if !ok {
		t.Fatal("text frame was not retained")
	}
	if detail.Error != "" || detail.DecodeError != "" || !strings.Contains(detail.DecodedPayload, `"message":"hello"`) {
		t.Fatalf("text envelope log = %#v, want decoded JSON without errors", detail)
	}

	bus = eventlog.NewBus(nil)
	hub = NewSocketHub(SocketHubConfig{Bus: bus})
	hub.logSocketFrame(eventlog.Event{Status: 200, DeliveryState: "written"}, []byte{0xff, 0x01}, websocket.MessageBinary, "")
	detail, ok = bus.Detail(bus.Summaries()[0].ID)
	if !ok {
		t.Fatal("binary frame was not retained")
	}
	if detail.Status != 200 || detail.DeliveryState != "written" || detail.Error != "" || detail.DecodeError == "" {
		t.Fatalf("binary frame log = %#v, want successful transport plus decode limitation", detail)
	}
}

func TestMixedModeLocalDispatchIsRecorded(t *testing.T) {
	bus := eventlog.NewBus(nil)
	modes, err := NewChannelModeRegistry(t.TempDir(), bus)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	if err := modes.Set(ChannelConfig{Channel: "/mixed", Mode: ModeMixed}); err != nil {
		t.Fatalf("Set mixed mode: %v", err)
	}
	recDir := t.TempDir()
	rec, err := NewRecorder(RecorderConfig{Dir: recDir, Modes: modes, Bus: bus})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	hub := NewSocketHub(SocketHubConfig{Bus: bus, Modes: modes, Recorder: rec})
	client := &SocketClient{
		id:            "raw-1",
		adapter:       "raw",
		protocol:      RawAdapter{},
		send:          make(chan EncodedServerMessage, 4),
		done:          make(chan struct{}),
		connected:     time.Now(),
		subscriptions: map[string]string{"/mixed": "sub-1"},
	}
	hub.addClient(client)
	hub.registry.Subscribe("/mixed", client.id)

	manifest, err := rec.Start("Mixed Local", "")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	payload := json.RawMessage(`{"local":true}`)
	result, err := hub.Dispatch(SocketDispatch{Channel: "/mixed", Payload: payload})
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if result.Delivered != 1 {
		t.Fatalf("delivered = %d, want 1", result.Delivered)
	}
	if _, err := rec.Stop(manifest.ID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	frames, err := rec.Frames(manifest.ID, "/mixed", 0, 10)
	if err != nil {
		t.Fatalf("Frames() error = %v", err)
	}
	if len(frames) != 1 || frames[0].Direction != "local" {
		t.Fatalf("frames = %#v, want one local frame", frames)
	}
	raw, err := base64.StdEncoding.DecodeString(frames[0].RawB64)
	if err != nil {
		t.Fatalf("raw_b64 invalid: %v", err)
	}
	if string(raw) != string(payload) {
		t.Fatalf("recorded raw = %s, want wrapped payload %s", raw, payload)
	}
}
