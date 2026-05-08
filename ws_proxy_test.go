package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestLiveBridgeForwardsBidirectionallyAndSwitchesMode(t *testing.T) {
	upstreamFrames := make(chan []byte, 4)
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
			select {
			case upstreamFrames <- append([]byte(nil), data...):
			default:
			}
			_ = conn.Write(r.Context(), typ, data)
		}
	}))
	defer upstream.Close()

	bus := NewEventBus()
	modes, err := NewChannelModeRegistry(t.TempDir(), bus, false)
	if err != nil {
		t.Fatalf("NewChannelModeRegistry() error = %v", err)
	}
	hub := NewSocketHub(bus, false, modes)
	target := NewLiveTargetManager(httpToWS(upstream.URL), nil)
	bridge := NewLiveBridge(target, hub)
	hub.SetLiveBridge(bridge)
	if err := modes.Set(ChannelConfig{Channel: "/live", Mode: ModeLive}); err != nil {
		t.Fatalf("Set live mode: %v", err)
	}

	local := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer local.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, httpToWS(local.URL)+"/__ditto__/socket?adapter=raw", nil)
	if err != nil {
		t.Fatalf("Dial local: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	subscribeFrame := []byte(`{"type":"subscribe","id":"sub","channel":"/live"}`)
	if err := conn.Write(ctx, websocket.MessageText, subscribeFrame); err != nil {
		t.Fatalf("subscribe write: %v", err)
	}
	select {
	case got := <-upstreamFrames:
		if string(got) != string(subscribeFrame) {
			t.Fatalf("upstream first frame = %s, want subscribe %s", got, subscribeFrame)
		}
	case <-ctx.Done():
		t.Fatalf("upstream did not receive subscribe frame: %v", ctx.Err())
	}
	_, _, _ = conn.Read(ctx)
	time.Sleep(100 * time.Millisecond)
	payload := []byte(`{"type":"message","channel":"/live","payload":{"ok":true}}`)
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("payload write: %v", err)
	}
	var got []byte
	for i := 0; i < 3; i++ {
		_, got, err = conn.Read(ctx)
		if err != nil {
			t.Fatalf("read echo: %v", err)
		}
		if string(got) == string(payload) {
			break
		}
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %s, want %s", got, payload)
	}
	if bridgeChannelClientCount(bridge, "/live") != 1 {
		t.Fatalf("bridge client count = %d, want 1", bridgeChannelClientCount(bridge, "/live"))
	}
	if err := modes.Set(ChannelConfig{Channel: "/live", Mode: ModeMock}); err != nil {
		t.Fatalf("switch mock: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if bridgeChannelClientCount(bridge, "/live") != 0 {
		t.Fatalf("bridge still attached after mock switch")
	}
}

func TestLiveBridgeDetachAttachStressKeepsNewClient(t *testing.T) {
	bridge := NewLiveBridge(NewLiveTargetManager("", nil), NewSocketHub(NewEventBus(), false))
	for i := 0; i < 250; i++ {
		channel := "/race"
		oldClient := testSocketClient("old")
		newClient := testSocketClient("new")
		bridge.Attach(channel, oldClient)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			bridge.Detach(channel, oldClient.id)
		}()
		go func() {
			defer wg.Done()
			bridge.Attach(channel, newClient)
		}()
		wg.Wait()

		bridge.mu.Lock()
		ch := bridge.chans[channel]
		bridge.mu.Unlock()
		if ch == nil {
			t.Fatalf("iteration %d: channel deleted after reattach", i)
		}
		ch.mu.RLock()
		_, ok := ch.clients[newClient.id]
		ch.mu.RUnlock()
		if !ok {
			t.Fatalf("iteration %d: new client missing after reattach", i)
		}
		bridge.DetachChannel(channel)
	}
}

func TestLiveBridgeEmptyTargetLogsOnceUntilTargetChanges(t *testing.T) {
	bus := NewEventBus()
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)
	manager := NewLiveTargetManager("", nil)
	hub := NewSocketHub(bus, false)
	bridge := NewLiveBridge(manager, hub)

	bridge.Attach("/empty", testSocketClient("client"))
	defer bridge.DetachChannel("/empty")

	if event := waitForSocketEvent(t, events, time.Second); event.Method != "ERROR" {
		t.Fatalf("first event = %#v, want ERROR", event)
	}
	select {
	case event := <-events:
		if event.Type == "SOCKET" && event.Method == "ERROR" && event.Path == "/empty" {
			t.Fatalf("unexpected repeated empty-target error before target change: %#v", event)
		}
	case <-time.After(400 * time.Millisecond):
	}
}

func httpToWS(raw string) string {
	return "ws" + strings.TrimPrefix(raw, "http")
}

func testSocketClient(id string) *SocketClient {
	return &SocketClient{
		id:            id,
		adapter:       "raw",
		protocol:      RawAdapter{},
		send:          make(chan EncodedServerMessage, 1),
		done:          make(chan struct{}),
		subscriptions: map[string]string{},
	}
}

func waitForSocketEvent(t *testing.T, events <-chan LogEvent, timeout time.Duration) LogEvent {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Type == "SOCKET" {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for socket event")
		}
	}
}

func bridgeChannelClientCount(bridge *LiveBridge, channel string) int {
	bridge.mu.Lock()
	ch := bridge.chans[channel]
	bridge.mu.Unlock()
	if ch == nil {
		return 0
	}
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	return len(ch.clients)
}
