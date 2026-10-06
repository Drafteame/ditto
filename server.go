package main

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ProxyManager allows changing the target URL at runtime.
type ProxyManager struct {
	mu     sync.RWMutex
	proxy  *httputil.ReverseProxy
	target string
}

func NewProxyManager(target string) *ProxyManager {
	pm := &ProxyManager{}
	if target != "" {
		pm.SetTarget(target)
	}
	return pm
}

func (pm *ProxyManager) SetTarget(target string) error {
	targetURL, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid target URL: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = targetURL.Host
		// Ask upstream for plain bytes. ReverseProxy streams the response
		// through untouched, so a compressed body would reach the log and
		// "Save as mock" as binary garbage.
		req.Header.Set("Accept-Encoding", "identity")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if capture, ok := w.(*responseCapture); ok {
			capture.proxyError = err.Error()
		}
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
	}

	pm.mu.Lock()
	pm.proxy = proxy
	pm.target = target
	pm.mu.Unlock()
	return nil
}

func (pm *ProxyManager) Target() string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.target
}

func (pm *ProxyManager) ServeHTTP(w http.ResponseWriter, r *http.Request) bool {
	served, _ := pm.ServeHTTPWithTarget(w, r)
	return served
}

// ServeHTTPWithTarget snapshots the proxy and target together so the request
// and its log always agree when a target changes concurrently.
func (pm *ProxyManager) ServeHTTPWithTarget(w http.ResponseWriter, r *http.Request) (bool, string) {
	pm.mu.RLock()
	proxy := pm.proxy
	target := pm.target
	pm.mu.RUnlock()

	if proxy == nil {
		return false, ""
	}
	proxy.ServeHTTP(w, r)
	return true, target
}

// ServerConfig holds all the parameters needed to create and run the HTTP server.
type ServerConfig struct {
	Port        int
	Target      string
	LiveTarget  string
	MocksDir    string
	Layout      DataLayout
	HTTPS       bool
	CertDir     string
	ServeUI     bool
	JSONLogs    bool
	ConfigStore *ConfigStore
}

// Server holds the running server state.
type Server struct {
	Mux        *http.ServeMux
	Store      *MockStore
	Bus        *EventBus
	ProxyMgr   *ProxyManager
	SocketHub  *SocketHub
	Modes      *ChannelModeRegistry
	Recorder   *Recorder
	Live       *LiveBridge
	LiveTarget *LiveTargetManager
	Schemas    *SchemaRegistry
	Templates  *EventTemplateRegistry
	Sequences  *EventSequenceRegistry
	Player     *SequencePlayer
	Info       ServerInfo
	Config     ServerConfig
	CertPath   string
	KeyPath    string

	mu       sync.Mutex
	listener net.Listener
}

