package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

type LiveTargetManager struct {
	mu      sync.RWMutex
	target  string
	store   *ConfigStore
	changed chan struct{}
}

func NewLiveTargetManager(target string, store *ConfigStore) *LiveTargetManager {
	return &LiveTargetManager{target: strings.TrimSpace(target), store: store, changed: make(chan struct{})}
}

func (m *LiveTargetManager) Target() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.target
}

func (m *LiveTargetManager) SetTarget(target string) error {
	target = strings.TrimSpace(target)
	if target != "" {
		u, err := url.Parse(target)
		if err != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") {
			return fmt.Errorf("live target must be a ws:// or wss:// URL")
		}
	}
	m.mu.Lock()
	m.target = target
	close(m.changed)
	m.changed = make(chan struct{})
	m.mu.Unlock()
	if m.store != nil {
		return m.store.SetLiveTarget(target)
	}
	return nil
}

func (m *LiveTargetManager) Snapshot() (string, <-chan struct{}) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.target, m.changed
}

type LiveBridge struct {
	mu      sync.Mutex
	targets *LiveTargetManager
	hub     *SocketHub
	chans   map[string]*liveChannel
}

type liveChannel struct {
	channel string
	bridge  *LiveBridge
	ctx     context.Context
	cancel  context.CancelFunc
	out     chan EncodedServerMessage

	mu        sync.RWMutex
	clients   map[string]*SocketClient
	connected bool
}

func NewLiveBridge(targets *LiveTargetManager, hub *SocketHub) *LiveBridge {
	return &LiveBridge{targets: targets, hub: hub, chans: make(map[string]*liveChannel)}
}

// Attach binds a client to a bridge-owned channel lifecycle. The caller's
// request context is intentionally not used to cancel the shared upstream.
func (b *LiveBridge) Attach(channel string, client *SocketClient) {
	if b == nil || client == nil {
		return
	}
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return
	}
	b.mu.Lock()
	ch := b.chans[channel]
	if ch == nil {
		ctx, cancel := context.WithCancel(context.Background())
		ch = &liveChannel{
			channel: channel,
			bridge:  b,
			ctx:     ctx,
			cancel:  cancel,
			out:     make(chan EncodedServerMessage, 128),
			clients: make(map[string]*SocketClient),
		}
		b.chans[channel] = ch
		go ch.run()
	}
	ch.mu.Lock()
	ch.clients[client.id] = client
	ch.mu.Unlock()
	b.mu.Unlock()
}

func (b *LiveBridge) Detach(channel, clientID string) {
	b.mu.Lock()
	ch := b.chans[channel]
	if ch == nil {
		b.mu.Unlock()
		return
	}
	ch.mu.Lock()
	delete(ch.clients, clientID)
	empty := len(ch.clients) == 0
	ch.mu.Unlock()
	if empty {
		delete(b.chans, channel)
		b.mu.Unlock()
		ch.cancel()
		return
	}
	b.mu.Unlock()
}

func (b *LiveBridge) DetachChannel(channel string) {
	b.mu.Lock()
	ch := b.chans[channel]
	delete(b.chans, channel)
	b.mu.Unlock()
	if ch != nil {
		ch.cancel()
	}
}

func (b *LiveBridge) ForwardFromClient(ctx context.Context, channel string, typ websocket.MessageType, data []byte) {
	b.mu.Lock()
	ch := b.chans[channel]
	b.mu.Unlock()
	if ch == nil {
		if b.hub != nil {
			b.hub.publishSocketEventWithSource("DROP", channel, http.StatusServiceUnavailable, "live upstream disconnected", 0, "live-disconnected")
		}
		return
	}
	// M5 intentionally forwards raw frames both ways. Adapter envelopes remain
	// only for local injections through dispatchRendered/WrapData.
	select {
	case ch.out <- EncodedServerMessage{Kind: typ, Data: append([]byte(nil), data...)}:
	case <-ctx.Done():
	case <-ch.ctx.Done():
	default:
		if b.hub != nil {
			b.hub.publishSocketEventWithSource("DROP", channel, http.StatusServiceUnavailable, "live upstream send queue full", 0, "live-disconnected")
		}
	}
}

func (ch *liveChannel) run() {
	backoff := 250 * time.Millisecond
	loggedEmptyTarget := false
	for {
		select {
		case <-ch.ctx.Done():
			return
		default:
		}
		target, targetChanged := ch.bridge.targets.Snapshot()
		if target == "" {
			if !loggedEmptyTarget {
				ch.bridge.hub.publishSocketEventWithSource("ERROR", ch.channel, http.StatusServiceUnavailable, "live target is not configured", 0, "live-disconnected")
				loggedEmptyTarget = true
			}
			select {
			case <-ch.ctx.Done():
				return
			case <-targetChanged:
				continue
			}
		}
		loggedEmptyTarget = false
		client := ch.firstClient()
		var subprotocols []string
		var headers http.Header
		var clientHost, clientAddr string
		if client != nil {
			subprotocols = client.protocol.Subprotocols()
			headers = cloneHeaders(client.upstreamHeaders)
			clientHost = client.upstreamHost
			clientAddr = client.remoteAddr
		}
		headers = applyForwardingHeaders(headers, clientHost, clientAddr, isSecureTarget(target))
		ch.bridge.hub.publishSocketEventWithSource(
			"LIVE_DIAL",
			ch.channel,
			0,
			fmt.Sprintf("target=%s client_host=%q client_addr=%q subprotocols=%v headers=%s",
				target, clientHost, clientAddr, subprotocols, summarizeHeaders(headers)),
			0,
			"live",
		)
		conn, _, err := websocket.Dial(ch.ctx, target, &websocket.DialOptions{
			Subprotocols: subprotocols,
			HTTPHeader:   headers,
		})
		if err != nil {
			ch.bridge.hub.publishSocketEventWithSource("ERROR", ch.channel, http.StatusBadGateway, err.Error(), 0, "live-disconnected")
			if !sleepContext(ch.ctx, backoff) {
				return
			}
			backoff = nextLiveBackoff(backoff)
			continue
		}
		backoff = 250 * time.Millisecond
		ch.mu.Lock()
		ch.connected = true
		ch.mu.Unlock()
		ch.bridge.hub.publishSocketEventWithSource("LIVE_CONNECT", ch.channel, http.StatusSwitchingProtocols, target, 0, "live")

		errCh := make(chan error, 2)
		go ch.readUpstream(conn, errCh)
		go ch.writeUpstream(conn, errCh)
		select {
		case <-ch.ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		case <-errCh:
			_ = conn.Close(websocket.StatusNormalClosure, "")
			ch.mu.Lock()
			ch.connected = false
			ch.mu.Unlock()
			ch.bridge.hub.publishSocketEventWithSource("LIVE_RECONNECT", ch.channel, http.StatusBadGateway, "upstream disconnected", 0, "live-disconnected")
		}
	}
}

