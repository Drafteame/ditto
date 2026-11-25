package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"ditto/internal/channelmode"
	"ditto/internal/eventtemplate"
	"ditto/internal/httpmock"
	"ditto/internal/jsonfile"
	"ditto/internal/recording"
	"ditto/internal/schema"
	"ditto/internal/sequence"
	"ditto/internal/socket"
)

// maxJSONBody caps every JSON request body.
const maxJSONBody = 8 << 20

// localOnly rejects requests from origins other than the dashboard's own or
// a local development server.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers with err's message and the status statusFor maps it to.
func writeError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), statusFor(err))
}

// statusFor maps the stores' errors to HTTP statuses in one place: unknown
// resources are 404, state conflicts 409, oversized bodies 413, and
// everything else, mostly validation, 400.
func statusFor(err error) int {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, sequence.ErrActive), errors.Is(err, recording.ErrActive):
		return http.StatusConflict
	case errors.Is(err, httpmock.ErrNotFound), errors.Is(err, eventtemplate.ErrNotFound),
		errors.Is(err, sequence.ErrNotFound), errors.Is(err, recording.ErrNotFound),
		errors.Is(err, recording.ErrNotActive), errors.Is(err, schema.ErrPackNotFound),
		errors.Is(err, channelmode.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

// readJSON decodes the request body into dst, answering the request itself
// and returning false when it cannot: a body that is not application/json
// (415), too large (413), unreadable or invalid (400), or missing when
// required (400). An empty optional body leaves dst untouched.
func readJSON(w http.ResponseWriter, r *http.Request, dst any, required bool) bool {
	if r.ContentLength != 0 && !hasJSONContentType(r) {
		http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "failed to read request body: "+err.Error(), http.StatusBadRequest)
		}
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		if required {
			http.Error(w, "request body is required", http.StatusBadRequest)
			return false
		}
		return true
	}
	if err := json.Unmarshal(body, dst); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func hasJSONContentType(r *http.Request) bool {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	return contentType == "application/json" || strings.HasPrefix(contentType, "application/json;")
}

// pathID returns the {id} path value when it is a valid store ID, and
// answers 404 otherwise.
func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !jsonfile.ValidID(id) {
		http.NotFound(w, r)
		return "", false
	}
	return id, true
}
