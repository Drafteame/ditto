package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nhooyr.io/websocket"
)

type ClientMsg struct {
	Type           string
	ID             string
	Channel        string
	Payload        json.RawMessage
	SubscriptionID string
}

type ServerMsg struct {
	Type    string
	ID      string
	Channel string
	Payload json.RawMessage
}

type ProtocolAdapter interface {
	ParseClientMessage(b []byte) (ClientMsg, error)
	EncodeServerMessage(msg ServerMsg) ([]byte, error)
	Heartbeat() ([]byte, time.Duration)
}

type SubscriptionRegistry struct {
	mu       sync.RWMutex
	channels map[string]map[string]struct{}
}

func NewSubscriptionRegistry() *SubscriptionRegistry {
	return &SubscriptionRegistry{channels: make(map[string]map[string]struct{})}
}

func (r *SubscriptionRegistry) Subscribe(channel, clientID string) {
	channel = strings.TrimSpace(channel)
	if channel == "" || clientID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.channels[channel] == nil {
		r.channels[channel] = make(map[string]struct{})
	}
	r.channels[channel][clientID] = struct{}{}
}

func (r *SubscriptionRegistry) Unsubscribe(channel, clientID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	clients := r.channels[channel]
	if clients == nil {
		return
	}
	delete(clients, clientID)
	if len(clients) == 0 {
		delete(r.channels, channel)
	}
}

func (r *SubscriptionRegistry) RemoveClient(clientID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for channel, clients := range r.channels {
		delete(clients, clientID)
		if len(clients) == 0 {
			delete(r.channels, channel)
		}
	}
}

func (r *SubscriptionRegistry) Clients(channel string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clients := r.channels[channel]
	if len(clients) == 0 {
		return nil
	}
	ids := make([]string, 0, len(clients))
	for id := range clients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

type SocketHub struct {
	registry *SubscriptionRegistry
	bus      *EventBus
	jsonLogs bool

	mu      sync.RWMutex
	clients map[string]*SocketClient
	nextID  atomic.Uint64
}

type SocketClient struct {
	id         string
	adapter    string
	protocol   ProtocolAdapter
	remoteAddr string
	connected  time.Time
	conn       *websocket.Conn
	send       chan []byte

	mu            sync.RWMutex
	subscriptions map[string]string
}

type SocketClientSnapshot struct {
	ID            string   `json:"id"`
	Adapter       string   `json:"adapter"`
	RemoteAddr    string   `json:"remote_addr"`
	ConnectedAt   string   `json:"connected_at"`
	Subscriptions []string `json:"subscriptions"`
}

type socketDispatchRequest struct {
	Channel string          `json:"channel"`
	Payload json.RawMessage `json:"payload"`
	Adapter string          `json:"adapter,omitempty"`
}

func NewSocketHub(bus *EventBus, jsonLogs bool) *SocketHub {
	return &SocketHub{
		registry: NewSubscriptionRegistry(),
		bus:      bus,
		jsonLogs: jsonLogs,
		clients:  make(map[string]*SocketClient),
	}
}

func RegisterSocketRoutes(mux *http.ServeMux, hub *SocketHub) {
	mux.HandleFunc("/__ditto__/socket", hub.ServeHTTP)
	mux.HandleFunc("/__ditto__/ws", hub.ServeHTTP)
	mux.HandleFunc("/__ditto__/api/socket/clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"clients": hub.Snapshot()})
	})
	mux.HandleFunc("/__ditto__/api/socket/dispatch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req socketDispatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Channel) == "" {
			http.Error(w, "channel is required", http.StatusBadRequest)
			return
		}
		if len(req.Payload) == 0 {
			req.Payload = json.RawMessage(`{}`)
		}
		delivered := hub.Dispatch(req.Channel, req.Payload, req.Adapter)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"delivered": delivered})
	})
}

func IsWebSocketRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func (h *SocketHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	adapterName := normalizeAdapter(r.URL.Query().Get("adapter"))
	if adapterName == "" {
		adapterName = "raw"
	}
	adapter, err := NewProtocolAdapter(adapterName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}

	id := fmt.Sprintf("ws-%d", h.nextID.Add(1))
	client := &SocketClient{
		id:            id,
		adapter:       adapterName,
		protocol:      adapter,
		remoteAddr:    r.RemoteAddr,
		connected:     time.Now(),
		conn:          conn,
		send:          make(chan []byte, 64),
		subscriptions: make(map[string]string),
	}
	h.addClient(client)
	h.publishSocketEvent("CONNECT", r.URL.RequestURI(), http.StatusSwitchingProtocols, "", 0)

	ctx := r.Context()
	done := make(chan struct{})
	go func() {
		h.writeLoop(ctx, client)
		close(done)
	}()

	h.readLoop(ctx, client)
	h.removeClient(client)
	close(client.send)
	<-done
	_ = conn.Close(websocket.StatusNormalClosure, "")
	h.publishSocketEvent("DISCONNECT", id, 0, "", 0)
}

