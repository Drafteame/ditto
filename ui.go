package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed web
var webFS embed.FS

// LogEvent represents a single request passing through Ditto.
type LogEvent struct {
	Timestamp    string `json:"timestamp"`
	Type         string `json:"type"` // MOCK, PROXY, MISS
	Method       string `json:"method"`
	Path         string `json:"path"`
	Status       int    `json:"status"`
	DurationMs   int64  `json:"duration_ms"`
	ResponseBody string `json:"response_body,omitempty"`
}

// EventBus broadcasts log events to connected SSE clients.
type EventBus struct {
	mu      sync.Mutex
	clients map[chan LogEvent]struct{}
}

func NewEventBus() *EventBus {
	return &EventBus{clients: make(map[chan LogEvent]struct{})}
}

func (b *EventBus) Subscribe() chan LogEvent {
	ch := make(chan LogEvent, 64)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *EventBus) Unsubscribe(ch chan LogEvent) {
	b.mu.Lock()
	delete(b.clients, ch)
	close(ch)
	b.mu.Unlock()
}

func (b *EventBus) Publish(event LogEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- event:
		default: // drop if client is slow
		}
	}
}

// ServerInfo holds metadata shown in the UI footer and connect panel.
type ServerInfo struct {
	Port     int      `json:"port"`
	Target   string   `json:"target"`
	HTTPS    bool     `json:"https"`
	MocksDir string   `json:"mocks_dir"`
	LocalIPs []string `json:"local_ips"`
	Version  string   `json:"version"`
}