// NewServer creates and configures the HTTP server with all routes.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.MocksDir == "" {
		return nil, fmt.Errorf("server config mocks dir is required")
	}
	if cfg.Layout.DescriptorsDir == "" {
		return nil, fmt.Errorf("server config layout with descriptors dir is required")
	}
	if cfg.Layout.EventTemplatesDir == "" {
		return nil, fmt.Errorf("server config layout with event templates dir is required")
	}
	if cfg.Layout.SequencesDir == "" {
		return nil, fmt.Errorf("server config layout with sequences dir is required")
	}
	if cfg.Layout.AdapterProfilesDir == "" {
		return nil, fmt.Errorf("server config layout with adapter profiles dir is required")
	}
	if cfg.Layout.RecordingsDir == "" {
		return nil, fmt.Errorf("server config layout with recordings dir is required")
	}
	if cfg.Layout.ChannelModesDir == "" {
		return nil, fmt.Errorf("server config layout with channel modes dir is required")
	}

	store := NewMockStore(cfg.MocksDir)
	if err := store.Load(); err != nil {
		return nil, fmt.Errorf("failed to load mocks: %w", err)
	}

	bus := NewEventBus()
	proxyMgr := NewProxyManager(cfg.Target)
	jsonLogs := cfg.JSONLogs
	modeRegistry, err := NewChannelModeRegistry(cfg.Layout.ChannelModesDir, bus, jsonLogs)
	if err != nil {
		return nil, fmt.Errorf("failed to load channel mode registry: %w", err)
	}
	socketHub := NewSocketHub(bus, jsonLogs, modeRegistry)
	descriptorsDir := cfg.Layout.DescriptorsDir
	schemaRegistry, err := NewSchemaRegistry(descriptorsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load schema registry: %w", err)
	}
	socketHub.SetSchemas(schemaRegistry)
	if err := LoadAdapterProfiles(cfg.Layout.AdapterProfilesDir); err != nil {
		return nil, fmt.Errorf("failed to load adapter profiles: %w", err)
	}
	eventTemplates, err := NewEventTemplateRegistry(cfg.Layout.EventTemplatesDir, schemaRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to load event template registry: %w", err)
	}
	eventSequences, err := NewEventSequenceRegistry(cfg.Layout.SequencesDir, eventTemplates, schemaRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to load event sequence registry: %w", err)
	}
	recorder, err := NewRecorder(cfg.Layout.RecordingsDir, schemaRegistry, modeRegistry, bus, jsonLogs)
	if err != nil {
		return nil, fmt.Errorf("failed to load recorder: %w", err)
	}
	modeRegistry.OnChange(recorder.HandleModeChange)
	socketHub.SetRecorder(recorder)
	liveTargets := NewLiveTargetManager(cfg.LiveTarget, cfg.ConfigStore)
	liveBridge := NewLiveBridge(liveTargets, socketHub)
	socketHub.SetLiveBridge(liveBridge)
	playerBroadcaster := NewPlayerBroadcaster()
	sequencePlayer := NewSequencePlayer(eventSequences, eventTemplates, schemaRegistry, socketHub, playerBroadcaster, nil)

	var certPath, keyPath string
	if cfg.HTTPS {
		var err error
		certPath, keyPath, err = EnsureCert(cfg.CertDir)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare TLS certificate: %w", err)
		}
	}

	mux := http.NewServeMux()

	var ipStrings []string
	for _, ip := range localIPs() {
		ipStrings = append(ipStrings, ip.String())
	}
	info := ServerInfo{
		Port:       cfg.Port,
		Target:     cfg.Target,
		LiveTarget: cfg.LiveTarget,
		HTTPS:      cfg.HTTPS,
		MocksDir:   cfg.MocksDir,
		LocalIPs:   ipStrings,
		Version:    version,
	}

	RegisterUI(mux, store, bus, proxyMgr, liveTargets.Target, info, cfg.ServeUI)
	RegisterSocketRoutes(mux, socketHub, schemaRegistry)
	RegisterChannelModeRoutes(mux, modeRegistry)
	RegisterLiveTargetRoutes(mux, liveTargets)
	RegisterRecordingRoutes(mux, recorder)
	RegisterSchemaRoutes(mux, schemaRegistry)
	RegisterEventTemplateRoutes(mux, eventTemplates, socketHub, schemaRegistry)
	RegisterSequenceRoutes(mux, eventSequences, sequencePlayer, playerBroadcaster)

	// Main proxy/mock handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__ditto__/") {
			return
		}
		if IsWebSocketRequest(r) {
			if !isAllowedSocketAPIRequest(r) {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			if shouldProxyWebSocket(r) && proxyMgr.Target() != "" {
				proxyMgr.ServeHTTP(w, r)
				return
			}
			socketHub.ServeHTTP(w, r)
			return
		}

		// Serve favicon at root
		if r.URL.Path == "/favicon.ico" {
			http.Redirect(w, r, "/__ditto__/favicon.png", http.StatusFound)
			return
		}

		start := time.Now()

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		requestHost, remoteAddr, protocol := r.Host, r.RemoteAddr, r.Proto
		requestURI := r.RequestURI
		if requestURI == "" {
			requestURI = r.URL.RequestURI()
		}
		requestPath := requestURI
		requestURL := scheme + "://" + requestHost + requestURI
		if originalURL, err := url.ParseRequestURI(requestURI); err == nil && originalURL.IsAbs() {
			requestURL = originalURL.String()
			requestPath = originalURL.RequestURI()
		}
		reqHeaders := r.Header.Clone()
		var reqBody []byte
		var reqReadErr error
		if r.Body != nil {
			reqBody, reqReadErr = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(reqBody))
		}
		requestBody, requestPayload := captureLogPayload(reqBody, int64(len(reqBody)), reqHeaders.Get("Content-Type"), reqHeaders.Get("Content-Encoding"), reqReadErr)
		if r.Body == nil {
			requestPayload.CaptureStatus = "not_captured"
		}
		formFields, uploadedFiles, formTruncated := parseRequestForm(reqBody, reqHeaders.Get("Content-Type"), reqReadErr != nil)
		requestContext := LogEvent{URL: requestURL, Host: requestHost, RemoteAddr: remoteAddr, Protocol: protocol,
			RequestBody: requestBody, RequestPayload: requestPayload, RequestHeaders: reqHeaders,
			RequestFormFields: formFields, RequestFiles: uploadedFiles, RequestFormsTruncated: formTruncated}
		if reqReadErr != nil {
			capture := newResponseCapture(w)
			capture.Header().Set("Content-Type", "application/json")
			capture.WriteHeader(http.StatusBadRequest)
			_, _ = capture.Write([]byte(`{"error":"failed to read request body"}`))
			responseBody, responsePayload := capture.responsePayload()
			event := requestContext
			event.Type, event.Method, event.Path, event.Status = "MISS", r.Method, requestPath, http.StatusBadRequest
			event.DurationMs, event.ResponseBody, event.ResponsePayload = time.Since(start).Milliseconds(), responseBody, responsePayload
			event.ResponseHeaders, event.Error = capture.responseHeaders(), reqReadErr.Error()
			publishLogEvent(jsonLogs, bus, event)
			return
		}

		resolved := store.MatchAndResolve(r, reqBody)
		if resolved != nil {
			if resolved.DelayMs > 0 {
				time.Sleep(time.Duration(resolved.DelayMs) * time.Millisecond)
			}
			capture := newResponseCapture(w)
			for k, v := range resolved.Headers {
				capture.Header().Set(k, v)
			}
			if capture.Header().Get("Content-Type") == "" {
				capture.Header().Set("Content-Type", "application/json")
			}
			capture.WriteHeader(resolved.Status)
			_, _ = capture.Write(resolved.Body)
			responseBody, responsePayload := capture.responsePayload()

			event := requestContext
			event.Type, event.Method, event.Path = "MOCK", r.Method, requestPath
			event.Status, event.DurationMs, event.MockIndex = capture.statusCode, time.Since(start).Milliseconds(), resolved.MockIndex
			event.ResponseBody, event.ResponsePayload, event.ResponseHeaders = responseBody, responsePayload, capture.responseHeaders()
			if capture.writeErr != nil {
				event.Error = capture.writeErr.Error()
			}
			if resolved.IsSequence {
				event.SequenceStep = resolved.SequenceStep
				event.SequenceLen = resolved.SequenceLen
			}
			publishLogEvent(jsonLogs, bus, event)
			return
		}

		capture := newResponseCapture(w)
		served, target := proxyMgr.ServeHTTPWithTarget(capture, r)
		if served {
			responseBody, responsePayload := capture.responsePayload()

			event := requestContext
			event.Type, event.Method, event.Path, event.Status = "PROXY", r.Method, requestPath, capture.statusCode
			event.DurationMs, event.ResponseBody, event.ResponsePayload = time.Since(start).Milliseconds(), responseBody, responsePayload
			event.ResponseHeaders, event.Target = capture.responseHeaders(), target
			if capture.proxyError != "" {
				event.Error = capture.proxyError
			}
			if capture.writeErr != nil {
				event.Error = capture.writeErr.Error()
			}
			publishLogEvent(jsonLogs, bus, event)
			return
		}

		capture = newResponseCapture(w)
		capture.Header().Set("Content-Type", "application/json")
		capture.WriteHeader(http.StatusBadGateway)
		_, _ = capture.Write([]byte(`{"error": "no mock found and no target configured"}`))
		responseBody, responsePayload := capture.responsePayload()

		event := requestContext
		event.Type, event.Method, event.Path, event.Status = "MISS", r.Method, requestPath, http.StatusBadGateway
		event.DurationMs, event.ResponseBody, event.ResponsePayload = time.Since(start).Milliseconds(), responseBody, responsePayload
		event.ResponseHeaders = capture.responseHeaders()
		if capture.writeErr != nil {
			event.Error = capture.writeErr.Error()
		}
		if reqReadErr != nil {
			event.Error = "request body: " + reqReadErr.Error()
		}
		publishLogEvent(jsonLogs, bus, event)
	})

	return &Server{
		Mux:        mux,
		Store:      store,
		Bus:        bus,
		ProxyMgr:   proxyMgr,
		SocketHub:  socketHub,
		Modes:      modeRegistry,
		Recorder:   recorder,
		Live:       liveBridge,
		LiveTarget: liveTargets,
		Schemas:    schemaRegistry,
		Templates:  eventTemplates,
		Sequences:  eventSequences,
		Player:     sequencePlayer,
		Info:       info,
		Config:     cfg,
		CertPath:   certPath,
		KeyPath:    keyPath,
	}, nil
}

