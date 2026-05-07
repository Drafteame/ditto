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

## v0.5 — Save as mock + mock management + runtime controls ✅

- ~~Response viewer: click a log entry to see the full response body~~
- ~~Save as mock: one-click button on any proxied response to save it as a mock file~~
- ~~Delete mock: remove a mock from the UI (deletes the JSON file)~~
- ~~Mock editor: edit body, status, headers, delay inline in the dashboard~~
- ~~Target URL input: change the backend URL from the dashboard without restarting~~

## v0.6 — Smart matching + de-duplication ✅

- ~~Match mocks on query parameters (e.g., `/transactions?status=pending` vs `/transactions?status=completed`)~~
- ~~Match on request headers~~
- ~~Match on request body content (partial JSON subset)~~
- ~~Multiple mocks per `method + path` with different conditions — most specific wins~~
- ~~De-duplication: identical mocks are auto-disabled when one is enabled, with a UI toast warning~~
- ~~Match conditions visible in the mock list (sidebar pills) and editable in the mock editor~~

## v0.7 — Headless mode ✅

- ~~`--headless` flag (replaces `--no-ui`) for CI pipelines, automated testing, and CLI-only users~~
- ~~`--log-format json` for machine-parseable, line-delimited JSON logs~~
- ~~Startup banner sent to stderr; request logs to stdout, so JSON output is pipe-friendly~~
- ~~REST API remains available in headless mode for programmatic mock management~~

## v1.0 — Stable release ✅

> **A product manager can download Ditto, start it, and use it end-to-end without opening a terminal or editing a file.**

- ~~macOS `.app` bundle with custom icon — double-click to launch~~
- ~~`.zip` packaging for macOS (double-click to extract)~~
- ~~Terminal window shows logs; Ctrl+C or closing the window stops Ditto~~
- ~~Example mock included in release archive~~

---

## Post-v1.0 — Prioritized

### v1.1 — Native desktop app ✅

- ~~Single window via Wails: close window = process dies. No orphaned processes.~~
- ~~Persistent storage: mocks in `~/Library/Application Support/Ditto/` (macOS). Survives app updates.~~
- ~~Launch in browser: button opens dashboard in default browser (hidden when already in browser).~~
- ~~QR code for phone: scan to open dashboard on phone (hidden on mobile).~~
- ~~Auto-update notification: checks GitHub Releases on startup, shows banner if newer version available.~~
- ~~CI pipeline: macOS builds via Wails on `macos-latest`, Linux/Windows headless on `ubuntu-latest`.~~

### v1.2 — Port management ✅

- ~~Port input in sidebar, changeable at runtime~~
- ~~Port check shows process name using the port + suggests alternatives~~
- ~~One-click suggestion buttons for common ports~~
- ~~Server restarts on new port; page redirects after polling confirms new port is ready~~
- ~~Persistent config: port and target URL saved to `config.json`, loaded on next launch~~
- ~~Config auto-saved on every change (port, target)~~
- ~~Reset to defaults endpoint~~

### v1.3 — UI improvements ✅

- ~~Log search/filter with clear button: instant client-side filtering by path, method, type, status~~
- ~~Quick-filter buttons: ALL / MOCK / PROXY / MISS with colored active states~~
- ~~Responsive sidebar: hamburger toggle below 768px, overlay with close button~~
- ~~Responsive log: hides Time/Method/Status/Duration on mobile, path fills available width~~
- ~~Responsive modal: full-screen on mobile, form fields stack vertically~~

### v1.4 — Migrate to React + Tailwind ✅

- ~~Vite + React + TypeScript + Tailwind project in `frontend/`~~
- ~~All existing functionality ported: log viewer, mock CRUD, editor, QR, port/target management, search/filters, responsive layout~~
- ~~Go embed updated to serve from `frontend/dist/`~~
- ~~CI pipeline updated with Node.js setup and frontend build step~~
- ~~CI workflow added for PR/push validation (frontend + headless + Wails builds)~~
- ~~Old `web/` and `desktop/` removed~~

### v1.5 — Sequences

Return different responses on subsequent calls to the same endpoint. Essential for testing polling flows.

- Define an ordered list of responses for a mock. Each call advances to the next response.
- Configurable behavior when the sequence ends: loop, stay on last, or reset.
- UI: visual sequence editor showing the response chain.
- Example: `GET /deposits/status` → call 1: `{"status": "pending"}` → call 2: `{"status": "processing"}` → call 3: `{"status": "completed"}`.

### v1.6 — Scenarios

Group mocks into named sets that activate together with a single toggle.

- A scenario is a named collection of mock references (+ optional sequence overrides).
- Activating a scenario enables all its mocks and disables any conflicting mocks from other scenarios.
- UI: scenario cards in the sidebar, one-click activation, visual indicator of active scenario.
- Scenarios are JSON files stored alongside mocks (e.g., `scenarios/failed_deposit.json`).
- Example scenarios: "Happy deposit flow", "Failed KYC", "Empty wallet", "Slow network".

