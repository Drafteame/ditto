# Log event API

Ditto assigns each log event a process-local ID and RFC3339Nano timestamp before writing it to stdout or publishing it to SSE. Both outputs use the same event values.

`GET /__ditto__/api/logs` returns the retained summary list. Summaries keep identity, timing, request/socket context, sequence information, and payload metadata, but omit request/response bodies, headers, and base64 payload bytes.

`GET /__ditto__/api/logs/{id}` returns one retained detail, including legacy body fields and headers. It returns 404 after the event has expired or if the event was too large to retain.

Retention is limited to 5,000 events and 32 MiB of serialized event JSON. New events evict the oldest retained events to fit. An event larger than the byte limit is still written to stdout and sent to current SSE subscribers, but is not retained for later lookup.

The current summary endpoint returns the full retained summary list. Paging, replay, and gap reporting are planned for phase 5.

## HTTP request and response captures

HTTP `MOCK`, `PROXY`, and `MISS` details include the original URL, host, remote address, protocol, request and response headers, and a request/response payload metadata object. Request capture is method-agnostic, including bodies sent with `GET`, `OPTIONS`, `PATCH`, and other methods. The request body still goes to mock matching and the proxy as before; logging stores at most 1 MiB of raw bytes per payload. `raw_base64` is the captured wire-byte prefix, while the legacy body string is a UTF-8 text preview (gzip and deflate are decoded for the preview when supported). Binary payloads have a short legacy placeholder and can be downloaded from retained detail. `capture_status` distinguishes empty, not captured, captured, binary, truncated, and failed captures.

URL-encoded and multipart form fields are summarized with limits of 1 MiB of field text and 1,000 fields/parts. Uploaded files are not separately retained; their names, content types, and complete byte sizes are recorded as `metadata_only`. On request-body read errors, Ditto returns HTTP 400, logs the captured prefix and error, and does not match or proxy the partial request. Response capture wraps the existing writer and continues streaming the original bytes; it records the final status/header snapshot and forwards flush support to the underlying writer.

The UI loads the retained detail by event ID when opening the inspector. If retention has evicted it, the event summary remains visible and the drawer reports that detail has expired. `Save as mock` is limited to complete JSON responses supported by the current mock editor. It carries response headers that are useful for a mock and omits transport, content-encoding/length, and `Set-Cookie` headers; the request body is not added as a match condition automatically.