// ListenAndServe starts the HTTP server (blocking).
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("0.0.0.0:%d", s.Config.Port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	if s.Config.HTTPS {
		return http.ServeTLS(ln, s.Mux, s.CertPath, s.KeyPath)
	}
	return http.Serve(ln, s.Mux)
}

// ListenAndServeAsync starts the HTTP server in a goroutine and returns
// once the server is ready to accept connections.
func (s *Server) ListenAndServeAsync() error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.ListenAndServe()
	}()

	// Wait for the server to start (or fail)
	for i := 0; i < 50; i++ {
		select {
		case err := <-errCh:
			return err
		default:
		}
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/__ditto__/api/mocks", s.Config.Port))
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("server failed to start within 5s")
}

// Stop closes the listener, freeing the port.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Player != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = s.Player.Shutdown(ctx)
		cancel()
	}
	if s.listener != nil {
		err := s.listener.Close()
		s.listener = nil
		return err
	}
	return nil
}

// Restart stops the server and starts it on a new port.
func (s *Server) Restart(newPort int) error {
	s.Stop()
	time.Sleep(500 * time.Millisecond)
	s.Config.Port = newPort
	s.Info.Port = newPort
	return s.ListenAndServeAsync()
}

// Port returns the current port.
func (s *Server) Port() int {
	return s.Config.Port
}