```json
{
  "name": "Failed deposit flow",
  "description": "Simulates a deposit that fails at payment confirmation",
  "mocks": [
    { "ref": "get_wallet_low_balance.json" },
    { "ref": "post_deposit_initiated.json" },
    {
      "ref": "get_deposit_status.json",
      "sequence": [
        { "status": 200, "body": { "status": "pending" } },
        { "status": 200, "body": { "status": "processing" } },
        { "status": 200, "body": { "status": "failed", "reason": "insufficient_funds" } }
      ]
    }
  ]
}
```

### v1.7 — Mock tree view

Collapsible tree view for the mock list sidebar, grouping mocks by path segments.

- Toggle between flat list (current) and grouped tree view
- Paths are split into segments: `/osb/config/limits` and `/osb/games/` both appear under an `/osb/` node
- Expand/collapse nodes to drill into path groups
- Bulk actions on groups: enable/disable all mocks under a path prefix
- Applies to the mock sidebar only — the request log stays chronological

### v1.8–v1.x — WebSocket mocking

Full-blown WebSocket mocking layer: send arbitrary events to a connected app, record real backend traffic, replay it (modified or not), and bundle everything into scenarios.

Designed to keep Ditto **agnostic to any specific business or domain** — Protobuf schemas, recordings, and scenarios all live as user-loadable artifacts (schema packs, collections), never hardcoded.

Capabilities, in incremental order:

- **Generic WS server + protocol adapter**: pluggable adapters (AppSync, Socket.IO, raw JSON) so any app's WS protocol can be supported.
- **Manual event dispatch**: send arbitrary events from the UI to specific channels.
- **Schema packs**: load `.proto` files at runtime (dynamic descriptors, no codegen). UI shows type dropdown + JSON editor with schema-aware autocomplete; Ditto serializes to Protobuf at dispatch.
- **Event templates** ✅: save reusable parameterized events (`{{ticketId}}`, `{{userId}}`, etc.) and quick-fire them from the socket dispatcher.
- **Event sequences**: timed event timelines with transport controls (play/pause/scrub/speed).
- **Per-channel modes**: each channel can be `mock`, `live` (passthrough to real backend), `record`, or `mixed`.
- **Recordings**: capture real WS sessions to disk, then edit, splice, and replay.
- **Scenarios** (extends v1.6): combine HTTP mocks + sequences + channel modes + HTTP→Socket triggers into one atomic activation. Lets you simulate complete flows (e.g. a full sports match, a casino session) by composition, with no domain-specific code.

See [docs/WEBSOCKET_MOCKING_PLAN.md](WEBSOCKET_MOCKING_PLAN.md) for the full milestone-by-milestone plan.

### v1.9 — Ditto Collections (`.dittopack`)

Shareable, versioned bundles of Ditto configuration. Like Postman Collections, but for everything Ditto manages.

- Bundle format: zip with `manifest.json` (with `schemaVersion`) + structured folders (`mocks/`, `scenarios/`, `sequences/`, `descriptors/`, `recordings/`).
- Export granularity: full config, individual scenario (with all referenced dependencies), schema pack alone, or single mock / sequence.
- Import with conflict resolution: per-item `git`-style diff with skip / overwrite / rename / merge options.
- Designed in tandem with the WebSocket plan — every artifact produced from v1.6 onwards is bundle-compatible by construction.
- Replaces the older "Import/export" and "Team sharing" backlog items, which are unified under this single mechanism.

### v2.0 — Stable release

Bundle v1.1–v1.9 as the second major release. The milestone:

> **Ditto is a native desktop product with polished UX, advanced mocking (HTTP + WebSocket, with sequences, scenarios, and recordings), and a portable Collections format for sharing setups across teams.**

---

## Backlog

Features not currently prioritized. Will be re-evaluated after v1.3.

### Traffic inspection & debugging
- **Breakpoints**: pause a request mid-flight, inspect/modify body and headers, then forward or reject. Like Charles Proxy's breakpoint feature.
- **Request verification**: assert that the app made specific calls with specific payloads. Useful for automated testing in CI with headless mode.

### Response generation
- **Dynamic response templates**: use variables from the request in response bodies — `{{request.path.id}}`, `{{randomUUID}}`, `{{timestamp}}`. One mock for `GET /users/*` returns a user with the requested ID.
- **Scripting hooks**: JS or Lua functions that run on each request/response for edge cases declarative mocks can't handle (e.g., "return 401 on every 5th request").

### Network simulation
- **Latency profiles**: simulate slow 3G, flaky Wi-Fi, high-latency satellite connections.
- **Failure injection**: random 500s, timeouts, connection resets for resilience testing.

### Collaboration
- **External format imports**: import from HAR files, Postman collections, or OpenAPI specs into Ditto's native artifacts. (Export and team sharing are covered by v1.9 — Ditto Collections.)

### Distribution & install
- **Record mode** (HTTP): bulk-capture all proxied HTTP responses as mock files automatically. (WS recording is covered by v1.8.)
- **Homebrew tap**: `brew install draftea/tap/ditto` for one-line install on macOS.
- **Code signing + notarization**: Apple Developer Program signing to remove the Gatekeeper warning on first launch.
- **Auto-update mechanism**: check for new versions and prompt to update in place.
- **Multi-target routing**: route different path prefixes to different backends (`/users/*` → service A, `/bets/*` → service B).
- **Config file**: YAML/JSON config as an alternative to CLI flags for complex setups.
