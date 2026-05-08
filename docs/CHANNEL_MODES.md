# Channel Modes

Ditto stores WebSocket behavior per logical channel in
`channel_modes/state.json`. Modes are consulted only at `dispatchRendered`, as
described in [ADR 0001](adr/0001-channel-modes-via-dispatch-rendered.md).

| Mode | Local dispatch | Live upstream | Recording | Notes |
|---|---:|---:|---:|---|
| `mock` | yes | no | no | Default for channels without saved config. |
| `live` | no | yes | no | Local sequence/template/manual dispatch is suppressed. |
| `record` | no | no | yes | Reserves channel recording config without local mock injection. |
| `mixed` | yes | yes | yes | Live traffic passes through and local injections still use adapters. |

When local dispatch is suppressed, callers receive a non-fatal dispatch result
with an error message such as `channel mode live suppressed local dispatch`.
Sequence players and templates keep calling the same dispatch path and degrade
without knowing the active mode.

Live and mixed modes use the server-level Live Target configured in Settings or
with `--live-target ws://...`. The target is not stored in adapter profiles.
Live Target dials whatever you point it at, including private and loopback
hosts. Treat the Ditto dashboard as trusted.

Live upstream dials reuse auth-like headers from the first local client attached
to a channel (`Authorization`, `Cookie`, and custom headers), excluding
WebSocket handshake headers and `Origin`. Because the M5 bridge is keyed only by
channel, multiple local clients on the same channel share one upstream
connection and therefore one header set until reconnect. If tokens rotate during
a local client session, reconnect that client to refresh the headers used for
future upstream dials.

Recording rate caps are configured per channel with `rate_cap_hz`; `0` disables
the cap. Dropped recording frames are counted in the recording manifest.

High-volume socket log entries are coalesced per channel in one-second windows:
the first events up to the threshold are still emitted, then Ditto publishes a
`DISPATCH_BURST` summary whose `total_frames` is the full window count.

## Saved Channels

Channels can be registered manually from the sidebar without waiting for a
client to subscribe. Saved channels are persisted in `channel_modes/state.json`
with their mode and optional `rate_cap_hz`, then shown both in the sidebar and
in the Sockets view's Channel modes list.

Calling `Get` for an unregistered channel still returns the implicit default
`mock` mode. Explicitly saving a channel as `mock` keeps it in the registry; use
the sidebar delete action or `DELETE /__ditto__/api/channel-modes?channel=...`
to remove it.

## Live Log Payloads

When at least one SSE client is consuming `/__ditto__/events`, Ditto decodes
upstream frames (`live`/`mixed`) and local dispatches (`mock`/`mixed`) with the
adapter profiles and schema registry, then includes the JSON payload in the live
socket log. Payloads larger than 4KB are truncated in the SSE body; use a
recording when you need the complete frame. If no SSE clients are connected,
Ditto skips this decode work to keep idle live proxy traffic cheap.
