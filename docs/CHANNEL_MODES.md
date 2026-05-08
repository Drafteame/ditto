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

Recording rate caps are configured per channel with `rate_cap_hz`; `0` disables
the cap. Dropped recording frames are counted in the recording manifest.

High-volume socket log entries are coalesced per channel in one-second windows:
the first events up to the threshold are still emitted, then Ditto publishes a
`DISPATCH_BURST` summary whose `total_frames` is the full window count.
