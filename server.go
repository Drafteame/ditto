package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"ditto/internal/adapter"
	"ditto/internal/capture"
	"ditto/internal/channelmode"
	"ditto/internal/eventlog"
	"ditto/internal/httpmock"
	"ditto/internal/recording"
	"ditto/internal/schema"
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
		if cw, ok := w.(*capture.Writer); ok {
			cw.SetProxyError(err.Error())
		}
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
	}

	pm.mu.Lock()
	pm.proxy = proxy
	pm.target = target
	pm.mu.Unlock()
	return nil
}

// Director returns the current reverse proxy's request director and target
// as one snapshot, or a nil director when no target is configured.
func (pm *ProxyManager) Director() (func(*http.Request), string) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if pm.proxy == nil || pm.target == "" {
		return nil, ""
	}
	return pm.proxy.Director, pm.target
}

func (pm *ProxyManager) Target() string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.target
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
	Store      *httpmock.Store
	Bus        *eventlog.Bus
	ProxyMgr   *ProxyManager
	SocketHub  *SocketHub
	Modes      *channelmode.Registry
	Recorder   *recording.Recorder
	LiveTarget *LiveTargetManager
	Schemas    *schema.Registry
	Templates  *EventTemplateRegistry
	Sequences  *EventSequenceRegistry
	Player     *SequencePlayer
	Info       ServerInfo
	Config     ServerConfig
	CertPath   string
	KeyPath    string

	mu         sync.Mutex
	http       *http.Server
	listener   net.Listener
	cancelBase context.CancelFunc
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

	store := httpmock.NewStore(cfg.MocksDir)
	if err := store.Load(); err != nil {
		return nil, fmt.Errorf("failed to load mocks: %w", err)
	}

	bus := eventlog.NewBus(eventlog.NewPrinter(os.Stdout, cfg.JSONLogs))
	proxyMgr := NewProxyManager(cfg.Target)
	modeRegistry, err := channelmode.NewRegistry(cfg.Layout.ChannelModesDir, bus)
	if err != nil {
		return nil, fmt.Errorf("failed to load channel mode registry: %w", err)
	}
	// Load order matters: templates skip entries whose adapter or schema type
	// is unknown, and sequences skip entries whose template is unknown.
	schemaRegistry, err := schema.NewRegistry(cfg.Layout.DescriptorsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load schema registry: %w", err)
	}
	adapters := &adapter.Registry{}
	if err := adapters.Load(cfg.Layout.AdapterProfilesDir); err != nil {
		return nil, fmt.Errorf("failed to load adapter profiles: %w", err)
	}
	eventTemplates, err := NewEventTemplateRegistry(cfg.Layout.EventTemplatesDir, schemaRegistry, adapters)
	if err != nil {
		return nil, fmt.Errorf("failed to load event template registry: %w", err)
	}
	eventSequences, err := NewEventSequenceRegistry(cfg.Layout.SequencesDir, eventTemplates, schemaRegistry, adapters)
	if err != nil {
		return nil, fmt.Errorf("failed to load event sequence registry: %w", err)
	}
	recorder, err := recording.NewRecorder(recording.Config{Dir: cfg.Layout.RecordingsDir, Schemas: schemaRegistry,
		Adapters: adapters, Modes: modeRegistry, Bus: bus})
	if err != nil {
		return nil, fmt.Errorf("failed to load recorder: %w", err)
	}
	liveTargets := NewLiveTargetManager(cfg.LiveTarget, cfg.ConfigStore)
	socketHub := NewSocketHub(SocketHubConfig{Bus: bus, Modes: modeRegistry, Schemas: schemaRegistry,
		Adapters: adapters, Recorder: recorder, LiveTargets: liveTargets})
	// Registered after the hub's own listener to keep the original call order.
	modeRegistry.OnChange(recorder.HandleModeChange)
	playerBroadcaster := NewPlayerBroadcaster()
	sequencePlayer := NewSequencePlayer(SequencePlayerConfig{Sequences: eventSequences, Schemas: schemaRegistry, Hub: socketHub, Broadcaster: playerBroadcaster})

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
	RegisterSocketRoutes(mux, socketHub)
	RegisterChannelModeRoutes(mux, modeRegistry)
	RegisterLiveTargetRoutes(mux, liveTargets)
	RegisterRecordingRoutes(mux, recorder)
	RegisterSchemaRoutes(mux, schemaRegistry)
	RegisterEventTemplateRoutes(mux, eventTemplates, socketHub)
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
			if shouldProxyWebSocket(r) {
				if direct, target := proxyMgr.Director(); direct != nil {
					socketHub.Tunnel(w, r, target, direct)
					return
				}
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
		requestBody, requestPayload := capture.Payload(reqBody, int64(len(reqBody)), reqHeaders.Get("Content-Type"), reqHeaders.Get("Content-Encoding"), reqReadErr)
		if r.Body == nil {
			requestPayload.CaptureStatus = "not_captured"
		}
		formFields, uploadedFiles, formTruncated := capture.Form(reqBody, reqHeaders.Get("Content-Type"), reqReadErr != nil)
		requestContext := eventlog.Event{URL: requestURL, Host: requestHost, RemoteAddr: remoteAddr, Protocol: protocol,
			RequestBody: requestBody, RequestPayload: requestPayload, RequestHeaders: reqHeaders,
			RequestFormFields: formFields, RequestFiles: uploadedFiles, RequestFormsTruncated: formTruncated}
		if reqReadErr != nil {
			cw := capture.NewWriter(w)
			cw.Header().Set("Content-Type", "application/json")
			cw.WriteHeader(http.StatusBadRequest)
			_, _ = cw.Write([]byte(`{"error":"failed to read request body"}`))
			responseBody, responsePayload := cw.Payload()
			event := requestContext
			event.Type, event.Method, event.Path, event.Status = "MISS", r.Method, requestPath, http.StatusBadRequest
			event.DurationMs, event.ResponseBody, event.ResponsePayload = time.Since(start).Milliseconds(), responseBody, responsePayload
			event.ResponseHeaders, event.Error = cw.Headers(), reqReadErr.Error()
			bus.Publish(event)
			return
		}

		resolved := store.Resolve(r, reqBody)
		if resolved != nil {
			if resolved.DelayMs > 0 {
				time.Sleep(time.Duration(resolved.DelayMs) * time.Millisecond)
			}
			cw := capture.NewWriter(w)
			for k, v := range resolved.Headers {
				cw.Header().Set(k, v)
			}
			if cw.Header().Get("Content-Type") == "" {
				cw.Header().Set("Content-Type", "application/json")
			}
			cw.WriteHeader(resolved.Status)
			_, _ = cw.Write(resolved.Body)
			responseBody, responsePayload := cw.Payload()

			event := requestContext
			event.Type, event.Method, event.Path = "MOCK", r.Method, requestPath
			event.Status, event.DurationMs, event.MockIndex = cw.Status(), time.Since(start).Milliseconds(), resolved.MockIndex
			event.ResponseBody, event.ResponsePayload, event.ResponseHeaders = responseBody, responsePayload, cw.Headers()
			if cw.WriteErr() != nil {
				event.Error = cw.WriteErr().Error()
			}
			if resolved.IsSequence {
				event.SequenceStep = resolved.SequenceStep
				event.SequenceLen = resolved.SequenceLen
			}
			bus.Publish(event)
			return
		}

		cw := capture.NewWriter(w)
		served, target := proxyMgr.ServeHTTPWithTarget(cw, r)
		if served {
			responseBody, responsePayload := cw.Payload()

			event := requestContext
			event.Type, event.Method, event.Path, event.Status = "PROXY", r.Method, requestPath, cw.Status()
			event.DurationMs, event.ResponseBody, event.ResponsePayload = time.Since(start).Milliseconds(), responseBody, responsePayload
			event.ResponseHeaders, event.Target = cw.Headers(), target
			if cw.ProxyError() != "" {
				event.Error = cw.ProxyError()
			}
			if cw.WriteErr() != nil {
				event.Error = cw.WriteErr().Error()
			}
			bus.Publish(event)
			return
		}

		cw = capture.NewWriter(w)
		cw.Header().Set("Content-Type", "application/json")
		cw.WriteHeader(http.StatusBadGateway)
		_, _ = cw.Write([]byte(`{"error": "no mock found and no target configured"}`))
		responseBody, responsePayload := cw.Payload()

		event := requestContext
		event.Type, event.Method, event.Path, event.Status = "MISS", r.Method, requestPath, http.StatusBadGateway
		event.DurationMs, event.ResponseBody, event.ResponsePayload = time.Since(start).Milliseconds(), responseBody, responsePayload
		event.ResponseHeaders = cw.Headers()
		if cw.WriteErr() != nil {
			event.Error = cw.WriteErr().Error()
		}
		if reqReadErr != nil {
			event.Error = "request body: " + reqReadErr.Error()
		}
		bus.Publish(event)
	})

	return &Server{
		Mux:        mux,
		Store:      store,
		Bus:        bus,
		ProxyMgr:   proxyMgr,
		SocketHub:  socketHub,
		Modes:      modeRegistry,
		Recorder:   recorder,
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

// Start binds the configured port and serves in the background. It returns
// once the port is bound, or the bind error.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.http == nil {
		baseCtx, cancel := context.WithCancel(context.Background())
		s.cancelBase = cancel
		s.http = &http.Server{
			Handler:           s.Mux,
			ReadHeaderTimeout: 10 * time.Second,
			// Cancelled on Shutdown so streaming (SSE) handlers return.
			BaseContext: func(net.Listener) context.Context { return baseCtx },
		}
	}
	ln, err := listen(s.Config.Port)
	if err != nil {
		return err
	}
	s.listener = ln
	go s.serve(s.http, ln)
	return nil
}

func listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
}

func (s *Server) serve(srv *http.Server, ln net.Listener) {
	var err error
	if s.Config.HTTPS {
		err = srv.ServeTLS(ln, s.CertPath, s.KeyPath)
	} else {
		err = srv.Serve(ln)
	}
	// Restart closes the old listener on purpose; Shutdown closes the server.
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		log.Printf("server on %s stopped: %v", ln.Addr(), err)
	}
}

// Restart moves the server to newPort. The new port is bound before the old
// listener closes, so a failed bind leaves the server on its current port,
// and connections already open (SSE streams, WebSockets) stay up. Running
// sequence players are stopped, as they always were on a port change.
func (s *Server) Restart(newPort int) error {
	s.stopPlayers()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.http == nil {
		return fmt.Errorf("server is not running")
	}
	ln, err := listen(newPort)
	if err != nil {
		return err
	}
	old := s.listener
	s.listener = ln
	s.Config.Port = newPort
	s.Info.Port = newPort
	go s.serve(s.http, ln)
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// Shutdown stops sequence players, ends streaming handlers and stops the
// server, waiting for in-flight requests until ctx is done. Hijacked
// WebSocket connections are not waited for.
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopPlayers()
	s.mu.Lock()
	srv, cancel := s.http, s.cancelBase
	s.listener = nil
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	cancel()
	return srv.Shutdown(ctx)
}

func (s *Server) stopPlayers() {
	if s.Player == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.Player.Shutdown(ctx)
}

// Port returns the current port.
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
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
