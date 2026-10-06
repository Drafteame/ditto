package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	mu            sync.RWMutex
	clients       map[string]*SocketClient
	connected     bool
	connectionID  string
	currentTarget string
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
	b.ForwardFromClientWithID(ctx, channel, typ, data, nil, newSocketDispatchID())
}

func (b *LiveBridge) ForwardFromClientWithID(ctx context.Context, channel string, typ websocket.MessageType, data []byte, client *SocketClient, dispatchID string) {
	b.mu.Lock()
	ch := b.chans[channel]
	b.mu.Unlock()
	if dispatchID == "" {
		dispatchID = newSocketDispatchID()
	}
	msg := EncodedServerMessage{Kind: typ, Data: append([]byte(nil), data...), DispatchID: dispatchID,
		Channel: channel, Source: "live-bridge", ControlType: "application_frame", Direction: "client_to_upstream"}
	if ch != nil {
		ch.mu.RLock()
		msg.Target = ch.currentTarget
		ch.mu.RUnlock()
	}
	if client != nil {
		msg.SubscriptionID, msg.ClientID, msg.Adapter, msg.Subprotocol = client.subscriptionID(channel), client.id, client.adapter, client.subprotocol
	}
	if ch == nil {
		if b.hub != nil {
			if client != nil {
				b.hub.logClientDelivery(client, msg, "dropped", fmt.Errorf("live upstream disconnected"))
			}
			b.hub.publishSocketEventWithSource("DROP", channel, http.StatusServiceUnavailable, "live upstream disconnected", 0, "live-disconnected")
		}
		return
	}
	// M5 intentionally forwards raw frames both ways. Adapter envelopes remain
	// only for local injections through dispatchRendered/WrapData.
	select {
	case ch.out <- msg:
		if client != nil && b.hub != nil {
			b.hub.logClientDelivery(client, msg, "queued", nil)
		}
	case <-ctx.Done():
		if client != nil && b.hub != nil {
			b.hub.logClientDelivery(client, msg, "dropped", ctx.Err())
		}
	case <-ch.ctx.Done():
		if client != nil && b.hub != nil {
			b.hub.logClientDelivery(client, msg, "dropped", fmt.Errorf("live channel closed"))
		}
	default:
		if b.hub != nil {
			if client != nil {
				b.hub.logClientDelivery(client, msg, "dropped", fmt.Errorf("live upstream send queue full"))
			}
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
		adapterName := adapterNameForClient(client)
		var subprotocols []string
		var headers http.Header
		var clientHost, clientAddr string
		if client != nil {
			subprotocols = client.protocol.Subprotocols()
			headers = client.upstreamHeaders.Clone()
			clientHost = client.upstreamHost
			clientAddr = client.remoteAddr
		}
		headers = applyForwardingHeaders(headers, clientHost, clientAddr, isSecureTarget(target))
		connectionID := fmt.Sprintf("live-%s-%d", ch.channel, time.Now().UnixNano())
		dialEvent := LogEvent{Type: "SOCKET", Method: "LIVE_DIAL", Path: ch.channel, Channel: ch.channel, URL: target, Target: target,
			Source: "live", Mode: ch.bridge.hub.currentSocketMode(ch.channel), ConnectionID: connectionID, Adapter: adapterName,
			Subprotocol: strings.Join(subprotocols, ","), Host: clientHost, RemoteAddr: clientAddr, RequestHeaders: headers.Clone()}
		if client != nil {
			dialEvent.ClientID = client.id
		}
		ch.bridge.hub.events.Publish(dialEvent)
		dialCapture := &websocketResponseCaptureTransport{base: http.DefaultTransport}
		conn, response, err := websocket.Dial(ch.ctx, target, &websocket.DialOptions{
			Subprotocols: subprotocols,
			HTTPHeader:   headers,
			HTTPClient:   &http.Client{Transport: dialCapture, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		})
		if err != nil {
			event := LogEvent{Type: "SOCKET", Method: "LIVE_DIAL_ERROR", Path: ch.channel, Channel: ch.channel, URL: target, Target: target,
				Status: http.StatusBadGateway, Error: err.Error(), Source: "live", Mode: ch.bridge.hub.currentSocketMode(ch.channel),
				RequestHeaders: headers.Clone(), Adapter: adapterName, ConnectionID: connectionID}
			if response != nil {
				event.Status = response.StatusCode
				event.ResponseHeaders = response.Header.Clone()
				if dialCapture.responsePayload != nil {
					event.ResponseBody, event.ResponsePayload = dialCapture.responseBody, dialCapture.responsePayload
				} else if response.Body != nil {
					prefix, _ := io.ReadAll(io.LimitReader(response.Body, MaxLogPayloadCaptureBytes))
					event.ResponseBody, event.ResponsePayload = captureLogPayload(prefix, int64(len(prefix)), response.Header.Get("Content-Type"), response.Header.Get("Content-Encoding"), nil)
				}
			}
			ch.bridge.hub.events.Publish(event)
			if !sleepContext(ch.ctx, backoff) {
				return
			}
			backoff = nextLiveBackoff(backoff)
			continue
		}
		backoff = 250 * time.Millisecond
		ch.mu.Lock()
		ch.connected = true
		ch.connectionID = connectionID
		ch.currentTarget = target
		ch.mu.Unlock()
		ch.bridge.hub.events.Publish(LogEvent{Type: "SOCKET", Method: "LIVE_CONNECT", Path: ch.channel, Channel: ch.channel,
			URL: target, Target: target, Status: http.StatusSwitchingProtocols, Source: "live", ConnectionID: connectionID,
			Adapter: adapterName, Subprotocol: conn.Subprotocol(), Mode: ch.bridge.hub.currentSocketMode(ch.channel), ClientID: clientID(client),
			Host: clientHost, RemoteAddr: clientAddr,
			RequestHeaders: headers.Clone(), ResponseHeaders: responseHeaders(response)})

		connCtx, cancelConn := context.WithCancel(ch.ctx)
		errCh, doneCh := make(chan error, 2), make(chan struct{}, 2)
		go ch.readUpstream(connCtx, conn, target, connectionID, errCh, doneCh)
		go ch.writeUpstream(connCtx, conn, target, connectionID, errCh, doneCh)
		select {
		case <-ch.ctx.Done():
			cancelConn()
			_ = conn.Close(websocket.StatusNormalClosure, "")
			<-doneCh
			<-doneCh
			return
		case disconnectErr := <-errCh:
			cancelConn()
			_ = conn.Close(websocket.StatusNormalClosure, "")
			<-doneCh
			<-doneCh
			ch.mu.Lock()
			ch.connected = false
			ch.mu.Unlock()
			code, reason := socketCloseDetails(disconnectErr)
			status := code
			if status < 0 {
				status = http.StatusBadGateway
			}
			ch.bridge.hub.events.Publish(LogEvent{Type: "SOCKET", Method: "LIVE_CLOSE", Path: ch.channel, Channel: ch.channel,
				URL: target, Target: target, Status: status, Error: errorText(disconnectErr), Source: "live",
				ConnectionID: connectionID, CloseCode: code, CloseReason: reason, Mode: ch.bridge.hub.currentSocketMode(ch.channel)})
		}
	}
}

func (ch *liveChannel) readUpstream(ctx context.Context, conn *websocket.Conn, target, connectionID string, errCh chan<- error, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				errCh <- err
			}
			return
		}
		if typ != websocket.MessageText && typ != websocket.MessageBinary {
			continue
		}
		dispatchID := newSocketDispatchID()
		client := ch.firstClient()
		event := LogEvent{Type: "SOCKET", Method: "FRAME", Path: ch.channel, Channel: ch.channel, Direction: "upstream_to_ditto",
			DispatchID: dispatchID, Source: "live", ConnectionID: connectionID, Target: target, Mode: ch.bridge.hub.currentSocketMode(ch.channel),
			Adapter: adapterNameForClient(client)}
		ch.bridge.hub.logSocketFrame(event, data, typ, "")
		ch.bridge.hub.forwardFromUpstreamID(ch.channel, typ, data, dispatchID, connectionID, target)
	}
}

