package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	port := flag.Int("port", 8888, "Port to listen on")
	target := flag.String("target", "", "Target backend URL to proxy unmatched requests")
	mocksDir := flag.String("mocks", "", "Directory containing mock JSON files (default: persistent app data)")
	https := flag.Bool("https", false, "Enable HTTPS using a self-signed certificate")
	certDir := flag.String("certs", "./certs", "Directory to store the self-signed certificate")
	headless := flag.Bool("headless", false, "Run without the desktop window (CLI/server mode)")
	logFormat := flag.String("log-format", "text", "Log format: 'text' (human-readable) or 'json' (one object per line)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	// During Wails binding generation, exit cleanly without starting anything
	if os.Getenv("WAILS_BINDING_GENERATION") == "true" {
		return
	}

	if *logFormat != "text" && *logFormat != "json" {
		fmt.Fprintf(os.Stderr, "invalid --log-format: %q (must be 'text' or 'json')\n", *logFormat)
		os.Exit(2)
	}

	log.SetOutput(os.Stderr)
	log.SetFlags(0)

	// Resolve mocks directory: explicit flag > persistent storage
	if *mocksDir == "" {
		defaultDir, err := DefaultMocksDir()
		if err != nil {
			log.Fatalf("Failed to determine data directory: %v", err)
		}
		*mocksDir = defaultDir
	}

	cfg := ServerConfig{
		Port:     *port,
		Target:   *target,
		MocksDir: *mocksDir,
		HTTPS:    *https,
		CertDir:  *certDir,
		ServeUI:  true,
		JSONLogs: *logFormat == "json",
	}

	srv, err := NewServer(cfg)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	if *headless {
		runHeadless(srv, cfg)
	} else {
		runDesktop(srv, cfg)
	}
}

func runHeadless(srv *Server, cfg ServerConfig) {
	if cfg.JSONLogs {
		printStartupJSON(srv.Store.Count(), cfg.Port, cfg.Target, cfg.MocksDir, cfg.HTTPS, false)
	} else {
		printStartup(srv.Store.All(), cfg.Port, cfg.Target, cfg.MocksDir, cfg.HTTPS, srv.CertPath, true)
	}

	log.Fatal(srv.ListenAndServe())
}

func openBrowser(url string) {
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
