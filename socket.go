package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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

type EncodedServerMessage struct {
	Data []byte
	Kind websocket.MessageType
}

type ProtocolAdapter interface {
	ParseClientMessage(b []byte) (ClientMsg, error)
	EncodeServerMessage(msg ServerMsg) (EncodedServerMessage, error)
	Heartbeat() (EncodedServerMessage, time.Duration)
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
	send       chan EncodedServerMessage
	done       chan struct{}
	closeOnce  sync.Once

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

type SocketDispatchResult struct {
	Delivered int      `json:"delivered"`
	Dropped   []string `json:"dropped,omitempty"`
	Errors    []string `json:"errors,omitempty"`
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
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
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
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if !hasJSONContentType(r) {
			http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
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
		result := hub.Dispatch(req.Channel, req.Payload, req.Adapter)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
}

func IsWebSocketRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func shouldProxyWebSocket(r *http.Request) bool {
	mode := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Ditto-WS-Mode")))
	queryMode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("__ditto_ws")))
	return mode == "proxy" || mode == "live" || queryMode == "proxy" || queryMode == "live"
}

func hasJSONContentType(r *http.Request) bool {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	return contentType == "application/json" || strings.HasPrefix(contentType, "application/json;")
}

func isAllowedSocketAPIRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return isLoopbackRemote(r.RemoteAddr)
	}
	return isAllowedOriginForRequest(origin, r.Host, r.RemoteAddr)
}

func isAllowedOriginForRequest(raw, requestHost, remoteAddr string) bool {
	if isLoopbackRemote(remoteAddr) && isLocalDevOrigin(raw) {
		return true
	}
	return isSameOrigin(raw, requestHost)
}

func isLocalDevOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" ||
		host == "127.0.0.1" ||
		host == "::1" ||
		host == "wails.localhost" ||
		strings.HasSuffix(host, ".localhost")
}

func isSameOrigin(raw, requestHost string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, requestHost)
}

func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost")
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
	if !isAllowedSocketAPIRequest(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
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
		send:          make(chan EncodedServerMessage, 64),
		done:          make(chan struct{}),
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
	client.close()
	h.removeClient(client)
	<-done
	_ = conn.Close(websocket.StatusNormalClosure, "")
	h.publishSocketEvent("DISCONNECT", id, 0, "", 0)
}

