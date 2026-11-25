package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"ditto/internal/adapter"
	"ditto/internal/socket"
)

type socketDispatchRequest struct {
	Channel  string          `json:"channel"`
	Payload  json.RawMessage `json:"payload"`
	Adapter  string          `json:"adapter,omitempty"`
	TypeName string          `json:"type_name,omitempty"`
}

func RegisterSocketRoutes(mux *http.ServeMux, hub *socket.Hub, adapters *adapter.Registry) {
	mux.HandleFunc("/__ditto__/socket", hub.ServeHTTP)
	mux.HandleFunc("/__ditto__/ws", hub.ServeHTTP)
	mux.HandleFunc("/__ditto__/api/socket/clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"clients": hub.Snapshot()})
	})
	mux.HandleFunc("/__ditto__/api/socket/adapter-profiles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adapters.Summaries())
	})
	mux.HandleFunc("/__ditto__/api/socket/dispatch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if !hasJSONContentType(r) {
			http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		var req socketDispatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Payload) == 0 {
			req.Payload = json.RawMessage(`{}`)
		}
		result, err := hub.Dispatch(socket.Dispatch{
			Channel:  req.Channel,
			Adapter:  req.Adapter,
			TypeName: req.TypeName,
			Payload:  req.Payload,
			Source:   "manual",
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
}

func RegisterLiveTargetRoutes(mux *http.ServeMux, manager *socket.LiveTargets) {
	if manager == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/socket/live-target", func(w http.ResponseWriter, r *http.Request) {
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"live_target": manager.Target()})
		case http.MethodPut:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var req struct {
				LiveTarget string `json:"live_target"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := manager.SetTarget(req.LiveTarget); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func hasJSONContentType(r *http.Request) bool {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	return contentType == "application/json" || strings.HasPrefix(contentType, "application/json;")
}