// CheckPort tests if a port is available. Returns nil if free,
// or an error with details about what's using it.
func CheckPort(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535")
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		process := identifyProcess(port)
		if process != "" {
			return fmt.Errorf("port %d is in use by %s", port, process)
		}
		return fmt.Errorf("port %d is in use", port)
	}
	ln.Close()
	return nil
}

// identifyProcess tries to find which process is using a port.
func identifyProcess(port int) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	out, err := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", port)).Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	pid := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	nameOut, err := exec.Command("ps", "-p", pid, "-o", "comm=").Output()
	if err != nil {
		return "PID " + pid
	}
	name := strings.TrimSpace(string(nameOut))
	if name != "" {
		return fmt.Sprintf("%s (PID %s)", name, pid)
	}
	return "PID " + pid
}

// SuggestPorts returns a list of common alternative ports, skipping any that are in use.
func SuggestPorts(exclude int) []int {
	candidates := []int{8888, 8080, 3001, 9000, 9090, 4000}
	var available []int
	for _, p := range candidates {
		if p == exclude {
			continue
		}
		if CheckPort(p) == nil {
			available = append(available, p)
		}
	}
	return available
}

// portStr helper for API responses
func portStr(port int) string {
	return strconv.Itoa(port)
}

const MaxLogPayloadCaptureBytes = 1 << 20
const MaxLogFormFields = 1000

