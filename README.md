# Ditto

A lightweight local proxy that lets you mock API responses without touching your app's code.

Drop a JSON file in the `mocks/` folder, start Ditto, and point your app to it. Requests that match a mock get a fake response instantly. Everything else is forwarded to your real backend.

## Install

### Download a prebuilt binary (recommended)

Grab the latest release for your platform from the [Releases page](https://github.com/dtlucho/ditto/releases). Extract the archive and you're done — no Go toolchain required.

**macOS**: Download the `.zip` for your chip (Apple Silicon = `darwin_arm64`, Intel = `darwin_amd64`). Double-click to extract, then:

1. **First launch only** — macOS blocks unsigned apps by default. To open Ditto:
   - **Right-click** (or Control+click) on `Ditto.app` and select **Open**
   - A dialog appears — click **Open** to confirm
   - If macOS still blocks it (shows "Move to Trash" with no Open option), run this in Terminal:
     ```bash
     xattr -cr /path/to/Ditto.app
     ```
     Then double-click `Ditto.app` normally.
2. Ditto opens as a native desktop window with the dashboard inside.
3. To stop, close the window.

After the first launch, subsequent opens work with a normal double-click.

**Linux**: Extract the `.tar.gz` and run `./ditto`. On amd64, you get the full desktop app (requires GTK3 + WebKit2GTK). On arm64, it runs in headless mode and opens the dashboard in your browser.

**Windows**: Extract the `.zip` and run `ditto.exe`. The desktop app opens automatically.

Available builds: macOS (Intel + Apple Silicon), Linux (amd64 + arm64), Windows (amd64).

### Build from source

Requires [Go](https://go.dev/dl/) 1.26+.

```bash
git clone https://github.com/dtlucho/ditto.git
cd ditto
go build -o ditto .
```

## Usage

```bash
# Mock only (unmatched requests return 502)
./ditto

# Mock + proxy to a real backend
./ditto --target https://api.example.com

# Custom port and mocks directory
./ditto --port 9000 --mocks ./my-mocks
```

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8888` | Port to listen on |
| `--target` | _(none)_ | Backend URL to forward unmatched requests to |
| `--mocks` | `./mocks` | Directory containing mock JSON files |
| `--https` | `false` | Serve over HTTPS (advanced — see [docs/HTTPS.md](docs/HTTPS.md)) |
| `--certs` | `./certs` | Directory to store the generated TLS certificate |
| `--headless` | `false` | Run without the web dashboard (API still available) |
| `--log-format` | `text` | Log format: `text` (human-readable) or `json` (one object per line) |

### Headless mode

For CI pipelines, automated testing, or terminal-only usage:

```bash
# No browser, no dashboard, but the REST API at /__ditto__/api/ stays available.
./ditto --headless --target https://api.example.com

# Pipe-friendly output: one JSON object per request, banner suppressed via stderr.
./ditto --headless --log-format json --target https://api.example.com 2>/dev/null
```

Sample JSON log line:

```json
{"timestamp":"22:41:25","type":"MOCK","method":"GET","path":"/users","status":200,"duration_ms":0,"response_body":"..."}
```

## Connecting your app

Ditto binds to `0.0.0.0`, so any device on the same network can reach it.

| Where the app runs | Base URL |
|---|---|
| Android emulator | `http://10.0.2.2:8888` |
| iOS simulator | `http://localhost:8888` |
| Physical device (same Wi-Fi) | `http://<your-machine-ip>:8888` |

### Allowing HTTP in your app

Modern mobile platforms block plain HTTP traffic by default. Add a **debug-only** exception in the app you want to use with Ditto. This is a one-time change per app.

#### Android

Create `android/app/src/debug/res/xml/network_security_config.xml`:

```xml
<?xml version="1.0" encoding="utf-8"?>
<network-security-config>
  <domain-config cleartextTrafficPermitted="true">
    <domain includeSubdomains="true">10.0.2.2</domain>
    <domain includeSubdomains="true">localhost</domain>
    <!-- Add your machine's local IP here for physical-device testing -->
  </domain-config>
</network-security-config>
```

Then reference it in `android/app/src/debug/AndroidManifest.xml`:

```xml
<application android:networkSecurityConfig="@xml/network_security_config" />
```

#### iOS

In `ios/Runner/Info-Debug.plist` (or your debug-only Info.plist), add:

```xml
<key>NSAppTransportSecurity</key>
<dict>
  <key>NSAllowsArbitraryLoadsForDevelopment</key>
  <true/>
</dict>
```

These changes only affect debug builds — production stays HTTPS-only.

### Need HTTPS instead?

If you can't modify the app or your project requires HTTPS in development, see [docs/HTTPS.md](docs/HTTPS.md). It works, but requires installing a self-signed certificate on each device.

## Creating mocks

Each `.json` file in the mocks directory defines one mock. Example:

```json
{
  "method": "GET",
  "path": "/api/v1/users",
  "status": 200,
  "headers": {
    "Content-Type": "application/json"
  },
  "body": {
    "users": [
      {"id": 1, "name": "John Doe"}
    ]
  },
  "delay_ms": 0
}
```

### Fields

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `method` | Yes | — | HTTP method (`GET`, `POST`, `PUT`, `DELETE`, etc.) |
| `path` | Yes | — | URL path to match |
| `status` | No | `200` | HTTP status code to return |
| `headers` | No | `{"Content-Type": "application/json"}` | Response headers |
| `body` | No | — | Response body (any valid JSON) |
| `delay_ms` | No | `0` | Simulated response delay in milliseconds |

### Path wildcards

Use `*` to match any single path segment:

```json
{
  "method": "GET",
  "path": "/api/v1/users/*",
  "status": 200,
  "body": {"id": 1, "name": "John Doe"}
}
```

This matches `/api/v1/users/1`, `/api/v1/users/abc`, etc.

## How it works

```
App request ──► Ditto ──┬── Mock found? ──► Return fake response
                        │
                        └── No mock? ──► Forward to --target backend
                                         (or 502 if no target)
```

Mocks are reloaded on every request, so you can add or edit mock files without restarting Ditto.

## Roadmap

See [ROADMAP.md](ROADMAP.md) for upcoming features.

## License

MIT
