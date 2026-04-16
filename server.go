package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
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
	Port     int
	Target   string
	MocksDir string
	HTTPS    bool
	CertDir  string
	ServeUI  bool
	JSONLogs bool
}

// Server holds the running server state.
type Server struct {
	Mux      *http.ServeMux
	Store    *MockStore
	Bus      *EventBus
	ProxyMgr *ProxyManager
	Info     ServerInfo
	Config   ServerConfig
	CertPath string
	KeyPath  string
}

// NewServer creates and configures the HTTP server with all routes.
func NewServer(cfg ServerConfig) (*Server, error) {
	store := NewMockStore(cfg.MocksDir)
	if err := store.Load(); err != nil {
		return nil, fmt.Errorf("failed to load mocks: %w", err)
	}

	bus := NewEventBus()
	proxyMgr := NewProxyManager(cfg.Target)

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
		Port:     cfg.Port,
		Target:   cfg.Target,
		HTTPS:    cfg.HTTPS,
		MocksDir: cfg.MocksDir,
		LocalIPs: ipStrings,
		Version:  version,
	}

	RegisterUI(mux, store, bus, proxyMgr, info, cfg.ServeUI)

	jsonLogs := cfg.JSONLogs

	// Main proxy/mock handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__ditto__/") {
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

		mock := store.Find(r, reqBody)
		if mock != nil {
			if mock.DelayMs > 0 {
				time.Sleep(time.Duration(mock.DelayMs) * time.Millisecond)
			}
			duration := time.Since(start).Milliseconds()

			for k, v := range mock.Headers {
				w.Header().Set(k, v)
			}
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(mock.Status)
			w.Write(mock.RawBody)

			event := LogEvent{
				Timestamp:    time.Now().Format("15:04:05"),
				Type:         "MOCK",
				Method:       r.Method,
				Path:         r.URL.RequestURI(),
				Status:       mock.Status,
				DurationMs:   duration,
				ResponseBody: string(mock.RawBody),
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
				Timestamp:    time.Now().Format("15:04:05"),
				Type:         "PROXY",
				Method:       r.Method,
				Path:         r.URL.RequestURI(),
				Status:       capture.statusCode,
				DurationMs:   duration,
				ResponseBody: capture.body.String(),
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
			Timestamp:    time.Now().Format("15:04:05"),
			Type:         "MISS",
			Method:       r.Method,
			Path:         r.URL.RequestURI(),
			Status:       502,
			DurationMs:   duration,
			ResponseBody: `{"error": "no mock found and no target configured"}`,
		}
		logRequest(jsonLogs, event)
		bus.Publish(event)
	})

	return &Server{
		Mux:      mux,
		Store:    store,
		Bus:      bus,
		ProxyMgr: proxyMgr,
		Info:     info,
		Config:   cfg,
		CertPath: certPath,
		KeyPath:  keyPath,
	}, nil
}

// ListenAndServe starts the HTTP server (blocking).
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("0.0.0.0:%d", s.Config.Port)
	if s.Config.HTTPS {
		return http.ListenAndServeTLS(addr, s.CertPath, s.KeyPath, s.Mux)
	}
	return http.ListenAndServe(addr, s.Mux)
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
