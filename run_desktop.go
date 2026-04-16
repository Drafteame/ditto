//go:build desktop

package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:desktop
var desktopAssets embed.FS

func runDesktop(srv *Server, cfg ServerConfig) {
	scheme := "http"
	if cfg.HTTPS {
		scheme = "https"
	}

	// During Wails binding generation, skip server startup
	if os.Getenv("WAILS_BINDING_GENERATION") != "true" {
		if err := srv.ListenAndServeAsync(); err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
		printStartup(srv.Store.All(), cfg.Port, cfg.Target, cfg.MocksDir, cfg.HTTPS, srv.CertPath, true)
	}

	app := &DittoApp{
		port:   cfg.Port,
		scheme: scheme,
	}

	bgColor := &options.RGBA{R: 13, G: 17, B: 23, A: 255} // #0d1117

	err := wails.Run(&options.App{
		Title:            "Ditto",
		Width:            1280,
		Height:           800,
		MinWidth:         800,
		MinHeight:        500,
		BackgroundColour: bgColor,
		AssetServer: &assetserver.Options{
			Assets: desktopAssets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatalf("Wails error: %v", err)
	}
}

// DittoApp is the Wails application struct with methods callable from JS.
type DittoApp struct {
	ctx    context.Context
	port   int
	scheme string
}

func (a *DittoApp) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *DittoApp) shutdown(ctx context.Context) {
}

// OpenInBrowser opens the dashboard in the default browser.
func (a *DittoApp) OpenInBrowser() {
	url := fmt.Sprintf("%s://localhost:%d/__ditto__/", a.scheme, a.port)
	openBrowser(url)
}
