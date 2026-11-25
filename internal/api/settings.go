package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	qrcode "github.com/skip2/go-qrcode"

	"ditto/internal/browser"
	"ditto/internal/update"
)

// currentInfo returns the server info with the live values: the current
// port, proxy target and live target.
func (a *API) currentInfo() Info {
	info := a.Info
	if a.Listener != nil {
		info.Port = a.Listener.Port()
	}
	if a.Upstream != nil {
		info.Target = a.Upstream.Target()
	}
	if a.LiveTargets != nil {
		info.LiveTarget = a.LiveTargets.Target()
	}
	return info
}

// dashboardURL is the dashboard's address on host.
func (a *API) dashboardURL(host string) string {
	info := a.currentInfo()
	scheme := "http"
	if info.HTTPS {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d/__ditto__/", scheme, host, info.Port)
}

func (a *API) getTarget(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"target": a.Upstream.Target()})
}

// setTarget changes the proxy target for this run only.
func (a *API) setTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if !readJSON(w, r, &req, true) {
		return
	}
	if req.Target == "" {
		http.Error(w, "target URL is required", http.StatusBadRequest)
		return
	}
	if err := a.Upstream.SetTarget(req.Target); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// saveTarget changes the proxy target and saves it for the next run.
func (a *API) saveTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if !readJSON(w, r, &req, true) {
		return
	}
	if err := a.Upstream.SetTarget(req.Target); err != nil {
		writeError(w, err)
		return
	}
	if a.Settings != nil {
		if err := a.Settings.SetTarget(req.Target); err != nil {
			slog.Warn("saving the target failed", "target", req.Target, "err", err)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// resetSettings restores the saved settings to their defaults.
func (a *API) resetSettings(w http.ResponseWriter, r *http.Request) {
	if a.Settings != nil {
		if err := a.Settings.Reset(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

type updateResponse struct {
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	Available   bool   `json:"available"`
	DownloadURL string `json:"download_url,omitempty"`
	Error       string `json:"error,omitempty"`
}

// checkUpdate reports whether GitHub has a newer release than this build.
func (a *API) checkUpdate(w http.ResponseWriter, r *http.Request) {
	current := a.Info.Version
	latest, downloadURL, err := update.Check()
	if err != nil {
		writeJSON(w, http.StatusOK, updateResponse{Current: current, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updateResponse{
		Current:     current,
		Latest:      latest,
		Available:   latest != "" && current != "dev" && update.IsNewer(latest, current),
		DownloadURL: downloadURL,
	})
}

// qrCode returns a PNG QR code of the dashboard URL on this machine's first
// LAN address, for opening the dashboard on a phone.
func (a *API) qrCode(w http.ResponseWriter, r *http.Request) {
	host := "localhost"
	if len(a.Info.LocalIPs) > 0 {
		host = a.Info.LocalIPs[0]
	}
	dashURL := a.dashboardURL(host)
	png, err := qrcode.Encode(dashURL, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Ditto-QR-URL", dashURL)
	w.Write(png)
}

// openDashboard opens the dashboard in the system browser.
func (a *API) openDashboard(w http.ResponseWriter, r *http.Request) {
	browser.Open(a.dashboardURL("localhost"))
	w.WriteHeader(http.StatusOK)
}

// openURL opens an http(s) URL in the system browser; the desktop webview
// cannot follow target="_blank" links itself.
func (a *API) openURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if !readJSON(w, r, &req, true) {
		return
	}
	if !strings.HasPrefix(req.URL, "https://") && !strings.HasPrefix(req.URL, "http://") {
		http.Error(w, "url must start with http:// or https://", http.StatusBadRequest)
		return
	}
	browser.Open(req.URL)
	w.WriteHeader(http.StatusOK)
}

type portResponse struct {
	Port        int    `json:"port"`
	Available   *bool  `json:"available,omitempty"`
	Error       string `json:"error,omitempty"`
	Suggestions []int  `json:"suggestions,omitempty"`
}

func (a *API) getPort(w http.ResponseWriter, r *http.Request) {
	port := a.Listener.Port()
	writeJSON(w, http.StatusOK, portResponse{Port: port, Suggestions: suggestPorts(port)})
}

// checkPortAvailable reports whether ?port= is free, suggesting free
// alternatives when it is not.
func (a *API) checkPortAvailable(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	available := true
	resp := portResponse{Port: port, Available: &available}
	if err := checkPort(port); err != nil {
		available = false
		resp.Error, resp.Suggestions = err.Error(), suggestPorts(port)
	}
	writeJSON(w, http.StatusOK, resp)
}

// changePort saves the new port and moves the server to it. The response is
// sent first; this connection survives the listener swap.
func (a *API) changePort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port int `json:"port"`
	}
	if !readJSON(w, r, &req, true) {
		return
	}
	if err := checkPort(req.Port); err != nil {
		writeJSON(w, http.StatusConflict, portResponse{Error: err.Error(), Suggestions: suggestPorts(req.Port)})
		return
	}
	if a.Settings != nil {
		if err := a.Settings.SetPort(req.Port); err != nil {
			slog.Warn("saving the port failed", "port", req.Port, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, portResponse{Port: req.Port})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if err := a.Listener.Restart(req.Port); err != nil {
		slog.Error("port change failed", "port", req.Port, "err", err)
	}
}
