# ADR 3 — Bundle ID Stability Policy

## Status

Proposed (will commit when M8 starts)

## Context

M8 collections need artifact references that survive renames and imports. Current
template and sequence IDs are generated as `slug-shortHash`, which is readable
but couples identity to naming policy. Bundle imports also need predictable
conflict handling.

## Decision

M8 should define stable artifact identity before implementing import/export. The
preferred direction is immutable IDs with separate display slugs; otherwise M8
must define an explicit migration and conflict policy for renamed artifacts.
Collections should reference stable identities, not mutable names.

## Consequences

- Renames can avoid breaking scenario and bundle references.
- Import conflict UI can compare stable identities first.
- Existing slug IDs may need a migration or compatibility layer.
