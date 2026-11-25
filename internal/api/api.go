// Package api serves Ditto's dashboard under /__ditto__/: the static UI,
// the REST endpoints for mocks, sockets, templates, sequences, recordings,
// schemas and settings, and the SSE log streams.
//
// Every /__ditto__/api/ endpoint and the /__ditto__/events log stream are
// restricted to local or same-origin callers (see socket.OriginAllowed); a
// wrong method on the API gets 405 with an Allow header.
package api

import (
	"io/fs"
	"net/http"

	"ditto/internal/adapter"
	"ditto/internal/appdata"
	"ditto/internal/channelmode"
	"ditto/internal/eventlog"
	"ditto/internal/eventtemplate"
	"ditto/internal/httpmock"
	"ditto/internal/proxy"
	"ditto/internal/recording"
	"ditto/internal/schema"
	"ditto/internal/sequence"
	"ditto/internal/socket"
)

// Info holds metadata shown in the UI footer and connect panel.
type Info struct {
	Port       int      `json:"port"`
	Target     string   `json:"target"`
	LiveTarget string   `json:"live_target,omitempty"`
	HTTPS      bool     `json:"https"`
	MocksDir   string   `json:"mocks_dir"`
	LocalIPs   []string `json:"local_ips"`
	Version    string   `json:"version"`
}

// Listener is the running server, which the port endpoints move to another
// port.
type Listener interface {
	Port() int
	Restart(port int) error
}

// Deps are the services the endpoints read and change.
type Deps struct {
	// UI holds the dashboard assets; nil serves only the API.
	UI   fs.FS
	Info Info

	Mocks       *httpmock.Store
	Bus         *eventlog.Bus
	Upstream    *proxy.Upstream
	Hub         *socket.Hub
	LiveTargets *socket.LiveTargets
	Adapters    *adapter.Registry
	Modes       *channelmode.Registry
	Recorder    *recording.Recorder
	Schemas     *schema.Registry
	Templates   *eventtemplate.Store
	Sequences   *sequence.Store
	Player      *sequence.Player
	Broadcaster *sequence.Broadcaster

	// Listener enables the port endpoints; Settings, when set, persists
	// port and target changes.
	Listener Listener
	Settings *appdata.SettingsStore
}

// API holds the endpoints' dependencies; its methods are the handlers.
type API struct {
	Deps
}

// Register adds the dashboard to mux: the UI at /__ditto__/, the log stream
// at /__ditto__/events, the socket endpoints, and the REST API at
// /__ditto__/api/. Endpoint groups whose dependency is nil are left out.
func Register(mux *http.ServeMux, d Deps) {
	a := &API{Deps: d}
	if a.UI != nil {
		mux.Handle("/__ditto__/", http.StripPrefix("/__ditto__/", http.FileServerFS(a.UI)))
	}
	// The log stream carries every request and response body, so it gets
	// the same origin check as the API.
	mux.Handle("GET /__ditto__/events", localOnly(http.HandlerFunc(a.streamLogs)))
	if a.Hub != nil {
		mux.Handle("/__ditto__/socket", a.Hub)
		mux.Handle("/__ditto__/ws", a.Hub)
	}
	// The API has its own mux so a wrong method is answered with 405 here
	// instead of falling through to the UI or the mock/proxy handler.
	mux.Handle("/__ditto__/api/", localOnly(a.routes()))
}