func (ch *liveChannel) readUpstream(conn *websocket.Conn, errCh chan<- error) {
	for {
		typ, data, err := conn.Read(ch.ctx)
		if err != nil {
			errCh <- err
			return
		}
		if typ != websocket.MessageText && typ != websocket.MessageBinary {
			continue
		}
		ch.bridge.hub.forwardFromUpstream(ch.channel, typ, data)
	}
}

func (ch *liveChannel) writeUpstream(conn *websocket.Conn, errCh chan<- error) {
	for {
		select {
		case <-ch.ctx.Done():
			return
		case msg := <-ch.out:
			ctx, cancel := context.WithTimeout(ch.ctx, 5*time.Second)
			err := conn.Write(ctx, msg.Kind, msg.Data)
			cancel()
			if err != nil {
				errCh <- err
				return
			}
		}
	}
}

func (ch *liveChannel) firstClient() *SocketClient {
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	for _, client := range ch.clients {
		return client
	}
	return nil
}

func (h *SocketHub) forwardFromUpstream(channel string, typ websocket.MessageType, data []byte) {
	adapterName := ""
	if ids := h.registry.Clients(channel); len(ids) > 0 {
		if client := h.client(ids[0]); client != nil {
			adapterName = client.adapter
		}
	}
	if h.recorder != nil && h.isRecordingMode(channel) {
		cfg := h.modes.Get(channel)
		h.recorder.Record(RecordFrameInput{
			Channel: channel, Direction: "upstream", Kind: frameKind(typ), Data: data,
			Adapter: adapterName, RateCapHz: cfg.RateCapHz,
		})
	}
	result := SocketDispatchResult{}
	for _, id := range h.registry.Clients(channel) {
		client := h.client(id)
		if client == nil {
			result.Dropped = append(result.Dropped, id)
			continue
		}
		if client.enqueue(EncodedServerMessage{Kind: typ, Data: append([]byte(nil), data...)}, 0) {
			result.Delivered++
		} else {
			result.Dropped = append(result.Dropped, id)
		}
	}
	decoded, decodeErr := DecodeWireFrame(h.schemas, frameKind(typ), data, adapterName)
	body := buildDispatchLogBody(result, decoded, decodeErr)
	h.publishSocketEventWithSource("DISPATCH", channel, http.StatusOK, body, 0, "live")
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextLiveBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > 30*time.Second {
		return 30 * time.Second
	}
	return next
}

// cloneHeaders returns a shallow copy of h. Used before mutating headers that
// belong to the SocketClient so subsequent dial attempts start from the same
// baseline rather than accumulating forwarding markers.
func cloneHeaders(h http.Header) http.Header {
	if h == nil {
		return make(http.Header)
	}
	out := make(http.Header, len(h))
	for k, vs := range h {
		copied := make([]string, len(vs))
		copy(copied, vs)
		out[k] = copied
	}
	return out
}

// isSecureTarget reports whether the live target uses a TLS-encrypted scheme
// (wss/https) so the X-Forwarded-Proto header reflects the upstream transport.
func isSecureTarget(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "wss" || scheme == "https"
}

// summarizeHeaders returns a one-line redacted view of the headers Ditto
// forwards to the upstream live target. Authorization / Cookie / API key
// values are truncated so logs do not leak credentials but still indicate
// whether the header was present.
func summarizeHeaders(h http.Header) string {
	if len(h) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		vs := h.Values(k)
		v := ""
		if len(vs) > 0 {
			v = vs[0]
		}
		kl := strings.ToLower(k)
		if kl == "authorization" || kl == "cookie" || strings.Contains(kl, "api-key") || strings.Contains(kl, "token") {
			if len(v) > 12 {
				v = v[:8] + "…(" + fmt.Sprintf("%d", len(v)) + "b)"
			} else if v != "" {
				v = "…(" + fmt.Sprintf("%d", len(v)) + "b)"
			}
		}
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	return "{" + strings.Join(parts, " ") + "}"
}

func RegisterLiveTargetRoutes(mux *http.ServeMux, manager *LiveTargetManager) {
	if manager == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/socket/live-target", func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"live_target": manager.Target()})
		case http.MethodPut:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var req struct {
				LiveTarget string `json:"live_target"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := manager.SetTarget(req.LiveTarget); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}