func (ch *liveChannel) writeUpstream(ctx context.Context, conn *websocket.Conn, target, connectionID string, errCh chan<- error, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch.out:
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(writeCtx, msg.Kind, msg.Data)
			cancel()
			state := "written"
			if err != nil {
				state = "write_error"
			}
			if ch.bridge.hub != nil {
				event := LogEvent{Type: "SOCKET", Method: "FRAME", Path: ch.channel, Channel: ch.channel,
					Direction: "client_to_upstream", DispatchID: msg.DispatchID, DeliveryState: state, Source: msg.Source,
					ConnectionID: connectionID, SubscriptionID: msg.SubscriptionID, Target: target, Mode: ch.bridge.hub.currentSocketMode(ch.channel), Error: errorText(err)}
				event.ClientID, event.Adapter, event.Subprotocol = msg.ClientID, msg.Adapter, msg.Subprotocol
				ch.bridge.hub.logSocketFrame(event, msg.Data, msg.Kind, "")
			}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					errCh <- err
				}
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
	h.forwardFromUpstreamID(channel, typ, data, newSocketDispatchID(), "", "")
}

func (h *SocketHub) forwardFromUpstreamID(channel string, typ websocket.MessageType, data []byte, dispatchID, connectionID, target string) {
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
		msg := EncodedServerMessage{Kind: typ, Data: append([]byte(nil), data...), DispatchID: dispatchID, Channel: channel,
			Source: "live-bridge", Target: target, Direction: "upstream_to_client", Adapter: client.adapter,
			ClientID: client.id, Subprotocol: client.subprotocol}
		if client.enqueue(msg, 0) {
			result.Delivered++
			result.Queued++
			h.logClientDelivery(client, msg, "queued", nil)
		} else {
			result.Dropped = append(result.Dropped, id)
			h.logClientDelivery(client, msg, "dropped", fmt.Errorf("client queue full or disconnected"))
		}
	}
	decoded, decodeErr := DecodeWireFrame(h.schemas, frameKind(typ), data, adapterName)
	body := buildDispatchLogBodyWithID(result, decoded, decodeErr, dispatchID)
	event := LogEvent{Type: "SOCKET", Method: "DISPATCH", Path: channel, Channel: channel, Status: http.StatusOK,
		Source: "live", DispatchID: dispatchID, Queued: result.Queued, ResponseBody: body, ConnectionID: connectionID, Target: target,
		Adapter: adapterName, DecodeError: decodeErr, Mode: h.currentSocketMode(channel)}
	if decoded != nil {
		event.TypeName, event.Alias = decoded.TypeName, decoded.Alias
		event.DecodedPayload, event.DecodedTruncated = boundedSocketJSON(decoded.PayloadJSON)
	}
	h.events.Publish(event)
}

