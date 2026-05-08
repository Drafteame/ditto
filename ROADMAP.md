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

### v1.5.x — UI restructure

Cosmetic / structural rework of the dashboard. **No features added, removed, or rewired** — same hooks, same stores, same backend contracts. The work moves existing components into a more semantic layout that stops feeling like "HTTP on one side, WebSocket on the other" and starts feeling like a single tool to reproduce complete app flows.

Output: one workspace where the left sidebar is *everything you can configure* (HTTP / Socket / Flows), the center is *what is happening right now* (Activity log + Channels strip + Dispatch dock), and the right sidebar is *the inspector / recorder*.

Plan: [docs/UI_RESTRUCTURE_PLAN.md](docs/UI_RESTRUCTURE_PLAN.md). Building-features guidelines: [docs/UI_GUIDELINES.md](docs/UI_GUIDELINES.md). Wireframe: [docs/wireframes/option-2-detail-v2.html](docs/wireframes/option-2-detail-v2.html).

The plan ships in seven milestones. **Feature parity with today's UI is reached at UI-M5**; UI-M6 is polish + forward-compat scaffolding (empty slots that future roadmap milestones fill in place):

- **UI-M0** — Inventory & component map (no code).
- **UI-M1** — Sidebar split into HTTP / Socket / Flows.
- **UI-M2** — Activity becomes the only live view, Recordings reachable via toggle.
- **UI-M3** — Channels strip + Dispatch dock under Activity.
- **UI-M4** — Connected clients move into the topbar pill; Settings modal absorbs Schemas / Adapter profiles / Workspace.
- **UI-M5** — Remove the old tabs. *Feature parity.*
- **UI-M6** — Polish + forward hooks (active-scenario banner slot, `linkedEventId` prop on log row, Scenarios sub-section, Workspace placeholder).

Forward-compat slots reserved for: M7 Scenarios (active-scenario banner above Activity, Scenarios sub-section in Flows, HTTP→Socket trigger arrows in Activity rows), M6 Recording replay/edit (Recordings tab grows the timeline in place), M9 Adapter profile editor (Settings → Adapter profiles becomes editable), M8 `.dittopack` (Settings → Workspace).

### v1.5 — Sequences

Return different responses on subsequent calls to the same endpoint. Essential for testing polling flows.

- Define an ordered list of responses for a mock. Each call advances to the next response.
- Configurable behavior when the sequence ends: loop, stay on last, or reset.
- UI: visual sequence editor showing the response chain.
- Example: `GET /deposits/status` → call 1: `{"status": "pending"}` → call 2: `{"status": "processing"}` → call 3: `{"status": "completed"}`.

### v1.6 — Scenarios (hybrid HTTP + WebSocket)

The unit that ties everything together: a single named bundle that atomically activates **HTTP mocks, WebSocket channel modes, WebSocket sequences, and HTTP→Socket triggers**. One toggle, one consistent stage for the app.

Pre-requisites: v1.5 (HTTP sequences) and the WebSocket MVP delivered in v1.8 (per-channel modes from M5 are required). Scope-wise this is M7 of the WebSocket track — see [docs/WEBSOCKET_ARCHITECTURE.md](docs/WEBSOCKET_ARCHITECTURE.md) for the architectural context.

A scenario can carry any combination of:

- **HTTP mocks** — references to mock files (`mocks/*.json`), with optional inline `sequence` overrides from v1.5.
- **WebSocket channel modes** — declare per-channel behaviour (`mock`, `live`, `record`, `mixed`) for the duration of the scenario.
- **WebSocket sequences** — references to timed event timelines (`sequences/*.json`), optionally auto-started on activation.
- **HTTP→Socket triggers** — when the app makes a matching HTTP call, fire a WS sequence. This is the bridge between both worlds: real backends often emit a socket event in response to an HTTP request, and triggers reproduce that without code.
- **Adapter profile dependencies** — scenarios declare required profiles up front (`requires: { "adapter_profiles": [...] }`) so activation fails clearly before dispatching if a profile is missing.

Activation is atomic: disables conflicting mocks/channels, enables the scenario's, arms triggers. UI lands in the slots reserved by **v1.5.x / UI-M6** — the active-scenario banner above Activity, the Scenarios sub-section under Sidebar → Flows, and the bidirectional HTTP↔WS trigger arrows on Activity rows (`linkedEventId` prop). See [docs/UI_GUIDELINES.md](docs/UI_GUIDELINES.md).

Scenarios are JSON files in `scenarios/` (e.g., `scenarios/match_day_happy_path.json`).