// RegisterUI sets up the dashboard routes on the given mux.
// If serveUI is true, the embedded static files are served; otherwise only the API is available.
func RegisterUI(mux *http.ServeMux, store *MockStore, bus *EventBus, proxyMgr *ProxyManager, info ServerInfo, serveUI bool) {
	// Serve embedded static files at /__ditto__/ (only when UI is enabled)
	if serveUI {
		webContent, _ := fs.Sub(webFS, "web")
		fileServer := http.FileServer(http.FS(webContent))
		mux.Handle("/__ditto__/", http.StripPrefix("/__ditto__/", fileServer))
	}

	// SSE endpoint
	mux.HandleFunc("/__ditto__/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		ch := bus.Subscribe()
		defer bus.Unsubscribe(ch)

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-ch:
				data, _ := json.Marshal(event)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	})

	// GET mocks list
	mux.HandleFunc("/__ditto__/api/mocks", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Derive actual port from the request (handles port changes at runtime)
			actualPort := info.Port
			if host := r.Host; host != "" {
				if _, portStr, err := net.SplitHostPort(host); err == nil {
					if p, err := strconv.Atoi(portStr); err == nil {
						actualPort = p
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"mocks": store.All(),
				"info": ServerInfo{
					Port:     actualPort,
					Target:   proxyMgr.Target(),
					HTTPS:    info.HTTPS,
					MocksDir: info.MocksDir,
					LocalIPs: info.LocalIPs,
					Version:  info.Version,
				},
			})

		case http.MethodPost:
			// Create a new mock
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			var mock Mock
			if err := json.Unmarshal(body, &mock); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			disabled, err := store.Create(mock)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"disabled_duplicates": disabled})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Mock operations: toggle, reload, update, delete
	mux.HandleFunc("/__ditto__/api/mocks/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/__ditto__/api/mocks/")
		parts := strings.Split(path, "/")

		// POST /__ditto__/api/mocks/reload
		if r.Method == http.MethodPost && len(parts) == 1 && parts[0] == "reload" {
			if err := store.Load(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		// Routes that need an index: /{index}/toggle, /{index}, etc.
		if len(parts) < 1 {
			http.NotFound(w, r)
			return
		}

		index, err := strconv.Atoi(parts[0])
		if err != nil {
			http.NotFound(w, r)
			return
		}

		// POST /__ditto__/api/mocks/{index}/toggle
		if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "toggle" {
			ok, disabled := store.Toggle(index)
			if !ok {
				http.Error(w, "mock not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"disabled_duplicates": disabled})
			return
		}

		// PUT /__ditto__/api/mocks/{index}
		if r.Method == http.MethodPut && len(parts) == 1 {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			var mock Mock
			if err := json.Unmarshal(body, &mock); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			disabled, err := store.Update(index, mock)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"disabled_duplicates": disabled})
			return
		}

		// DELETE /__ditto__/api/mocks/{index}
		if r.Method == http.MethodDelete && len(parts) == 1 {
			if err := store.Delete(index); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		http.NotFound(w, r)
	})

	// Target URL management
	mux.HandleFunc("/__ditto__/api/target", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"target": proxyMgr.Target(),
			})

		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			var req struct {
				Target string `json:"target"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if req.Target == "" {
				http.Error(w, "target URL is required", http.StatusBadRequest)
				return
			}
			if err := proxyMgr.SetTarget(req.Target); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Update check endpoint
	mux.HandleFunc("/__ditto__/api/update-check", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		latest, downloadURL, err := checkForUpdate()
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{
				"current":   version,
				"latest":    "",
				"available": false,
				"error":     err.Error(),
			})
			return
		}
		available := latest != "" && latest != version && version != "dev"
		json.NewEncoder(w).Encode(map[string]any{
			"current":      version,
			"latest":       latest,
			"available":    available,
			"download_url": downloadURL,
		})
	})

	// QR code endpoint — returns a PNG image
	mux.HandleFunc("/__ditto__/api/qr", func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if info.HTTPS {
			scheme = "https"
		}
		// Use the first local IP for the physical device URL
		ip := "localhost"
		if len(info.LocalIPs) > 0 {
			ip = info.LocalIPs[0]
		}
		dashURL := fmt.Sprintf("%s://%s:%d/__ditto__/", scheme, ip, info.Port)

		png, err := qrcode.Encode(dashURL, qrcode.Medium, 256)
		if err != nil {
			http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Ditto-QR-URL", dashURL)
		w.Write(png)
	})

	// Open in browser endpoint
	mux.HandleFunc("/__ditto__/api/open-browser", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		scheme := "http"
		if info.HTTPS {
			scheme = "https"
		}
		dashURL := fmt.Sprintf("%s://localhost:%d/__ditto__/", scheme, info.Port)
		openBrowser(dashURL)
		w.WriteHeader(http.StatusOK)
	})
}

// RegisterPortRoutes adds port management and config persistence endpoints.
// Called after the server is created so the server reference is available.
func RegisterPortRoutes(mux *http.ServeMux, srv *Server, proxyMgr *ProxyManager, cfgStore *ConfigStore) {
	// Port check — probe if a port is available
	mux.HandleFunc("/__ditto__/api/port/check", func(w http.ResponseWriter, r *http.Request) {
		portStr := r.URL.Query().Get("port")
		port, err := strconv.Atoi(portStr)
		if err != nil {
			http.Error(w, "invalid port", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := CheckPort(port); err != nil {
			json.NewEncoder(w).Encode(map[string]any{
				"port":        port,
				"available":   false,
				"error":       err.Error(),
				"suggestions": SuggestPorts(port),
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"port":      port,
			"available": true,
		})
	})

	// Port get/change
	mux.HandleFunc("/__ditto__/api/port", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"port":        srv.Port(),
				"suggestions": SuggestPorts(srv.Port()),
			})

		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			var req struct {
				Port int `json:"port"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if err := CheckPort(req.Port); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]any{
					"error":       err.Error(),
					"suggestions": SuggestPorts(req.Port),
				})
				return
			}
			// Respond first, then restart async (closing the listener kills in-flight requests)
			if cfgStore != nil {
				cfgStore.SetPort(req.Port)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"port": req.Port,
			})
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			go func() {
				time.Sleep(200 * time.Millisecond)
				if err := srv.Restart(req.Port); err != nil {
					log.Printf("Failed to restart on port %d: %v", req.Port, err)
				}
			}()

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Target change with auto-save (overrides the basic target endpoint)
	mux.HandleFunc("/__ditto__/api/target/save", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		var req struct {
			Target string `json:"target"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := proxyMgr.SetTarget(req.Target); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if cfgStore != nil {
			cfgStore.SetTarget(req.Target)
		}
		w.WriteHeader(http.StatusOK)
	})

	// Config reset
	mux.HandleFunc("/__ditto__/api/config/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfgStore != nil {
			cfgStore.Reset()
		}
		w.WriteHeader(http.StatusOK)
	})
}
