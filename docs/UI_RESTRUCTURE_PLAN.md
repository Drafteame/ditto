# Ditto · UI restructure plan

> **Scope:** **cosmetic / structural rework only.** No features added, removed, or rewired. Same hooks, same stores, same backend contracts. We are moving existing components into a more semantic layout that stops feeling like "HTTP on one side, WebSocket on the other" and starts feeling like a single tool to reproduce complete app flows.
>
> Visual reference: [`docs/wireframes/option-2-detail-v2.html`](wireframes/option-2-detail-v2.html) · [`docs/wireframes/option-2-detail-v2.png`](wireframes/option-2-detail-v2.png).
>
> Companion doc for *what to do when adding a feature*: [`docs/UI_GUIDELINES.md`](UI_GUIDELINES.md).

---

## Why

Today the app uses top-level tabs (`Requests`, `Sockets`, `Event Templates`, `Sequences`, `Recordings`) that mix **configuration** (templates, sequence definitions, mock list) with **runtime usage** (live log, manual dispatch, connected clients). The sidebar only knows about HTTP mocks. This makes Ditto feel like two parallel tools instead of one.

**Goal:** one workspace where the left sidebar is *everything you can configure*, the center is *what is happening right now* (with the runtime states it depends on), and the right sidebar is *the inspector / recorder*.

---

## Scope of this plan

This plan covers **the features Ditto ships today**:

- HTTP mocks (port, target, mocks list, save-as-mock, response inspector)
- WebSocket: connected clients, channel modes (`mock / live / record / mixed`), schemas, adapter profiles, manual dispatch
- Event templates (M3)
- Event sequences (M4) with player
- Recordings capture (M5)

