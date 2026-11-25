// Package server assembles Ditto: it loads every store and registry from
// the data directory, wires them into the socket hub, dashboard API and
// catch-all mock/proxy handler, and runs the HTTP(S) listener.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"ditto/internal/adapter"
	"ditto/internal/api"
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

// Config holds all the parameters needed to create and run the HTTP server.
type Config struct {
	Port       int
	Target     string
	LiveTarget string
	MocksDir   string
	Layout     appdata.Layout
	HTTPS      bool
	CertDir    string
	// UI holds the dashboard assets; nil serves only the API.
	UI          fs.FS
	JSONLogs    bool
	ConfigStore *appdata.SettingsStore
	// Version is reported by the dashboard and the update check.
	Version string
}

// Server holds the running server state.
type Server struct {
	Mux        *http.ServeMux
	Store      *httpmock.Store
	Bus        *eventlog.Bus
	ProxyMgr   *proxy.Upstream
	SocketHub  *socket.Hub
	Modes      *channelmode.Registry
	Recorder   *recording.Recorder
	LiveTarget *socket.LiveTargets
	Schemas    *schema.Registry
	Templates  *eventtemplate.Store
	Sequences  *sequence.Store
	Player     *sequence.Player
	Info       api.Info
	Config     Config
	CertPath   string
	KeyPath    string

	mu         sync.Mutex
	http       *http.Server
	listener   net.Listener
	cancelBase context.CancelFunc
}

// New creates and configures the HTTP server with all routes.
func New(cfg Config) (*Server, error) {
	if cfg.MocksDir == "" {
		return nil, fmt.Errorf("server config mocks dir is required")
	}
	if cfg.Layout.DescriptorsDir == "" {
		return nil, fmt.Errorf("server config layout with descriptors dir is required")
	}
	if cfg.Layout.EventTemplatesDir == "" {
		return nil, fmt.Errorf("server config layout with event templates dir is required")
	}
	if cfg.Layout.SequencesDir == "" {
		return nil, fmt.Errorf("server config layout with sequences dir is required")
	}
	if cfg.Layout.AdapterProfilesDir == "" {
		return nil, fmt.Errorf("server config layout with adapter profiles dir is required")
	}
	if cfg.Layout.RecordingsDir == "" {
		return nil, fmt.Errorf("server config layout with recordings dir is required")
	}
	if cfg.Layout.ChannelModesDir == "" {
		return nil, fmt.Errorf("server config layout with channel modes dir is required")
	}

	store := httpmock.NewStore(cfg.MocksDir)
	if err := store.Load(); err != nil {
		return nil, fmt.Errorf("failed to load mocks: %w", err)
	}

	bus := eventlog.NewBus(eventlog.NewPrinter(os.Stdout, cfg.JSONLogs))
	proxyMgr, err := proxy.NewUpstream(cfg.Target)
	if err != nil {
		return nil, err
	}
	modeRegistry, err := channelmode.NewRegistry(cfg.Layout.ChannelModesDir, bus)
	if err != nil {
		return nil, fmt.Errorf("failed to load channel mode registry: %w", err)
	}
	// Load order matters: templates skip entries whose adapter or schema type
	// is unknown, and sequences skip entries whose template is unknown.
	schemaRegistry, err := schema.NewRegistry(cfg.Layout.DescriptorsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load schema registry: %w", err)
	}
	adapters := &adapter.Registry{}
	if err := adapters.Load(cfg.Layout.AdapterProfilesDir); err != nil {
		return nil, fmt.Errorf("failed to load adapter profiles: %w", err)
	}
	eventTemplates, err := eventtemplate.NewStore(cfg.Layout.EventTemplatesDir, schemaRegistry, adapters)
	if err != nil {
		return nil, fmt.Errorf("failed to load event template registry: %w", err)
	}
	eventSequences, err := sequence.NewStore(cfg.Layout.SequencesDir, eventTemplates, schemaRegistry, adapters)
	if err != nil {
		return nil, fmt.Errorf("failed to load event sequence registry: %w", err)
	}
	recorder, err := recording.NewRecorder(recording.Config{Dir: cfg.Layout.RecordingsDir, Schemas: schemaRegistry,
		Adapters: adapters, Modes: modeRegistry, Bus: bus})
	if err != nil {
		return nil, fmt.Errorf("failed to load recorder: %w", err)
	}
	var persistLiveTarget func(string) error
	if cfg.ConfigStore != nil {
		persistLiveTarget = cfg.ConfigStore.SetLiveTarget
	}
	liveTargets := socket.NewLiveTargets(cfg.LiveTarget, persistLiveTarget)
	socketHub := socket.NewHub(socket.Config{Bus: bus, Modes: modeRegistry, Schemas: schemaRegistry,
		Adapters: adapters, Recorder: recorder, LiveTargets: liveTargets})
	// Registered after the hub's own listener to keep the original call order.
	modeRegistry.OnChange(recorder.HandleModeChange)
	playerBroadcaster := sequence.NewBroadcaster()
	sequencePlayer := sequence.NewPlayer(sequence.PlayerConfig{Sequences: eventSequences, Schemas: schemaRegistry, Hub: socketHub, Broadcaster: playerBroadcaster})

	var certPath, keyPath string
	if cfg.HTTPS {
		var err error
		certPath, keyPath, err = EnsureCert(cfg.CertDir)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare TLS certificate: %w", err)
		}
	}

	mux := http.NewServeMux()

	var ipStrings []string
	for _, ip := range localIPs() {
		ipStrings = append(ipStrings, ip.String())
	}
	info := api.Info{
		Port:       cfg.Port,
		Target:     cfg.Target,
		LiveTarget: cfg.LiveTarget,
		HTTPS:      cfg.HTTPS,
		MocksDir:   cfg.MocksDir,
		LocalIPs:   ipStrings,
		Version:    cfg.Version,
	}

	mux.Handle("/", &proxy.Handler{Mocks: store, Upstream: proxyMgr, Bus: bus, Hub: socketHub})

	srv := &Server{
		Mux:        mux,
		Store:      store,
		Bus:        bus,
		ProxyMgr:   proxyMgr,
		SocketHub:  socketHub,
		Modes:      modeRegistry,
		Recorder:   recorder,
		LiveTarget: liveTargets,
		Schemas:    schemaRegistry,
		Templates:  eventTemplates,
		Sequences:  eventSequences,
		Player:     sequencePlayer,
		Info:       info,
		Config:     cfg,
		CertPath:   certPath,
		KeyPath:    keyPath,
	}
	api.Register(mux, api.Deps{
		UI: cfg.UI, Info: info, Mocks: store, Bus: bus, Upstream: proxyMgr, Hub: socketHub,
		LiveTargets: liveTargets, Adapters: adapters, Modes: modeRegistry, Recorder: recorder,
		Schemas: schemaRegistry, Templates: eventTemplates, Sequences: eventSequences,
		Player: sequencePlayer, Broadcaster: playerBroadcaster, Listener: srv, Settings: cfg.ConfigStore,
	})
	return srv, nil
}

