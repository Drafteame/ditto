# WebSocket mocking — implementation plan

Plan for adding WebSocket event mocking to Ditto. Scoped, incremental, milestone-based.

## Guiding principle

Ditto stays **agnostic to any specific business or domain**. The capability to "simulate a full match", "replay a casino session", or any other rich scenario is an **emergent property** of generic primitives — record, replay, scenarios, and manual dispatch — not a hardcoded feature.

Domain-specific data (Protobuf schemas, mocks, recordings, scenarios) lives in **user-loadable artifacts** (schema packs, collections), never in Ditto's source.

## Target architecture

```
┌─ Client app ─────────────────────────┐
│  WS_BASE_URL = wss://localhost:PORT  │
└──────────────┬───────────────────────┘
               │  (any protocol: AppSync, Socket.IO, raw)
               ▼
┌─ Ditto ─────────────────────────────────────────────────────┐
│  WS Server (generic)                                        │
│   ├─ Subscription Registry (channel → clients)              │
│   ├─ Pluggable Protocol Adapter (AppSync / SocketIO / raw)  │
│   └─ Per-channel mode: mock | live | record | mixed         │
│                                                             │
│  Schema Registry (loadable)                                 │
│   └─ Dynamic descriptors (.proto, JSON Schema, raw bytes)   │
│                                                             │
│  Event Engine                                               │
│   ├─ Templates (parameterized)                              │
│   ├─ Sequences (timeline + transport controls)              │
│   ├─ Recordings (real captures)                             │
│   └─ Scenarios (HTTP mocks + sequences + triggers)          │
│                                                             │
│  Collections (.dittopack import / export)                   │
└────────────────┬────────────────────────────────────────────┘
                 │  (live mode)
                 ▼
        Real backend (optional)
```

## Foundational decisions

| Topic | Decision |
|---|---|
| Protobuf handling | Dynamic descriptors at runtime (`bufbuild/protocompile` + `google.golang.org/protobuf/dynamicpb` + `protojson`). No codegen, no schema coupling. |
| Schema distribution | "Schema packs" — zip bundles with manifest + descriptors, loaded by the user. Path B in design discussion. |
| Frontend state | Zustand from M0. Theme-scoped stores (`useSocketStore`, `useScenarioStore`, `useSchemaStore`, etc.). |
| WS library | `nhooyr.io/websocket` (no CGO, plays well with Wails). |
| Filesystem layout | Designed from day 1 to be exportable as `.dittopack`. See M0. |
| Match/scenario simulation | No hardcoded generators. Composition of record + replay + scenario + dispatch. |

## Filesystem layout

```
~/Library/Application Support/Ditto/
  config.json          ← already exists
  mocks/               ← already exists
  descriptors/         ← schema packs loaded
  event_templates/     ← reusable parameterized events
  sequences/           ← ordered event timelines
  recordings/          ← real captured WS traffic
  scenarios/           ← HTTP mocks + sequences + triggers, atomic activation
```

## `.dittopack` manifest format (stub)

Future import/export work will package user-loadable artifacts as a zip with a
manifest at the root and paths matching the filesystem layout above. M0 only
defines the compatibility contract; it does not implement bundle import/export.

```json
{
  "manifest_version": 1,
  "name": "Example pack",
  "description": "Optional human-readable notes",
  "created_at": "2026-05-06T00:00:00Z",
  "ditto_min_version": "1.0.0",
  "artifacts": {
    "mocks": ["mocks/users.json"],
    "descriptors": ["descriptors/events/manifest.json"],
    "event_templates": ["event_templates/ticket_created.json"],
    "sequences": ["sequences/happy_path.json"],
    "recordings": ["recordings/session/channel.jsonl"],
    "scenarios": ["scenarios/match_day.json"]
  }
}
```

Rules:

- `manifest_version` is required and starts at `1`.
- Artifact paths are relative to the bundle root and must stay inside the
  bundle.
- Unknown top-level fields are allowed so future Ditto versions can add
  metadata without breaking older packs.
- Domain-specific schemas, mocks, recordings, and scenarios belong in these
  artifacts, not in Ditto source.

---

## Milestones

### M0 — Foundations

**Goal:** decouple the base without touching features.

- Add `nhooyr.io/websocket`, `bufbuild/protocompile`, `google.golang.org/protobuf/dynamicpb` dependencies.
- Frontend refactor: introduce Zustand. Move state from `App.tsx` into theme-scoped stores (`useMockStore`, `useSocketStore`, `useScenarioStore`, `useSchemaStore`). No functional change — pure refactor.
- Define final filesystem layout (see above).
- Stub `.dittopack` manifest format spec in this doc. Don't implement import/export yet — just guarantee future milestones produce bundle-compatible artifacts.
- Set up minimal Go test scaffolding. There are zero tests today; this milestone establishes the convention.

