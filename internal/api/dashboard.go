package api

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"ditto/internal/browser"
	"ditto/internal/eventlog"
	"ditto/internal/httpmock"
	"ditto/internal/proxy"
	"ditto/internal/update"
)

// Info holds metadata shown in the UI footer and connect panel.
type Info struct {
	Port       int      `json:"port"`
	Target     string   `json:"target"`
	LiveTarget string   `json:"live_target,omitempty"`
	HTTPS      bool     `json:"https"`
	MocksDir   string   `json:"mocks_dir"`
	LocalIPs   []string `json:"local_ips"`
	Version    string   `json:"version"`
}

// registerDashboardRoutes sets up the dashboard routes on the given mux.
// If serveUI is true, the embedded static files are served; otherwise only the API is available.
func registerDashboardRoutes(mux *http.ServeMux, store *httpmock.Store, bus *eventlog.Bus, proxyMgr *proxy.Upstream, liveTarget func() string, info Info, ui fs.FS) {
	// Serve the dashboard assets at /__ditto__/ when there are any.
	if ui != nil {
		mux.Handle("/__ditto__/", http.StripPrefix("/__ditto__/", http.FileServer(http.FS(ui))))
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
		w.Header().Set("X-Accel-Buffering", "no")
		since := r.URL.Query().Get("since")
		if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
			since = lastEventID
		}
		ch, replay, gap := bus.SubscribeAfter(since)
		defer bus.Unsubscribe(ch)
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		if gap != nil {
			marker := eventlog.Event{Type: "GAP", Method: "GAP", StreamGap: true, GapReason: gap.Reason,
				GapFromCursor: gap.From, GapToCursor: gap.To}
			if writeSSELogEvent(w, flusher, marker) != nil {
				return
			}
		}
		for _, event := range replay {
			if writeSSELogEvent(w, flusher, event) != nil || r.Context().Err() != nil {
				return
			}
		}

		ctx := r.Context()
		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			case event := <-ch:
				if writeSSELogEvent(w, flusher, event) != nil {
					return
				}
			}
		}
	})

	// GET log summaries and an individual retained event. Summary rows omit
	// body strings; details remain available while the bounded event is retained.
	mux.HandleFunc("/__ditto__/api/logs/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := r.URL.Query()
		offset, _ := strconv.Atoi(query.Get("offset"))
		limit, _ := strconv.Atoi(query.Get("limit"))
		expected, _ := strconv.Atoi(query.Get("expected"))
		history := bus.History(eventlog.HistoryQuery{
			Since: query.Get("since"), From: query.Get("from"), To: query.Get("to"),
			Channel: query.Get("channel"), Method: query.Get("method"), Direction: query.Get("direction"),
			Source: query.Get("source"), DispatchID: query.Get("dispatch_id"),
			Offset: offset, Limit: limit, Expected: expected,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(history)
	})
	mux.HandleFunc("/__ditto__/api/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(bus.Summaries())
	})
	mux.HandleFunc("/__ditto__/api/logs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/__ditto__/api/logs/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		event, ok := bus.Detail(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(event)
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
			currentLiveTarget := info.LiveTarget
			if liveTarget != nil {
				currentLiveTarget = liveTarget()
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"mocks": store.All(),
				"info": Info{
					Port:       actualPort,
					Target:     proxyMgr.Target(),
					LiveTarget: currentLiveTarget,
					HTTPS:      info.HTTPS,
					MocksDir:   info.MocksDir,
					LocalIPs:   info.LocalIPs,
					Version:    info.Version,
				},
			})

		case http.MethodPost:
			// Create a new mock
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			var mock httpmock.Mock
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

		// POST /__ditto__/api/mocks/{index}/sequence/reset
		if r.Method == http.MethodPost && len(parts) == 3 && parts[1] == "sequence" && parts[2] == "reset" {
			if ok := store.ResetSequence(index); !ok {
				http.Error(w, "mock not found or not a sequence", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		// PUT /__ditto__/api/mocks/{index}
		if r.Method == http.MethodPut && len(parts) == 1 {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			var mock httpmock.Mock
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
		latest, downloadURL, err := update.Check()
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{
				"current":   info.Version,
				"latest":    "",
				"available": false,
				"error":     err.Error(),
			})
			return
		}
		available := latest != "" && info.Version != "dev" && update.IsNewer(latest, info.Version)
		json.NewEncoder(w).Encode(map[string]any{
			"current":      info.Version,
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
		browser.Open(dashURL)
		w.WriteHeader(http.StatusOK)
	})

	// Open an arbitrary URL in the system browser (used by the desktop app
	// where target="_blank" doesn't escape the Wails webview).
	mux.HandleFunc("/__ditto__/api/open-url", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil || req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		// Only allow https URLs for safety
		if !strings.HasPrefix(req.URL, "https://") && !strings.HasPrefix(req.URL, "http://") {
			http.Error(w, "url must start with http:// or https://", http.StatusBadRequest)
			return
		}
		browser.Open(req.URL)
		w.WriteHeader(http.StatusOK)
	})
}

func writeSSELogEvent(w http.ResponseWriter, flusher http.Flusher, event eventlog.Event) error {
	data, _ := json.Marshal(event)
	if event.Cursor != "" && !event.StreamGap {
		if _, err := fmt.Fprintf(w, "id: %s\n", event.Cursor); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