func (a *API) routes() *http.ServeMux {
	mux := http.NewServeMux()
	const p = "/__ditto__/api"

	mux.HandleFunc("GET "+p+"/logs", a.listLogs)
	mux.HandleFunc("GET "+p+"/logs/history", a.logHistory)
	mux.HandleFunc("GET "+p+"/logs/{id}", a.getLog)

	mux.HandleFunc("GET "+p+"/mocks", a.listMocks)
	mux.HandleFunc("POST "+p+"/mocks", a.createMock)
	mux.HandleFunc("POST "+p+"/mocks/reload", a.reloadMocks)
	mux.HandleFunc("PUT "+p+"/mocks/{id}", a.updateMock)
	mux.HandleFunc("DELETE "+p+"/mocks/{id}", a.deleteMock)
	mux.HandleFunc("POST "+p+"/mocks/{id}/toggle", a.toggleMock)
	mux.HandleFunc("POST "+p+"/mocks/{id}/sequence/reset", a.resetMockSequence)

	mux.HandleFunc("GET "+p+"/target", a.getTarget)
	mux.HandleFunc("PUT "+p+"/target", a.setTarget)
	mux.HandleFunc("PUT "+p+"/target/save", a.saveTarget)
	mux.HandleFunc("POST "+p+"/config/reset", a.resetSettings)
	mux.HandleFunc("GET "+p+"/update-check", a.checkUpdate)
	mux.HandleFunc("GET "+p+"/qr", a.qrCode)
	mux.HandleFunc("POST "+p+"/open-browser", a.openDashboard)
	mux.HandleFunc("POST "+p+"/open-url", a.openURL)
	if a.Listener != nil {
		mux.HandleFunc("GET "+p+"/port", a.getPort)
		mux.HandleFunc("PUT "+p+"/port", a.changePort)
		mux.HandleFunc("GET "+p+"/port/check", a.checkPortAvailable)
	}

	if a.Hub != nil {
		mux.HandleFunc("GET "+p+"/socket/clients", a.listSocketClients)
		mux.HandleFunc("GET "+p+"/socket/adapter-profiles", a.listAdapterProfiles)
		mux.HandleFunc("POST "+p+"/socket/dispatch", a.dispatchSocketEvent)
	}
	if a.LiveTargets != nil {
		mux.HandleFunc("GET "+p+"/socket/live-target", a.getLiveTarget)
		mux.HandleFunc("PUT "+p+"/socket/live-target", a.setLiveTarget)
	}
	if a.Modes != nil {
		mux.HandleFunc("GET "+p+"/channel-modes", a.listChannelModes)
		mux.HandleFunc("PUT "+p+"/channel-modes", a.setChannelMode)
		mux.HandleFunc("DELETE "+p+"/channel-modes", a.deleteChannelMode)
	}
	if a.Recorder != nil {
		mux.HandleFunc("GET "+p+"/recordings", a.listRecordings)
		mux.HandleFunc("POST "+p+"/recordings", a.startRecording)
		mux.HandleFunc("GET "+p+"/recordings/{id}", a.getRecording)
		mux.HandleFunc("DELETE "+p+"/recordings/{id}", a.deleteRecording)
		mux.HandleFunc("POST "+p+"/recordings/{id}/stop", a.stopRecording)
		mux.HandleFunc("GET "+p+"/recordings/{id}/frames", a.recordingFrames)
	}
	if a.Schemas != nil {
		mux.HandleFunc("GET "+p+"/schemas/packs", a.listSchemaPacks)
		mux.HandleFunc("POST "+p+"/schemas/packs", a.uploadSchemaPack)
		mux.HandleFunc("DELETE "+p+"/schemas/packs/{id}", a.deleteSchemaPack)
		mux.HandleFunc("GET "+p+"/schemas/types", a.listSchemaTypes)
	}
	if a.Templates != nil {
		mux.HandleFunc("GET "+p+"/event-templates", a.listTemplates)
		mux.HandleFunc("POST "+p+"/event-templates", a.createTemplate)
		mux.HandleFunc("GET "+p+"/event-templates/{id}", a.getTemplate)
		mux.HandleFunc("PUT "+p+"/event-templates/{id}", a.updateTemplate)
		mux.HandleFunc("DELETE "+p+"/event-templates/{id}", a.deleteTemplate)
		mux.HandleFunc("POST "+p+"/event-templates/{id}/dispatch", a.dispatchTemplate)
	}
	if a.Sequences != nil && a.Player != nil {
		mux.HandleFunc("GET "+p+"/sequences", a.listSequences)
		mux.HandleFunc("POST "+p+"/sequences", a.createSequence)
		mux.HandleFunc("GET "+p+"/sequences/state", a.sequenceStates)
		mux.HandleFunc("GET "+p+"/sequences/events", a.streamSequenceEvents)
		mux.HandleFunc("GET "+p+"/sequences/{id}", a.getSequence)
		mux.HandleFunc("PUT "+p+"/sequences/{id}", a.updateSequence)
		mux.HandleFunc("DELETE "+p+"/sequences/{id}", a.deleteSequence)
		mux.HandleFunc("POST "+p+"/sequences/{id}/play", a.playSequence)
		mux.HandleFunc("POST "+p+"/sequences/{id}/pause", a.pauseSequence)
		mux.HandleFunc("POST "+p+"/sequences/{id}/stop", a.stopSequence)
		mux.HandleFunc("POST "+p+"/sequences/{id}/seek", a.seekSequence)
		mux.HandleFunc("POST "+p+"/sequences/{id}/speed", a.setSequenceSpeed)
	}
	return mux
}
