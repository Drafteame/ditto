package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ditto/internal/appdata"
	"ditto/internal/httpmock"
	"ditto/internal/server"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	port := flag.Int("port", 8888, "Port to listen on")
	target := flag.String("target", "", "Target backend URL to proxy unmatched requests")
	liveTarget := flag.String("live-target", "", "WebSocket upstream URL for live channel mode")
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
	if *logFormat == "json" {
		// Diagnostics on stderr are JSON too, like the request log on stdout.
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	}

	layout, err := appdata.Locate()
	if err != nil {
		log.Fatalf("Failed to determine data directory: %v", err)
	}
	cfgStore := appdata.NewSettingsStore(layout.ConfigPath)
	saved := cfgStore.Get()

	if *mocksDir == "" {
		*mocksDir = layout.MocksDir
		if err := appdata.SeedExampleMock(*mocksDir); err != nil {
			log.Fatalf("Failed to seed the example mock: %v", err)
		}
	}

	// Saved settings fill in only the flags the user did not pass.
	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if !explicit["port"] && saved.Port != 0 {
		*port = saved.Port
	}
	if !explicit["target"] {
		*target = saved.Target
	}
	if !explicit["live-target"] {
		*liveTarget = saved.LiveTarget
	}

	cfg := server.Config{
		Port:        *port,
		Target:      *target,
		LiveTarget:  *liveTarget,
		MocksDir:    *mocksDir,
		Layout:      layout,
		HTTPS:       *https,
		CertDir:     *certDir,
		UI:          dashboardAssets(),
		JSONLogs:    *logFormat == "json",
		ConfigStore: cfgStore,
		Version:     version,
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	if *headless {
		runHeadless(srv, cfg)
	} else {
		runDesktop(srv, cfg)
	}
}

func runHeadless(srv *server.Server, cfg server.Config) {
	if cfg.JSONLogs {
		printStartupJSON(srv.Store.Count(), cfg.Port, cfg.Target, cfg.MocksDir, cfg.HTTPS, false)
	} else {
		printStartup(srv.Store.All(), cfg.Port, cfg.Target, cfg.MocksDir, cfg.HTTPS, srv.CertPath, true)
	}

	if err := srv.Start(); err != nil {
		log.Fatal(err)
	}
	waitForSignalAndShutdown(srv)
}

// waitForSignalAndShutdown blocks until SIGINT or SIGTERM, then shuts the
// server down gracefully.
func waitForSignalAndShutdown(srv *server.Server) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutdownServer(srv)
}

func shutdownServer(srv *server.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func printStartup(mocks []httpmock.Mock, port int, target, mocksDir string, https bool, certPath string, ui bool) {
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
