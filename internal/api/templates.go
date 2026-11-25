package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"ditto/internal/eventtemplate"
	"ditto/internal/socket"
)

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

func (a *API) listTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"templates": a.Templates.Templates()})
}

func (a *API) createTemplate(w http.ResponseWriter, r *http.Request) {
	var tmpl eventtemplate.Template
	if !readJSON(w, r, &tmpl, true) {
		return
	}
	created, err := a.Templates.Create(tmpl)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *API) getTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tmpl, err := a.Templates.Get(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tmpl)
}

func (a *API) updateTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var tmpl eventtemplate.Template
	if !readJSON(w, r, &tmpl, true) {
		return
	}
	updated, err := a.Templates.Update(id, tmpl)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (a *API) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.Templates.Delete(id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// dispatchTemplate renders a template with the request's variables and
// dispatches it. Unresolved or badly typed variables answer 400 with the
// partially resolved payload instead of dispatching.
func (a *API) dispatchTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req eventTemplateDispatchRequest
	if !readJSON(w, r, &req, false) {
		return
	}
	vars, err := eventtemplate.VarsFromJSON(req.Variables)
	if err != nil {
		writeError(w, err)
		return
	}
	rendered, err := a.Templates.Render(id, vars)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(rendered.Missing) > 0 || len(rendered.InvalidCasts) > 0 {
		writeJSON(w, http.StatusBadRequest, eventTemplateDispatchResponse{
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
	result, err := a.Hub.Dispatch(dispatch)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, eventTemplateDispatchResponse{Result: result, ResolvedPayload: rendered.Payload})
}