// Start binds the configured port and serves in the background. It returns
// once the port is bound, or the bind error.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.http == nil {
		baseCtx, cancel := context.WithCancel(context.Background())
		s.cancelBase = cancel
		s.http = &http.Server{
			Handler:           s.Mux,
			ReadHeaderTimeout: 10 * time.Second,
			// Cancelled on Shutdown so streaming (SSE) handlers return.
			BaseContext: func(net.Listener) context.Context { return baseCtx },
		}
	}
	ln, err := listen(s.Config.Port)
	if err != nil {
		return err
	}
	s.listener = ln
	go s.serve(s.http, ln)
	return nil
}

func listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
}

func (s *Server) serve(srv *http.Server, ln net.Listener) {
	var err error
	if s.Config.HTTPS {
		err = srv.ServeTLS(ln, s.CertPath, s.KeyPath)
	} else {
		err = srv.Serve(ln)
	}
	// Restart closes the old listener on purpose; Shutdown closes the server.
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		slog.Error("server stopped", "addr", ln.Addr().String(), "err", err)
	}
}

// Restart moves the server to newPort. The new port is bound before the old
// listener closes, so a failed bind leaves the server on its current port,
// and connections already open (SSE streams, WebSockets) stay up. Running
// sequence players are stopped, as they always were on a port change.
func (s *Server) Restart(newPort int) error {
	s.stopPlayers()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.http == nil {
		return fmt.Errorf("server is not running")
	}
	ln, err := listen(newPort)
	if err != nil {
		return err
	}
	old := s.listener
	s.listener = ln
	s.Config.Port = newPort
	s.Info.Port = newPort
	go s.serve(s.http, ln)
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// Shutdown stops sequence players, ends streaming handlers and stops the
// server, waiting for in-flight requests until ctx is done. Hijacked
// WebSocket connections are not waited for.
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopPlayers()
	s.mu.Lock()
	srv, cancel := s.http, s.cancelBase
	s.listener = nil
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	cancel()
	return srv.Shutdown(ctx)
}

func (s *Server) stopPlayers() {
	if s.Player == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.Player.Shutdown(ctx)
}

// Port returns the current port.
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Config.Port
}
