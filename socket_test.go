package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
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

func TestSocketHubDispatchesToRawSubscriber(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false)
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
	hub := NewSocketHub(NewEventBus(), false)
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
	hub := NewSocketHub(NewEventBus(), false)
	rawClient := &SocketClient{
		id:            "raw-1",
		adapter:       "raw",
		protocol:      RawAdapter{},
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		subscriptions: map[string]string{"/scores": "raw-sub"},
	}
	appSyncClient := &SocketClient{
		id:            "appsync-1",
		adapter:       "appsync",
		protocol:      AppSyncAdapter{},
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

func TestSocketAPIRejectsCrossOriginTextPlainDispatch(t *testing.T) {
	hub := NewSocketHub(NewEventBus(), false)
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