func (h *SocketHub) Dispatch(channel string, payload json.RawMessage, adapterFilter string) int {
	channel = strings.TrimSpace(channel)
	adapterFilter = normalizeAdapter(adapterFilter)
	ids := h.registry.Clients(channel)
	delivered := 0
	for _, id := range ids {
		client := h.client(id)
		if client == nil {
			continue
		}
		if adapterFilter != "" && client.adapter != adapterFilter {
			continue
		}
		subID := client.subscriptionID(channel)
		data, err := client.protocol.EncodeServerMessage(ServerMsg{
			Type:    "data",
			ID:      subID,
			Channel: channel,
			Payload: payload,
		})
		if err != nil {
			continue
		}
		select {
		case client.send <- data:
			delivered++
		default:
		}
	}
	h.publishSocketEvent("DISPATCH", channel, http.StatusOK, string(payload), 0)
	return delivered
}

func (h *SocketHub) Snapshot() []SocketClientSnapshot {
	h.mu.RLock()
	clients := make([]*SocketClient, 0, len(h.clients))
	for _, client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	sort.Slice(clients, func(i, j int) bool {
		return clients[i].connected.Before(clients[j].connected)
	})

	snapshots := make([]SocketClientSnapshot, 0, len(clients))
	for _, client := range clients {
		snapshots = append(snapshots, SocketClientSnapshot{
			ID:            client.id,
			Adapter:       client.adapter,
			RemoteAddr:    client.remoteAddr,
			ConnectedAt:   client.connected.Format(time.RFC3339),
			Subscriptions: client.subscriptionList(),
		})
	}
	return snapshots
}

func (h *SocketHub) addClient(client *SocketClient) {
	h.mu.Lock()
	h.clients[client.id] = client
	h.mu.Unlock()
}

func (h *SocketHub) removeClient(client *SocketClient) {
	h.registry.RemoveClient(client.id)
	h.mu.Lock()
	delete(h.clients, client.id)
	h.mu.Unlock()
}

func (h *SocketHub) client(id string) *SocketClient {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.clients[id]
}

func (h *SocketHub) readLoop(ctx context.Context, client *SocketClient) {
	for {
		typ, data, err := client.conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText && typ != websocket.MessageBinary {
			continue
		}

		msg, err := client.protocol.ParseClientMessage(data)
		if err != nil {
			h.publishSocketEvent("ERROR", client.id, http.StatusBadRequest, err.Error(), 0)
			continue
		}
		switch msg.Type {
		case "connection_init":
			h.enqueueControl(client, ServerMsg{Type: "connection_ack"})
		case "subscribe":
			channel := strings.TrimSpace(msg.Channel)
			if channel == "" {
				h.publishSocketEvent("ERROR", client.id, http.StatusBadRequest, "subscribe message missing channel", 0)
				continue
			}
			subID := msg.SubscriptionID
			if subID == "" {
				subID = msg.ID
			}
			if subID == "" {
				subID = channel
			}
			client.addSubscription(channel, subID)
			h.registry.Subscribe(channel, client.id)
			h.enqueueControl(client, ServerMsg{Type: "subscribe_ack", ID: subID, Channel: channel})
			h.publishSocketEvent("SUBSCRIBE", channel, http.StatusOK, client.id, 0)
		case "unsubscribe":
			channel := strings.TrimSpace(msg.Channel)
			if channel == "" {
				channel = client.channelForSubscription(msg.ID)
			}
			if channel == "" {
				continue
			}
			client.removeSubscription(channel)
			h.registry.Unsubscribe(channel, client.id)
			h.publishSocketEvent("UNSUBSCRIBE", channel, http.StatusOK, client.id, 0)
		case "ping":
			h.enqueueControl(client, ServerMsg{Type: "pong"})
		}
	}
}

