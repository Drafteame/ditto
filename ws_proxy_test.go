package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func httpToWS(raw string) string {
	return "ws" + strings.TrimPrefix(raw, "http")
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
