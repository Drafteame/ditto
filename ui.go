package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

//go:embed web
var webFS embed.FS

// LogEvent represents a single request passing through Ditto.
type LogEvent struct {
	Timestamp  string `json:"timestamp"`
	Type       string `json:"type"` // MOCK, PROXY, MISS
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMs int64  `json:"duration_ms"`
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

// ServerInfo holds metadata shown in the UI footer.
type ServerInfo struct {
	Port     int    `json:"port"`
	Target   string `json:"target"`
	HTTPS    bool   `json:"https"`
	MocksDir string `json:"mocks_dir"`
}

// RegisterUI sets up the dashboard routes on the given mux.
func RegisterUI(mux *http.ServeMux, store *MockStore, bus *EventBus, info ServerInfo) {
	// Serve embedded static files at /__ditto__/
	webContent, _ := fs.Sub(webFS, "web")
	fileServer := http.FileServer(http.FS(webContent))
	mux.Handle("/__ditto__/", http.StripPrefix("/__ditto__/", fileServer))

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

	// Mock list
	mux.HandleFunc("/__ditto__/api/mocks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"mocks": store.All(),
			"info":  info,
		})
	})

	// Toggle a mock
	mux.HandleFunc("/__ditto__/api/mocks/", func(w http.ResponseWriter, r *http.Request) {
		// Match: POST /__ditto__/api/mocks/{index}/toggle
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/__ditto__/api/mocks/")
		parts := strings.Split(path, "/")

		if len(parts) == 2 && parts[1] == "toggle" {
			index, err := strconv.Atoi(parts[0])
			if err != nil {
				http.Error(w, "invalid index", http.StatusBadRequest)
				return
			}
			if !store.Toggle(index) {
				http.Error(w, "mock not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		// Match: POST /__ditto__/api/mocks/reload
		if len(parts) == 1 && parts[0] == "reload" {
			if err := store.Load(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		http.NotFound(w, r)
	})
}
