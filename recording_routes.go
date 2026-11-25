package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"ditto/internal/jsonfile"
	"ditto/internal/recording"
)

func RegisterRecordingRoutes(mux *http.ServeMux, recorder *recording.Recorder) {
	if recorder == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/recordings", func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			items, err := recorder.List()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"recordings": items, "active_id": recorder.ActiveID()})
		case http.MethodPost:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var req struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			manifest, err := recorder.Start(req.Name, req.Description)
			if err != nil {
				status := http.StatusBadRequest
				if strings.Contains(err.Error(), "already active") {
					status = http.StatusConflict
				}
				http.Error(w, err.Error(), status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(manifest)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/__ditto__/api/recordings/", func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		rel := strings.Trim(strings.TrimPrefix(r.URL.Path, "/__ditto__/api/recordings/"), "/")
		parts := strings.Split(rel, "/")
		if len(parts) == 0 || !jsonfile.ValidID(parts[0]) {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		if len(parts) == 2 && parts[1] == "stop" && r.Method == http.MethodPost {
			manifest, err := recorder.Stop(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(manifest)
			return
		}
		if len(parts) == 2 && parts[1] == "frames" && r.Method == http.MethodGet {
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			frames, err := recorder.Frames(id, r.URL.Query().Get("channel"), offset, limit)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"frames": frames, "offset": offset, "limit": limit})
			return
		}
		if len(parts) == 1 && r.Method == http.MethodGet {
			manifest, err := recorder.Manifest(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(manifest)
			return
		}
		if len(parts) == 1 && r.Method == http.MethodDelete {
			if err := recorder.Delete(id); err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, fs.ErrNotExist) {
					status = http.StatusNotFound
				}
				http.Error(w, err.Error(), status)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})
}