func adapterNameForClient(client *SocketClient) string {
	if client == nil {
		return ""
	}
	return client.adapter
}

func clientID(client *SocketClient) string {
	if client == nil {
		return ""
	}
	return client.id
}

func responseHeaders(response *http.Response) http.Header {
	if response == nil {
		return nil
	}
	return response.Header.Clone()
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	if status := websocket.CloseStatus(err); status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway || errors.Is(err, context.Canceled) {
		return ""
	}
	return err.Error()
}

// ProxyWebSocket tunnels complete messages through nhooyr's streaming Reader
// and Writer APIs. The logger retains only a bounded prefix of each message.
func (pm *ProxyManager) ProxyWebSocket(w http.ResponseWriter, r *http.Request, hub *SocketHub) bool {
	pm.mu.RLock()
	proxy, target := pm.proxy, pm.target
	pm.mu.RUnlock()
	if proxy == nil || target == "" {
		return false
	}
	upstreamReq := r.Clone(r.Context())
	upstreamReq.URL = cloneURL(r.URL)
	upstreamReq.Header = extractUpstreamHeaders(r.Header)
	proxy.Director(upstreamReq)
	u := upstreamReq.URL
	if u.Scheme == "http" {
		u.Scheme = "ws"
	} else if u.Scheme == "https" {
		u.Scheme = "wss"
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		http.Error(w, "invalid websocket target", http.StatusBadGateway)
		return true
	}
	protocols := websocketProtocols(r.Header)
	started := time.Now()
	connectionID := fmt.Sprintf("proxy-ws-%d", hub.nextID.Add(1))
	requestURL := socketRequestURL(r, r.RequestURI)
	adapter := normalizeAdapter(r.URL.Query().Get("adapter"))
	if adapter == "" {
		adapter = "raw"
	}
	responseCaptureTransport := &websocketResponseCaptureTransport{base: http.DefaultTransport, writer: w}
	conn, response, err := websocket.Dial(r.Context(), u.String(), &websocket.DialOptions{HTTPHeader: upstreamReq.Header, Subprotocols: protocols,
		HTTPClient: &http.Client{Transport: responseCaptureTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		status := http.StatusBadGateway
		respHeaders := make(http.Header)
		if response != nil {
			status = response.StatusCode
			respHeaders = response.Header.Clone()
			body, metadata := responseCaptureTransport.responseBody, responseCaptureTransport.responsePayload
			event := LogEvent{Type: "SOCKET", Method: "HANDSHAKE_ERROR", Path: r.RequestURI, URL: requestURL, Host: r.Host,
				RemoteAddr: r.RemoteAddr, Protocol: r.Proto, Status: status, DurationMs: time.Since(started).Milliseconds(),
				Source: "websocket-proxy", Error: err.Error(), Target: target, ConnectionID: connectionID, ClientID: connectionID,
				RequestHeaders: r.Header.Clone(), ResponseHeaders: respHeaders, ResponseBody: body, ResponsePayload: metadata}
			if responseCaptureTransport.responseError != "" {
				event.Error += "; response body capture: " + responseCaptureTransport.responseError
			}
			hub.events.Publish(event)
		} else {
			http.Error(w, http.StatusText(status), status)
			hub.events.Publish(LogEvent{Type: "SOCKET", Method: "HANDSHAKE_ERROR", Path: r.RequestURI, URL: requestURL, Host: r.Host,
				RemoteAddr: r.RemoteAddr, Protocol: r.Proto, Status: status, DurationMs: time.Since(started).Milliseconds(),
				Source: "websocket-proxy", Error: err.Error(), Target: target, ConnectionID: connectionID, ClientID: connectionID,
				RequestHeaders: r.Header.Clone()})
		}
		return true
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	copyResponseHeaders(w.Header(), response.Header)
	capture := newResponseCapture(w)
	clientConn, acceptErr := websocket.Accept(capture, r, &websocket.AcceptOptions{InsecureSkipVerify: true,
		CompressionMode: websocket.CompressionDisabled, Subprotocols: []string{conn.Subprotocol()}, OriginPatterns: []string{"*"}})
	if acceptErr != nil {
		_ = conn.Close(websocket.StatusInternalError, "client handshake failed")
		hub.events.Publish(LogEvent{Type: "SOCKET", Method: "HANDSHAKE_ERROR", Path: r.RequestURI, URL: requestURL,
			Status: capture.statusCode, DurationMs: time.Since(started).Milliseconds(), Source: "websocket-proxy", Error: acceptErr.Error(),
			Target: target, ConnectionID: connectionID, RequestHeaders: r.Header.Clone(), ResponseHeaders: capture.responseHeaders()})
		return true
	}
	conn.SetReadLimit(-1)
	clientConn.SetReadLimit(-1)
	hub.events.Publish(LogEvent{Type: "SOCKET", Method: "CONNECT", Path: r.RequestURI, URL: requestURL, Host: r.Host,
		RemoteAddr: r.RemoteAddr, Protocol: r.Proto, Status: http.StatusSwitchingProtocols, DurationMs: time.Since(started).Milliseconds(),
		Source: "websocket-proxy", Target: target, ConnectionID: connectionID, ClientID: connectionID, Adapter: adapter, Mode: "proxy",
		Subprotocol: conn.Subprotocol(), RequestHeaders: r.Header.Clone(), ResponseHeaders: capture.responseHeaders()})
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	errCh := make(chan error, 2)
	go tunnelWebSocketMessages(ctx, clientConn, conn, hub, r, target, connectionID, adapter, conn.Subprotocol(), "client_to_upstream", errCh)
	go tunnelWebSocketMessages(ctx, conn, clientConn, hub, r, target, connectionID, adapter, conn.Subprotocol(), "upstream_to_client", errCh)
	firstErr := <-errCh
	closeCode, closeReason := socketCloseDetails(firstErr)
	if closeCode < 0 {
		closeCode, closeReason = int(websocket.StatusGoingAway), "proxy stream ended"
	}
	_ = clientConn.Close(websocket.StatusCode(closeCode), closeReason)
	_ = conn.Close(websocket.StatusCode(closeCode), closeReason)
	cancel()
	<-errCh
	hub.events.Publish(LogEvent{Type: "SOCKET", Method: "CLOSE", Path: r.RequestURI, URL: requestURL, Host: r.Host,
		RemoteAddr: r.RemoteAddr, Protocol: r.Proto, Status: closeCode, DurationMs: time.Since(started).Milliseconds(),
		Source: "websocket-proxy", Target: target, ConnectionID: connectionID, ClientID: connectionID,
		Subprotocol: conn.Subprotocol(), CloseCode: closeCode, CloseReason: closeReason, Error: errorText(firstErr)})
	return true
}

type boundedLogWriter struct {
	buf   bytes.Buffer
	total int64
}

// websocket.Dial reads only a short rejection preview before closing the HTTP
// body. Spool rejection bodies to disk so the client receives the full body
// without retaining an unbounded response in memory.
type websocketResponseCaptureTransport struct {
	base            http.RoundTripper
	writer          http.ResponseWriter
	responseBody    string
	responsePayload *LogPayloadMetadata
	responseError   string
}

func (t *websocketResponseCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.StatusCode == http.StatusSwitchingProtocols || resp.Body == nil {
		return resp, err
	}
	defer resp.Body.Close()
	if t.writer != nil {
		copyResponseHeaders(t.writer.Header(), resp.Header)
		capture := newResponseCapture(t.writer)
		capture.WriteHeader(resp.StatusCode)
		_, err = io.Copy(capture, resp.Body)
		t.responseBody, t.responsePayload = capture.responsePayload()
	} else {
		capture := &boundedLogWriter{}
		_, err = io.Copy(io.Discard, io.TeeReader(resp.Body, capture))
		t.responseBody, t.responsePayload = captureLogPayload(capture.buf.Bytes(), capture.total,
			resp.Header.Get("Content-Type"), resp.Header.Get("Content-Encoding"), err)
	}
	if err != nil {
		t.responseError = err.Error()
	}
	resp.Body = http.NoBody
	return resp, nil
}

func (w *boundedLogWriter) Write(p []byte) (int, error) {
	w.total += int64(len(p))
	n := len(p)
	remaining := MaxLogPayloadCaptureBytes - w.buf.Len()
	if remaining > n {
		remaining = n
	}
	if remaining > 0 {
		_, _ = w.buf.Write(p[:remaining])
	}
	return n, nil
}

func tunnelWebSocketMessages(ctx context.Context, src, dst *websocket.Conn, hub *SocketHub, req *http.Request, target, connectionID, adapter, subprotocol, direction string, errCh chan<- error) {
	for {
		typ, reader, err := src.Reader(ctx)
		if err != nil {
			errCh <- err
			return
		}
		writer, err := dst.Writer(ctx, typ)
		if err != nil {
			errCh <- err
			return
		}
		capture := &boundedLogWriter{}
		_, copyErr := io.Copy(writer, io.TeeReader(reader, capture))
		closeErr := writer.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		dispatchID := newSocketDispatchID()
		contentType := "application/octet-stream"
		kind := "binary"
		if typ == websocket.MessageText {
			contentType = "text/plain; charset=utf-8"
			kind = "text"
		}
		body, meta := captureLogPayload(capture.buf.Bytes(), capture.total, contentType, "", copyErr)
		if typ == websocket.MessageText {
			body = truncateSocketPreview(body)
		}
		event := LogEvent{Type: "SOCKET", Method: "FRAME", Path: req.URL.RequestURI(), URL: socketRequestURL(req, req.RequestURI),
			Host: req.Host, RemoteAddr: req.RemoteAddr, Protocol: req.Proto, Status: http.StatusOK, Source: "websocket-proxy",
			ConnectionID: connectionID, ClientID: connectionID, Target: target, Direction: direction, Subprotocol: subprotocol,
			Adapter: adapter, Mode: "proxy",
			DispatchID: dispatchID, DeliveryState: "written", FrameKind: kind, RequestBody: body, RequestPayload: meta}
		if strings.HasPrefix(direction, "upstream") {
			event.ResponseBody = body
			event.ResponsePayload = meta
			event.RequestBody = ""
			event.RequestPayload = nil
		}
		if copyErr != nil {
			event.Error = copyErr.Error()
			event.DeliveryState = "write_error"
			event.Status = http.StatusBadGateway
		}
		if typ == websocket.MessageBinary {
			event.DecodeError = "binary frame has no text envelope; raw bytes are available"
		} else if decoded, decodeErr := hub.decodeSocketFrame(typ, capture.buf.Bytes(), adapter, ""); decoded != nil {
			event.TypeName, event.Alias = decoded.TypeName, decoded.Alias
			event.DecodedPayload, event.DecodedTruncated = boundedSocketJSON(decoded.PayloadJSON)
			if decodeErr != "" {
				event.DecodeError = decodeErr
			}
		}
		if typ == websocket.MessageText {
			var envelope struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(capture.buf.Bytes(), &envelope) == nil && isSocketControlType(envelope.Type) {
				event.ControlType = envelope.Type
			}
		}
		hub.events.Publish(event)
		if copyErr != nil {
			errCh <- copyErr
			return
		}
	}
}

func isSocketControlType(value string) bool {
	switch strings.ToLower(value) {
	case "connection_init", "connection_ack", "subscribe", "subscribe_ack", "unsubscribe", "ping", "pong", "heartbeat", "ack", "init":
		return true
	default:
		return false
	}
}

func cloneURL(src *url.URL) *url.URL {
	if src == nil {
		return &url.URL{}
	}
	copy := *src
	return &copy
}
func websocketProtocols(h http.Header) []string {
	var out []string
	for _, line := range h.Values("Sec-WebSocket-Protocol") {
		for _, token := range strings.Split(line, ",") {
			if token = strings.TrimSpace(token); token != "" {
				out = append(out, token)
			}
		}
	}
	return out
}
func copyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		lower := strings.ToLower(key)
		if lower == "connection" || lower == "upgrade" || lower == "sec-websocket-accept" || lower == "sec-websocket-protocol" || lower == "sec-websocket-extensions" {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
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
