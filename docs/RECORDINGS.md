# Recordings

Recordings live under `recordings/<recording-id>/` and are shaped so future
`.dittopack` export can include the directory as-is.

```text
recordings/
  qa-session-9f3c24c2/
    manifest.json
    games-123-a1b2c3d4.jsonl
```

## Manifest

`manifest.json` uses version `1`.

```json
{
  "version": 1,
  "id": "qa-session-9f3c24c2",
  "name": "QA Session",
  "description": "",
  "started_at": "2026-05-07T12:00:00Z",
  "stopped_at": null,
  "channels": [
    {
      "channel": "/games/123",
      "events": 142,
      "dropped": 0,
      "rate_cap_hz": 50,
      "adapter_profile": "appsync-profile",
      "profile_changes": []
    }
  ],
  "adapter_profile": "",
  "schema_pack_ids": ["pack-id-1"]
}
```

`stopped_at` is `null` while active. If Ditto restarts during a recording, the
manifest is marked stopped with `error: "interrupted"` on next startup.

## Frames

Each channel file is JSONL. One line is one `RecordedFrame`.

```json
{
  "ts_ms": 1234,
  "direction": "upstream",
  "channel": "/games/123",
  "frame_kind": "text",
  "raw_b64": "eyJ0eXBlIjoiZGF0YSJ9",
  "decoded": {
    "type_name": "example.Event",
    "payload_json": {"id": "evt-1"},
    "alias": "event"
  },
  "decode_error": ""
}
```

`raw_b64` is always present and contains the complete original WebSocket frame.
`decoded` is optional best-effort metadata; `decode_error` explains why it is
missing. This keeps M6 free to replay original frames or build editable events
from decoded payloads when schemas are available.
