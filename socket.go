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
	"unicode/utf8"

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
	Data           []byte
	Kind           websocket.MessageType
	DispatchID     string
	Channel        string
	SubscriptionID string
	Source         string
	ControlType    string
	TypeName       string
	Target         string
	ClientID       string
	Adapter        string
	Subprotocol    string
	Direction      string
}

type EncodedPayload struct {
	Data        []byte
	Kind        websocket.MessageType
	Value       any
	ContentType string
	TypeName    string
}

type GreetingAdapter interface {
	GreetsOnConnect() bool
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
	schemas  *SchemaRegistry
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
	upstreamHeaders http.Header
	upstreamHost    string
	url             string
	host            string
	protocolName    string
	subprotocol     string
	requestHeaders  http.Header

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
	Queued    int      `json:"queued"`
	Dropped   []string `json:"dropped,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

const dispatchPayloadMaxBytes = 4096

// Payload is an object/array/scalar when complete. If Truncated is true, it is
// a JSON string containing the first dispatchPayloadMaxBytes bytes.
type DispatchLogBody struct {
	Delivered   int             `json:"delivered"`
	Queued      int             `json:"queued"`
	DispatchID  string          `json:"dispatch_id,omitempty"`
	Dropped     int             `json:"dropped"`
	Errors      int             `json:"errors"`
	TypeName    string          `json:"type_name,omitempty"`
	Alias       string          `json:"alias,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	DecodeError string          `json:"decode_error,omitempty"`
	Truncated   bool            `json:"truncated,omitempty"`
}

type dispatchDecodeHint struct {
	TypeName       string
	Payload        json.RawMessage
	RawData        []byte
	RawKind        websocket.MessageType
	RawContentType string
}

var nextSocketDispatchID atomic.Uint64

func newSocketDispatchID() string { return fmt.Sprintf("dispatch-%d", nextSocketDispatchID.Add(1)) }

func (h *SocketHub) currentSocketMode(channel string) string {
	if h == nil || h.modes == nil || channel == "" {
		return ""
	}
	return string(h.modes.Get(channel).Mode)
}

func socketFramePreview(raw []byte, kind websocket.MessageType) (string, *LogPayloadMetadata) {
	contentType := "application/octet-stream"
	if kind == websocket.MessageText {
		contentType = "text/plain; charset=utf-8"
	}
	preview, metadata := captureLogPayload(raw, int64(len(raw)), contentType, "", nil)
	return truncateSocketPreview(preview), metadata
}

func truncateSocketPreview(preview string) string {
	if len(preview) > dispatchPayloadMaxBytes {
		preview = preview[:dispatchPayloadMaxBytes]
		for !utf8.ValidString(preview) {
			preview = preview[:len(preview)-1]
		}
	}
	return preview
}

func (h *SocketHub) logSocketFrame(event LogEvent, data []byte, kind websocket.MessageType, typeName string) {
	event.Type = "SOCKET"
	event.FrameKind = frameKind(kind)
	event.TypeName = typeName
	preview, metadata := socketFramePreview(data, kind)
	if event.Direction == "client_to_ditto" || event.Direction == "client_to_upstream" || event.Direction == "ditto_to_upstream" {
		event.RequestBody, event.RequestPayload = preview, metadata
	} else {
		event.ResponseBody, event.ResponsePayload = preview, metadata
	}
	decoded, decodeErr := h.decodeSocketFrame(kind, data, event.Adapter, typeName)
	if decoded != nil {
		event.TypeName = decoded.TypeName
		event.Alias = decoded.Alias
		if len(decoded.PayloadJSON) > 0 {
			event.DecodedPayload, event.DecodedTruncated = boundedSocketJSON(decoded.PayloadJSON)
		}
	}
	if decodeErr != "" {
		event.DecodeError = decodeErr
	}
	h.events.Publish(event)
}

func (h *SocketHub) decodeSocketFrame(kind websocket.MessageType, data []byte, adapter, typeName string) (*DecodedFrame, string) {
	if kind == websocket.MessageBinary && typeName != "" {
		decoded := &DecodedFrame{TypeName: typeName}
		if h.schemas == nil {
			return decoded, "schema not loaded"
		}
		payload, err := h.schemas.Decode(typeName, data)
		if err != nil {
			return decoded, err.Error()
		}
		decoded.PayloadJSON = payload
		return decoded, ""
	}
	return DecodeWireFrame(h.schemas, frameKind(kind), data, adapter)
}

