package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"ditto/internal/channelmode"
	"ditto/internal/socket"
)

func registerChannelModeRoutes(mux *http.ServeMux, registry *channelmode.Registry) {
	if registry == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/channel-modes", func(w http.ResponseWriter, r *http.Request) {
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"channels": registry.Snapshot()})
		case http.MethodPut:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var cfg channelmode.Config
			if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := registry.Set(cfg); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(registry.Get(cfg.Channel))
		case http.MethodDelete:
			channel := r.URL.Query().Get("channel")
			if err := registry.Delete(channel); err != nil {
				if errors.Is(err, channelmode.ErrNotFound) {
					http.Error(w, err.Error(), http.StatusNotFound)
					return
				}
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}
