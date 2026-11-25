package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"ditto/internal/appdata"
	"ditto/internal/proxy"
)

// registerPortRoutes adds port management and config persistence endpoints.
// Called after the server is created so the server reference is available.
func registerPortRoutes(mux *http.ServeMux, srv Listener, proxyMgr *proxy.Upstream, cfgStore *appdata.SettingsStore) {
	// Port check — probe if a port is available
	mux.HandleFunc("/__ditto__/api/port/check", func(w http.ResponseWriter, r *http.Request) {
		portStr := r.URL.Query().Get("port")
		port, err := strconv.Atoi(portStr)
		if err != nil {
			http.Error(w, "invalid port", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := checkPort(port); err != nil {
			json.NewEncoder(w).Encode(map[string]any{
				"port":        port,
				"available":   false,
				"error":       err.Error(),
				"suggestions": suggestPorts(port),
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
				"suggestions": suggestPorts(srv.Port()),
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
			if err := checkPort(req.Port); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]any{
					"error":       err.Error(),
					"suggestions": suggestPorts(req.Port),
				})
				return
			}
			// Respond before restarting; this connection survives the
			// listener swap, so the client still reads the response.
			if cfgStore != nil {
				if err := cfgStore.SetPort(req.Port); err != nil {
					slog.Warn("saving the port failed", "port", req.Port, "err", err)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"port": req.Port,
			})
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if err := srv.Restart(req.Port); err != nil {
				slog.Error("port change failed", "port", req.Port, "err", err)
			}

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
			if err := cfgStore.SetTarget(req.Target); err != nil {
				slog.Warn("saving the target failed", "target", req.Target, "err", err)
			}
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
			if err := cfgStore.Reset(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

// checkPort tests if a port is available. Returns nil if free,
// or an error with details about what's using it.
func checkPort(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535")
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		process := identifyProcess(port)
		if process != "" {
			return fmt.Errorf("port %d is in use by %s", port, process)
		}
		return fmt.Errorf("port %d is in use", port)
	}
	ln.Close()
	return nil
}

// identifyProcess tries to find which process is using a port.
func identifyProcess(port int) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	out, err := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", port)).Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	pid := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	nameOut, err := exec.Command("ps", "-p", pid, "-o", "comm=").Output()
	if err != nil {
		return "PID " + pid
	}
	name := strings.TrimSpace(string(nameOut))
	if name != "" {
		return fmt.Sprintf("%s (PID %s)", name, pid)
	}
	return "PID " + pid
}

// suggestPorts returns a list of common alternative ports, skipping any that are in use.
func suggestPorts(exclude int) []int {
	candidates := []int{8888, 8080, 3001, 9000, 9090, 4000}
	var available []int
	for _, p := range candidates {
		if p == exclude {
			continue
		}
		if checkPort(p) == nil {
			available = append(available, p)
		}
	}
	return available
}
