# Event Sequences

Event sequences are timed WebSocket timelines stored as JSON files under
Ditto's `sequences/` data directory. They are designed to be copied into
`.dittopack` bundles as `sequences/<id>.json`.

## File Shape

```json
{
  "version": 1,
  "id": "ticket-flow-45b1ad22",
  "name": "Ticket Flow",
  "description": "Optional notes",
  "vars": {
    "ticketId": "T-123"
  },
  "on_end": "stay",
  "steps": [
    {
      "id": "step-9f3c24c2",
      "name": "Ticket created",
      "delay_ms": 250,
      "template_ref": "ticket-created-9f3c24c2",
      "vars_override": {
        "attempt": "1"
      }
    },
    {
      "id": "step-a71b03de",
      "name": "Inline settlement",
      "delay_ms": 1000,
      "channel": "tickets/settled",
      "adapter": "raw",
      "type_name": "ditto.events.TicketSettled",
      "payload": {
        "ticketId": "{{ticketId}}",
        "settledAt": "{{now}}"
      }
    }
  ],
  "created_at": "2026-05-07T00:00:00Z",
  "updated_at": "2026-05-07T00:00:00Z"
}
```

`version` is currently `1`. Missing versions are treated as version 1 when
loading older files.

## Steps

Each step either references an event template or carries an inline dispatch.

When `template_ref` is empty, `channel` and `payload` are required. When
`template_ref` is set, the referenced template supplies channel, adapter,
payload, type, and variable defaults. Non-empty fields on the step override the
template for that step.

`delay_ms` is the wait before the step dispatches, including the first step.
This keeps every step symmetric: a timeline always says how long to wait before
the next event leaves Ditto.

Delays must be non-negative and are capped at 24 hours.

## Variables

Sequence and step variables are JSON values on disk, so strings, numbers,
booleans, objects, arrays, and null are accepted. They are converted through the
same rules as template dispatch variables before rendering.

Variables resolve with this precedence, from strongest to weakest:

- runtime vars sent to `/play`
- `step.vars_override`
- `sequence.vars`
- template variable defaults
- built-ins such as `{{now}}`, `{{now_unix_ms}}`, and `{{uuid}}`

Built-ins are regenerated for each step. A sequence with three `{{uuid}}`
placeholders in three different steps dispatches three different UUIDs. Use a
sequence variable such as `runId` when the same value must be shared across
steps.

Inline payloads use the same resolver and typed casts as event templates:
`{{int:x}}`, `{{float:x}}`, `{{bool:x}}`, and `{{json:x}}` must occupy the whole
JSON string.

## End Behavior

`on_end` controls the cursor after the last step:

- `stay`: status becomes `completed`, cursor stays on the last step.
- `reset`: status becomes `completed`, cursor returns to step 0.
- `loop`: status remains `playing`, cursor returns to step 0 and waits for
  step 0's delay again.

Missing `on_end` defaults to `stay`.

The SSE stream emits `completed` only for terminal completion. Looping
sequences emit `looped` at the wrap boundary while the state remains
`playing`.

## Transport Controls

The player is owned by the backend. The UI reflects state over SSE and sends
transport commands:

- `play`: starts a snapshot of the sequence, resumes when paused, and is
  idempotent while already playing.
- `pause`: preserves the remaining delay for the pending step.
- `stop`: cancels any pending delay and does not dispatch the pending step.
- `seek`: moves the cursor. While playing, the destination step's delay is
  armed again. While paused, only the cursor changes.
- `speed`: accepts floats. `1` is real time, `2` is twice as fast, and `0`
  means Max mode.

When `/play` omits `speed`, Ditto starts at `1x`. Sending `"speed": 0`
explicitly starts in Max mode.

Max mode (`speed = 0`) skips all waits and dispatches steps back-to-back.

## Schemas

If a resolved step has `type_name`, Ditto validates that the schema type is
loaded and pre-encodes the rendered payload before dispatch. The pre-encoded
payload is passed through `dispatchRendered`, so sequences use the same socket
exit point as manual dispatch and templates.

## Runtime Snapshots

The player takes a snapshot of the sequence when playback starts. Edits to the
sequence file are visible on the next play, not the current run.

Ditto validates sequences before they enter the registry and again before play.
Sequences with zero steps are rejected instead of creating an idle runner.

If a referenced template is deleted during playback, or dispatch fails, the
player emits an error event and aborts the run. Per-step error policy is left
for a later milestone.

## IDs And Files

Sequence IDs are generated from the name as `slug-shortHash`, for example
`ticket-flow-45b1ad22`. Step IDs are stable `step-shortHash` values and are
preserved on update when sent back by the UI.

IDs must be path-safe and may not contain `/`, `..`, or path separators.

Ditto writes sequences atomically through a temporary file and assumes a single
Ditto process owns a given data directory.

## Editor Notes

The sequence editor currently uses the same plain JSON textareas as the
template editor. A shared schema-aware JSON editor for dispatcher, templates,
and sequences is planned as follow-up UI debt.
