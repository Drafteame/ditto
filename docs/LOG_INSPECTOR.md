# Log event API

Ditto assigns each log event a process-local ID and RFC3339Nano timestamp before writing it to stdout or publishing it to SSE. Both outputs use the same event values.

`GET /__ditto__/api/logs` returns the retained summary list. Summaries keep identity, timing, request/socket context, sequence information, and payload metadata, but omit request/response bodies, headers, and base64 payload bytes.

`GET /__ditto__/api/logs/{id}` returns one retained detail, including legacy body fields and headers. It returns 404 after the event has expired or if the event was too large to retain.

Retention is limited to 5,000 events and 32 MiB of serialized event JSON. New events evict the oldest retained events to fit. An event larger than the byte limit is still written to stdout and sent to current SSE subscribers, but is not retained for later lookup.

The current summary endpoint returns the full retained summary list. Paging, replay, and gap reporting are planned for phase 5.
