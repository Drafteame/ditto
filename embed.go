package main

import (
	"embed"
	"io/fs"
)

//go:embed all:frontend/dist
var webFS embed.FS

// dashboardAssets returns the built dashboard served at /__ditto__/.
func dashboardAssets() fs.FS {
	assets, _ := fs.Sub(webFS, "frontend/dist") // fails only for an invalid path
	return assets
}
