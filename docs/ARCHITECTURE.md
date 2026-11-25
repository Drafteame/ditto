# Backend architecture

How Ditto's Go code is organised, and the conventions to follow when changing it. For the WebSocket feature design (adapters, modes, recordings, schema packs), see [WEBSOCKET_ARCHITECTURE.md](WEBSOCKET_ARCHITECTURE.md).

## Layout

Package `main` at the repository root is deliberately thin: flags, saved settings, the startup banner, the dashboard `//go:embed` (`embed.go`), and the Wails and headless entry points. It stays at the root because Wails v2 builds the module root and `//go:embed` cannot reach a parent directory. Everything else lives under `internal/`, one package per feature:

| Package | Responsibility |
|---|---|
| `server` | Composition root. Loads every store from the data directory, wires them together, and runs the HTTP(S) listener (`Start`, `Restart`, `Shutdown`). |
| `api` | The dashboard under `/__ditto__/`: static UI, REST endpoints, SSE log stream. |
| `proxy` | The catch-all handler: matching mocks are served, everything else is proxied to the target (`Upstream`), and both are logged. |
| `httpmock` | HTTP mock model, request matching and the file-backed `Store`. |
| `socket` | WebSocket server: `Hub`, clients, subscriptions, dispatch, live bridge to an upstream, proxied tunnel, origin policy. |
| `adapter` | WebSocket wire protocols (raw, AppSync, Relay) and JSON-configured profiles; frame decoding. |
| `channelmode` | Per-channel mode (mock, live, record, mixed). |
| `recording` | Records socket traffic to disk. |
| `eventtemplate` | Reusable socket events with `{{var}}` placeholders. |
| `sequence` | Timed sequences of events and their `Player`. |
| `schema` | Protobuf schema packs loaded at runtime (dynamic descriptors). |
| `eventlog` | `Event`, the retaining `Bus` with resumable cursors, the `Coalescer`, and the `--log-format` `Printer`. |
| `capture` | Bounded capture of request and response bodies for the log. |
| `appdata` | Per-OS data directory layout and saved settings. |
| `jsonfile` | Atomic writes and filename-safe IDs for the directory-of-JSON-files stores. |
| `update`, `browser` | GitHub release check; opening URLs in the system browser. |
| `golden` | Test helper: golden-file assertions with `-update`. |

## Dependencies

Dependencies point one way; there are no cycles:

```
main → server → api, proxy → socket → recording, channelmode, adapter → schema → jsonfile
                             sequence → socket, eventtemplate → adapter, schema
                                        eventlog, capture, httpmock, appdata (leaves)
```

Run `go list -f '{{.ImportPath}}: {{.Imports}}' ./internal/...` for the exact graph. When two packages would need each other, move the shared type down or have the consumer declare a small interface, rather than adding a setter.

### Load order

`server.New` loads the stores in dependency order, and the order matters: templates naming an adapter profile or protobuf type that is not loaded yet are skipped, and so are sequences whose template is missing.

```
channel modes → schemas → adapter profiles → event templates → sequences → recorder → socket hub → player
```

## Package design

- **Package by feature, not by layer.** There are no `models/`, `services/` or `repositories/` packages. Inside a package, files separate concerns instead (for example `httpmock/mock.go`, `match.go`, `store.go`).
- **Use cases are methods** on the concrete type (`Store.Create`, `Hub.Dispatch`, `Player.Play`, `Recorder.Start`). They are not `XxxUseCase` objects.
- **Interfaces only where they pay off:** `adapter.Protocol` has several implementations chosen at runtime; `sequence.Clock` lets tests use a fake; `api.Listener` breaks the cycle with `server`. Everything else is concrete. Accept interfaces and return structs.
- **Dependencies are explicit.** Constructors take them, using a `Config` struct when there are many (`socket.Config`, `recording.Config`, `sequence.PlayerConfig`). There are no setters and no package-level mutable state.
- **Names drop the package stutter:** `socket.Hub`, not `socket.SocketHub`. Every exported identifier is documented.

## Conventions

- **Errors.** Stores return sentinel errors (`httpmock.ErrNotFound`, `sequence.ErrActive`, `recording.ErrNotActive`, and so on), wrapped with `%w`. The API maps them to HTTP statuses in one place, `api.statusFor`: 404 for unknown resources, 409 for conflicts, 413 for oversized bodies, 400 otherwise. Never match on error strings.
- **HTTP.**
  - Routes use Go 1.22 method and path patterns on the API's own mux, so a wrong method gets 405 with an `Allow` header.
  - Read bodies with `readJSON`: it requires `application/json`, caps bodies at 8 MiB and handles a required versus an optional body. Answer with `writeJSON` or `writeError`.
  - Every `/__ditto__/api/` endpoint and the `/__ditto__/events` stream go through `localOnly` (`socket.OriginAllowed`): same-origin callers and local dev servers only.
- **Logging.**
  - Use `log/slog` for diagnostics, on stderr. It is JSON when `--log-format json` is set.
  - The per-request log on stdout is a separate contract, written by `eventlog.Printer`. Its format is pinned by a golden.
- **Persistence.** Write files with `jsonfile.WriteAtomic` or `jsonfile.Write` (temp file, fsync, rename). IDs that become file names must pass `jsonfile.ValidID`, or, for mocks, be a plain `.json` name.
- **Lifecycle.** Whoever starts a goroutine owns stopping it. `server.Shutdown` cancels the base context (ending SSE handlers), stops sequence players and shuts down the `http.Server`; `main` calls it on SIGINT or SIGTERM.

## Testing

- **Placement.** Tests live next to their package. White-box tests (`package x`) are fine where they need internals.
- **Goldens pin the external contracts:**
  - `internal/server/testdata/routes.golden`: status and error body for every route × method × caller.
  - `internal/eventlog/testdata/*.golden`: the `Event` JSON shape and both `--log-format` outputs.

  When a change is intended, regenerate with `go test ./internal/<pkg> -run <Test> -update` and review the diff in the PR.
- **What CI runs:** `go vet ./...`, `golangci-lint run` (config in `.golangci.yml`) and `go test -race ./...`.

## Building the desktop app locally

CI builds with the Go version in `go.mod` and `wails build`. The Wails CLI v2.12 fails with newer local Go versions (for example Go 1.27: `internal error: package "embed" without types`). Either build with the `go.mod` Go version, or skip the CLI:

```bash
CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -o Ditto.app/Contents/MacOS/ditto .
```

This needs an existing `Ditto.app` bundle, for example `build/bin/Ditto.app` from an earlier `wails build`.