// responseCapture preserves streaming while retaining at most one body prefix.
type responseCapture struct {
	http.ResponseWriter
	statusCode    int
	headerWritten bool
	headers       http.Header
	body          bytes.Buffer
	totalBytes    int64
	writeErr      error
	proxyError    string
}

func (rc *responseCapture) WriteHeader(code int) {
	final := code >= http.StatusOK || code == http.StatusSwitchingProtocols
	if final && rc.headerWritten {
		return
	}
	if final {
		rc.statusCode = code
		rc.headerWritten = true
		rc.headers = rc.ResponseWriter.Header().Clone()
	}
	rc.ResponseWriter.WriteHeader(code)
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	if !rc.headerWritten {
		rc.statusCode, rc.headerWritten = http.StatusOK, true
		rc.headers = rc.ResponseWriter.Header().Clone()
	}
	n, err := rc.ResponseWriter.Write(b)
	if n > 0 {
		rc.totalBytes += int64(n)
		remaining := MaxLogPayloadCaptureBytes - rc.body.Len()
		if remaining > 0 {
			if n < remaining {
				remaining = n
			}
			_, _ = rc.body.Write(b[:remaining])
		}
	}
	if err != nil {
		rc.writeErr = err
	}
	return n, err
}

func newResponseCapture(w http.ResponseWriter) *responseCapture {
	return &responseCapture{ResponseWriter: w, statusCode: http.StatusOK}
}

// Unwrap lets http.ResponseController reach optional transport capabilities.
func (rc *responseCapture) Unwrap() http.ResponseWriter { return rc.ResponseWriter }

func (rc *responseCapture) Flush() {
	_ = rc.FlushError()
}

func (rc *responseCapture) FlushError() error {
	if !rc.headerWritten {
		rc.statusCode, rc.headerWritten = http.StatusOK, true
		rc.headers = rc.ResponseWriter.Header().Clone()
	}
	err := http.NewResponseController(rc.ResponseWriter).Flush()
	if err != nil {
		rc.writeErr = err
	}
	return err
}

func (rc *responseCapture) responseHeaders() http.Header {
	if rc.headers != nil {
		return rc.headers.Clone()
	}
	return rc.ResponseWriter.Header().Clone()
}

// decodedBody returns the captured body as text suitable for the log stream
// and for "Save as mock". Upstreams are asked for identity encoding, but some
// compress anyway, so gzip/deflate are decoded here as a fallback. Anything
// that still isn't valid UTF-8 is binary and is summarised rather than dumped
// as replacement characters.
func (rc *responseCapture) decodedBody() string {
	body, _, _ := rc.decodedBodyInfo()
	return body
}

func (rc *responseCapture) decodedBodyInfo() (string, bool, error) {
	headers := rc.responseHeaders()
	wire := rc.body.Bytes()
	if len(wire) == 0 {
		return "", false, nil
	}
	wireTruncated := rc.totalBytes > int64(len(wire))
	raw, supported, decodedTruncated, decodeErr := decodePayload(wire, headers.Get("Content-Encoding"))
	truncated := wireTruncated || decodedTruncated
	if decodeErr != nil && !truncated {
		return "", truncated, decodeErr
	}
	if !supported {
		return fmt.Sprintf("<binary response: %d bytes, content-type=%q, content-encoding=%q>",
			rc.totalBytes, headers.Get("Content-Type"), headers.Get("Content-Encoding")), truncated, nil
	}
	if truncated && isTextContentType(headers.Get("Content-Type")) && !utf8.Valid(raw) {
		if prefix, ok := incompleteUTF8Prefix(raw); ok {
			raw = prefix
		}
	}
	if !utf8.Valid(raw) || !isTextContentType(headers.Get("Content-Type")) {
		return fmt.Sprintf("<binary response: %d bytes, content-type=%q, content-encoding=%q>",
			rc.totalBytes, headers.Get("Content-Type"), headers.Get("Content-Encoding")), truncated, nil
	}
	return string(raw), truncated, decodeErr
}

