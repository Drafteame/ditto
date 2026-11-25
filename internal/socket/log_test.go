package socket

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"ditto/internal/adapter"
	"ditto/internal/channelmode"
	"ditto/internal/eventlog"
	"ditto/internal/schema"
)

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
	bus := eventlog.NewBus(nil)
	modes, err := channelmode.NewRegistry(t.TempDir(), bus)
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(Config{Bus: bus, Modes: modes, LiveTargets: NewLiveTargets(httpToWS(upstream.URL), nil)})
	if err := modes.Set(channelmode.Config{Channel: "/live", Mode: channelmode.Live}); err != nil {
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
		for _, summary := range bus.Summaries() {
			event, ok := bus.Detail(summary.ID)
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
	schemas, err := schema.NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := schemas.Encode("ditto.test.Item", json.RawMessage(`{"name":"without-subscriber"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded := adapter.ProtobufPayload("ditto.test.Item", data)
	bus := eventlog.NewBus(nil)
	hub := NewHub(Config{Bus: bus})
	hub.schemas = schemas
	result := hub.dispatchEncoded("/proto", encoded, "", "manual", json.RawMessage(`{"name":"without-subscriber"}`))
	if result.Queued != 0 || result.Delivered != 0 {
		t.Fatalf("dispatch result = %#v, want no recipients", result)
	}
	var detail eventlog.Event
	for _, summary := range bus.Summaries() {
		if summary.Method == "DISPATCH" {
			detail, _ = bus.Detail(summary.ID)
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

	modes, err := channelmode.NewRegistry(t.TempDir(), bus)
	if err != nil {
		t.Fatal(err)
	}
	suppressedHub := NewHub(Config{Bus: bus, Modes: modes})
	if err := modes.Set(channelmode.Config{Channel: "/suppressed", Mode: channelmode.Live}); err != nil {
		t.Fatal(err)
	}
	_, err = suppressedHub.Dispatch(Dispatch{Channel: "/suppressed", Source: "sequence", Payload: json.RawMessage(`{"visible":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, summary := range bus.Summaries() {
		if summary.Method != "DISPATCH_SUPPRESSED" {
			continue
		}
		item, ok := bus.Detail(summary.ID)
		if ok && item.Source == "sequence" && item.RequestBody == `{"visible":true}` && item.DeliveryState == "suppressed" {
			found = true
		}
	}
	if !found {
		t.Fatal("suppressed dispatch detail did not retain its payload/source")
	}
}

func TestDispatchLogsPerClientQueueDrop(t *testing.T) {
	bus := eventlog.NewBus(nil)
	hub := NewHub(Config{Bus: bus})
	client := testSocketClient("full")
	client.url, client.host, client.remoteAddr, client.protocolName = "ws://client.test/socket", "client.test", "127.0.0.1:5555", "HTTP/1.1"
	client.addSubscription("/drop", "sub-1")
	client.send <- adapter.Message{Kind: websocket.MessageText, Data: []byte("already queued")}
	hub.addClient(client)
	hub.registry.Subscribe("/drop", client.id)
	result := hub.dispatchJSON("/drop", json.RawMessage(`{"drop":true}`), "", "")
	if result.Delivered != 0 || len(result.Dropped) != 1 {
		t.Fatalf("dispatch result = %#v", result)
	}
	for _, summary := range bus.Summaries() {
		event, ok := bus.Detail(summary.ID)
		if ok && event.Method == "FRAME" && event.ClientID == client.id && event.DeliveryState == "dropped" {
			if event.URL != client.url || event.Host != client.host || event.RemoteAddr != client.remoteAddr || event.Protocol != client.protocolName {
				t.Fatalf("delivery context = %#v", event)
			}
			return
		}
	}
	t.Fatal("dropped client frame was not retained")
}