**Done when:** the app behaves identically, all state flows through Zustand, at least one Go test runs in CI.

---

### M1 — WS server + protocol adapter + manual dispatch

**Goal:** the client app connects to Ditto and receives events sent manually. No Protobuf yet — raw JSON only.

- `socket.go`: WS endpoint with upgrader, connection management, ping/pong.
- `SubscriptionRegistry`: thread-safe `channel → []clientID` map.
- `ProtocolAdapter` interface:

  ```go
  type ProtocolAdapter interface {
      ParseClientMessage(b []byte) (ClientMsg, error)
      EncodeServerMessage(msg ServerMsg) ([]byte, error)
      Heartbeat() ([]byte, time.Duration)
  }
  ```

- Initial implementations: `AppSyncAdapter` (for AWS AppSync Events protocol clients) and `RawAdapter` (plain JSON, no envelope).
- REST API: `POST /__ditto__/api/socket/dispatch` with `{channel, payload, adapter?}`.
- Frontend: "Sockets" tab with:
  - Connected clients list + their subscriptions
  - Free-form JSON editor + channel selector + "Dispatch" button
  - Live event log (reuse current `EventBus` + SSE pipeline)

**Done when:** point a client app's WS URL at `wss://localhost:PORT`, the app connects and subscribes, you compose JSON in the UI, click "Dispatch", the event arrives.

---

### M2 — Schema packs + Protobuf encoding

**Goal:** pick a type from a loaded schema and edit JSON with autocomplete; Ditto serializes to Protobuf at dispatch time.

- Schema pack loader: parse `.proto` with `protocompile`, build `protoreflect.FileDescriptor`s.
- `SchemaRegistry`: `RegisterPack(path) → []TypeDescriptor`, lookup by fully-qualified name.
- Dispatch flow: if the chosen type has a descriptor, `protojson.Unmarshal(json, dynamicpb.NewMessage(desc))` → `proto.Marshal(msg)` → bytes embedded in the protocol adapter's envelope.
- REST API: `POST /__ditto__/api/schemas/packs` (upload pack), `GET /__ditto__/api/schemas/types`.
- Frontend:
  - "Schema Packs" modal: upload + loaded packs list + available types
  - Dispatcher: type dropdown, JSON editor with schema-aware autocomplete (Monaco + JSON Schema generated from the descriptor).