```json
{
  "name": "Match Day - Happy Path",
  "description": "Full match flow: lineup loads, user places a ticket, live stats stream",
  "requires": { "adapter_profiles": ["appsync-draftea"] },
  "mocks": [
    { "ref": "match_lineup.json" },
    { "ref": "user_balance.json" },
    {
      "ref": "get_deposit_status.json",
      "sequence": [
        { "status": 200, "body": { "status": "pending" } },
        { "status": 200, "body": { "status": "processing" } },
        { "status": 200, "body": { "status": "completed" } }
      ]
    }
  ],
  "channel_modes": {
    "/games/123": "mock",
    "/livestats/123": "mock"
  },
  "sequences": [
    { "ref": "match_full_90min.json", "auto_start": false }
  ],
  "triggers": [
    {
      "on": "http",
      "match": { "method": "POST", "path": "/osb/tickets/" },
      "fire": { "sequence": "ticket_created_flow.json" }
    }
  ]
}
```

Example scenarios: "Match Day - Happy Path", "Failed deposit flow", "Live game with lag spikes", "Empty wallet".

ADR: [Fire And Forget For HTTP Triggers](docs/adr/0002-fire-and-forget-for-http-triggers.md).

**Done when:** activate a scenario → relevant HTTP mocks load, relevant channels switch to `mock` mode, the sequence is armed. Exercise the app: trigger fires the sequence on the matching HTTP call. Everything choreographed — delivers "simulate a full session" without any session-specific code in Ditto.

### v1.7 — Mock tree view

Collapsible tree view for the mock list sidebar, grouping mocks by path segments.

- Toggle between flat list (current) and grouped tree view
- Paths are split into segments: `/osb/config/limits` and `/osb/games/` both appear under an `/osb/` node
- Expand/collapse nodes to drill into path groups
- Bulk actions on groups: enable/disable all mocks under a path prefix
- Applies to the mock sidebar only — the request log stays chronological

### v1.8 — WebSocket mocking

Full-blown WebSocket mocking layer: send arbitrary events to a connected app, record real backend traffic, replay it (modified or not), and bundle everything into scenarios.

Designed to keep Ditto **agnostic to any specific business or domain** — Protobuf schemas, recordings, and scenarios all live as user-loadable artifacts (schema packs, collections), never hardcoded. Architecture, filesystem layout, `.dittopack` manifest, and adapter profile spec: [docs/WEBSOCKET_ARCHITECTURE.md](docs/WEBSOCKET_ARCHITECTURE.md).

The work is broken into milestones (M0–M9). The MVP — M0 through M5 — ships as v1.8. Two follow-on capabilities (M6, M9) ship as v1.x increments under this umbrella; the remaining two land in their own roadmap slots: **Scenarios (M7) → v1.6** and **Ditto Collections (M8) → v1.9**.

#### MVP delivered (M0–M5)

✅ = delivered, see milestone tag for the PR that landed it.

- ✅ **Generic WS server + protocol adapter** (M1): pluggable adapters (AppSync, raw JSON) so any app's WS protocol can be supported.
- ✅ **Manual event dispatch** (M1): send arbitrary events from the UI to specific channels.
- ✅ **Schema packs** (M2): load `.proto` files at runtime (dynamic descriptors, no codegen). UI shows type dropdown + JSON editor with schema-aware autocomplete; Ditto serializes to Protobuf at dispatch.
- ✅ **Event templates** (M3): save reusable parameterized events (`{{ticketId}}`, `{{userId}}`, etc.).
- ✅ **Event sequences** (M4): timed event timelines with transport controls (play/pause/scrub/speed). `delay_ms` is wait-before-step (including step 1); `speed = 0` is Max mode and skips waits; playback uses a snapshot taken at `play` so edits affect the next run.
- ✅ **Per-channel modes + live target** (M5): each channel can be `mock`, `live` (passthrough to real backend), `record`, or `mixed`, using a server-level WS upstream. Mode decisions sit behind `dispatchRendered` (see [ADR 0001](docs/adr/0001-channel-modes-via-dispatch-rendered.md)).
- ✅ **Recordings capture** (M5): capture raw-frame-first WS sessions to disk with manifests, JSONL frames, decode metadata, rate caps, and visible drop counters. Decoder strategy: [ADR 0004](docs/adr/0004-recording-decoder-strategy.md).
- ✅ **Live bridge + mixed mode** (M5): share one upstream WS connection per active channel, forward raw frames both directions, and keep local injections available in `mixed` mode.
- ✅ **Recording operations UI** (M5): configure channel modes/rate caps, set the Live Target, start/stop recordings, browse recording manifests, and prepare paged frame loading for M6.
- ✅ **WS throttling + backpressure visibility** (M5): coalesced burst logs, recorder queue-drop counters, recording rate-cap drops, and per-client dropped-frame badges.
- ✅ **Adapter profiles** (M2): backend-specific envelope shape and type aliases live as JSON in `adapter_profiles/`. Bundled defaults are seeded on first run; supporting a new backend means dropping a JSON file in, not shipping a new Ditto version. Visual editor lands in M9 (below).

