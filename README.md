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
