package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	pm.mu.RLock()
	proxy := pm.proxy
	pm.mu.RUnlock()

	if proxy == nil {
		return false
	}
	proxy.ServeHTTP(w, r)
	return true
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
	eventTemplates, err := NewEventTemplateRegistry(cfg.Layout.EventTemplatesDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load event template registry: %w", err)
	}
	eventSequences, err := NewEventSequenceRegistry(cfg.Layout.SequencesDir, eventTemplates, schemaRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to load event sequence registry: %w", err)
	}
	if err := LoadAdapterProfiles(cfg.Layout.AdapterProfilesDir); err != nil {
		return nil, fmt.Errorf("failed to load adapter profiles: %w", err)
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

		var reqBody []byte
		if r.Body != nil && r.ContentLength != 0 {
			reqBody, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(reqBody))
		}

		// Snapshot client headers before proxying — ReverseProxy mutates
		// r.Header (e.g. adds X-Forwarded-For), so we copy first to log
		// the headers exactly as the client sent them.
		reqHeaders := r.Header.Clone()

		resolved := store.MatchAndResolve(r, reqBody)
		if resolved != nil {
			if resolved.DelayMs > 0 {
				time.Sleep(time.Duration(resolved.DelayMs) * time.Millisecond)
			}
			duration := time.Since(start).Milliseconds()

			for k, v := range resolved.Headers {
				w.Header().Set(k, v)
			}
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(resolved.Status)
			w.Write(resolved.Body)

			event := LogEvent{
				Timestamp:      time.Now().Format("15:04:05"),
				Type:           "MOCK",
				Method:         r.Method,
				Path:           r.URL.RequestURI(),
				Status:         resolved.Status,
				DurationMs:     duration,
				ResponseBody:   string(resolved.Body),
				RequestHeaders: reqHeaders,
				MockIndex:      resolved.MockIndex,
			}
			if resolved.IsSequence {
				event.SequenceStep = resolved.SequenceStep
				event.SequenceLen = resolved.SequenceLen
			}
			logRequest(jsonLogs, event)
			bus.Publish(event)
			return
		}

		if proxyMgr.Target() != "" {
			capture := &responseCapture{ResponseWriter: w, statusCode: 200}
			proxyStart := time.Now()
			proxyMgr.ServeHTTP(capture, r)
			duration := time.Since(proxyStart).Milliseconds()

			event := LogEvent{
				Timestamp:      time.Now().Format("15:04:05"),
				Type:           "PROXY",
				Method:         r.Method,
				Path:           r.URL.RequestURI(),
				Status:         capture.statusCode,
				DurationMs:     duration,
				ResponseBody:   capture.body.String(),
				RequestHeaders: reqHeaders,
			}
			logRequest(jsonLogs, event)
			bus.Publish(event)
			return
		}

		duration := time.Since(start).Milliseconds()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error": "no mock found and no target configured"}`))

		event := LogEvent{
			Timestamp:      time.Now().Format("15:04:05"),
			Type:           "MISS",
			Method:         r.Method,
			Path:           r.URL.RequestURI(),
			Status:         502,
			DurationMs:     duration,
			ResponseBody:   `{"error": "no mock found and no target configured"}`,
			RequestHeaders: reqHeaders,
		}
		logRequest(jsonLogs, event)
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

// responseCapture wraps http.ResponseWriter to capture the status code and body.
type responseCapture struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
}

func (rc *responseCapture) WriteHeader(code int) {
	rc.statusCode = code
	rc.ResponseWriter.WriteHeader(code)
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	rc.body.Write(b)
	return rc.ResponseWriter.Write(b)
}

// logRequest writes a single request log line.
func logRequest(jsonMode bool, e LogEvent) {
	if jsonMode {
		data, err := json.Marshal(e)
		if err != nil {
			return
		}
		fmt.Fprintln(os.Stdout, string(data))
		return
	}
	fmt.Fprintf(os.Stdout, "%s %-6s %s %s → %d (%dms)\n",
		e.Timestamp, e.Type, e.Method, e.Path, e.Status, e.DurationMs)
}
