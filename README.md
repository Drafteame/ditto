# Ditto

A lightweight local proxy that lets you mock API responses without touching your app's code.

Start Ditto, point your app at it, and configure mocks from the built-in dashboard. Matched requests get a fake response instantly. Everything else is forwarded to your real backend.

## Install

Download the latest build for your platform from the [Releases page](https://github.com/Drafteame/ditto/releases).

### macOS

Download the `.zip` for your chip:
- Apple Silicon → `darwin_arm64`
- Intel → `darwin_amd64`

Double-click to extract, then open `Ditto.app`.

**First launch only.** macOS blocks unsigned apps by default:

1. Right-click (or Control+click) on `Ditto.app` → **Open** → **Open** in the dialog.
2. If macOS still blocks it, run this in Terminal and try again:
   ```bash
   xattr -cr /path/to/Ditto.app
   ```

Subsequent launches open with a normal double-click.

### Linux

Extract the `.tar.gz` and run `./ditto`.

- **amd64** → full desktop app (requires `libgtk-3` and `libwebkit2gtk-4.0`)
- **arm64** → headless mode; opens the dashboard in your browser

### Windows

Extract the `.zip` and run `ditto.exe`. The desktop app opens automatically.

### Build from source

Requires [Go](https://go.dev/dl/) 1.26+ and Node.js 20+.

```bash
git clone https://github.com/Drafteame/ditto.git
cd ditto
cd frontend && npm install && npm run build && cd ..
go build -o ditto .
```

## WebSocket mocking

Ditto can also stand in for your app's WebSocket backend. The minimum to start mocking events:

1. **Point your app at Ditto.** Set its WebSocket URL to `ws://localhost:8888/__ditto__/socket` (or `wss://...` if you started Ditto with `--https`). Ditto exposes `/__ditto__/ws` as an alias.
2. **Open the Sockets tab** in the dashboard. Connected clients and the channels they have subscribed to appear there in real time.
3. **Pick a protocol adapter.** Choose a built-in (`raw` for plain JSON, `appsync` for AWS AppSync Events) or any **adapter profile** loaded from `adapter_profiles/` (e.g. `appsync-draftea` ships as a default for AppSync backends with a Draftea-style custom envelope). Profiles are JSON config files that customise envelope shape and type aliases per backend with no Go code — see the [Adapter profiles section](WEBSOCKET_MOCKING_PLAN.md#adapter-profiles) of the plan. A visual editor for profiles is planned (M9 in the roadmap).
4. **Dispatch an event.** Select a channel the client is subscribed to, write the JSON payload, and click **Dispatch**. The client receives it immediately.

That's enough to send arbitrary events to a connected app. The features below are optional layers on top:

- **Protobuf payloads** — upload a `.proto` schema pack from the Sockets tab. Pick a type from the dropdown and edit JSON with schema-aware autocomplete; Ditto serializes to Protobuf at dispatch time. No codegen.
- **Reusable events** — save a composed event as a template with `{{vars}}` substitution. See [docs/EVENT_TEMPLATES.md](docs/EVENT_TEMPLATES.md).
- **Timed sequences** — chain templates into a timeline with delays and play/pause/scrub/speed transport controls. See [docs/EVENT_SEQUENCES.md](docs/EVENT_SEQUENCES.md).

Full plan and milestone-by-milestone breakdown: [WEBSOCKET_MOCKING_PLAN.md](WEBSOCKET_MOCKING_PLAN.md).

## Headless mode

For CI, servers, or terminal-only workflows. Runs without the desktop window but keeps the REST API available.

```bash
# Minimal: just run the proxy
./ditto --headless

# With a backend target — unmatched requests are forwarded
./ditto --headless --target https://api.example.com

# Machine-readable logs (one JSON object per request on stdout, banner on stderr)
./ditto --headless --log-format json --target https://api.example.com 2>/dev/null
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8888` | Port to listen on |
| `--target` | _(none)_ | Backend URL to forward unmatched requests to |
| `--mocks` | _(persistent app data)_ | Directory containing mock JSON files |
| `--headless` | `false` | Run without the desktop window (API stays available) |
| `--log-format` | `text` | Log format: `text` or `json` |
| `--https` | `false` | Serve over HTTPS using a self-signed certificate (see below) |
| `--certs` | `./certs` | Directory to store the generated TLS certificate |
| `--version` | — | Print the version and exit |

Sample JSON log line:

```json
{"timestamp":"22:41:25","type":"MOCK","method":"GET","path":"/users","status":200,"duration_ms":0,"response_body":"..."}
```

## HTTPS

The recommended setup is plain HTTP with a debug-only network config in your app. If you need HTTPS instead (app can't be modified, strict requirements, etc.), see [docs/HTTPS.md](docs/HTTPS.md).

## Roadmap

See [ROADMAP.md](ROADMAP.md).

## License

MIT