Feature parity with the current UI is reached at **UI-M5**. UI-M6 is polish + forward-compatibility scaffolding (empty slots) for the next roadmap milestones — see [§ Forward-compatibility](#forward-compatibility-baked-in).

Anything past parity (Scenarios v1.6, recording replay/edit M6, adapter profile editor M9, `.dittopack` import/export M8) is **out of scope** for this restructure. The layout is designed so those features slot in without redesign — see the same section.

---

## Forward-compatibility baked in

This restructure has been validated against the rest of the WebSocket roadmap so the layout doesn't have to be torn up at every milestone. Every region has a designated home for what's coming. The slots themselves are introduced in **UI-M6** (empty placeholders); the actual features land with their respective roadmap milestones.

| Future roadmap item | Where it lands in this layout | Slot reserved in |
| --- | --- | --- |
| **M5 throttling / coalescing** (already shipped, surfacing today) | Aggregate row inside Activity (`"42 frames in 1s"`) + per-client `dropped: N` counter inside the Connected-clients popover in the topbar. | UI-M2 (coalesced log row) + UI-M4 (popover counter) |
| **M6 Recording replay + editing** | The existing **Recordings** workspace (already a `RecordingsView + RecordingDetail` pair) grows the timeline + per-event actions in place. Reachable from the `Activity / Recordings` toggle introduced in UI-M2. | UI-M2 (Recordings tab) |
| **M7 Scenarios** | New **Scenarios** sub-section in the sidebar Flows group, next to Sequences. | UI-M6 (empty sub-section wired to the existing `useScenarioStore`) |
| **M7 active-scenario indicator** | Full-width banner directly above Activity with `Stop`, `Pause`, summarised current step. | UI-M6 (banner slot renders `null` until M7) |
| **M7 HTTP→Socket triggers** | Bidirectional visual link in Activity rows: `→ fired tickets · settled` on the HTTP row, `← from POST /osb/tickets` on the WS row. | UI-M6 (`linkedEventId` optional prop on the log row component) |
| **M2 / M9 Schemas & Adapter profiles** | A ⚙️ **Settings** button in the topbar opens a modal with sections: Schemas · Adapter profiles · Workspace. The `⚙ Schemas` and `⚙ Adapter profiles` sidebar entries deep-link here. | UI-M4 (Settings modal scaffolded; M9 makes Adapter profiles editable in place) |
| **M8 `.dittopack` import / export** | Same Settings modal, **Workspace** section: Import bundle · Export current setup. Conflict resolution UI lives there. | UI-M6 (Workspace placeholder) |

---

## Target layout

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  TOPBAR                                                                     │
│  D Ditto [dev]  ● Connected · 2 ws clients · 4 channels ▾   ⚙   QR  ↻  🗑   │
├──────────┬──────────────────────────────────────────────────────┬───────────┤
│ SIDEBAR  │  ┌─ Active scenario · slot (empty until M7) ─────┐    │ INSPECTOR │
│          │  └────────────────────────────────────────────────┘    │ /        │
│ HTTP     │                                                       │ RECORDER │
│  port    │  ┌─ Activity / Recordings ────────────────────────┐    │           │
│  target  │  │ ⏸  ⌘K filter…   [HTTP] [WS] [MOCK] [PROXY] [⌟]│    │  PROXY    │
│  mocks   │  ├────────────────────────────────────────────────┤    │  GET 200  │
│          │  │ 19:29:20 HTTP GET  /locations/config    PROXY  │    │           │
│ SOCKET   │  │ 19:29:25 HTTP POST /users/refresh       PROXY  │    │ /locations│
│  port    │  │ 19:29:29 WS  SUB   /app/notifications   WS     │    │  /config  │
│  events  │  │ 19:29:30 HTTP POST /osb/tickets               │    │           │
│  schemas │  │ 19:29:30 WS  EMIT  tickets · settled          │    │ ↓ Save    │
│  profiles│  ├─ Channels ─────────────────────────────────── ▴│    │   as mock │
│          │  │ /app/notifications  mock   3 clients  0 dropped│    │           │
│ FLOWS    │  │ /games/123/leagues  live   1 client   42/s     │    │ Response  │
│  Sequences│ │ /livestats/.../*    record 2 clients  0 dropped│    │ Request   │
│  Scenarios│ ├─ Dispatch ────────────────────────────────────┤    │ Headers   │
│  (slot, M7)│ │ [Manual][Templ.][Seq.] channel  payload   ↗   │    │           │
│          │  └────────────────────────────────────────────────┘    │ {…}       │
└──────────┴──────────────────────────────────────────────────────┴───────────┘
```

### Region breakdown

| Region | Contents | Source today |
| --- | --- | --- |
| **Topbar** | Logo · env pill · Connection pill (HTTP + WS clients popover with per-client `dropped` counter) · ⚙️ Settings · QR · Reload · Clear | `Header.tsx` |
| **Sidebar — HTTP** | Port · Target URL · Mocks list | `Sidebar.tsx` |
| **Sidebar — Socket** | Port · Event Templates list · entry to Schemas / Adapter profiles (deep-edit opens Settings modal) | `SocketPanel.tsx` (templates) + new |
| **Sidebar — Flows** | Sequences (Scenarios sub-section is an empty slot wired to `useScenarioStore`, populated by v1.6) | `SequencesPanel.tsx` + slot for M7 |
| **Center — Active scenario banner** | Empty slot until M7. Banner renders `null` when `useScenarioStore.activeScenarioId === null`. | new (M7 fills it) |
| **Center — Activity / Recordings toggle** | Activity = unified live log (HTTP + WS) with `⌘K` filter and chips. Recordings = today's `RecordingsView + RecordingDetail` (document viewer). | `LogPanel.tsx` + `RecordingsView.tsx` |
| **Center — Channels strip** | Always-visible (auto-collapsed if all `mock`) per-channel mode selector + rate + dropped counter | `SocketPanel.tsx:257-298` (today) |
| **Center — Dispatch dock** | Mode rail (Manual / Template / Sequence) + form + recent | parts of `SocketPanel.tsx` + `EventTemplatesPanel.tsx` + `SequencePlayerView.tsx` |
| **Right sidebar** | Inspector + Save-as-mock recorder | existing `Drawer.tsx` |

---

## Sidebar structure (final)

```
─── HTTP ─────────────────
  Port           [8888]
  Target         [api.dev.draftea.com]
  Mocks (4)            + New
    GET /api/v1/users …
    POST /api/v1/login …

─── SOCKET ───────────────
  Port           [8888]
  Event templates (5)  + New
    WS  tickets · settled
    WS  tickets · cancelled
    WS  wallet · credited
  ⚙ Schemas · 2 packs    →  (opens Settings)
  ⚙ Adapter profiles · 1 →  (opens Settings)

─── FLOWS ────────────────
  Sequences (3)        + New
    SEQ place-bet → settle · 5 steps
    SEQ onboarding · 3 steps
  Scenarios (slot · M7)        ← empty until v1.6
```

**Why three groups:**

- **HTTP** and **Socket** are the two transports. Each owns its day-to-day artifacts (mocks for HTTP, event templates for Socket) and quick links to its deeper config (schemas / adapter profiles, which open the Settings modal for editing).
- **Flows** is everything that *composes* across transports. Sequences (timeline of WS events) live here today; Scenarios (atomic activation of HTTP mocks + channel modes + sequences + triggers) join in v1.6 — they are different abstraction levels and **coexist**, not a rename or a superset.

---

## Non-goals

- No new endpoints, no new stores, no new socket adapters.
- No change to mock-matching logic, sequence runner, recorder, schema or adapter registry.
- No copy rewrites beyond section headings.
- No theming/token changes — keep the current dark palette and teal accent.
- No new behavior in UI-M6 forward hooks: every slot renders `null` until the corresponding roadmap milestone fills it.

---

## Milestones

Each milestone is a single PR that compiles, ships, and can be merged independently. After UI-M0 the app keeps working at every step; the old tabs are removed only at UI-M5.

> Naming: `UI-Mn` to disambiguate from the WebSocket roadmap milestones (`M5`, `M6`, …). Where the UI rework anticipates a roadmap milestone, it is called out explicitly.

### UI-M0 · Inventory & component map *(no code changes)*

- Document which components today live under each tab and which sidebar slot they need to land in.
- Tag each one as **transport-config**, **flow-artifact**, **runtime-state**, or **document**.
- Confirm the props/hooks each component needs so we know what state crosses panels.
- Output: `frontend/src/components/README.md` (one-page map).

> **Exit criteria:** every component in `frontend/src/components/` and `frontend/src/views/` has a tag and a destination region.

### UI-M1 · Sidebar split into HTTP / Socket / Flows

- Restructure `Sidebar.tsx` into three collapsible groups: **HTTP**, **Socket**, **Flows**.
- HTTP group keeps today's `Port`, `Target URL`, `Mocks` list verbatim.
- Socket group adds: a `Port` stub (same value as HTTP for now — single server), `Event Templates` list (moved from `EventTemplatesPanel`), and two read-only entries `⚙ Schemas` / `⚙ Adapter profiles` that open today's standalone modals (Settings modal arrives in UI-M4 and absorbs them).
- Flows group: `Sequences` list (moved from `SequencesPanel`). Scenarios sub-section is **not** rendered yet — it is added as an empty slot in UI-M6.
- Single `+ New` per group (HTTP → New mock; Socket → New template; Flows → New sequence).
- Type-filter chips at the top of each list as appropriate (today's pills stay).
- **Search input does not live in the sidebar.** Search lives in Activity.

> **Exit criteria:** every action that was reachable from the Templates and Sequences tabs is reachable from the sidebar. The tabs still exist and still work.

### UI-M2 · Activity becomes the only live view

- Promote the current `LogPanel` to occupy the top portion of the main area whenever the app is connected.
- Keep its existing filter chips (`ALL / MOCK / PROXY / MISS / SOCKET`) and the `⌘K` filter input exactly as today.
- Add `HTTP` and `WS` chips to the same chip row — pure client-side filters over the same log array, no new data.
- Socket events already stream through SSE; ensure `SOCKET` rows render with the same row component.
- Add the **Activity / Recordings** toggle in the panel header. Recordings tab renders today's `RecordingsView + RecordingDetail` (document viewer) — same components, new home.
- Render coalesced WS bursts as a single row (`"42 frames in 1s (coalesced)"`) — surfacing the M5 throttling already in the log stream.

> **Exit criteria:** HTTP requests and WS events appear interleaved in time order in one list, and Recordings is reachable without leaving the screen.

### UI-M3 · Channels strip + Dispatch dock under Activity

- Pull the current channel-mode panel (`SocketPanel.tsx:257-298`) out and dock it as a thin **Channels** strip between Activity and Dispatch.
- Strip is collapsed by default if all visible channels are `mock`; expanded otherwise. Auto-expands when the SSE `MODE` event reports a change.
- Per-channel row shows: channel · mode selector (`mock / live / record / mixed`, with the current disabled-state logic for live/mixed without a target) · subscriber count · rate (events/sec) · `dropped` counter.
- Pull `Manual Dispatch` and `Templates picker` out of `SocketPanel.tsx` and render them as a fixed-height bottom dock under the Channels strip.
- Mode rail on the left of the dock with three states: **Manual** · **Template** · **Sequence** — they swap the right side's form, nothing else. The Sequence rail re-uses the existing `SequencePlayerView` controls.
- Dock height is fixed (~280 px) in v1; leave a drag handle TODO for later.
- "Recent" column shows the last N templates the user dispatched — read from existing template store, no new persistence.

> **Exit criteria:** every dispatch action a user could do from the Sockets tab is doable from this dock, and the channel-mode panel is visible without opening a popover. The Sockets tab still exists.

### UI-M4 · Connected clients move into the topbar pill + Settings entry

- The "Connected" badge becomes a popover trigger: lists WS clients, their subscriptions, their `dropped` counter (data already flows through today's hooks).
- Disconnected state stays the same red pill, no popover.
- Add a ⚙️ **Settings** button to the topbar that opens a modal with three sections:
  - **Schemas** — today's modal content moves in.
  - **Adapter profiles** — today's seeded list moves in (read-only until M9 lands the editor).
  - **Workspace** — placeholder section (string + disabled buttons) for the M8 import/export flow.
- The two `⚙` rows in the sidebar Socket group route here.

> **Exit criteria:** the dedicated `Connected clients` block in `SocketPanel` and the standalone Schemas/Adapter modals can be removed without losing information.

### UI-M5 · Remove the old tabs *(← feature parity)*

- Delete `MainTabs.tsx`'s tab bar.
- Delete `SocketPanel.tsx`, `EventTemplatesPanel.tsx`, `SequencesPanel.tsx` shells (their *content* has already moved out in UI-M1–M4).
- Routing: tab fragments now no-op; redirect any saved deep links to `/`.
- Right sidebar (inspector / recorder) stays untouched the entire migration.

> **Exit criteria:** there is one screen. Sidebar (HTTP / Socket / Flows), Activity (with Recordings toggle), Channels strip, Dispatch dock, and the Inspector are always visible (the inspector still slides in only on selection). **All features that exist on `main` today are reachable in the new layout.**

### UI-M6 · Polish + forward hooks

- Empty states: each sidebar group, Activity ("waiting for requests…"), Recordings ("no recordings yet"), Dispatch hint when no clients are connected, Channels strip when no channel is active.
- Keyboard: `⌘K` focuses the Activity filter (today's behavior, just confirm it still works).
- Audit `data-testid`s — keep every existing one on the moved DOM node so e2e tests do not break.
- Visual QA against `docs/wireframes/option-2-detail-v2.png`.
- **Forward hooks** (slots only, no behavior — the M7/M6/M8/M9 PRs flip them on):
  - Reserved space at the top of the main column for the **active-scenario banner** component (renders `null` until M7).
  - Activity row component accepts an optional `linkedEventId` prop (renders the bidirectional arrow between HTTP triggers and WS events when set — the renderer is in place even if no producer sets the prop yet).
  - Sidebar Flows group renders a stub `Scenarios` sub-section already wired to the existing (currently empty) `useScenarioStore`.
  - Settings → Workspace tab is a placeholder section ready to host M8 import/export.

> **Exit criteria:** every UI-M5 feature still works; nothing in this milestone changes user-visible behavior. Every M6/M7/M8/M9 feature has a named slot to land in.

---

## Things explicitly **not** changing

- The mock matching engine, sequence player, scenario engine (today's stub), recorder, schema registry, adapter registry.
- The shape of any persisted file under `mocks/`, `event_templates/`, `sequences/`, `recordings/`, `descriptors/`, `adapter_profiles/`.
- The SSE log endpoint or its event format.
- The set of adapters and how they are selected.
- The recorder flow ("Save as mock" from a proxied response).
- All copy inside modals (New Template, New Sequence) — they open exactly as today.

---

## Risk register

| Risk | Mitigation |
| --- | --- |
| Component coupling — `SocketPanel` owns dispatch, clients, channels, log (659 lines today) | UI-M2/M3/M4 split it; each lands behind the existing tab so we revert one milestone at a time. |
| Sidebar becomes too tall on small screens | Each group (HTTP / Socket / Flows) is collapsible; remember collapsed state in `localStorage`. |
| Channels strip crowds the center column when many channels are active | Strip is virtualised + scroll-on-overflow; auto-collapses to a summary chip (`5 channels · 1 live · 1 record`) when more than N rows are visible. |
| Users with deep-linked tab URLs | UI-M5 redirects all known fragments to `/` and logs a one-time toast. |
| e2e tests assert on tab DOM | UI-M6 audits `data-testid`s before tabs are removed. |
| Dispatch dock fixed height crowds smaller laptops | Fixed at ~280 px in v1; drag handle TODO is captured in UI-M3 exit notes. |

---

## Hand-off checklist for the implementing agent

When you start, please:

1. Read this file end to end before opening any component.
2. Open [`docs/wireframes/option-2-detail-v2.html`](wireframes/option-2-detail-v2.html) side by side — that is the target.
3. Skim [`docs/UI_GUIDELINES.md`](UI_GUIDELINES.md) for the region map and the rules for adding new features.
4. Work milestone by milestone. Open one PR per UI-M.
5. **If you find yourself adding a feature or changing behavior, stop.** Ping the design owner. Scope is cosmetic / structural.
6. After each milestone, run the existing app, exercise the moved feature, and confirm parity with the previous tab.
7. Cross-reference each UI-M with [`docs/WEBSOCKET_ARCHITECTURE.md`](WEBSOCKET_ARCHITECTURE.md) so M6+ slots land where this plan reserves them.