type adapterPayload struct {
	payload EncodedPayload
	err     error
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

func NewSocketHub(bus *EventBus, jsonLogs bool, modeRegistry *ChannelModeRegistry) *SocketHub {
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

func (h *SocketHub) SetSchemas(schemas *SchemaRegistry) {
	h.schemas = schemas
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

// extractUpstreamHeaders clones the inbound client headers, dropping the ones
// that the WebSocket handshake or HTTP transport must control on the upstream
// dial. Everything else (Authorization, Cookie, Origin, X-* etc.) is forwarded
// verbatim so the upstream sees the same request the client would send directly.
func extractUpstreamHeaders(src http.Header) http.Header {
	if len(src) == 0 {
		return nil
	}
	out := make(http.Header, len(src))
	for k, vs := range src {
		canonical := http.CanonicalHeaderKey(k)
		if strings.HasPrefix(canonical, "Sec-Websocket-") {
			continue
		}
		switch canonical {
		case "Connection",
			"Upgrade",
			"Host",
			"Content-Length",
			"X-Ditto-Ws-Mode":
			continue
		}
		copied := make([]string, len(vs))
		copy(copied, vs)
		out[canonical] = copied
	}
	return out
}

// applyForwardingHeaders annotates the headers Ditto sends to the live target
// with the standard proxy markers so the upstream can see the original client.
// Existing values are extended (X-Forwarded-For is appended) rather than
// replaced, matching common reverse-proxy behaviour.
func applyForwardingHeaders(headers http.Header, clientHost, clientAddr string, tls bool) http.Header {
	if headers == nil {
		headers = make(http.Header)
	}
	if ip := clientIPFromRemoteAddr(clientAddr); ip != "" {
		if prev := headers.Get("X-Forwarded-For"); prev != "" {
			headers.Set("X-Forwarded-For", prev+", "+ip)
		} else {
			headers.Set("X-Forwarded-For", ip)
		}
		if headers.Get("X-Real-IP") == "" {
			headers.Set("X-Real-IP", ip)
		}
	}
	if clientHost != "" && headers.Get("X-Forwarded-Host") == "" {
		headers.Set("X-Forwarded-Host", clientHost)
	}
	if headers.Get("X-Forwarded-Proto") == "" {
		if tls {
			headers.Set("X-Forwarded-Proto", "wss")
		} else {
			headers.Set("X-Forwarded-Proto", "ws")
		}
	}
	return headers
}

func clientIPFromRemoteAddr(addr string) string {
	if addr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
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
	fail := func(reason string, suppressed bool) {
		hub.logDispatchFailure(channel, rendered.Source, rendered.TypeName, rendered.Payload, reason, suppressed)
	}
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
		err := fmt.Errorf("channel is required")
		fail(err.Error(), false)
		return SocketDispatchResult{}, err
	}
	if strings.ContainsAny(channel, "\r\n") {
		err := fmt.Errorf("channel cannot contain newlines")
		fail(err.Error(), false)
		return SocketDispatchResult{}, err
	}
	if hub.modes != nil && !hub.modes.AllowsLocalDispatch(channel) {
		mode := hub.modes.Get(channel).Mode
		msg := fmt.Sprintf("channel mode %s suppressed local dispatch", mode)
		fail(msg, true)
		return SocketDispatchResult{Errors: []string{msg}}, nil
	}
	adapter = normalizeAdapter(adapter)
	if _, err := NewProtocolAdapter(adapter); err != nil {
		fail(err.Error(), false)
		return SocketDispatchResult{}, err
	}
	typeName := strings.TrimSpace(rendered.TypeName)
	if typeName != "" {
		if schemas == nil {
			err := fmt.Errorf("schema registry is not available")
			fail(err.Error(), false)
			return SocketDispatchResult{}, err
		}
		encoded := rendered.EncodedPayload
		if encoded == nil {
			next, err := schemas.Encode(typeName, rendered.Payload)
			if err != nil {
				wrapped := fmt.Errorf("protobuf encode failed: %w", err)
				fail(wrapped.Error(), false)
				return SocketDispatchResult{}, wrapped
			}
			encoded = &next
		}
		return hub.DispatchEncodedWithSourcePayload(channel, *encoded, adapter, rendered.Source, rendered.Payload), nil
	}
	payload := rendered.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	return hub.DispatchWithSource(channel, payload, adapter, rendered.Source), nil
}

func (h *SocketHub) logDispatchFailure(channel, source, typeName string, payload json.RawMessage, reason string, suppressed bool) {
	method, state := "DISPATCH_FAILED", "error"
	if suppressed {
		method, state = "DISPATCH_SUPPRESSED", "suppressed"
	}
	id := newSocketDispatchID()
	event := LogEvent{Type: "SOCKET", Method: method, Path: channel, Channel: channel, Status: http.StatusServiceUnavailable,
		Source: source, DispatchID: id, DeliveryState: state, TypeName: typeName, Error: reason,
		Mode: h.currentSocketMode(channel)}
	if !suppressed {
		event.Errors = 1
	}
	if len(payload) > 0 {
		preview, metadata := captureLogPayload(payload, int64(len(payload)), "application/json", "", nil)
		event.RequestBody, event.RequestPayload = preview, metadata
		decoded, decodeErr := DecodeWireFrame(h.schemas, "text", payload, "raw")
		if decoded != nil {
			event.TypeName, event.Alias = decoded.TypeName, decoded.Alias
			event.DecodedPayload, event.DecodedTruncated = boundedSocketJSON(decoded.PayloadJSON)
		}
		if decodeErr != "" && event.DecodeError == "" {
			event.DecodeError = decodeErr
		}
	}
	h.events.Publish(event)
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
	start := time.Now()
	connectionID := fmt.Sprintf("ws-%d", h.nextID.Add(1))
	requestURI := r.RequestURI
	if requestURI == "" {
		requestURI = r.URL.RequestURI()
	}
	requestURL := socketRequestURL(r, requestURI)
	requestHeaders := r.Header.Clone()
	capture := newResponseCapture(w)
	logHandshakeFailure := func(err error) {
		body, metadata := capture.responsePayload()
		event := LogEvent{Type: "SOCKET", Method: "HANDSHAKE_ERROR", Path: requestURI, URL: requestURL,
			Host: r.Host, RemoteAddr: r.RemoteAddr, Protocol: r.Proto, Status: capture.statusCode,
			DurationMs: time.Since(start).Milliseconds(), Source: "socket-hub", ConnectionID: connectionID,
			ClientID: connectionID, RequestHeaders: requestHeaders, ResponseHeaders: capture.responseHeaders(),
			ResponseBody: body, ResponsePayload: metadata}
		if err != nil {
			event.Error = err.Error()
		}
		h.events.Publish(event)
	}

	adapterName := normalizeAdapter(r.URL.Query().Get("adapter"))
	if adapterName == "" {
		adapterName = "raw"
	}
	adapter, err := NewProtocolAdapter(adapterName)
	if err != nil {
		http.Error(capture, err.Error(), http.StatusBadRequest)
		logHandshakeFailure(err)
		return
	}
	if !isAllowedSocketAPIRequest(r) {
		err := errors.New("origin not allowed")
		http.Error(capture, err.Error(), http.StatusForbidden)
		logHandshakeFailure(err)
		return
	}

	conn, err := websocket.Accept(capture, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
		Subprotocols:       adapter.Subprotocols(),
	})
	if err != nil {
		logHandshakeFailure(err)
		return
	}

	client := &SocketClient{
		id:              connectionID,
		adapter:         adapterName,
		protocol:        adapter,
		remoteAddr:      r.RemoteAddr,
		connected:       time.Now(),
		conn:            conn,
		control:         make(chan EncodedServerMessage, 16),
		send:            make(chan EncodedServerMessage, 64),
		done:            make(chan struct{}),
		subscriptions:   make(map[string]string),
		upstreamHeaders: extractUpstreamHeaders(r.Header),
		upstreamHost:    r.Host,
		url:             requestURL,
		host:            r.Host,
		protocolName:    r.Proto,
		subprotocol:     conn.Subprotocol(),
		requestHeaders:  requestHeaders,
	}
	h.addClient(client)
	h.events.Publish(LogEvent{Type: "SOCKET", Method: "CONNECT", Path: requestURI, URL: requestURL, Host: r.Host,
		RemoteAddr: r.RemoteAddr, Protocol: r.Proto, Status: capture.statusCode, DurationMs: time.Since(start).Milliseconds(),
		Source: "socket-hub", ConnectionID: connectionID, ClientID: connectionID, Adapter: adapterName,
		Subprotocol: conn.Subprotocol(), RequestHeaders: requestHeaders, ResponseHeaders: capture.responseHeaders()})

	ctx, cancel := context.WithCancel(r.Context())
	writeDone := make(chan error, 1)
	go func() {
		err := h.writeLoop(ctx, client)
		if err != nil && !errors.Is(err, context.Canceled) {
			cancel()
			_ = conn.Close(websocket.StatusGoingAway, "writer stopped")
		}
		writeDone <- err
	}()

	if adapterGreetsOnConnect(adapter) {
		h.enqueueControl(client, ServerMsg{Type: "connection_ack"})
	}

	readErr := h.readLoop(ctx, client)
	cancel()
	client.close()
	h.removeClient(client)
	writeErr := <-writeDone
	closeCode, closeReason := socketCloseDetails(readErr)
	if closeCode < 0 && writeErr != nil {
		closeCode, closeReason = int(websocket.StatusInternalError), writeErr.Error()
	}
	if closeCode < 0 {
		closeCode = int(websocket.StatusAbnormalClosure)
	}
	if closeCode != int(websocket.StatusAbnormalClosure) {
		_ = conn.Close(websocket.StatusCode(closeCode), closeReason)
	}
	closeEvent := LogEvent{Type: "SOCKET", Method: "CLOSE", Path: requestURI, URL: requestURL, Host: r.Host,
		RemoteAddr: r.RemoteAddr, Protocol: r.Proto, Status: closeCode, DurationMs: time.Since(client.connected).Milliseconds(),
		Source: "socket-hub", ConnectionID: connectionID, ClientID: connectionID, Adapter: adapterName,
		Subprotocol: client.subprotocol, CloseCode: closeCode, CloseReason: closeReason}
	if readErr != nil && websocket.CloseStatus(readErr) == -1 && !errors.Is(readErr, context.Canceled) {
		closeEvent.Error = readErr.Error()
	}
	if writeErr != nil && !errors.Is(writeErr, context.Canceled) {
		closeEvent.Error = writeErr.Error()
	}
	h.events.Publish(closeEvent)
}

func socketRequestURL(r *http.Request, requestURI string) string {
	if parsed, err := url.ParseRequestURI(requestURI); err == nil && parsed.IsAbs() {
		return parsed.String()
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + requestURI
}

func socketCloseDetails(err error) (int, string) {
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		return int(closeErr.Code), closeErr.Reason
	}
	if err == nil || errors.Is(err, context.Canceled) {
		return int(websocket.StatusNormalClosure), ""
	}
	return -1, err.Error()
}

func (h *SocketHub) Dispatch(channel string, payload json.RawMessage, adapterFilter string) SocketDispatchResult {
	return h.DispatchWithSource(channel, payload, adapterFilter, "")
}

func (h *SocketHub) DispatchWithSource(channel string, payload json.RawMessage, adapterFilter, source string) SocketDispatchResult {
	return h.dispatch(channel, adapterFilter, source, dispatchDecodeHint{Payload: payload}, func(client *SocketClient) (EncodedPayload, error) {
		return client.protocol.EncodePayload(payload)
	})
}

func (h *SocketHub) DispatchEncoded(channel string, payload EncodedPayload, adapterFilter string) SocketDispatchResult {
	return h.DispatchEncodedWithSource(channel, payload, adapterFilter, "")
}

func (h *SocketHub) DispatchEncodedWithSource(channel string, payload EncodedPayload, adapterFilter, source string) SocketDispatchResult {
	return h.DispatchEncodedWithSourcePayload(channel, payload, adapterFilter, source, nil)
}

func (h *SocketHub) DispatchEncodedWithSourcePayload(channel string, payload EncodedPayload, adapterFilter, source string, logicalPayload json.RawMessage) SocketDispatchResult {
	return h.dispatch(channel, adapterFilter, source, dispatchDecodeHint{TypeName: payload.TypeName, Payload: logicalPayload,
		RawData: payload.Data, RawKind: payload.Kind, RawContentType: payload.ContentType}, func(client *SocketClient) (EncodedPayload, error) {
		return payload, nil
	})
}

func (h *SocketHub) dispatch(channel string, adapterFilter string, source string, hint dispatchDecodeHint, encode func(client *SocketClient) (EncodedPayload, error)) SocketDispatchResult {
	channel = strings.TrimSpace(channel)
	adapterFilter = normalizeAdapter(adapterFilter)
	dispatchID := newSocketDispatchID()
	ids := h.registry.Clients(channel)
	result := SocketDispatchResult{Queued: 0}
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
		data.DispatchID, data.Channel, data.SubscriptionID = dispatchID, channel, subID
		data.Source, data.TypeName = source, cached.payload.TypeName
		if client.enqueue(data, 0) {
			result.Delivered++
			result.Queued++
			h.logClientDelivery(client, data, "queued", nil)
		} else {
			result.Dropped = append(result.Dropped, client.id)
			h.logClientDelivery(client, data, "dropped", errors.New("client send queue full or connection closed"))
		}
	}
	decoded, decodeErr := h.decodeDispatchLogPayload(hint, payloadCache)
	body := buildDispatchLogBodyWithID(result, decoded, decodeErr, dispatchID)
	event := LogEvent{Type: "SOCKET", Method: "DISPATCH", Path: channel, Channel: channel, Status: http.StatusOK,
		Source: source, DispatchID: dispatchID, Queued: result.Queued, Dropped: len(result.Dropped), Errors: len(result.Errors), ResponseBody: body,
		TypeName: hint.TypeName, DecodeError: decodeErr, Mode: h.currentSocketMode(channel)}
	if decoded != nil {
		event.TypeName, event.Alias = decoded.TypeName, decoded.Alias
		if len(decoded.PayloadJSON) > 0 {
			event.DecodedPayload, event.DecodedTruncated = boundedSocketJSON(decoded.PayloadJSON)
		}
	}
	if len(result.Errors) > 0 {
		event.Error = strings.Join(result.Errors, "; ")
	}
	if len(hint.RawData) > 0 {
		contentType := hint.RawContentType
		if contentType == "" {
			contentType = "application/octet-stream"
			if hint.RawKind == websocket.MessageText {
				contentType = "text/plain; charset=utf-8"
			}
		}
		event.RequestBody, event.RequestPayload = captureLogPayload(hint.RawData, int64(len(hint.RawData)), contentType, "", nil)
	} else if len(hint.Payload) > 0 {
		event.RequestBody, event.RequestPayload = captureLogPayload(hint.Payload, int64(len(hint.Payload)), "application/json", "", nil)
	}
	h.events.Publish(event)
	return result
}

