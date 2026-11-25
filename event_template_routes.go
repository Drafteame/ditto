package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ditto/internal/eventtemplate"
	"ditto/internal/jsonfile"
	"ditto/internal/socket"
)

const maxEventTemplateBodyBytes = 1 << 20

type eventTemplatesResponse struct {
	Templates []eventtemplate.Template `json:"templates"`
}

type eventTemplateDispatchRequest struct {
	Variables       map[string]json.RawMessage `json:"variables,omitempty"`
	ChannelOverride string                     `json:"channel_override,omitempty"`
	AdapterOverride string                     `json:"adapter_override,omitempty"`
}

type eventTemplateDispatchResponse struct {
	socket.Result
	ResolvedPayload  json.RawMessage             `json:"resolved_payload"`
	MissingVariables []string                    `json:"missing_variables,omitempty"`
	InvalidCasts     []eventtemplate.InvalidCast `json:"invalid_casts,omitempty"`
}

// eventtemplate.NewStore loads the templates in dir. Templates that name an
// adapter or schema type unknown to adapters or schemas are skipped, so both
// must be loaded first.

func RegisterEventTemplateRoutes(mux *http.ServeMux, registry *eventtemplate.Store, hub *socket.Hub) {
	if registry == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/event-templates", func(w http.ResponseWriter, r *http.Request) {
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(eventTemplatesResponse{Templates: registry.Templates()})
		case http.MethodPost:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var tmpl eventtemplate.Template
			if ok := decodeEventTemplateJSON(w, r, &tmpl, false); !ok {
				return
			}
			created, err := registry.Create(tmpl)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(created)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/__ditto__/api/event-templates/", func(w http.ResponseWriter, r *http.Request) {
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/__ditto__/api/event-templates/")
		id, action, ok := splitEventTemplatePath(rest)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if action == "dispatch" {
			handleEventTemplateDispatch(w, r, registry, hub, id)
			return
		}
		switch r.Method {
		case http.MethodGet:
			tmpl, err := registry.Get(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tmpl)
		case http.MethodPut:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var tmpl eventtemplate.Template
			if ok := decodeEventTemplateJSON(w, r, &tmpl, false); !ok {
				return
			}
			updated, err := registry.Update(id, tmpl)
			if err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, eventtemplate.ErrNotFound) {
					status = http.StatusNotFound
				}
				http.Error(w, err.Error(), status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(updated)
		case http.MethodDelete:
			if err := registry.Delete(id); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func handleEventTemplateDispatch(w http.ResponseWriter, r *http.Request, registry *eventtemplate.Store, hub *socket.Hub, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Content-Type") != "" && !hasJSONContentType(r) {
		http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	var req eventTemplateDispatchRequest
	if ok := decodeEventTemplateJSON(w, r, &req, true); !ok {
		return
	}
	vars, err := eventtemplate.VarsFromJSON(req.Variables)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rendered, err := registry.Render(id, vars)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, eventtemplate.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	if len(rendered.Missing) > 0 || len(rendered.InvalidCasts) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(eventTemplateDispatchResponse{
			ResolvedPayload:  rendered.Payload,
			MissingVariables: rendered.Missing,
			InvalidCasts:     rendered.InvalidCasts,
		})
		return
	}
	dispatch := socket.Dispatch{
		Channel:  rendered.Channel,
		Adapter:  rendered.Adapter,
		TypeName: rendered.TypeName,
		Payload:  rendered.Payload,
		Source:   "template:" + id,
	}
	if override := strings.TrimSpace(req.ChannelOverride); override != "" {
		dispatch.Channel = override
	}
	if strings.TrimSpace(req.AdapterOverride) != "" {
		dispatch.Adapter = req.AdapterOverride
	}
	result, err := hub.Dispatch(dispatch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(eventTemplateDispatchResponse{
		Result:          result,
		ResolvedPayload: rendered.Payload,
	})
}

func splitEventTemplatePath(rest string) (id string, action string, ok bool) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 1 && jsonfile.ValidID(parts[0]) {
		return parts[0], "", true
	}
	if len(parts) == 2 && parts[1] == "dispatch" && jsonfile.ValidID(parts[0]) {
		return parts[0], parts[1], true
	}
	return "", "", false
}

func decodeEventTemplateJSON(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventTemplateBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "failed to read request body: "+err.Error(), http.StatusBadRequest)
		}
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		if allowEmpty {
			return true
		}
		http.Error(w, "request body is required", http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
