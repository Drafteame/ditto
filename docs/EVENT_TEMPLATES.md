# Event Templates

Event templates are reusable WebSocket dispatch artifacts stored as JSON files
under Ditto's `event_templates/` data directory. They are designed to be copied
into `.dittopack` bundles as `event_templates/<id>.json`.

## File Shape

```json
{
  "version": 1,
  "id": "ticket-created-9f3c24c2",
  "name": "Ticket Created",
  "description": "Optional notes",
  "channel": "tickets/created",
  "adapter": "raw",
  "type_name": "ditto.events.TicketCreated",
  "payload": {
    "ticketId": "{{ticketId}}",
    "attempt": "{{int:attempt}}",
    "sentAt": "{{now}}"
  },
  "variables": [
    {
      "name": "ticketId",
      "description": "Ticket id from the app"
    },
    {
      "name": "attempt",
      "default": "1"
    }
  ],
  "created_at": "2026-05-07T00:00:00Z",
  "updated_at": "2026-05-07T00:00:00Z"
}
```

`version` is currently `1`. Missing versions are treated as version 1 when
loading older files.

## Resolver

Variables are resolved only in JSON values. Object keys are not templated.

Plain placeholders keep string semantics:

```json
{ "ticketId": "{{ticketId}}" }
```

With `ticketId = "123"`, the resolved value is `"123"`, not `123`. This keeps
protobuf and JSON-schema string fields stable.

Typed placeholders are explicit and must occupy the whole JSON string:

- `{{str:name}}`
- `{{int:name}}`
- `{{float:name}}`
- `{{bool:name}}`
- `{{json:name}}`

For example, `"{{json:metadata}}"` can resolve to an object when `metadata` is
passed as `{"score":7}`. Typed placeholders inside concatenated strings are
rejected at save time, so `"age {{int:age}}"` is invalid.

Unsupported casts, such as `{{date:dob}}`, are rejected. Invalid values at
dispatch time are returned as `invalid_casts`; variables with no user value and
no default are returned as `missing_variables`.

When a variable is sent as JSON `null`, plain placeholders keep string
semantics: `{{x}}` resolves to the literal string `"null"`. Use `{{json:x}}`
when the resolved payload needs a true JSON `null`.

Resolution is single-pass. If `a = "{{b}}"`, then `"{{a}}"` resolves to the
literal string `"{{b}}"`.

## Built-Ins

Built-ins are available without user input:

- `{{now}}`: RFC3339 UTC timestamp
- `{{now_unix}}`: Unix seconds
- `{{now_unix_ms}}`: Unix milliseconds
- `{{uuid}}`: random UUID

Built-ins are generated once per render. Multiple `{{uuid}}` placeholders in a
single dispatch share the same UUID. User variables override defaults and
built-ins; defaults override built-ins.

## Defaults

A variable entry without `default` means the value is required. A variable entry
with `"default": ""` has an explicit empty-string default.

## Channels

Channels are trimmed and must not contain newlines. Channel templating is not
supported in version 1; `{{...}}` in `channel` is rejected.

## IDs And Files

Template IDs are generated from the name as `slug-shortHash`, for example
`ticket-created-9f3c24c2`. IDs must be path-safe and may not contain `/`, `..`,
or path separators.

Ditto writes templates atomically through a temporary file and assumes a single
Ditto process owns a given data directory.
