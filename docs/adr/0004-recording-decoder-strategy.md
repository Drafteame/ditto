# ADR 4 — Recording Decoder Strategy

## Status

Accepted for M5

## Context

M5 records WebSocket traffic from live and mixed channels. Future milestones
will replay, edit, and package these recordings, so the capture format must
survive adapter changes and schema availability changes.

There are three decoder choices:

- infer the envelope from the adapter family;
- require adapter profiles to define inverse templates;
- store raw frames first and treat decoding as optional metadata.

## Decision

Use a raw-frame-first hybrid.

Every `RecordedFrame` stores the original WebSocket frame bytes as `raw_b64`.
M5 then attempts best-effort decoding based on `base_adapter`. For AppSync-like
profiles it looks for a `data` frame containing an alias and base64 payload,
uses the profile's inverse alias map, and asks the schema registry to decode
the protobuf bytes. For raw text frames it keeps valid JSON as decoded metadata.

Decoded output is optional. Failures leave `decoded` empty and set
`decode_error`; the raw frame remains replayable.

## Consequences

- M6 can replay original frames without needing schemas or profiles.
- M6/M9 can add explicit inverse templates without changing JSONL shape.
- Recordings remain readable and bundle-friendly for M8.
- Decoder quality can improve over time while old captures stay valid.