Per-format docs: [docs/EVENT_TEMPLATES.md](docs/EVENT_TEMPLATES.md), [docs/EVENT_SEQUENCES.md](docs/EVENT_SEQUENCES.md), [docs/CHANNEL_MODES.md](docs/CHANNEL_MODES.md), [docs/RECORDINGS.md](docs/RECORDINGS.md).

#### Recording replay + editing (M6)

**Goal:** open a recording, edit individual events, play it back as a sequence.

- Timeline view for recordings (similar to the M4 player but with real chronological events). Lands in the existing Recordings tab introduced by **v1.5.x / UI-M2** — no new region.
- Per-event actions: edit payload, delete, duplicate, export-to-template, "send a copy now".
- Recording-level operations: trim (cut start/end), splice (concatenate recordings), filter (only events from channels X).
- Conversion: `recording → sequence` (generate an editable `EventSequence` from a recording) — the resulting sequence becomes a Flows → Sequences entry, the visible bridge from Recordings to Library.
- Replay with original timing / compressed (multiplier) / sticky (skip long gaps).

**Done when:** open a 10-minute recording, filter to one channel, edit one event, save as a sequence, play it back at 5x.

#### Adapter profile UI (M9)

**Goal:** create, edit, validate, and test adapter profiles from the dashboard. Today profiles are hand-written JSON files (see the [adapter profiles section](docs/WEBSOCKET_ARCHITECTURE.md#adapter-profiles) of the architecture doc); M9 adds the visual layer so a non-technical user can onboard a new backend without touching files. Lives in the **Settings → Adapter profiles** section reserved by **v1.5.x / UI-M4** (read-only listing) — M9 turns it editable in place. See [docs/UI_GUIDELINES.md](docs/UI_GUIDELINES.md).

- **Profile editor:** form-driven UI for `name`, `base_adapter`, `subprotocols`, envelope templates (with syntax highlighting), and the `type_aliases` mapping.
- **Live preview pane:** pick a sample subscription ID, type, and payload — see exactly what frame will be sent on the wire as you edit.
- **Validation:** known variables are recognised, missing/typo'd vars flagged, the rendered output is JSON-validated, dry-run dispatch against a connected client is available.
- **CRUD via REST:** `POST/PUT/DELETE /__ditto__/api/socket/adapter-profiles`. New or edited profiles persist to `adapter_profiles/` and re-register at runtime without a restart.
- **Runtime reload:** profile writes validate first, then atomically update the adapter registry for new connections/dispatches.
- **Reference profiles:** ship a small library (vanilla AppSync, Pusher-style, Socket.IO-style) so first-time users have a starting point.

**Done when:** a non-technical user opens the dashboard, picks a starting reference profile, edits envelope and aliases visually, sees a live preview that matches their backend's actual frame, saves, and dispatches successfully without ever opening a JSON file.

#### Cross-cutting

Tests, documentation conventions, backward-compatibility, and performance expectations for this track are documented in the [cross-cutting concerns section](docs/WEBSOCKET_ARCHITECTURE.md#cross-cutting-concerns) of the architecture doc.

### v1.9 — Ditto Collections (`.dittopack`)

Shareable, versioned bundles of Ditto configuration. Like Postman Collections, but for everything Ditto manages. Implementation lands as M8 of the WebSocket track. UI surface is the **Settings → Workspace** section reserved by **v1.5.x / UI-M6** (placeholder there until M8 fills it). See [docs/UI_GUIDELINES.md](docs/UI_GUIDELINES.md).

- Bundle format: zip with `manifest.json` (with `schemaVersion`) + structured folders (`mocks/`, `scenarios/`, `sequences/`, `descriptors/`, `recordings/`, `adapter_profiles/`, `event_templates/`). The compatibility contract has been honoured since M0 so every artifact produced from v1.6 onwards is bundle-compatible by construction. See the [`.dittopack` manifest format](docs/WEBSOCKET_ARCHITECTURE.md#dittopack-manifest-format) in the architecture doc.
- Export granularity:
  - Full config (entire Ditto data dir).
  - Scenario + its dependencies (mocks, sequences, recordings, schema packs, adapter profiles referenced).
  - Schema pack alone.
  - Individual mock / sequence.
- Import with conflict resolution UI: per conflicting item, show a `git`-style diff + skip / overwrite / rename / merge options.
- Adapter profile conflicts key by `name + manifest_version`. Same name/version applies cleanly; different versions require explicit user diff/choice.
- Adapter profile seed markers (e.g. `.seeded`) are runtime state and must not be exported. Revisit moving the marker out of `adapter_profiles/` when bundle import/export defines which state files are packaged.
- Persist manifest format with `schemaVersion` for future compatibility.
- Replaces the older "Import/export" and "Team sharing" backlog items, which are unified under this single mechanism.

ADR: [Bundle ID Stability Policy](docs/adr/0003-bundle-id-stability-policy.md).

**Done when:** export a scenario as `match-day-v1.dittopack`, share with a teammate, they import with a click, see conflicts (if any), resolve, activate the scenario, it works identically.

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
