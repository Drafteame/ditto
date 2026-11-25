package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"ditto/internal/schema"
	"ditto/internal/socket"
)

type schemaPacksResponse struct {
	Packs []schema.Pack `json:"packs"`
}

type schemaTypesResponse struct {
	Types []schema.Type `json:"types"`
}

func registerSchemaRoutes(mux *http.ServeMux, schemas *schema.Registry) {
	if schemas == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/schemas/packs", func(w http.ResponseWriter, r *http.Request) {
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(schemaPacksResponse{Packs: schemas.Packs()})
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, schema.MaxUploadBytes)
			if err := r.ParseMultipartForm(schema.MaxUploadBytes); err != nil {
				http.Error(w, "invalid multipart upload: "+err.Error(), http.StatusBadRequest)
				return
			}
			file, header, err := r.FormFile("pack")
			if err != nil {
				http.Error(w, "missing upload field \"pack\"", http.StatusBadRequest)
				return
			}
			defer file.Close()

			pack, err := schemas.Import(header.Filename, file)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(pack)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/__ditto__/api/schemas/packs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/__ditto__/api/schemas/packs/"))
		if id == "" || id == "." || id == ".." || strings.Contains(id, "/") || strings.Contains(id, string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}
		if err := schemas.DeletePack(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/__ditto__/api/schemas/types", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(schemaTypesResponse{Types: schemas.Types()})
	})
}