func decodePayload(raw []byte, encoding string) ([]byte, bool, bool, error) {
	var reader io.ReadCloser
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return raw, true, false, nil
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, true, false, err
		}
		reader = zr
	case "deflate":
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err == nil {
			reader = zr
		} else {
			reader = flate.NewReader(bytes.NewReader(raw))
		}
	default:
		return raw, false, false, nil
	}
	out, readErr := io.ReadAll(io.LimitReader(reader, MaxLogPayloadCaptureBytes+1))
	closeErr := reader.Close()
	truncated := len(out) > MaxLogPayloadCaptureBytes
	if truncated {
		out = out[:MaxLogPayloadCaptureBytes]
	}
	if readErr != nil {
		return out, true, truncated, readErr
	}
	if closeErr != nil {
		return out, true, truncated, closeErr
	}
	return out, true, truncated, nil
}

func incompleteUTF8Prefix(data []byte) ([]byte, bool) {
	start := len(data)
	for start > 0 && len(data)-start < utf8.UTFMax {
		start--
		if utf8.Valid(data[:start]) && !utf8.FullRune(data[start:]) {
			return data[:start], true
		}
	}
	return data, false
}

func isTextContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return contentType == ""
	}
	mediaType = strings.ToLower(mediaType)
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" ||
		strings.HasSuffix(mediaType, "+json") || mediaType == "application/xml" ||
		strings.HasSuffix(mediaType, "+xml") || mediaType == "application/javascript" ||
		mediaType == "application/x-www-form-urlencoded" || mediaType == "multipart/form-data"
}

func (rc *responseCapture) responsePayload() (string, *LogPayloadMetadata) {
	captured := rc.body.Bytes()
	body, decodedTruncated, decodeErr := rc.decodedBodyInfo()
	headers := rc.responseHeaders()
	meta := &LogPayloadMetadata{SizeBytes: rc.totalBytes, CapturedBytes: int64(len(captured)),
		ContentType: headers.Get("Content-Type"), Encoding: headers.Get("Content-Encoding"),
		RawBase64: base64.StdEncoding.EncodeToString(captured)}
	switch {
	case decodeErr != nil && !decodedTruncated && rc.totalBytes <= int64(len(captured)):
		meta.CaptureStatus, meta.Error = "error", decodeErr.Error()
	case rc.writeErr != nil:
		meta.CaptureStatus, meta.Error = "error", rc.writeErr.Error()
	case rc.totalBytes == 0:
		meta.CaptureStatus = "empty"
	case decodedTruncated || rc.totalBytes > int64(len(captured)):
		meta.CaptureStatus = "truncated"
		if decodeErr != nil {
			meta.Error = decodeErr.Error()
		}
	case decodeErr != nil:
		meta.CaptureStatus, meta.Error = "error", decodeErr.Error()
	case strings.HasPrefix(body, "<binary response:"):
		meta.CaptureStatus = "binary"
	default:
		meta.CaptureStatus = "captured"
	}
	return body, meta
}

func captureLogPayload(raw []byte, total int64, contentType, encoding string, captureErr error) (string, *LogPayloadMetadata) {
	captured := raw
	truncated := total > int64(len(captured))
	if len(captured) > MaxLogPayloadCaptureBytes {
		captured, truncated = captured[:MaxLogPayloadCaptureBytes], true
	}
	displayBytes, supported, decodedTruncated, decodeErr := decodePayload(captured, encoding)
	truncated = truncated || decodedTruncated
	meta := &LogPayloadMetadata{SizeBytes: total, CapturedBytes: int64(len(captured)), ContentType: contentType,
		Encoding: encoding, RawBase64: base64.StdEncoding.EncodeToString(captured)}
	switch {
	case captureErr != nil:
		meta.CaptureStatus, meta.Error = "error", captureErr.Error()
	case total == 0:
		meta.CaptureStatus = "empty"
	case decodeErr != nil && !truncated && captureErr == nil:
		meta.CaptureStatus, meta.Error = "error", decodeErr.Error()
	case !supported:
		meta.CaptureStatus = "binary"
	case truncated:
		meta.CaptureStatus = "truncated"
	case !utf8.Valid(displayBytes) || !isTextContentType(contentType):
		meta.CaptureStatus = "binary"
	default:
		meta.CaptureStatus = "captured"
	}
	if decodeErr != nil && meta.Error == "" {
		meta.Error = decodeErr.Error()
	}
	if meta.CaptureStatus == "binary" {
		return fmt.Sprintf("<binary request: %d bytes, content-type=%q, content-encoding=%q>", total, contentType, encoding), meta
	}
	if (truncated || captureErr != nil) && isTextContentType(contentType) && !utf8.Valid(displayBytes) {
		if prefix, ok := incompleteUTF8Prefix(displayBytes); ok {
			displayBytes = prefix
		}
	}
	if !utf8.Valid(displayBytes) {
		return "", meta
	}
	return string(displayBytes), meta
}

