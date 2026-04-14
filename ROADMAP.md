# Roadmap

Planned features and improvements for Ditto, in priority order.

## Guiding principle

Ditto is built for **internal teams**, including non-technical product folks. Every feature should be evaluated against:

> Could a product manager who has never opened a terminal use this?

If the answer is no, we need to fix the UX before adding new capabilities.

## v0.2 — HTTPS support ✅

- ~~Auto-generate a self-signed certificate at startup~~
- ~~Add `--https` flag to enable TLS mode~~
- ~~Document how to trust the cert on iOS/Android devices~~

> HTTPS is an **advanced, optional** feature. The recommended setup is HTTP + a debug-only network config in the app. See README + [docs/HTTPS.md](docs/HTTPS.md).

## v0.3 — Distributable binary ✅

- ~~Set up a GitHub Actions workflow for automated releases on git tags~~
- ~~Cross-compile for macOS (Intel + Apple Silicon), Linux (amd64 + arm64), Windows (amd64)~~
- ~~Drop the `git clone` + `go build` requirement — README now shows binary download as the primary install path~~

## v0.4 — Web UI foundation ✅

- ~~Embedded SPA served by the same Go binary~~
- ~~Live request log via SSE~~
- ~~Mock list with on/off toggles~~
- ~~Reload mocks from UI~~
- ~~Connection URLs panel (Android emulator, iOS simulator, physical device)~~
- ~~Auto-opens browser on startup (`--no-ui` to opt out)~~

## v0.5 — Record mode + runtime UI controls

- **Record mode**: toggle on/off from the dashboard (and via `--record` CLI flag)
- When enabled, proxy all requests to the target and **save the responses as mock files automatically**
- **Target URL input** in the dashboard — change where unmatched requests go without restarting
- UI: review captured requests, choose which ones to keep as mocks

## v0.6 — Query parameter matching

- Match mocks on query parameters (e.g., `/transactions?status=pending` vs `/transactions?status=completed`)
- Match on request headers
- Match on request body content
- Multiple mocks per `method + path` with different conditions

## v0.7 — Mock management in the UI

- Create / edit / delete mocks from the browser
- Form-based editor with JSON validation
- Duplicate mocks
- No more hand-editing JSON files for most use cases

## v0.8 — Headless mode

- `--headless` flag for CI pipelines, automated testing, and CLI-only users
- JSON-formatted log output for machine parsing
- Clean separation between UI and headless modes

## v1.0 — Stable release

Bundle v0.5–v0.8 as the first stable release. The milestone:

> **A product manager can download Ditto, start it, and use it end-to-end without opening a terminal or editing a file.**

---

## Backlog

Features for post-v1.0. Not prioritized — will be re-evaluated after v1.0 ships.

- **Config file + multi-target**: YAML/JSON config, per-path routing to different backends, environment switching (dev/staging)
- **First-run wizard**: detect connected devices, walk through app configuration with copy-paste snippets
- **One-click cert install**: UI buttons for Android/iOS cert installation, `.mobileconfig` generation, QR codes
- **Dynamic responses**: templates that use values from the request (echo back path params, headers, etc.)
- **Stateful mocks**: `POST /users` creates a record, subsequent `GET /users/:id` returns it
- **Mock chaining / sequences**: return different responses on subsequent calls to the same endpoint
- **Latency simulation profiles**: slow 3G, flaky network, intermittent failures
- **Failure injection**: random 500s, timeouts for resilience testing
- **Homebrew tap**: one-line install on macOS
- **Signed installers**: `.dmg` / `.pkg` / `.deb` for native OS installation experience
- **Auto-update mechanism**: check for new versions and update in place
- **HAR file import/export**: import recorded sessions from browser dev tools