func (h *SocketHub) logClientDelivery(client *SocketClient, msg EncodedServerMessage, state string, err error) {
	event := h.clientSocketEvent(client, "FRAME", msg.Channel)
	event.Status, event.SubscriptionID, event.Source, event.Target = http.StatusOK, msg.SubscriptionID, msg.Source, msg.Target
	event.DeliveryState, event.ControlType, event.DispatchID = state, msg.ControlType, msg.DispatchID
	event.Direction = "ditto_to_client"
	if msg.Direction != "" {
		event.Direction = msg.Direction
	}
	if msg.TypeName != "" {
		event.TypeName = msg.TypeName
	}
	if state == "dropped" || state == "write_error" {
		event.Status = http.StatusServiceUnavailable
	}
	if err != nil {
		event.Error = err.Error()
	}
	h.logSocketFrame(event, msg.Data, msg.Kind, msg.TypeName)
}

func boundedSocketJSON(raw []byte) (string, bool) {
	if len(raw) > MaxLogPayloadCaptureBytes {
		raw = raw[:MaxLogPayloadCaptureBytes]
		for !utf8.Valid(raw) {
			raw = raw[:len(raw)-1]
		}
		return string(raw), true
	}
	return string(raw), false
}

func (h *SocketHub) decodeDispatchLogPayload(hint dispatchDecodeHint, payloadCache map[string]adapterPayload) (*DecodedFrame, string) {
	if hint.TypeName != "" {
		decoded := &DecodedFrame{TypeName: hint.TypeName}
		if h.schemas == nil {
			return decoded, "schema not loaded"
		}
		names := make([]string, 0, len(payloadCache))
		for name := range payloadCache {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			cached := payloadCache[name]
			if cached.err == nil && cached.payload.Kind == websocket.MessageBinary && len(cached.payload.Data) > 0 {
				payload, err := h.schemas.Decode(hint.TypeName, cached.payload.Data)
				if err != nil {
					return decoded, err.Error()
				}
				decoded.PayloadJSON = payload
				return decoded, ""
			}
		}
		if len(hint.Payload) > 0 {
			decoded.PayloadJSON = append(json.RawMessage(nil), hint.Payload...)
			return decoded, ""
		}
		return decoded, "schema payload not available"
	}
	if len(hint.Payload) > 0 {
		return &DecodedFrame{PayloadJSON: hint.Payload}, ""
	}
	return nil, ""
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

func (h *SocketHub) readLoop(ctx context.Context, client *SocketClient) error {
	for {
		typ, data, err := client.conn.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageText && typ != websocket.MessageBinary {
			continue
		}

		msg, err := client.protocol.ParseClientMessage(data)
		dispatchID := newSocketDispatchID()
		channel := strings.TrimSpace(msg.Channel)
		if channel == "" {
			channel = strings.TrimSpace(msg.SubscriptionID)
		}
		if channel == "" && msg.Type == "unsubscribe" {
			channel = client.channelForSubscription(msg.ID)
		}
		subscriptionID := msg.SubscriptionID
		if subscriptionID == "" {
			subscriptionID = msg.ID
		}
		frameEvent := h.clientSocketEvent(client, "FRAME", channel)
		frameEvent.Direction, frameEvent.DispatchID = "client_to_ditto", dispatchID
		frameEvent.SubscriptionID, frameEvent.ControlType = subscriptionID, msg.Type
		frameEvent.Source = "client"
		if err != nil {
			frameEvent.DecodeError, frameEvent.Error = err.Error(), err.Error()
		}
		h.logSocketFrame(frameEvent, data, typ, "")
		if err != nil {
			if h.forwardToLiveSubscriptions(ctx, client, typ, data, dispatchID) {
				continue
			}
			errorEvent := h.clientSocketEvent(client, "ERROR", channel)
			errorEvent.Status, errorEvent.Error, errorEvent.Source = http.StatusBadRequest, err.Error(), "socket-hub"
			h.events.Publish(errorEvent)
			continue
		}
		switch msg.Type {
		case "connection_init":
			h.enqueueControl(client, ServerMsg{Type: "connection_ack"})
		case "subscribe":
			channel = strings.TrimSpace(msg.Channel)
			if channel == "" {
				errorEvent := h.clientSocketEvent(client, "ERROR", "")
				errorEvent.Status, errorEvent.Error, errorEvent.Source = http.StatusBadRequest, "subscribe message missing channel", "socket-hub"
				h.events.Publish(errorEvent)
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
			event := h.clientSocketEvent(client, "SUBSCRIBE", channel)
			event.Status, event.SubscriptionID, event.Source = http.StatusOK, subID, "socket-hub"
			h.events.Publish(event)
			if h.isLiveMode(channel) && h.live != nil {
				h.live.Attach(channel, client)
			}
			h.forwardLiveFromClient(ctx, client, channel, typ, data, dispatchID)
		case "unsubscribe":
			channel = strings.TrimSpace(msg.Channel)
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
			event := h.clientSocketEvent(client, "UNSUBSCRIBE", channel)
			event.Status, event.SubscriptionID, event.Source = http.StatusOK, subscriptionID, "socket-hub"
			h.events.Publish(event)
		case "ping":
			h.enqueueControl(client, ServerMsg{Type: "pong"})
		default:
			if channel != "" {
				h.forwardLiveFromClient(ctx, client, channel, typ, data, dispatchID)
			}
		}
	}
}

func (h *SocketHub) clientSocketEvent(client *SocketClient, method, channel string) LogEvent {
	return LogEvent{Type: "SOCKET", Method: method, Path: channel, Channel: channel, Status: http.StatusOK,
		URL: client.url, Host: client.host, RemoteAddr: client.remoteAddr, Protocol: client.protocolName,
		ConnectionID: client.id, ClientID: client.id, Adapter: client.adapter, Subprotocol: client.subprotocol,
		Mode: h.currentSocketMode(channel)}
}

func (h *SocketHub) forwardToLiveSubscriptions(ctx context.Context, client *SocketClient, typ websocket.MessageType, data []byte, dispatchID string) bool {
	for _, channel := range client.subscriptionList() {
		if h.isLiveMode(channel) {
			h.forwardLiveFromClient(ctx, client, channel, typ, data, dispatchID)
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

func (h *SocketHub) forwardLiveFromClient(ctx context.Context, client *SocketClient, channel string, typ websocket.MessageType, data []byte, dispatchID string) {
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
	h.live.ForwardFromClientWithID(ctx, channel, typ, data, client, dispatchID)
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

func (h *SocketHub) writeLoop(ctx context.Context, client *SocketClient) error {
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

	if len(heartbeat.Data) > 0 {
		heartbeat.DispatchID, heartbeat.Source, heartbeat.ControlType = newSocketDispatchID(), "socket-hub", "heartbeat"
	}
	writeMsg := func(msg EncodedServerMessage) error {
		if msg.Kind == 0 {
			return errors.New("empty websocket message type")
		}
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := client.conn.Write(writeCtx, msg.Kind, msg.Data)
		cancel()
		state := "written"
		if err != nil {
			state = "write_error"
		}
		h.logClientDelivery(client, msg, state, err)
		return err
	}

	for {
		select {
		case msg := <-client.control:
			if err := writeMsg(msg); err != nil {
				return err
			}
			continue
		default:
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-client.done:
			return nil
		case msg := <-client.control:
			if err := writeMsg(msg); err != nil {
				return err
			}
		case msg := <-client.send:
			if err := writeMsg(msg); err != nil {
				return err
			}
		case <-heartbeatC:
			heartbeat.DispatchID = newSocketDispatchID()
			if err := writeMsg(heartbeat); err != nil {
				return err
			}
		case <-pingTicker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := client.conn.Ping(pingCtx)
			cancel()
			event := h.clientSocketEvent(client, "PING", "")
			event.Direction, event.Source, event.FrameKind = "ditto_to_client", "socket-hub", "control"
			event.ControlType, event.DispatchID, event.DeliveryState = "websocket_ping", newSocketDispatchID(), "written"
			if err != nil {
				event.Error, event.DeliveryState = err.Error(), "write_error"
			}
			h.events.Publish(event)
			if err != nil {
				return err
			}
		}
	}
}

func (h *SocketHub) enqueueControl(client *SocketClient, msg ServerMsg) {
	data, err := client.protocol.EncodeServerMessage(msg)
	if err != nil {
		event := h.clientSocketEvent(client, "ERROR", msg.Channel)
		event.Status, event.Error, event.Source = http.StatusBadRequest, err.Error(), "socket-hub"
		h.events.Publish(event)
		return
	}
	if data.Kind == 0 {
		event := h.clientSocketEvent(client, "ERROR", msg.Channel)
		event.Status, event.Error, event.Source = http.StatusBadRequest, "adapter returned empty websocket message type", "socket-hub"
		h.events.Publish(event)
		return
	}
	data.DispatchID, data.Channel, data.SubscriptionID = newSocketDispatchID(), msg.Channel, msg.ID
	data.Source, data.ControlType = "socket-hub", msg.Type
	if client.enqueueOn(client.control, data, 500*time.Millisecond) {
		h.logClientDelivery(client, data, "queued", nil)
	} else {
		err := errors.New("control message dropped")
		event := h.clientSocketEvent(client, "ERROR", msg.Channel)
		event.Status, event.Error, event.Source = http.StatusServiceUnavailable, err.Error(), "socket-hub"
		h.events.Publish(event)
		h.logClientDelivery(client, data, "dropped", err)
	}
}

func (h *SocketHub) publishSocketEvent(method, path string, status int, body string, duration int64) {
	h.publishSocketEventWithSource(method, path, status, body, duration, "")
}

func (h *SocketHub) publishSocketEventWithSource(method, path string, status int, body string, duration int64, source string) {
	event := LogEvent{
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

func buildDispatchLogBody(result SocketDispatchResult, decoded *DecodedFrame, decodeErr string) string {
	return buildDispatchLogBodyWithID(result, decoded, decodeErr, "")
}

func buildDispatchLogBodyWithID(result SocketDispatchResult, decoded *DecodedFrame, decodeErr, dispatchID string) string {
	body := DispatchLogBody{
		Delivered:  result.Delivered,
		Queued:     result.Queued,
		DispatchID: dispatchID,
		Dropped:    len(result.Dropped),
		Errors:     len(result.Errors),
	}
	if decoded != nil {
		body.TypeName = decoded.TypeName
		body.Alias = decoded.Alias
		if len(decoded.PayloadJSON) > 0 {
			if len(decoded.PayloadJSON) > dispatchPayloadMaxBytes {
				truncated := string(decoded.PayloadJSON[:dispatchPayloadMaxBytes])
				body.Payload, _ = json.Marshal(truncated)
				body.Truncated = true
			} else {
				body.Payload = decoded.PayloadJSON
			}
		}
	}
	if decodeErr != "" {
		body.DecodeError = decodeErr
	}
	data, err := json.Marshal(body)
	if err != nil {
		return dispatchSummary(result)
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
	case "relay":
		return RelayAdapter{}, nil
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

type RelayAdapter struct{}

func (RelayAdapter) ParseClientMessage(b []byte) (ClientMsg, error) {
	return RawAdapter{}.ParseClientMessage(b)
}

func (RelayAdapter) EncodeServerMessage(msg ServerMsg) (EncodedServerMessage, error) {
	switch msg.Type {
	case "connection_ack":
		return marshalTextMessage(map[string]any{"type": "connection_ack"})
	case "subscribe_ack":
		return marshalTextMessage(map[string]any{"type": "subscribe_success", "id": msg.ID, "channel": msg.Channel})
	case "pong":
		return marshalTextMessage(map[string]any{"type": "pong"})
	case "error":
		return marshalTextMessage(map[string]any{"type": "error", "id": msg.ID, "channel": msg.Channel, "payload": rawPayload(msg.Payload)})
	case "data", "":
		payload, err := RelayAdapter{}.EncodePayload(msg.Payload)
		if err != nil {
			return EncodedServerMessage{}, err
		}
		return RelayAdapter{}.WrapData(payload, msg.ID, msg.Channel)
	default:
		return marshalTextMessage(map[string]any{"type": msg.Type, "channel": msg.Channel})
	}
}

func (RelayAdapter) EncodePayload(payload json.RawMessage) (EncodedPayload, error) {
	return AppSyncAdapter{}.EncodePayload(payload)
}

func (RelayAdapter) WrapData(payload EncodedPayload, subID, channel string) (EncodedServerMessage, error) {
	value := payload.Value
	if payload.Kind == websocket.MessageBinary {
		value = map[string]any{
			"base64":       base64.StdEncoding.EncodeToString(payload.Data),
			"content_type": payload.ContentType,
			"type_name":    payload.TypeName,
		}
	}
	return marshalTextMessage(map[string]any{
		"type":    "data",
		"channel": channel,
		"event":   value,
	})
}

func (RelayAdapter) Heartbeat() (EncodedServerMessage, time.Duration) {
	return textMessage([]byte(`{"type":"ka"}`)), 5 * time.Second
}

func (RelayAdapter) Subprotocols() []string {
	return nil
}

func (RelayAdapter) GreetsOnConnect() bool {
	return true
}

func adapterGreetsOnConnect(adapter ProtocolAdapter) bool {
	greeter, ok := adapter.(GreetingAdapter)
	return ok && greeter.GreetsOnConnect()
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