func parseRequestForm(body []byte, contentType string, inputIncomplete bool) (map[string][]string, []LogFileMetadata, bool) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, nil, false
	}
	switch mediaType {
	case "application/x-www-form-urlencoded":
		captured := body[:min(len(body), MaxLogPayloadCaptureBytes)]
		truncated := inputIncomplete || len(captured) < len(body)
		fieldCount := 1
		for i, b := range captured {
			if b == '&' {
				fieldCount++
				if fieldCount > MaxLogFormFields {
					captured, truncated = captured[:i], true
					break
				}
			}
		}
		values, err := url.ParseQuery(string(captured))
		if err != nil && !truncated {
			return nil, nil, false
		}
		return map[string][]string(values), nil, truncated
	case "multipart/form-data":
		boundary := params["boundary"]
		if boundary == "" {
			return nil, nil, false
		}
		reader := multipart.NewReader(bytes.NewReader(body), boundary)
		fields := make(map[string][]string)
		fieldBytesRemaining := MaxLogPayloadCaptureBytes
		fieldCount := 0
		partCount := 0
		formsTruncated := inputIncomplete
		var files []LogFileMetadata
		for {
			part, err := reader.NextPart()
			if err != nil {
				if err != io.EOF {
					formsTruncated = true
				}
				break
			}
			partCount++
			if partCount > MaxLogFormFields {
				formsTruncated = true
				_ = part.Close()
				break
			}
			if name := part.FileName(); name != "" {
				size, readErr := io.Copy(io.Discard, part)
				status := "metadata_only"
				if readErr != nil {
					status = "error"
				}
				files = append(files, LogFileMetadata{Name: name, ContentType: part.Header.Get("Content-Type"),
					SizeBytes: size, CapturedBytes: 0, CaptureStatus: status})
				_ = part.Close()
				continue
			}
			if fieldCount >= MaxLogFormFields {
				formsTruncated = true
				_ = part.Close()
				break
			}
			if fieldBytesRemaining <= 0 {
				formsTruncated = true
				_ = part.Close()
				continue
			}
			partData, readErr := io.ReadAll(io.LimitReader(part, int64(fieldBytesRemaining)+1))
			_ = part.Close()
			if len(partData) > fieldBytesRemaining {
				partData = partData[:fieldBytesRemaining]
				formsTruncated = true
			}
			if readErr != nil {
				formsTruncated = true
				continue
			}
			fields[part.FormName()] = append(fields[part.FormName()], string(partData))
			fieldCount++
			fieldBytesRemaining -= len(partData)
		}
		if len(fields) == 0 {
			fields = nil
		}
		return fields, files, formsTruncated
	}
	return nil, nil, false
}

// logRequest writes a single request log line.
func logRequest(jsonMode bool, e LogEvent) {
	logRequestTo(os.Stdout, jsonMode, e)
}

func logRequestTo(w io.Writer, jsonMode bool, e LogEvent) {
	if jsonMode {
		data, err := json.Marshal(e)
		if err != nil {
			return
		}
		fmt.Fprintln(w, string(data))
		return
	}
	fmt.Fprintf(w, "%s %s %-6s %s %s → %d (%dms)\n",
		e.Timestamp, e.ID, e.Type, e.Method, e.Path, e.Status, e.DurationMs)
}
