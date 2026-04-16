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

### v1.1 — Native desktop app

Replace the browser + Terminal setup with a standalone native application using [Wails](https://wails.io) (Go + web frontend in a native window).

- **Single window**: dashboard runs inside the app, not in a browser. Close the window = process dies. No orphaned processes, no port leaks.
- **Persistent storage**: mocks, scenarios, and config stored in `~/Library/Application Support/Ditto/` (macOS), `~/.config/ditto/` (Linux), `%APPDATA%\Ditto\` (Windows). Data survives app updates and reinstalls.
- **Launch in browser**: button in the header opens the dashboard in the default browser. Useful for screen sharing, second monitors, or sharing with teammates on the same network.
- **QR code for phone**: "Open on phone" button shows a QR code encoding the physical device dashboard URL. Scan with phone camera → dashboard opens on the phone's browser.
- **Auto-update notification**: on startup, check GitHub Releases for a newer version. If available, show a banner in the dashboard with a direct download link. No silent installs — user replaces the app themselves.
- **System tray / menu bar** (stretch): minimize to tray, quick-access to start/stop.
- Reuses 100% of existing HTML/CSS/JS dashboard code.
- Builds to a native `.app` (macOS), `.exe` (Windows) — ~15MB binary.

### v1.2 — Port management

Configurable port from the UI with full error handling.

- Check if port is available before binding. If taken, show an error with the process name using it.
- Suggest alternative common ports (8888, 8080, 3001, 9000) and let the user pick.
- App starts even if the default port is busy — dashboard loads in a "disconnected" state with the port selector visible, so the user can pick an available one.
- Changing the port restarts the listener; connection URLs update automatically.

### v1.3 — UI improvements

- **Responsive layout**: dashboard adapts to phone, tablet, and desktop sizes. Sidebar collapses to a hamburger/tabs on small screens, log table becomes a card layout, modal goes full-screen on mobile.
- **Log search/filter**: text input that filters log rows by substring match on path, method, or type. Instant client-side filtering as you type.
- **Quick-filter buttons**: one-click buttons for MOCK / PROXY / MISS to isolate a specific type in the log.

### v1.4 — Sequences

Return different responses on subsequent calls to the same endpoint. Essential for testing polling flows.

- Define an ordered list of responses for a mock. Each call advances to the next response.
- Configurable behavior when the sequence ends: loop, stay on last, or reset.
- UI: visual sequence editor showing the response chain.
- Example: `GET /deposits/status` → call 1: `{"status": "pending"}` → call 2: `{"status": "processing"}` → call 3: `{"status": "completed"}`.

### v1.5 — Scenarios

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

### v1.6 — Mock tree view

Collapsible tree view for the mock list sidebar, grouping mocks by path segments.

- Toggle between flat list (current) and grouped tree view
- Paths are split into segments: `/osb/config/limits` and `/osb/games/` both appear under an `/osb/` node
- Expand/collapse nodes to drill into path groups
- Bulk actions on groups: enable/disable all mocks under a path prefix
- Applies to the mock sidebar only — the request log stays chronological

### v2.0 — Stable release

Bundle v1.1–v1.6 as the second major release. The milestone:

> **Ditto is a native desktop product with polished UX, advanced mocking (sequences + scenarios), and a scalable mock management UI.**

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
- **Import/export**: import from HAR files, Postman collections, or OpenAPI specs. Export scenarios to share with teammates.
- **Team sharing**: export/import full Ditto configs (mocks + scenarios) so the whole team runs the same setup.

### Distribution & install
- **Record mode**: bulk-capture all proxied responses as mock files automatically.
- **Homebrew tap**: `brew install draftea/tap/ditto` for one-line install on macOS.
- **Code signing + notarization**: Apple Developer Program signing to remove the Gatekeeper warning on first launch.
- **Auto-update mechanism**: check for new versions and prompt to update in place.
- **Multi-target routing**: route different path prefixes to different backends (`/users/*` → service A, `/bets/*` → service B).
- **Config file**: YAML/JSON config as an alternative to CLI flags for complex setups.
