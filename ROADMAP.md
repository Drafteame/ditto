# Roadmap

Planned features and improvements for Ditto, roughly in priority order.

## v0.2 — HTTPS support

- Auto-generate a self-signed certificate at startup
- Add `--https` flag to enable TLS mode
- Document how to trust the cert on iOS/Android devices

## v0.3 — Record mode

- Add `--record` flag
- When enabled, proxy all requests to the target backend and **save the responses as mock files automatically**
- Eliminates the need to write mock JSON by hand for most cases

## v0.4 — Smarter request matching

- Match on query parameters
- Match on request headers
- Match on request body content
- Multiple mocks per `method + path` with different conditions (e.g., different response per user ID)

## v0.5 — Web UI

- Embedded web dashboard served by the same Go binary
- Live request log via WebSocket
- Create / edit / toggle mocks from the browser
- No separate frontend project to maintain

## v0.6 — Config file

- Replace / supplement CLI flags with a YAML or JSON config file
- Support more complex setups (multiple targets, per-path routing, environment switching)

## Ideas / nice to have

- Dynamic responses (templates that use values from the request — e.g., echo back a path param)
- Stateful mocks (e.g., `POST /users` creates a record, subsequent `GET /users/:id` returns it)
- Mock chaining / sequences (return different responses on subsequent calls to the same endpoint)
- Latency simulation profiles (slow 3G, flaky network, etc.)
- Failure injection (random 500s, timeouts) for resilience testing
- Distributable binaries via GitHub Releases (Mac, Linux, Windows)
- Homebrew tap for easy installation

## Out of scope (for now)

- Forward proxy mode (sitting between app and backend like Charles/Proxyman) — Ditto is intentionally a reverse proxy
- HAR file import/export — possible later, but not a priority