- First pack created manually for testing (any project's `.proto`s).

**Done when:** load a schema pack, pick a type from the dropdown, edit JSON with autocomplete, dispatch to a channel, the client receives a valid Protobuf event.

---

### M3 — Event templates + quick-fire palette

**Goal:** save composed events as reusable templates with variable substitution.

- `EventTemplate` model: `{name, description, type, channel, payload (JSON with {{vars}}), defaults}`.
- Variable resolver at dispatch time: `{{ticketId}}`, `{{userId}}`, `{{now}}`, `{{uuid}}`, plus user-defined.
- REST API: CRUD on `/__ditto__/api/event-templates`.
- Persist as JSON files in `event_templates/`.
- Frontend:
  - "Event Templates" view (CRUD)
  - Side palette in the dispatcher: list of templates, click to load, quick form for variables.

**Done when:** save a parameterized template, fire it five times in a row with different variable values without rewriting the JSON.

---

### M4 — Event sequences + transport controls ✅

**Goal:** compose timed sequences of events and play them like a video.

- `EventSequence` model: `{name, steps: [{template_ref|inline, channel, delay_ms, vars_override}], on_end: loop|stay|reset}`.
- `SequencePlayer` engine in Go: one goroutine per active sequence, pausable, scrubbable, with adjustable speed (1x, 2x, 10x, max).
- REST API: `POST /__ditto__/api/sequences/{id}/play|pause|stop|seek|speed`.
- Player state broadcast over SSE (current cursor, current step, status).
- Frontend:
  - Sequence editor: drag-and-drop step list with delays.
  - Player view: visual timeline, transport controls (play/pause/stop/scrub), speed selector.
  - Live indicator showing which step is executing.

**Done when:** compose a 5-step sequence, play, pause at step 3, scrub to step 5. Each action reflects in the connected app.

Implementation notes:

- `delay_ms` is always the wait before the step, including the first step.
- `speed = 0` is Max mode and skips waits, dispatching steps back-to-back.
- Sequence playback uses a snapshot taken at `play`; edits affect the next run.
- Steps with `type_name` pre-encode their rendered payload and still exit
  through `dispatchRendered`.

---

### M5 — Live mode + Recording

**Goal:** Ditto can pass WS traffic through to a real backend and record it.

- WS reverse proxy (per-channel routing): when a channel is in `live` mode, Ditto opens an upstream connection and forwards both directions.
- Per-channel modes (config persisted): `mock` (default), `live`, `record`, `mixed` (live + permits additional injection).
- `Recorder`: when a channel is in `record` mode, persist each incoming event with relative timestamp to `recordings/{name}/{channel}.jsonl`.
- Decode to JSON using schema registry if a descriptor exists; otherwise store raw bytes + base64.
- Frontend:
  - In the channel view: 4-mode selector.
  - "Recordings" view: list of recordings, metadata (duration, # events, channels involved), stop button if active.

**Done when:** point Ditto at a real backend, configure target channels in `record` mode, exercise the app, stop recording. A navigable recording exists on disk.

---

### M6 — Replay + recording editing

**Goal:** open a recording, edit individual events, play it back as a sequence.

- Timeline view for recordings (similar to M4 player but with real chronological events).
- Per-event actions: edit payload, delete, duplicate, export-to-template, "send a copy now".
- Recording-level operations: trim (cut start/end), splice (concatenate recordings), filter (only events from channels X).
- Conversion: `recording → sequence` (generate an editable `EventSequence` from a recording).
- Replay with original timing / compressed (multiplier) / sticky (skip long gaps).

**Done when:** open a 10-minute recording, filter to one channel, edit one event, save as a sequence, play it back at 5x.

---

### M7 — Scenarios (v1.6 feature)

**Goal:** the gold unit. Combines HTTP mocks, sequences, channel configuration, into one atomic activation.

- `Scenario` model:

  ```json
  {
    "name": "Match Day - Happy Path",
    "mocks": [{ "ref": "match_lineup.json" }, { "ref": "user_balance.json" }],
    "channel_modes": { "/games/123": "mock", "/livestats/...": "mock" },
    "sequences": [{ "ref": "match_full_90min.json", "auto_start": false }],
    "triggers": [
      {
        "on": "http",
        "match": { "method": "POST", "path": "/osb/tickets/" },
        "fire": { "sequence": "ticket_created_flow.json" }
      }
    ]
  }
  ```

- Activation: disables conflicting mocks/channels, enables the scenario's, arms triggers.
- HTTP→Socket triggers: when an HTTP request matches, Ditto fires a sequence. This solves the common pattern "the backend emits a socket event when it receives request X".
- Frontend: scenario cards in the sidebar (aligned with ROADMAP v1.6 spec), active-scenario visual indicator, "Stop scenario" button.

**Done when:** activate a scenario → relevant HTTP mocks load, relevant channels switch to `mock` mode, the sequence is armed. Exercise the app: trigger fires the sequence on the matching HTTP call. Everything choreographed.

This milestone delivers **"simulate a full session"** without any session-specific code in Ditto. The scenario carries everything that defines that case.

---

### M8 — Ditto Collections (`.dittopack` import/export)

**Goal:** share complete setups with teammates.

- Bundle format: zip with `manifest.json` (version, name, author, refs) + folder structure.
- Export granularity:
  - Full config (entire Ditto data dir)
  - Scenario + its dependencies (mocks, sequences, recordings, schema packs referenced)
  - Schema pack alone
  - Individual mock / sequence
- Import with conflict resolution UI: per conflicting item, show a `git`-style diff + skip / overwrite / rename / merge options.
- Persist manifest format with `schemaVersion` for future compatibility.

**Done when:** export a scenario as `match-day-v1.dittopack`, share with a teammate, they import with a click, see conflicts (if any), resolve, activate the scenario, it works identically.

---

## Cross-cutting concerns

- **Tests:** every milestone adds Go unit tests for its slice (protocol adapter, registry, sequence player engine). E2E: a Go-based test WS client that masquerades as the app and verifies events. Defer client-side integration testing until M5+.
- **Documentation:** every milestone updates ROADMAP.md and adds a doc in `docs/` describing the format of any new artifacts (templates, sequences, recordings, scenarios). These docs are part of DoD.
- **Backward compatibility:** Protobuf schemas evolve. Recordings and sequences depending on them must tolerate added fields (Protobuf's forward compat helps) and warn when a referenced field disappears. Schema packs are versioned (`pack-v1`, `pack-v2`); allow coexistence.
- **Performance:** the `SequencePlayer` should run dozens of concurrent sequences without saturation. Benchmark from M4.

---

## Suggested entry paths

| Goal | Path |
|---|---|
| "I can send events to my app" (raw JSON) | M0 → M1 → M3 |
| "I can simulate Protobuf-based flows" | M0 → M1 → M2 → M3 → M4 |
| "I can simulate a full session with one click" | M0 → ... → M7 |
| "I can share my setup" | + M8 |
