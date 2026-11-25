// Package eventlog records what Ditto serves: every HTTP request, socket
// frame, mode change and recording event becomes an Event. The Bus retains
// a bounded history with resumable cursors and fans summaries out to
// dashboard subscribers; a Printer writes the --log-format request log.
package eventlog

// Event is one log entry. HTTP requests, socket frames and dispatches,
// mode changes and recording events share it; fields that do not apply
// stay empty. Its JSON form is the dashboard and --log-format json contract.
type Event struct {
	ID                    string              `json:"id"`
	Timestamp             string              `json:"timestamp"`
	Type                  string              `json:"type"` // MOCK, PROXY, MISS, SOCKET, MODE, RECORD
	Method                string              `json:"method"`
	Path                  string              `json:"path"`
	Status                int                 `json:"status"`
	DurationMs            int64               `json:"duration_ms"`
	Cursor                string              `json:"cursor,omitempty"`
	NotRetained           bool                `json:"not_retained,omitempty"`
	StreamGap             bool                `json:"stream_gap,omitempty"`
	GapReason             string              `json:"gap_reason,omitempty"`
	GapFromCursor         string              `json:"gap_from_cursor,omitempty"`
	GapToCursor           string              `json:"gap_to_cursor,omitempty"`
	ResponseBody          string              `json:"response_body,omitempty"`
	RequestBody           string              `json:"request_body,omitempty"`
	RequestPayload        *Payload            `json:"request_payload,omitempty"`
	ResponsePayload       *Payload            `json:"response_payload,omitempty"`
	Source                string              `json:"source,omitempty"`
	Error                 string              `json:"error,omitempty"`
	RequestHeaders        map[string][]string `json:"request_headers,omitempty"`
	ResponseHeaders       map[string][]string `json:"response_headers,omitempty"`
	RequestFormFields     map[string][]string `json:"request_form_fields,omitempty"`
	RequestFiles          []FileMeta          `json:"request_files,omitempty"`
	RequestFormsTruncated bool                `json:"request_forms_truncated,omitempty"`
	URL                   string              `json:"url,omitempty"`
	Host                  string              `json:"host,omitempty"`
	RemoteAddr            string              `json:"remote_addr,omitempty"`
	Protocol              string              `json:"protocol,omitempty"`
	Direction             string              `json:"direction,omitempty"`
	ConnectionID          string              `json:"connection_id,omitempty"`
	ClientID              string              `json:"client_id,omitempty"`
	SubscriptionID        string              `json:"subscription_id,omitempty"`
	Channel               string              `json:"channel,omitempty"`
	DispatchID            string              `json:"dispatch_id,omitempty"`
	DeliveryState         string              `json:"delivery_state,omitempty"`
	FrameKind             string              `json:"frame_kind,omitempty"`
	ControlType           string              `json:"control_type,omitempty"`
	TypeName              string              `json:"type_name,omitempty"`
	Alias                 string              `json:"alias,omitempty"`
	DecodeError           string              `json:"decode_error,omitempty"`
	DecodedPayload        string              `json:"decoded_payload,omitempty"`
	DecodedTruncated      bool                `json:"decoded_truncated,omitempty"`
	CloseCode             int                 `json:"close_code,omitempty"`
	CloseReason           string              `json:"close_reason,omitempty"`
	Queued                int                 `json:"queued,omitempty"`
	Dropped               int                 `json:"dropped,omitempty"`
	Errors                int                 `json:"errors,omitempty"`
	Written               int                 `json:"written,omitempty"`
	BurstID               string              `json:"burst_id,omitempty"`
	BurstMethod           string              `json:"burst_method,omitempty"`
	BurstCount            int                 `json:"burst_count,omitempty"`
	BurstDirection        string              `json:"burst_direction,omitempty"`
	BurstSource           string              `json:"burst_source,omitempty"`
	BurstStartCursor      string              `json:"burst_start_cursor,omitempty"`
	BurstEndCursor        string              `json:"burst_end_cursor,omitempty"`
	BurstWindowMs         int64               `json:"burst_window_ms,omitempty"`
	Adapter               string              `json:"adapter,omitempty"`
	Subprotocol           string              `json:"subprotocol,omitempty"`
	Mode                  string              `json:"mode,omitempty"`
	Target                string              `json:"target,omitempty"`
	MockID                string              `json:"mock_id,omitempty"`       // Source of the mock that served a MOCK event
	SequenceStep          int                 `json:"sequence_step,omitempty"` // 1-based; 0 for non-sequence or reset-fallback
	SequenceLen           int                 `json:"sequence_len,omitempty"`
}

// Payload describes a captured payload without requiring consumers
// to inspect or decode its legacy string field.
type Payload struct {
	SizeBytes     int64  `json:"size_bytes"`
	CapturedBytes int64  `json:"captured_bytes"`
	ContentType   string `json:"content_type,omitempty"`
	Encoding      string `json:"encoding,omitempty"`
	CaptureStatus string `json:"capture_status"` // empty, not_captured, captured, binary, truncated, error, unavailable, metadata_only, or omitted
	RawBase64     string `json:"raw_base64,omitempty"`
	Error         string `json:"error,omitempty"`
}

// FileMeta describes an uploaded multipart file; its bytes are not captured.
type FileMeta struct {
	Name          string `json:"name"`
	ContentType   string `json:"content_type,omitempty"`
	SizeBytes     int64  `json:"size_bytes"`
	CapturedBytes int64  `json:"captured_bytes"`
	CaptureStatus string `json:"capture_status"`
}

// Retention bounds for a Bus: the oldest events are evicted past either.
const (
	MaxRetainedEvents = 5000
	MaxRetainedBytes  = 32 << 20
)

func cloneEvent(event Event) Event {
	cloneHeaders := func(src map[string][]string) map[string][]string {
		if src == nil {
			return nil
		}
		dst := make(map[string][]string, len(src))
		for key, values := range src {
			dst[key] = append([]string(nil), values...)
		}
		return dst
	}
	event.RequestHeaders = cloneHeaders(event.RequestHeaders)
	event.ResponseHeaders = cloneHeaders(event.ResponseHeaders)
	event.RequestFormFields = cloneHeaders(event.RequestFormFields)
	event.RequestFiles = append([]FileMeta(nil), event.RequestFiles...)
	if event.RequestPayload != nil {
		value := *event.RequestPayload
		event.RequestPayload = &value
	}
	if event.ResponsePayload != nil {
		value := *event.ResponsePayload
		event.ResponsePayload = &value
	}
	return event
}

func summaryEvent(event Event) Event {
	event.RequestBody, event.ResponseBody, event.DecodedPayload = "", "", ""
	event.RequestHeaders, event.ResponseHeaders = nil, nil
	event.RequestFormFields, event.RequestFiles, event.RequestFormsTruncated = nil, nil, false
	if event.RequestPayload != nil {
		meta := *event.RequestPayload
		meta.RawBase64 = ""
		event.RequestPayload = &meta
	}
	if event.ResponsePayload != nil {
		meta := *event.ResponsePayload
		meta.RawBase64 = ""
		event.ResponsePayload = &meta
	}
	return event
}
