package api

import (
	"encoding/json"
	"net/http"

	"ditto/internal/channelmode"
	"ditto/internal/socket"
)

func (a *API) listSocketClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"clients": a.Hub.Snapshot()})
}

func (a *API) listAdapterProfiles(w http.ResponseWriter, r *http.Request) {
	// A nil registry knows only the built-ins and lists no profiles.
	writeJSON(w, http.StatusOK, a.Adapters.Summaries())
}

type socketDispatchRequest struct {
	Channel  string          `json:"channel"`
	Payload  json.RawMessage `json:"payload"`
	Adapter  string          `json:"adapter,omitempty"`
	TypeName string          `json:"type_name,omitempty"`
}

// dispatchSocketEvent sends a payload composed in the dashboard to a
// channel's subscribers.
func (a *API) dispatchSocketEvent(w http.ResponseWriter, r *http.Request) {
	var req socketDispatchRequest
	if !readJSON(w, r, &req, true) {
		return
	}
	if len(req.Payload) == 0 {
		req.Payload = json.RawMessage(`{}`)
	}
	result, err := a.Hub.Dispatch(socket.Dispatch{
		Channel:  req.Channel,
		Adapter:  req.Adapter,
		TypeName: req.TypeName,
		Payload:  req.Payload,
		Source:   "manual",
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) getLiveTarget(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"live_target": a.LiveTargets.Target()})
}

func (a *API) setLiveTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LiveTarget string `json:"live_target"`
	}
	if !readJSON(w, r, &req, true) {
		return
	}
	if err := a.LiveTargets.SetTarget(req.LiveTarget); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *API) listChannelModes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"channels": a.Modes.Snapshot()})
}

// setChannelMode stores a channel's mode and returns the stored config.
func (a *API) setChannelMode(w http.ResponseWriter, r *http.Request) {
	var cfg channelmode.Config
	if !readJSON(w, r, &cfg, true) {
		return
	}
	if err := a.Modes.Set(cfg); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.Modes.Get(cfg.Channel))
}

// deleteChannelMode returns ?channel= to the default mode.
func (a *API) deleteChannelMode(w http.ResponseWriter, r *http.Request) {
	if err := a.Modes.Delete(r.URL.Query().Get("channel")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
