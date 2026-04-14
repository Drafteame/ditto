package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	port := flag.Int("port", 8888, "Port to listen on")
	target := flag.String("target", "", "Target backend URL to proxy unmatched requests")
	mocksDir := flag.String("mocks", "./mocks", "Directory containing mock JSON files")
	https := flag.Bool("https", false, "Enable HTTPS using a self-signed certificate")
	certDir := flag.String("certs", "./certs", "Directory to store the self-signed certificate")
	noUI := flag.Bool("no-ui", false, "Disable the web dashboard")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	// Load mocks
	store := NewMockStore(*mocksDir)
	if err := store.Load(); err != nil {
		log.Fatalf("Failed to load mocks: %v", err)
	}

	// Event bus for live log streaming
	bus := NewEventBus()

	// Reverse proxy
	var proxy *httputil.ReverseProxy
	if *target != "" {
		targetURL, err := url.Parse(*target)
		if err != nil {
			log.Fatalf("Invalid target URL: %v", err)
		}
		proxy = httputil.NewSingleHostReverseProxy(targetURL)
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.Host = targetURL.Host
		}
	}

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

	// Register UI routes
	if !*noUI {
		info := ServerInfo{
			Port:     *port,
			Target:   *target,
			HTTPS:    *https,
			MocksDir: *mocksDir,
		}
		RegisterUI(mux, store, bus, info)
	}

	// Main proxy/mock handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Skip UI paths
		if strings.HasPrefix(r.URL.Path, "/__ditto__/") {
			return
		}

		start := time.Now()

		mock := store.Find(r.Method, r.URL.Path)
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

			log.Printf("MOCK   %s %s → %d (%dms)", r.Method, r.URL.Path, mock.Status, duration)
			bus.Publish(LogEvent{
				Timestamp:  time.Now().Format("15:04:05"),
				Type:       "MOCK",
				Method:     r.Method,
				Path:       r.URL.Path,
				Status:     mock.Status,
				DurationMs: duration,
			})
			return
		}

		if proxy != nil {
			log.Printf("PROXY  %s %s", r.Method, r.URL.Path)
			proxyStart := time.Now()
			proxy.ServeHTTP(w, r)
			duration := time.Since(proxyStart).Milliseconds()

			bus.Publish(LogEvent{
				Timestamp:  time.Now().Format("15:04:05"),
				Type:       "PROXY",
				Method:     r.Method,
				Path:       r.URL.Path,
				Status:     200,
				DurationMs: duration,
			})
			return
		}

		duration := time.Since(start).Milliseconds()
		log.Printf("MISS   %s %s (no target configured)", r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error": "no mock found and no target configured"}`))

		bus.Publish(LogEvent{
			Timestamp:  time.Now().Format("15:04:05"),
			Type:       "MISS",
			Method:     r.Method,
			Path:       r.URL.Path,
			Status:     502,
			DurationMs: duration,
		})
	})

	// Startup
	scheme := "http"
	if *https {
		scheme = "https"
	}
	printStartup(store.All(), *port, *target, *mocksDir, *https, certPath, !*noUI)

	// Open browser
	if !*noUI {
		go openBrowser(fmt.Sprintf("%s://localhost:%d/__ditto__/", scheme, *port))
	}

	addr := fmt.Sprintf("0.0.0.0:%d", *port)
	if *https {
		log.Fatal(http.ListenAndServeTLS(addr, certPath, keyPath, mux))
	} else {
		log.Fatal(http.ListenAndServe(addr, mux))
	}
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
	fmt.Println()
	fmt.Println("  ┌──────────────────────────────────┐")
	fmt.Printf("  │           DITTO %-16s│\n", version)
	fmt.Println("  └──────────────────────────────────┘")
	fmt.Println()
	fmt.Printf("  URL:        %s://0.0.0.0:%d\n", scheme, port)
	fmt.Printf("  Mocks dir:  %s\n", mocksDir)
	if https {
		fmt.Printf("  TLS cert:   %s\n", certPath)
	}
	if target != "" {
		fmt.Printf("  Target:     %s\n", target)
	} else {
		fmt.Printf("  Target:     (none — unmatched requests return 502)\n")
	}
	fmt.Printf("  Mocks:      %d loaded\n", len(mocks))
	if ui {
		fmt.Printf("  Dashboard:  %s://localhost:%d/__ditto__/\n", scheme, port)
	}
	fmt.Println()
	if len(mocks) > 0 {
		fmt.Println("  Registered mocks:")
		for _, m := range mocks {
			delay := ""
			if m.DelayMs > 0 {
				delay = fmt.Sprintf(" (delay: %dms)", m.DelayMs)
			}
			fmt.Printf("    %-7s %s → %d%s\n", m.Method, m.Path, m.Status, delay)
		}
		fmt.Println()
	}
	fmt.Printf("  Listening on %s://0.0.0.0:%d ...\n\n", scheme, port)
}
