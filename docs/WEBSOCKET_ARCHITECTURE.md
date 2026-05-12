# WebSocket mocking — architecture

Reference document for Ditto's WebSocket mocking layer. Captures the architecture, data formats, and cross-cutting design decisions that span every WebSocket milestone.

The roadmap entries that schedule this work live in [ROADMAP.md](../ROADMAP.md): see **v1.6 (Scenarios)**, **v1.8 (WebSocket mocking)**, and **v1.9 (Ditto Collections)**.

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
| Schema distribution | "Schema packs" — zip bundles with manifest + descriptors, loaded by the user. |
| Frontend state | Zustand from M0. Theme-scoped stores (`useSocketStore`, `useScenarioStore`, `useSchemaStore`, etc.). |
| WS library | `nhooyr.io/websocket` (no CGO, plays well with Wails). |
| Filesystem layout | Designed from day 1 to be exportable as `.dittopack`. |
| Match/scenario simulation | No hardcoded generators. Composition of record + replay + scenario + dispatch. |
| Adapter customization | Profile-based (`adapter_profiles/*.json`). A generic profile-driven adapter at runtime applies envelope templates + type aliases. No per-backend Go code; supporting a new backend = dropping a JSON file. |

## Filesystem layout

```
~/Library/Application Support/Ditto/
  config.json          ← already exists
  mocks/               ← already exists
  descriptors/         ← schema packs loaded
  adapter_profiles/    ← protocol adapter configs (envelope template + type aliases)
  event_templates/     ← reusable parameterized events
  sequences/           ← ordered event timelines
  recordings/          ← real captured WS traffic
  scenarios/           ← HTTP mocks + sequences + triggers, atomic activation
```

## `.dittopack` manifest format

Bundle import/export packages user-loadable artifacts as a zip with a manifest at the root and paths matching the filesystem layout above. The compatibility contract was defined from M0; every milestone produces bundle-compatible artifacts. Bundle import/export itself is delivered in v1.9.

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
    "adapter_profiles": ["adapter_profiles/appsync-draftea.json"],
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

## Adapter profiles

Adapter profiles parameterize the WebSocket protocol adapters per backend. They are JSON files in `adapter_profiles/` (and travel inside `.dittopack` bundles, see v1.9) so that backend-specific envelope shape and type aliases live as **data, not Go code**. This keeps Ditto agnostic: supporting a new backend means dropping a JSON file in, not shipping a new Ditto version.

### Profile JSON format

Example: `adapter_profiles/appsync-draftea.json` (shipped as a seeded default).

```json
{
  "manifest_version": 1,
  "name": "appsync-draftea",
  "base_adapter": "appsync",
  "subprotocols": ["aws-appsync-event-ws"],
  "envelope": {
    "outer":        "{\"id\":\"${sub_id}\",\"type\":\"data\",\"event\":${inner_string}}",
    "inner_binary": "{\"t\":\"${alias}\",\"e\":\"${base64}\"}",
    "inner_json":   "{\"t\":\"${alias}\",\"e\":${json}}"
  },
  "type_aliases": {
    "appsync.recovery.Recovery": "recovery",
    "appsync.gameinfo.GameEventDto": "gameInfo",
    "appsync.betinfo.BetEventDto": "betInfo",
    "appsync.betinfo.BetsInfoDto": "betsInfo",
    "appsync.statsinfo.StatsRealtimePayloadDto": "statsInfo",
    "appsync.livestatsinfo.LiveStatEventDto": "liveStatsInfo",
    "appsync.livestatsinfo.LiveStatsEventDto": "liveStatsInfoBatch",
    "appsync.earlycashoutinfo.EarlyCashoutEventDto": "ticketCashoutInfo",
    "appsync.ticketinfo.TicketEventDto": "ticketInfo",
    "appsync.ticketbetinfo.TicketBetEventDto": "ticketBetInfo",
    "appsync.livetableupdate.LiveTableUpdate": "liveTableUpdate",
    "appsync.livetableupdate.LiveTableUpdates": "liveTableUpdates"
  }
}
```

Fields:

