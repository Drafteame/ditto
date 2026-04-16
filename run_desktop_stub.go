//go:build !desktop

package main

import (
	"fmt"
	"log"
)

// runDesktop is the fallback when Wails is not available (headless builds).
// It starts the server and opens the dashboard in the default browser.
func runDesktop(srv *Server, cfg ServerConfig) {
	if err := srv.ListenAndServeAsync(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	scheme := "http"
	if cfg.HTTPS {
		scheme = "https"
	}
	printStartup(srv.Store.All(), cfg.Port, cfg.Target, cfg.MocksDir, cfg.HTTPS, srv.CertPath, true)

	dashURL := fmt.Sprintf("%s://localhost:%d/__ditto__/", scheme, cfg.Port)
	openBrowser(dashURL)

	// Block until the server exits
	select {}
}