func (h *SocketHub) Dispatch(channel string, payload json.RawMessage, adapterFilter string) SocketDispatchResult {
	channel = strings.TrimSpace(channel)
	adapterFilter = normalizeAdapter(adapterFilter)
	ids := h.registry.Clients(channel)
	result := SocketDispatchResult{}
	for _, id := range ids {
		client := h.client(id)
		if client == nil {
			result.Dropped = append(result.Dropped, id)
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
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", client.id, err))
			continue
		}
		if data.Kind == 0 {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: adapter returned empty websocket message type", client.id))
			continue
		}
		if client.enqueue(data, 0) {
			result.Delivered++
		} else {
			result.Dropped = append(result.Dropped, client.id)
		}
	}
	h.publishSocketEvent("DISPATCH", channel, http.StatusOK, dispatchSummary(result), 0)
	return result
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
				h.enqueueControl(client, ServerMsg{Type: "error", ID: msg.ID, Payload: json.RawMessage(`{"error":"subscribe message missing channel"}`)})
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
	var heartbeatC <-chan time.Time
	var heartbeatTicker *time.Ticker
	if len(heartbeat.Data) > 0 {
		if heartbeatEvery <= 0 {
			heartbeatEvery = 30 * time.Second
		}
		heartbeatTicker = time.NewTicker(heartbeatEvery)
		heartbeatC = heartbeatTicker.C
		defer heartbeatTicker.Stop()
	}

	pingTicker := time.NewTicker(75 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-client.done:
			return
		case msg := <-client.send:
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := client.conn.Write(writeCtx, msg.Kind, msg.Data)
			cancel()
			if err != nil {
				return
			}
		case <-heartbeatC:
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := client.conn.Write(writeCtx, heartbeat.Kind, heartbeat.Data)
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
		h.publishSocketEvent("ERROR", client.id, http.StatusBadRequest, err.Error(), 0)
		return
	}
	if data.Kind == 0 {
		h.publishSocketEvent("ERROR", client.id, http.StatusBadRequest, "adapter returned empty websocket message type", 0)
		return
	}
	if !client.enqueue(data, 2*time.Second) {
		h.publishSocketEvent("ERROR", client.id, http.StatusServiceUnavailable, "control message dropped", 0)
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

func dispatchSummary(result SocketDispatchResult) string {
	data, err := json.Marshal(map[string]any{
		"delivered": result.Delivered,
		"dropped":   len(result.Dropped),
		"errors":    len(result.Errors),
	})
	if err != nil {
		return ""
	}
	return string(data)
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

func (c *SocketClient) close() {
	c.closeOnce.Do(func() {
		close(c.done)
	})
}

func (c *SocketClient) enqueue(msg EncodedServerMessage, timeout time.Duration) bool {
	if msg.Kind == 0 {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
	}
	if timeout <= 0 {
		select {
		case <-c.done:
			return false
		case c.send <- msg:
			return true
		default:
			return false
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c.done:
		return false
	case c.send <- msg:
		return true
	case <-timer.C:
		return false
	}
}

func textMessage(data []byte) EncodedServerMessage {
	return EncodedServerMessage{Data: data, Kind: websocket.MessageText}
}

func marshalTextMessage(v any) (EncodedServerMessage, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return EncodedServerMessage{}, err
	}
	return textMessage(data), nil
}

func rawPayload(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

func appSyncErrorPayload(raw json.RawMessage) any {
	message := "socket error"
	if len(raw) > 0 {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err == nil {
			if value, ok := obj["error"].(string); ok && value != "" {
				message = value
			} else if value, ok := obj["message"].(string); ok && value != "" {
				message = value
			}
		} else {
			message = string(raw)
		}
	}
	return map[string]any{
		"errors": []map[string]string{{"message": message}},
	}
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

func (RawAdapter) EncodeServerMessage(msg ServerMsg) (EncodedServerMessage, error) {
	switch msg.Type {
	case "data", "":
		if len(msg.Payload) == 0 {
			return textMessage([]byte(`{}`)), nil
		}
		return textMessage(msg.Payload), nil
	case "connection_ack":
		return marshalTextMessage(map[string]any{"type": "connection_ack"})
	case "subscribe_ack":
		return marshalTextMessage(map[string]any{"type": "subscribe_ack", "channel": msg.Channel})
	case "pong":
		return marshalTextMessage(map[string]any{"type": "pong"})
	case "error":
		return marshalTextMessage(map[string]any{"type": "error", "id": msg.ID, "payload": rawPayload(msg.Payload)})
	default:
		return marshalTextMessage(map[string]any{"type": msg.Type})
	}
}

func (RawAdapter) Heartbeat() (EncodedServerMessage, time.Duration) {
	return textMessage([]byte(`{"type":"ping"}`)), 30 * time.Second
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

func (AppSyncAdapter) EncodeServerMessage(msg ServerMsg) (EncodedServerMessage, error) {
	switch msg.Type {
	case "connection_ack":
		return marshalTextMessage(map[string]any{
			"type":    "connection_ack",
			"payload": map[string]any{"connectionTimeoutMs": 300000},
		})
	case "subscribe_ack":
		return marshalTextMessage(map[string]any{"type": "subscribe_success", "id": msg.ID})
	case "pong":
		return marshalTextMessage(map[string]any{"type": "pong"})
	case "error":
		return marshalTextMessage(map[string]any{"type": "error", "id": msg.ID, "payload": appSyncErrorPayload(msg.Payload)})
	case "data", "":
		var payload any = map[string]any{}
		if len(msg.Payload) > 0 {
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				payload = string(msg.Payload)
			}
		}
		return marshalTextMessage(map[string]any{
			"type": "data",
			"id":   msg.ID,
			"payload": map[string]any{
				"data": payload,
			},
		})
	default:
		return marshalTextMessage(map[string]any{"type": msg.Type, "id": msg.ID})
	}
}

func (AppSyncAdapter) Heartbeat() (EncodedServerMessage, time.Duration) {
	return textMessage([]byte(`{"type":"ka"}`)), 5 * time.Second
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
