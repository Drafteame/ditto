package main

import (
	"context"
	"encoding/base64"
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

type EncodedPayload struct {
	Data        []byte
	Kind        websocket.MessageType
	Value       any
	ContentType string
	TypeName    string
}

type ProtocolAdapter interface {
	ParseClientMessage(b []byte) (ClientMsg, error)
	EncodePayload(payload json.RawMessage) (EncodedPayload, error)
	WrapData(payload EncodedPayload, subID, channel string) (EncodedServerMessage, error)
	EncodeServerMessage(msg ServerMsg) (EncodedServerMessage, error)
	Heartbeat() (EncodedServerMessage, time.Duration)
	Subprotocols() []string
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
	modes    *ChannelModeRegistry
	live     *LiveBridge
	recorder *Recorder
	events   *CoalescingPublisher

	mu      sync.RWMutex
	clients map[string]*SocketClient
	nextID  atomic.Uint64
}

type SocketClient struct {
	id              string
	adapter         string
	protocol        ProtocolAdapter
	remoteAddr      string
	connected       time.Time
	conn            *websocket.Conn
	control         chan EncodedServerMessage
	send            chan EncodedServerMessage
	done            chan struct{}
	closed          atomic.Bool
	closeOnce       sync.Once
	droppedToClient atomic.Uint64

	mu            sync.RWMutex
	subscriptions map[string]string
}

type SocketClientSnapshot struct {
	ID              string   `json:"id"`
	Adapter         string   `json:"adapter"`
	RemoteAddr      string   `json:"remote_addr"`
	ConnectedAt     string   `json:"connected_at"`
	Subscriptions   []string `json:"subscriptions"`
	DroppedToClient uint64   `json:"dropped_to_client"`
}

type socketDispatchRequest struct {
	Channel  string          `json:"channel"`
	Payload  json.RawMessage `json:"payload"`
	Adapter  string          `json:"adapter,omitempty"`
	TypeName string          `json:"type_name,omitempty"`
}

type SocketDispatchResult struct {
	Delivered int      `json:"delivered"`
	Dropped   []string `json:"dropped,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

type RenderedDispatch struct {
	Channel  string          `json:"channel"`
	Adapter  string          `json:"adapter,omitempty"`
	TypeName string          `json:"type_name,omitempty"`
	Payload  json.RawMessage `json:"payload"`
	Source   string          `json:"source,omitempty"`
	// EncodedPayload is an M4 hook for sequence players that pre-encode a step.
	EncodedPayload *EncodedPayload            `json:"-"`
	Missing        []string                   `json:"missing,omitempty"`
	InvalidCasts   []EventTemplateInvalidCast `json:"invalid_casts,omitempty"`
}

func NewSocketHub(bus *EventBus, jsonLogs bool, modes ...*ChannelModeRegistry) *SocketHub {
	var modeRegistry *ChannelModeRegistry
	if len(modes) > 0 {
		modeRegistry = modes[0]
	}
	hub := &SocketHub{
		registry: NewSubscriptionRegistry(),
		bus:      bus,
		jsonLogs: jsonLogs,
		modes:    modeRegistry,
		events:   NewCoalescingPublisher(bus, jsonLogs),
		clients:  make(map[string]*SocketClient),
	}
	if modeRegistry != nil {
		modeRegistry.OnChange(func(cfg ChannelConfig) {
			if hub.live == nil {
				return
			}
			if cfg.Mode == ModeLive || cfg.Mode == ModeMixed {
				hub.attachLiveSubscribers(cfg.Channel)
				return
			}
			hub.live.DetachChannel(cfg.Channel)
		})
	}
	return hub
}

func (h *SocketHub) SetLiveBridge(live *LiveBridge) {
	h.live = live
}

func (h *SocketHub) SetRecorder(recorder *Recorder) {
	h.recorder = recorder
}

func RegisterSocketRoutes(mux *http.ServeMux, hub *SocketHub, registries ...*SchemaRegistry) {
	var schemas *SchemaRegistry
	if len(registries) > 0 {
		schemas = registries[0]
	}
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
	mux.HandleFunc("/__ditto__/api/socket/adapter-profiles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AdapterProfileSummaries())
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
		if len(req.Payload) == 0 {
			req.Payload = json.RawMessage(`{}`)
		}
		rendered := RenderedDispatch{
			Channel:  req.Channel,
			Adapter:  req.Adapter,
			TypeName: req.TypeName,
			Payload:  req.Payload,
			Source:   "manual",
		}
		result, err := dispatchRendered(hub, schemas, rendered, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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

type dispatchOverrides struct {
	Channel string
	Adapter string
}

func dispatchRendered(hub *SocketHub, schemas *SchemaRegistry, rendered RenderedDispatch, overrides *dispatchOverrides) (SocketDispatchResult, error) {
	if hub == nil {
		return SocketDispatchResult{}, fmt.Errorf("socket hub is not available")
	}
	channel := strings.TrimSpace(rendered.Channel)
	adapter := rendered.Adapter
	if overrides != nil {
		if strings.TrimSpace(overrides.Channel) != "" {
			channel = strings.TrimSpace(overrides.Channel)
		}
		if strings.TrimSpace(overrides.Adapter) != "" {
			adapter = overrides.Adapter
		}
	}
	if channel == "" {
		return SocketDispatchResult{}, fmt.Errorf("channel is required")
	}
	if strings.ContainsAny(channel, "\r\n") {
		return SocketDispatchResult{}, fmt.Errorf("channel cannot contain newlines")
	}
	if hub.modes != nil && !hub.modes.AllowsLocalDispatch(channel) {
		mode := hub.modes.Get(channel).Mode
		msg := fmt.Sprintf("channel mode %s suppressed local dispatch", mode)
		return SocketDispatchResult{Errors: []string{msg}}, nil
	}
	adapter = normalizeAdapter(adapter)
	if _, err := NewProtocolAdapter(adapter); err != nil {
		return SocketDispatchResult{}, err
	}
	typeName := strings.TrimSpace(rendered.TypeName)
	if typeName != "" {
		if schemas == nil {
			return SocketDispatchResult{}, fmt.Errorf("schema registry is not available")
		}
		encoded := rendered.EncodedPayload
		if encoded == nil {
			next, err := schemas.Encode(typeName, rendered.Payload)
			if err != nil {
				return SocketDispatchResult{}, fmt.Errorf("protobuf encode failed: %w", err)
			}
			encoded = &next
		}
		return hub.DispatchEncodedWithSource(channel, *encoded, adapter, rendered.Source), nil
	}
	payload := rendered.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	return hub.DispatchWithSource(channel, payload, adapter, rendered.Source), nil
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
		Subprotocols:       adapter.Subprotocols(),
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
		control:       make(chan EncodedServerMessage, 16),
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
	return h.DispatchWithSource(channel, payload, adapterFilter, "")
}

func (h *SocketHub) DispatchWithSource(channel string, payload json.RawMessage, adapterFilter, source string) SocketDispatchResult {
	return h.dispatch(channel, adapterFilter, source, func(client *SocketClient) (EncodedPayload, error) {
		return client.protocol.EncodePayload(payload)
	})
}

func (h *SocketHub) DispatchEncoded(channel string, payload EncodedPayload, adapterFilter string) SocketDispatchResult {
	return h.DispatchEncodedWithSource(channel, payload, adapterFilter, "")
}

func (h *SocketHub) DispatchEncodedWithSource(channel string, payload EncodedPayload, adapterFilter, source string) SocketDispatchResult {
	return h.dispatch(channel, adapterFilter, source, func(client *SocketClient) (EncodedPayload, error) {
		return payload, nil
	})
}

func (h *SocketHub) dispatch(channel string, adapterFilter string, source string, encode func(client *SocketClient) (EncodedPayload, error)) SocketDispatchResult {
	channel = strings.TrimSpace(channel)
	adapterFilter = normalizeAdapter(adapterFilter)
	ids := h.registry.Clients(channel)
	result := SocketDispatchResult{}
	type adapterPayload struct {
		payload EncodedPayload
		err     error
	}
	payloadCache := make(map[string]adapterPayload)
	recordedAdapters := make(map[string]struct{})
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
		cached, ok := payloadCache[client.adapter]
		if !ok {
			encoded, err := encode(client)
			cached = adapterPayload{payload: encoded, err: err}
			payloadCache[client.adapter] = cached
		}
		if cached.err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", client.id, cached.err))
			continue
		}
		data, err := client.protocol.WrapData(cached.payload, subID, channel)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", client.id, err))
			continue
		}
		if data.Kind == 0 {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: adapter returned empty websocket message type", client.id))
			continue
		}
		if h.recorder != nil && h.isRecordingMode(channel) {
			if _, recorded := recordedAdapters[client.adapter]; !recorded {
				cfg := h.modes.Get(channel)
				h.recorder.Record(RecordFrameInput{
					Channel: channel, Direction: "local", Kind: frameKind(data.Kind), Data: data.Data,
					Adapter: client.adapter, RateCapHz: cfg.RateCapHz,
				})
				recordedAdapters[client.adapter] = struct{}{}
			}
		}
		if client.enqueue(data, 0) {
			result.Delivered++
		} else {
			result.Dropped = append(result.Dropped, client.id)
		}
	}
	h.publishSocketEventWithSource("DISPATCH", channel, http.StatusOK, dispatchSummary(result), 0, source)
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
			ID:              client.id,
			Adapter:         client.adapter,
			RemoteAddr:      client.remoteAddr,
			ConnectedAt:     client.connected.Format(time.RFC3339),
			Subscriptions:   client.subscriptionList(),
			DroppedToClient: client.droppedToClient.Load(),
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
	for _, channel := range client.subscriptionList() {
		if h.live != nil {
			h.live.Detach(channel, client.id)
		}
	}
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
			if h.forwardToLiveSubscriptions(ctx, client, typ, data) {
				continue
			}
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
			if h.isLiveMode(channel) && h.live != nil {
				h.live.Attach(channel, client)
			}
			h.forwardLiveFromClient(ctx, client, channel, typ, data)
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
			if h.live != nil {
				h.live.Detach(channel, client.id)
			}
			h.publishSocketEvent("UNSUBSCRIBE", channel, http.StatusOK, client.id, 0)
		case "ping":
			h.enqueueControl(client, ServerMsg{Type: "pong"})
		default:
			channel := strings.TrimSpace(msg.Channel)
			if channel == "" {
				channel = strings.TrimSpace(msg.SubscriptionID)
			}
			if channel != "" {
				h.forwardLiveFromClient(ctx, client, channel, typ, data)
			}
		}
	}
}

func (h *SocketHub) forwardToLiveSubscriptions(ctx context.Context, client *SocketClient, typ websocket.MessageType, data []byte) bool {
	for _, channel := range client.subscriptionList() {
		if h.isLiveMode(channel) {
			h.forwardLiveFromClient(ctx, client, channel, typ, data)
			return true
		}
	}
	return false
}

func (h *SocketHub) isLiveMode(channel string) bool {
	if h.modes == nil {
		return false
	}
	mode := h.modes.Get(channel).Mode
	return mode == ModeLive || mode == ModeMixed
}

func (h *SocketHub) isRecordingMode(channel string) bool {
	if h.modes == nil {
		return false
	}
	mode := h.modes.Get(channel).Mode
	return mode == ModeRecord || mode == ModeMixed
}

func (h *SocketHub) forwardLiveFromClient(ctx context.Context, client *SocketClient, channel string, typ websocket.MessageType, data []byte) {
	if !h.isLiveMode(channel) {
		return
	}
	if h.recorder != nil && h.modes.Get(channel).Mode == ModeMixed {
		h.recorder.Record(RecordFrameInput{
			Channel: channel, Direction: "local", Kind: frameKind(typ), Data: data,
			Adapter: client.adapter, RateCapHz: h.modes.Get(channel).RateCapHz,
		})
	}
	if h.live == nil {
		h.publishSocketEventWithSource("ERROR", channel, http.StatusServiceUnavailable, "live target is not configured", 0, "live-disconnected")
		return
	}
	h.live.ForwardFromClient(ctx, channel, typ, data)
}

func (h *SocketHub) attachLiveSubscribers(channel string) {
	if h.live == nil {
		return
	}
	// Clients returns a snapshot; a client can disconnect before lookup below.
	for _, id := range h.registry.Clients(channel) {
		if client := h.client(id); client != nil {
			h.live.Attach(channel, client)
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

	writeMsg := func(msg EncodedServerMessage) bool {
		if msg.Kind == 0 {
			return false
		}
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := client.conn.Write(writeCtx, msg.Kind, msg.Data)
		cancel()
		return err == nil
	}

	for {
		select {
		case msg := <-client.control:
			if !writeMsg(msg) {
				return
			}
			continue
		default:
		}

		select {
		case <-ctx.Done():
			return
		case <-client.done:
			return
		case msg := <-client.control:
			if !writeMsg(msg) {
				return
			}
		case msg := <-client.send:
			if !writeMsg(msg) {
				return
			}
		case <-heartbeatC:
			if !writeMsg(heartbeat) {
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
	if !client.enqueueOn(client.control, data, 500*time.Millisecond) {
		h.publishSocketEvent("ERROR", client.id, http.StatusServiceUnavailable, "control message dropped", 0)
	}
}

func (h *SocketHub) publishSocketEvent(method, path string, status int, body string, duration int64) {
	h.publishSocketEventWithSource(method, path, status, body, duration, "")
}

func (h *SocketHub) publishSocketEventWithSource(method, path string, status int, body string, duration int64, source string) {
	event := LogEvent{
		Timestamp:    time.Now().Format("15:04:05"),
		Type:         "SOCKET",
		Method:       method,
		Path:         path,
		Status:       status,
		DurationMs:   duration,
		ResponseBody: body,
		Source:       strings.TrimSpace(source),
	}
	h.events.Publish(event)
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
		c.closed.Store(true)
		close(c.done)
	})
}

func (c *SocketClient) enqueue(msg EncodedServerMessage, timeout time.Duration) bool {
	return c.enqueueOn(c.send, msg, timeout)
}

func (c *SocketClient) enqueueOn(ch chan EncodedServerMessage, msg EncodedServerMessage, timeout time.Duration) bool {
	if msg.Kind == 0 {
		return false
	}
	if ch == nil || c.closed.Load() {
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
		case ch <- msg:
			return true
		default:
			if ch == c.send {
				c.droppedToClient.Add(1)
			}
			return false
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c.done:
		return false
	case ch <- msg:
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
		var value any
		if err := json.Unmarshal(raw, &value); err == nil {
			if text, ok := value.(string); ok && text != "" {
				message = text
			} else if obj, ok := value.(map[string]any); ok {
				if value, ok := obj["error"].(string); ok && value != "" {
					message = value
				} else if value, ok := obj["message"].(string); ok && value != "" {
					message = value
				}
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
	adapterName := normalizeAdapter(name)
	if adapterName == "" {
		adapterName = "raw"
	}
	if profile, ok := adapterProfile(adapterName); ok {
		return NewProfileAdapter(profile)
	}
	return newBuiltinProtocolAdapter(adapterName)
}

func newBuiltinProtocolAdapter(name string) (ProtocolAdapter, error) {
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
		payload, err := RawAdapter{}.EncodePayload(msg.Payload)
		if err != nil {
			return EncodedServerMessage{}, err
		}
		return RawAdapter{}.WrapData(payload, msg.ID, msg.Channel)
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

func (RawAdapter) EncodePayload(payload json.RawMessage) (EncodedPayload, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	return EncodedPayload{
		Data: append([]byte(nil), payload...),
		Kind: websocket.MessageText,
	}, nil
}

func (RawAdapter) WrapData(payload EncodedPayload, subID, channel string) (EncodedServerMessage, error) {
	return EncodedServerMessage{Data: payload.Data, Kind: payload.Kind}, nil
}

func (RawAdapter) Heartbeat() (EncodedServerMessage, time.Duration) {
	return textMessage([]byte(`{"type":"ping"}`)), 30 * time.Second
}

func (RawAdapter) Subprotocols() []string {
	return nil
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
		payload, err := AppSyncAdapter{}.EncodePayload(msg.Payload)
		if err != nil {
			return EncodedServerMessage{}, err
		}
		return AppSyncAdapter{}.WrapData(payload, msg.ID, msg.Channel)
	default:
		return marshalTextMessage(map[string]any{"type": msg.Type, "id": msg.ID})
	}
}

func (AppSyncAdapter) EncodePayload(payload json.RawMessage) (EncodedPayload, error) {
	var value any = map[string]any{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &value); err != nil {
			value = string(payload)
		}
	}
	return EncodedPayload{Value: value, Kind: websocket.MessageText}, nil
}

func (AppSyncAdapter) WrapData(payload EncodedPayload, subID, channel string) (EncodedServerMessage, error) {
	value := payload.Value
	if payload.Kind == websocket.MessageBinary {
		value = map[string]any{
			"base64":       base64.StdEncoding.EncodeToString(payload.Data),
			"content_type": payload.ContentType,
			"type_name":    payload.TypeName,
		}
	}
	return marshalTextMessage(map[string]any{
		"type": "data",
		"id":   subID,
		"payload": map[string]any{
			"data": value,
		},
	})
}

func (AppSyncAdapter) Heartbeat() (EncodedServerMessage, time.Duration) {
	return textMessage([]byte(`{"type":"ka"}`)), 5 * time.Second
}

func (AppSyncAdapter) Subprotocols() []string {
	return []string{"aws-appsync-event-ws"}
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
