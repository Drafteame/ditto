// Package api serves Ditto's dashboard under /__ditto__/: the static UI,
// the REST endpoints for mocks, sockets, templates, sequences, recordings,
// schemas and settings, and the SSE log stream.
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

// Register adds every dashboard route to mux. Feature routes whose
// dependency is nil are left out.
func Register(mux *http.ServeMux, d Deps) {
	registerDashboardRoutes(mux, d.Mocks, d.Bus, d.Upstream, d.LiveTargets.Target, d.Info, d.UI)
	registerSocketRoutes(mux, d.Hub, d.Adapters)
	registerChannelModeRoutes(mux, d.Modes)
	registerLiveTargetRoutes(mux, d.LiveTargets)
	registerRecordingRoutes(mux, d.Recorder)
	registerSchemaRoutes(mux, d.Schemas)
	registerEventTemplateRoutes(mux, d.Templates, d.Hub)
	registerSequenceRoutes(mux, d.Sequences, d.Player, d.Broadcaster)
	if d.Listener != nil {
		registerPortRoutes(mux, d.Listener, d.Upstream, d.Settings)
	}
}
