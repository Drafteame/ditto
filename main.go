package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

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

func main() {
	port := flag.Int("port", 8888, "Port to listen on")
	target := flag.String("target", "", "Target backend URL to proxy unmatched requests")
	mocksDir := flag.String("mocks", "./mocks", "Directory containing mock JSON files")
	https := flag.Bool("https", false, "Enable HTTPS using a self-signed certificate")
	certDir := flag.String("certs", "./certs", "Directory to store the self-signed certificate")
	headless := flag.Bool("headless", false, "Run without the web dashboard (API still available)")
	logFormat := flag.String("log-format", "text", "Log format: 'text' (human-readable) or 'json' (one object per line)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if *logFormat != "text" && *logFormat != "json" {
		fmt.Fprintf(os.Stderr, "invalid --log-format: %q (must be 'text' or 'json')\n", *logFormat)
		os.Exit(2)
	}

	// Log to stderr by default so stdout stays clean for JSON consumers.
	// In text mode, requests still go through stdout via the helper.
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	jsonLogs := *logFormat == "json"

	// Load mocks
	store := NewMockStore(*mocksDir)
	if err := store.Load(); err != nil {
		log.Fatalf("Failed to load mocks: %v", err)
	}

	// Event bus for live log streaming
	bus := NewEventBus()

	// Reverse proxy
	proxyMgr := NewProxyManager(*target)

	// TLS
	var certPath, keyPath string
	if *https {
		var err error
		certPath, keyPath, err = EnsureCert(*certDir)
		if err != nil {
			log.Fatalf("Failed to prepare TLS certificate: %v", err)
		}
	}

	// HTTP mux
	mux := http.NewServeMux()

	// Register routes (API always available, static files only with UI)
	var ipStrings []string
	for _, ip := range localIPs() {
		ipStrings = append(ipStrings, ip.String())
	}
	info := ServerInfo{
		Port:     *port,
		Target:   *target,
		HTTPS:    *https,
		MocksDir: *mocksDir,
		LocalIPs: ipStrings,
	}
	RegisterUI(mux, store, bus, proxyMgr, info, !*headless)

	// Main proxy/mock handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Skip UI paths
		if strings.HasPrefix(r.URL.Path, "/__ditto__/") {
			return
		}

		start := time.Now()

		// Read the request body so it can be inspected by mock matching AND
		// still forwarded by the reverse proxy.
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

	// Startup
	scheme := "http"
	if *https {
		scheme = "https"
	}
	if jsonLogs {
		printStartupJSON(store.Count(), *port, *target, *mocksDir, *https, !*headless)
	} else {
		printStartup(store.All(), *port, *target, *mocksDir, *https, certPath, !*headless)
	}

	// Open browser
	if !*headless {
		go openBrowser(fmt.Sprintf("%s://localhost:%d/__ditto__/", scheme, *port))
	}

	addr := fmt.Sprintf("0.0.0.0:%d", *port)
	if *https {
		log.Fatal(http.ListenAndServeTLS(addr, certPath, keyPath, mux))
	} else {
		log.Fatal(http.ListenAndServe(addr, mux))
	}
}

// logRequest writes a single request log line. In JSON mode it emits a
// JSON object on stdout (line-delimited, suitable for log aggregators);
// in text mode it emits a human-friendly line on stdout.
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

func openBrowser(url string) {
	time.Sleep(500 * time.Millisecond)
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
	case "linux":
		exec.Command("xdg-open", url).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
}

func printStartup(mocks []Mock, port int, target, mocksDir string, https bool, certPath string, ui bool) {
	scheme := "http"
	if https {
		scheme = "https"
	}
	w := os.Stderr
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  ┌──────────────────────────────────┐")
	fmt.Fprintf(w, "  │           DITTO %-16s│\n", version)
	fmt.Fprintln(w, "  └──────────────────────────────────┘")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  URL:        %s://0.0.0.0:%d\n", scheme, port)
	fmt.Fprintf(w, "  Mocks dir:  %s\n", mocksDir)
	if https {
		fmt.Fprintf(w, "  TLS cert:   %s\n", certPath)
	}
	if target != "" {
		fmt.Fprintf(w, "  Target:     %s\n", target)
	} else {
		fmt.Fprintf(w, "  Target:     (none — unmatched requests return 502)\n")
	}
	fmt.Fprintf(w, "  Mocks:      %d loaded\n", len(mocks))
	if ui {
		fmt.Fprintf(w, "  Dashboard:  %s://localhost:%d/__ditto__/\n", scheme, port)
	}
	fmt.Fprintln(w)
	if len(mocks) > 0 {
		fmt.Fprintln(w, "  Registered mocks:")
		for _, m := range mocks {
			delay := ""
			if m.DelayMs > 0 {
				delay = fmt.Sprintf(" (delay: %dms)", m.DelayMs)
			}
			fmt.Fprintf(w, "    %-7s %s → %d%s\n", m.Method, m.Path, m.Status, delay)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "  Listening on %s://0.0.0.0:%d ...\n\n", scheme, port)
}

func printStartupJSON(mockCount, port int, target, mocksDir string, https bool, ui bool) {
	startup := map[string]any{
		"event":     "startup",
		"version":   version,
		"port":      port,
		"target":    target,
		"https":     https,
		"mocks_dir": mocksDir,
		"mocks":     mockCount,
		"ui":        ui,
	}
	data, _ := json.Marshal(startup)
	fmt.Fprintln(os.Stdout, string(data))
}
