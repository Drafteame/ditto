# ADR 1 — Channel Modes Via Dispatch Rendered

## Status

Proposed (will commit when M5 / M7 starts)

## Context

M5 introduces channel modes such as mock, live, record, and mixed. Sequence
runners should not need to know which transport mode a channel currently uses.
M7 scenarios will combine channel configuration with sequence playback, so this
boundary needs to stay stable.

## Decision

Keep mode decisions behind a `ChannelModeRegistry` consulted by
`dispatchRendered`. Sequence runners and templates continue to resolve payloads
and call the same WebSocket exit point. Live, record, and mixed behavior hangs
off that single dispatch boundary.

## Consequences

- Sequence playback remains agnostic to transport mode.
- Scenarios can activate channel modes without changing runner semantics.
- `dispatchRendered` becomes the policy boundary for socket delivery.
