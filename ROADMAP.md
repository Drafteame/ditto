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

> ⚠️ HTTPS is now an **advanced, optional** feature. The recommended setup is HTTP + a debug-only network config in the app. See README + [docs/HTTPS.md](docs/HTTPS.md).

## v0.3 — Distributable binary ✅

- ~~Set up a GitHub Actions workflow for automated releases on git tags~~
- ~~Cross-compile for macOS (Intel + Apple Silicon), Linux (amd64 + arm64), Windows (amd64)~~
- ~~Drop the `git clone` + `go build` requirement — README now shows binary download as the primary install path~~
- Future: signed `.dmg` / `.pkg` / `.deb` installers, Homebrew tap

## v0.4 — Web UI (foundation) ✅

- ~~Embed a small SPA into the binary (no separate frontend project to maintain)~~
- ~~Live request log via SSE (see what's flowing through Ditto in real time)~~
- ~~Visual list of mocks with on/off toggles~~
- ~~Open the UI on startup (`--no-ui` to opt out)~~
- ~~Mock reload from UI (re-reads mocks directory without restarting)~~
- ~~REST API at `/__ditto__/api/` for programmatic access~~

## v0.5 — Mock management in the UI

- Create / edit / delete mocks from the browser
- Form-based editor with JSON validation
- Duplicate / reorder mocks
- No more hand-editing JSON files

## v0.6 — One-click cert install

For teams that *do* need HTTPS, eliminate the manual cert pain:

- "Install on Android emulator" button → runs `adb` commands behind the scenes
- "Install on iOS simulator" button → uses `xcrun simctl` to add the cert
- For physical devices: generate a `.mobileconfig` for iOS (Firebase-style profile install) and a QR code linking to the cert
- A `ditto doctor` command to verify the trust setup on each connected device

## v0.7 — Record mode

- Add `--record` flag (and a UI toggle)
- Proxy all requests to the target backend and **save the responses as mock files automatically**
- Eliminates writing mock JSON by hand for most cases
- UI: review captured requests, mark which ones to keep as mocks

## v0.8 — Smarter request matching

- Match on query parameters
- Match on request headers
- Match on request body content
- Multiple mocks per `method + path` with different conditions (e.g., different response per user ID)

## v0.9 — First-run wizard

- Detects connected emulators / simulators on launch
- Walks the user through configuring their app (with copy-paste snippets specific to their stack)
- Offers to install certs if they need HTTPS
- Goal: zero-friction onboarding for someone who has never seen Ditto before

## v1.0 — Config file + multi-target

- YAML or JSON config file as an alternative to CLI flags
- Multiple backends with per-path routing (e.g., `/users/*` → service A, `/bets/*` → service B)
- Switchable environments (dev, staging) without restarting

## Ideas / nice to have

- Dynamic responses (templates that use values from the request — e.g., echo back a path param)
- Stateful mocks (`POST /users` creates a record, subsequent `GET /users/:id` returns it)
- Mock chaining / sequences (return different responses on subsequent calls)
- Latency simulation profiles (slow 3G, flaky network)
- Failure injection (random 500s, timeouts) for resilience testing
- Homebrew tap for one-line install on macOS
- Auto-update mechanism

## Out of scope (for now)

- Forward proxy mode (sitting between app and backend like Charles/Proxyman) — Ditto is intentionally a reverse proxy
- HAR file import/export — possible later, but not a priority