- `manifest_version` (required, int): starts at `1`.
- `name` (required): unique adapter name. Becomes the value of `?adapter=<name>` and the `adapter` field in REST/template/sequence requests.
- `base_adapter` (required): one of the built-in adapters (`raw`, `appsync`). The profile inherits its control plane (`connection_init`, `subscribe`, `pong`, …) and overrides `Subprotocols()` and the data envelope wrapping. `raw` means the profile fully owns the data envelope and there is no built-in AppSync-style subscribe/ack handshake.
- `subprotocols` (optional): WebSocket subprotocols negotiated during handshake.
- `envelope` (required): templates rendered at dispatch time.
  - `outer`: top-level WS frame. Variables: `${sub_id}`, `${channel}`, `${inner_object}`, `${inner_string}`. `${inner_object}` inserts the rendered inner envelope as a raw JSON object/array/value, e.g. `"event":${inner_object}`. `${inner_string}` is a raw JSON string literal containing the rendered inner envelope, so `"event":${inner_string}` yields a string field like `"event":"{\"t\":\"recovery\",\"e\":\"...\"}"`.
  - `inner_binary`: payload wrapper for protobuf-encoded binary payloads. Variables: `${alias}`, `${type_name}`, `${channel}`, `${base64}`.
  - `inner_json`: payload wrapper for raw JSON payloads. Variables: `${alias}`, `${type_name}`, `${channel}`, `${json}`.
- `type_aliases` (optional): map from proto FQN to short alias used as `${alias}`. If the dispatched type has no alias entry, `${alias}` falls back to `${type_name}` (the FQN).

### Loader and registration

At startup Ditto scans `adapter_profiles/`, parses each `.json`, validates it, and registers it as a protocol adapter under its `name`. Profile files are capped at 1 MB. Bundled defaults (currently `appsync-draftea.json`) are seeded into the directory on first run so out-of-the-box `?adapter=appsync-draftea` works without manual setup. Existing files are not overwritten, and a `.seeded` marker prevents the default from being recreated if the user renames or removes it.

REST: `GET /__ditto__/api/socket/adapter-profiles` lists available profiles (read-only). Create/update via UI is delivered in v1.x — Adapter profile UI (see ROADMAP).

Until the visual editor lands, validation checks profile shape only. Template typos and malformed rendered JSON surface on dispatch, so profile authors should test at least one dispatch before relying on a hand-written profile.

### Why a separate artifact

Schema packs describe types. Profiles describe how a backend wraps those types on the wire. Two backends can share a schema pack while differing in envelope; one backend can use multiple schema packs under the same envelope. Keeping them separate avoids forcing re-packaging when only the envelope changes.

## Cross-cutting concerns

- **Tests:** every milestone adds Go unit tests for its slice (protocol adapter, registry, sequence player engine). E2E: a Go-based test WS client that masquerades as the app and verifies events. Client-side integration testing starts at M5+.
- **Documentation:** every milestone updates `ROADMAP.md` and adds a doc in `docs/` describing the format of any new artifacts (templates, sequences, recordings, scenarios). These docs are part of DoD.
- **Backward compatibility:** Protobuf schemas evolve. Recordings and sequences depending on them must tolerate added fields (Protobuf's forward compat helps) and warn when a referenced field disappears. Schema packs are versioned (`pack-v1`, `pack-v2`); allow coexistence.
- **Channel modes:** see [ADR 0001 — Channel Modes Via Dispatch Rendered](adr/0001-channel-modes-via-dispatch-rendered.md).
- **HTTP→Socket triggers:** see [ADR 0002 — Fire And Forget For HTTP Triggers](adr/0002-fire-and-forget-for-http-triggers.md).
- **Bundle ID stability:** see [ADR 0003 — Bundle ID Stability Policy](adr/0003-bundle-id-stability-policy.md).
- **Recording decoder strategy:** see [ADR 0004 — Recording Decoder Strategy](adr/0004-recording-decoder-strategy.md).
- **Performance:** the `SequencePlayer` should run dozens of concurrent sequences without saturation. Benchmark from M4.