func (h *SocketHub) writeLoop(ctx context.Context, client *SocketClient) {
	heartbeat, heartbeatEvery := client.protocol.Heartbeat()
	if heartbeatEvery <= 0 {
		heartbeatEvery = 30 * time.Second
	}
	heartbeatTicker := time.NewTicker(heartbeatEvery)
	defer heartbeatTicker.Stop()

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-client.send:
			if !ok {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := client.conn.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
		case <-heartbeatTicker.C:
			if len(heartbeat) == 0 {
				continue
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := client.conn.Write(writeCtx, websocket.MessageText, heartbeat)
			cancel()
			if err != nil {
				return
			}
		case <-pingTicker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := client.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (h *SocketHub) enqueueControl(client *SocketClient, msg ServerMsg) {
	data, err := client.protocol.EncodeServerMessage(msg)
	if err != nil {
		return
	}
	select {
	case client.send <- data:
	default:
	}
}

func (h *SocketHub) publishSocketEvent(method, path string, status int, body string, duration int64) {
	event := LogEvent{
		Timestamp:    time.Now().Format("15:04:05"),
		Type:         "SOCKET",
		Method:       method,
		Path:         path,
		Status:       status,
		DurationMs:   duration,
		ResponseBody: body,
	}
	logRequest(h.jsonLogs, event)
	h.bus.Publish(event)
}

func (c *SocketClient) addSubscription(channel, subID string) {
	c.mu.Lock()
	c.subscriptions[channel] = subID
	c.mu.Unlock()
}

func (c *SocketClient) removeSubscription(channel string) {
	c.mu.Lock()
	delete(c.subscriptions, channel)
	c.mu.Unlock()
}

func (c *SocketClient) subscriptionID(channel string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.subscriptions[channel]
}

func (c *SocketClient) channelForSubscription(subID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for channel, id := range c.subscriptions {
		if id == subID {
			return channel
		}
	}
	return ""
}

func (c *SocketClient) subscriptionList() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	channels := make([]string, 0, len(c.subscriptions))
	for channel := range c.subscriptions {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	return channels
}

func NewProtocolAdapter(name string) (ProtocolAdapter, error) {
	switch normalizeAdapter(name) {
	case "", "raw":
		return RawAdapter{}, nil
	case "appsync":
		return AppSyncAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported socket adapter %q", name)
	}
}

func normalizeAdapter(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

type RawAdapter struct{}

func (RawAdapter) ParseClientMessage(b []byte) (ClientMsg, error) {
	var env struct {
		Type    string          `json:"type"`
		Action  string          `json:"action"`
		Op      string          `json:"op"`
		ID      string          `json:"id"`
		Channel string          `json:"channel"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return ClientMsg{}, err
	}
	msgType := firstNonEmpty(env.Type, env.Action, env.Op)
	msgType = normalizeClientMessageType(msgType)
	if msgType == "" && env.Channel != "" {
		msgType = "subscribe"
	}
	if msgType == "" {
		return ClientMsg{}, errors.New("message missing type")
	}
	return ClientMsg{
		Type:           msgType,
		ID:             env.ID,
		Channel:        env.Channel,
		Payload:        env.Payload,
		SubscriptionID: env.ID,
	}, nil
}

func (RawAdapter) EncodeServerMessage(msg ServerMsg) ([]byte, error) {
	switch msg.Type {
	case "data", "":
		if len(msg.Payload) == 0 {
			return []byte(`{}`), nil
		}
		return msg.Payload, nil
	case "connection_ack":
		return json.Marshal(map[string]any{"type": "connection_ack"})
	case "subscribe_ack":
		return json.Marshal(map[string]any{"type": "subscribe_ack", "channel": msg.Channel})
	case "pong":
		return json.Marshal(map[string]any{"type": "pong"})
	default:
		return json.Marshal(map[string]any{"type": msg.Type})
	}
}

func (RawAdapter) Heartbeat() ([]byte, time.Duration) {
	return []byte(`{"type":"ping"}`), 30 * time.Second
}

type AppSyncAdapter struct{}

func (AppSyncAdapter) ParseClientMessage(b []byte) (ClientMsg, error) {
	var env struct {
		Type    string          `json:"type"`
		ID      string          `json:"id"`
		Channel string          `json:"channel"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return ClientMsg{}, err
	}

	msgType := normalizeClientMessageType(env.Type)
	if msgType == "" {
		return ClientMsg{}, errors.New("message missing type")
	}

	channel := strings.TrimSpace(env.Channel)
	if channel == "" && len(env.Payload) > 0 {
		channel = channelFromPayload(env.Payload)
	}

	return ClientMsg{
		Type:           msgType,
		ID:             env.ID,
		Channel:        channel,
		Payload:        env.Payload,
		SubscriptionID: env.ID,
	}, nil
}

func (AppSyncAdapter) EncodeServerMessage(msg ServerMsg) ([]byte, error) {
	switch msg.Type {
	case "connection_ack":
		return json.Marshal(map[string]any{
			"type":    "connection_ack",
			"payload": map[string]any{"connectionTimeoutMs": 300000},
		})
	case "subscribe_ack":
		return json.Marshal(map[string]any{"type": "subscribe_success", "id": msg.ID})
	case "pong":
		return json.Marshal(map[string]any{"type": "pong"})
	case "data", "":
		var payload any = map[string]any{}
		if len(msg.Payload) > 0 {
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				payload = string(msg.Payload)
			}
		}
		return json.Marshal(map[string]any{
			"type": "data",
			"id":   msg.ID,
			"payload": map[string]any{
				"data": payload,
			},
		})
	default:
		return json.Marshal(map[string]any{"type": msg.Type, "id": msg.ID})
	}
}

func (AppSyncAdapter) Heartbeat() ([]byte, time.Duration) {
	return []byte(`{"type":"ka"}`), 5 * time.Second
}

func normalizeClientMessageType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "connection_init", "init":
		return "connection_init"
	case "subscribe", "start":
		return "subscribe"
	case "unsubscribe", "stop", "complete":
		return "unsubscribe"
	case "ping":
		return "ping"
	default:
		return strings.ToLower(strings.TrimSpace(t))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func channelFromPayload(payload json.RawMessage) string {
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return ""
	}
	for _, key := range []string{"channel", "path", "topic"} {
		if value, ok := obj[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if ext, ok := obj["extensions"].(map[string]any); ok {
		for _, key := range []string{"channel", "path", "topic"} {
			if value, ok := ext[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}
