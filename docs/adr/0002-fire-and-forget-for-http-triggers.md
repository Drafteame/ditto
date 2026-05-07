# ADR 2 — Fire And Forget For HTTP Triggers

## Status

Proposed (will commit when M7 starts)

## Context

M7 scenarios add HTTP triggers that fire sequences when matching requests arrive.
The UI already has a tracked `SequencePlayer.Play` runner per sequence ID.
Concurrent HTTP requests must be able to launch independent snapshots without
colliding with that UI-owned runner.

## Decision

HTTP-triggered runs should use fire-and-forget sequence execution. Each trigger
hit gets an independent snapshot of the referenced sequence. UI transport
controls continue to operate on the tracked player runner and do not own
triggered background runs.

## Consequences

- Concurrent trigger hits can run the same sequence safely.
- UI playback and scenario automation stay separate.
- Triggered run observability will need separate event/log attribution.
