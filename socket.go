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
	"unicode/utf8"

	"nhooyr.io/websocket"

	"ditto/internal/adapter"
	"ditto/internal/capture"
	"ditto/internal/channelmode"
	"ditto/internal/eventlog"
	"ditto/internal/schema"
)

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
	bus      *eventlog.Bus
	modes    *channelmode.Registry
	live     *LiveBridge
	recorder *Recorder
	schemas  *schema.Registry
	adapters *adapter.Registry
	events   *eventlog.Coalescer

	mu      sync.RWMutex
	clients map[string]*SocketClient
	nextID  atomic.Uint64
}

type SocketClient struct {
	id              string
	adapter         string
	protocol        adapter.Protocol
	remoteAddr      string
	connected       time.Time
	conn            *websocket.Conn
	control         chan adapter.Message
	send            chan adapter.Message
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

func socketFramePreview(raw []byte, kind websocket.MessageType) (string, *eventlog.Payload) {
	contentType := "application/octet-stream"
	if kind == websocket.MessageText {
		contentType = "text/plain; charset=utf-8"
	}
	preview, metadata := capture.Payload(raw, int64(len(raw)), contentType, "", nil)
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

func (h *SocketHub) logSocketFrame(event eventlog.Event, data []byte, kind websocket.MessageType, typeName string) {
	event.Type = "SOCKET"
	event.FrameKind = adapter.FrameKind(kind)
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

func (h *SocketHub) decodeSocketFrame(kind websocket.MessageType, data []byte, adapterName, typeName string) (*adapter.Decoded, string) {
	if kind == websocket.MessageBinary && typeName != "" {
		decoded := &adapter.Decoded{TypeName: typeName}
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
	return h.adapters.DecodeFrame(h.schemas, adapter.FrameKind(kind), data, adapterName)
}

type adapterPayload struct {
	payload adapter.Payload
	err     error
}

// SocketDispatch is one event to deliver to a channel's subscribers.
type SocketDispatch struct {
	Channel string
	// Adapter limits delivery to clients using that adapter; empty means all.
	Adapter string
	// TypeName selects protobuf encoding; empty sends Payload as JSON.
	TypeName string
	Payload  json.RawMessage
	Source   string
	// Encoded is a pre-encoded protobuf payload (the sequence player caches
	// one per step); when set, Payload is not encoded again.
	Encoded *adapter.Payload
}

// SocketHubConfig holds the hub's collaborators. Bus is required; every other
// field is optional and its zero value disables the matching feature.
type SocketHubConfig struct {
	Bus      *eventlog.Bus
	Modes    *channelmode.Registry
	Schemas  *schema.Registry
	Adapters *adapter.Registry
	Recorder *Recorder
	// LiveTargets enables live/mixed channel forwarding to an upstream.
	LiveTargets *LiveTargetManager
}

func NewSocketHub(cfg SocketHubConfig) *SocketHub {
	hub := &SocketHub{
		registry: NewSubscriptionRegistry(),
		bus:      cfg.Bus,
		modes:    cfg.Modes,
		recorder: cfg.Recorder,
		schemas:  cfg.Schemas,
		adapters: cfg.Adapters,
		events:   eventlog.NewCoalescer(cfg.Bus),
		clients:  make(map[string]*SocketClient),
	}
	if cfg.LiveTargets != nil {
		hub.live = NewLiveBridge(cfg.LiveTargets, hub)
	}
	if cfg.Modes != nil {
		cfg.Modes.OnChange(func(change channelmode.Config) {
			if hub.live == nil {
				return
			}
			if change.Mode == channelmode.Live || change.Mode == channelmode.Mixed {
				hub.attachLiveSubscribers(change.Channel)
				return
			}
			hub.live.DetachChannel(change.Channel)
		})
	}
	return hub
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
		json.NewEncoder(w).Encode(hub.adapters.Summaries())
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
		result, err := hub.Dispatch(SocketDispatch{
			Channel:  req.Channel,
			Adapter:  req.Adapter,
			TypeName: req.TypeName,
			Payload:  req.Payload,
			Source:   "manual",
		})
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

// Dispatch validates d against the channel mode and adapter, encodes it when
// it names a protobuf type, and fans it out to the channel's subscribers.
// A mode that suppresses local dispatch is reported in the result, not as an
// error. Every rejection is logged.
func (h *SocketHub) Dispatch(d SocketDispatch) (SocketDispatchResult, error) {
	if h == nil {
		return SocketDispatchResult{}, fmt.Errorf("socket hub is not available")
	}
	channel := strings.TrimSpace(d.Channel)
	fail := func(reason string, suppressed bool) {
		h.logDispatchFailure(channel, d.Source, d.TypeName, d.Payload, reason, suppressed)
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
	if h.modes != nil && !h.modes.AllowsLocalDispatch(channel) {
		mode := h.modes.Get(channel).Mode
		msg := fmt.Sprintf("channel mode %s suppressed local dispatch", mode)
		fail(msg, true)
		return SocketDispatchResult{Errors: []string{msg}}, nil
	}
	adapterName := adapter.Normalize(d.Adapter)
	if _, err := h.adapters.Protocol(adapterName); err != nil {
		fail(err.Error(), false)
		return SocketDispatchResult{}, err
	}
	typeName := strings.TrimSpace(d.TypeName)
	if typeName != "" {
		if h.schemas == nil {
			err := fmt.Errorf("schema registry is not available")
			fail(err.Error(), false)
			return SocketDispatchResult{}, err
		}
		encoded := d.Encoded
		if encoded == nil {
			data, err := h.schemas.Encode(typeName, d.Payload)
			if err != nil {
				wrapped := fmt.Errorf("protobuf encode failed: %w", err)
				fail(wrapped.Error(), false)
				return SocketDispatchResult{}, wrapped
			}
			next := adapter.ProtobufPayload(typeName, data)
			encoded = &next
		}
		return h.dispatchEncoded(channel, *encoded, adapterName, d.Source, d.Payload), nil
	}
	payload := d.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	return h.dispatchJSON(channel, payload, adapterName, d.Source), nil
}

func (h *SocketHub) logDispatchFailure(channel, source, typeName string, payload json.RawMessage, reason string, suppressed bool) {
	method, state := "DISPATCH_FAILED", "error"
	if suppressed {
		method, state = "DISPATCH_SUPPRESSED", "suppressed"
	}
	id := newSocketDispatchID()
	event := eventlog.Event{Type: "SOCKET", Method: method, Path: channel, Channel: channel, Status: http.StatusServiceUnavailable,
		Source: source, DispatchID: id, DeliveryState: state, TypeName: typeName, Error: reason,
		Mode: h.currentSocketMode(channel)}
	if !suppressed {
		event.Errors = 1
	}
	if len(payload) > 0 {
		preview, metadata := capture.Payload(payload, int64(len(payload)), "application/json", "", nil)
		event.RequestBody, event.RequestPayload = preview, metadata
		decoded, decodeErr := h.adapters.DecodeFrame(h.schemas, "text", payload, "raw")
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
	cw := capture.NewWriter(w)
	logHandshakeFailure := func(err error) {
		body, metadata := cw.Payload()
		event := eventlog.Event{Type: "SOCKET", Method: "HANDSHAKE_ERROR", Path: requestURI, URL: requestURL,
			Host: r.Host, RemoteAddr: r.RemoteAddr, Protocol: r.Proto, Status: cw.Status(),
			DurationMs: time.Since(start).Milliseconds(), Source: "socket-hub", ConnectionID: connectionID,
			ClientID: connectionID, RequestHeaders: requestHeaders, ResponseHeaders: cw.Headers(),
			ResponseBody: body, ResponsePayload: metadata}
		if err != nil {
			event.Error = err.Error()
		}
		h.events.Publish(event)
	}

	adapterName := adapter.Normalize(r.URL.Query().Get("adapter"))
	if adapterName == "" {
		adapterName = "raw"
	}
	protocol, err := h.adapters.Protocol(adapterName)
	if err != nil {
		http.Error(cw, err.Error(), http.StatusBadRequest)
		logHandshakeFailure(err)
		return
	}
	if !isAllowedSocketAPIRequest(r) {
		err := errors.New("origin not allowed")
		http.Error(cw, err.Error(), http.StatusForbidden)
		logHandshakeFailure(err)
		return
	}

	conn, err := websocket.Accept(cw, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
		Subprotocols:       protocol.Subprotocols(),
	})
	if err != nil {
		logHandshakeFailure(err)
		return
	}

	client := &SocketClient{
		id:              connectionID,
		adapter:         adapterName,
		protocol:        protocol,
		remoteAddr:      r.RemoteAddr,
		connected:       time.Now(),
		conn:            conn,
		control:         make(chan adapter.Message, 16),
		send:            make(chan adapter.Message, 64),
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
	h.events.Publish(eventlog.Event{Type: "SOCKET", Method: "CONNECT", Path: requestURI, URL: requestURL, Host: r.Host,
		RemoteAddr: r.RemoteAddr, Protocol: r.Proto, Status: cw.Status(), DurationMs: time.Since(start).Milliseconds(),
		Source: "socket-hub", ConnectionID: connectionID, ClientID: connectionID, Adapter: adapterName,
		Subprotocol: conn.Subprotocol(), RequestHeaders: requestHeaders, ResponseHeaders: cw.Headers()})

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

	if adapter.GreetsOnConnect(protocol) {
		h.enqueueControl(client, adapter.ServerMessage{Type: "connection_ack"})
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
	closeEvent := eventlog.Event{Type: "SOCKET", Method: "CLOSE", Path: requestURI, URL: requestURL, Host: r.Host,
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

// dispatchJSON fans a JSON payload out without mode or adapter validation;
// Dispatch is the validating entry point.
func (h *SocketHub) dispatchJSON(channel string, payload json.RawMessage, adapterFilter, source string) SocketDispatchResult {
	return h.dispatch(channel, adapterFilter, source, dispatchDecodeHint{Payload: payload}, func(client *SocketClient) (adapter.Payload, error) {
		return client.protocol.EncodePayload(payload)
	})
}

// dispatchEncoded fans out a pre-encoded payload. logicalPayload is the JSON
// it was encoded from, used only for logging.
func (h *SocketHub) dispatchEncoded(channel string, payload adapter.Payload, adapterFilter, source string, logicalPayload json.RawMessage) SocketDispatchResult {
	return h.dispatch(channel, adapterFilter, source, dispatchDecodeHint{TypeName: payload.TypeName, Payload: logicalPayload,
		RawData: payload.Data, RawKind: payload.Kind, RawContentType: payload.ContentType}, func(client *SocketClient) (adapter.Payload, error) {
		return payload, nil
	})
}

func (h *SocketHub) dispatch(channel string, adapterFilter string, source string, hint dispatchDecodeHint, encode func(client *SocketClient) (adapter.Payload, error)) SocketDispatchResult {
	channel = strings.TrimSpace(channel)
	adapterFilter = adapter.Normalize(adapterFilter)
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
					Channel: channel, Direction: "local", Kind: adapter.FrameKind(data.Kind), Data: data.Data,
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
	event := eventlog.Event{Type: "SOCKET", Method: "DISPATCH", Path: channel, Channel: channel, Status: http.StatusOK,
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
		event.RequestBody, event.RequestPayload = capture.Payload(hint.RawData, int64(len(hint.RawData)), contentType, "", nil)
	} else if len(hint.Payload) > 0 {
		event.RequestBody, event.RequestPayload = capture.Payload(hint.Payload, int64(len(hint.Payload)), "application/json", "", nil)
	}
	h.events.Publish(event)
	return result
}

func (h *SocketHub) logClientDelivery(client *SocketClient, msg adapter.Message, state string, err error) {
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
	if len(raw) > capture.MaxBytes {
		raw = raw[:capture.MaxBytes]
		for !utf8.Valid(raw) {
			raw = raw[:len(raw)-1]
		}
		return string(raw), true
	}
	return string(raw), false
}

func (h *SocketHub) decodeDispatchLogPayload(hint dispatchDecodeHint, payloadCache map[string]adapterPayload) (*adapter.Decoded, string) {
	if hint.TypeName != "" {
		decoded := &adapter.Decoded{TypeName: hint.TypeName}
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
		return &adapter.Decoded{PayloadJSON: hint.Payload}, ""
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
			h.enqueueControl(client, adapter.ServerMessage{Type: "connection_ack"})
		case "subscribe":
			channel = strings.TrimSpace(msg.Channel)
			if channel == "" {
				errorEvent := h.clientSocketEvent(client, "ERROR", "")
				errorEvent.Status, errorEvent.Error, errorEvent.Source = http.StatusBadRequest, "subscribe message missing channel", "socket-hub"
				h.events.Publish(errorEvent)
				h.enqueueControl(client, adapter.ServerMessage{Type: "error", ID: msg.ID, Payload: json.RawMessage(`{"error":"subscribe message missing channel"}`)})
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
			h.enqueueControl(client, adapter.ServerMessage{Type: "subscribe_ack", ID: subID, Channel: channel})
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
			h.enqueueControl(client, adapter.ServerMessage{Type: "pong"})
		default:
			if channel != "" {
				h.forwardLiveFromClient(ctx, client, channel, typ, data, dispatchID)
			}
		}
	}
}

func (h *SocketHub) clientSocketEvent(client *SocketClient, method, channel string) eventlog.Event {
	return eventlog.Event{Type: "SOCKET", Method: method, Path: channel, Channel: channel, Status: http.StatusOK,
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
	return mode == channelmode.Live || mode == channelmode.Mixed
}

func (h *SocketHub) isRecordingMode(channel string) bool {
	if h.modes == nil {
		return false
	}
	mode := h.modes.Get(channel).Mode
	return mode == channelmode.Record || mode == channelmode.Mixed
}

func (h *SocketHub) forwardLiveFromClient(ctx context.Context, client *SocketClient, channel string, typ websocket.MessageType, data []byte, dispatchID string) {
	if !h.isLiveMode(channel) {
		return
	}
	if h.recorder != nil && h.modes.Get(channel).Mode == channelmode.Mixed {
		h.recorder.Record(RecordFrameInput{
			Channel: channel, Direction: "local", Kind: adapter.FrameKind(typ), Data: data,
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
	writeMsg := func(msg adapter.Message) error {
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

func (h *SocketHub) enqueueControl(client *SocketClient, msg adapter.ServerMessage) {
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

func (h *SocketHub) publishSocketEventWithSource(method, path string, status int, body string, duration int64, source string) {
	event := eventlog.Event{
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

func buildDispatchLogBody(result SocketDispatchResult, decoded *adapter.Decoded, decodeErr string) string {
	return buildDispatchLogBodyWithID(result, decoded, decodeErr, "")
}

func buildDispatchLogBodyWithID(result SocketDispatchResult, decoded *adapter.Decoded, decodeErr, dispatchID string) string {
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

func (c *SocketClient) enqueue(msg adapter.Message, timeout time.Duration) bool {
	return c.enqueueOn(c.send, msg, timeout)
}

func (c *SocketClient) enqueueOn(ch chan adapter.Message, msg adapter.Message, timeout time.Duration) bool {
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
