package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed all:frontend/dist
var webFS embed.FS

// LogEvent represents a single request passing through Ditto.
type LogEvent struct {
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
	RequestPayload        *LogPayloadMetadata `json:"request_payload,omitempty"`
	ResponsePayload       *LogPayloadMetadata `json:"response_payload,omitempty"`
	Source                string              `json:"source,omitempty"`
	Error                 string              `json:"error,omitempty"`
	RequestHeaders        map[string][]string `json:"request_headers,omitempty"`
	ResponseHeaders       map[string][]string `json:"response_headers,omitempty"`
	RequestFormFields     map[string][]string `json:"request_form_fields,omitempty"`
	RequestFiles          []LogFileMetadata   `json:"request_files,omitempty"`
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
	MockIndex             int                 `json:"mock_index"`              // index into mocks list; valid when Type == "MOCK"
	SequenceStep          int                 `json:"sequence_step,omitempty"` // 1-based; 0 for non-sequence or reset-fallback
	SequenceLen           int                 `json:"sequence_len,omitempty"`
}

// LogPayloadMetadata describes a captured payload without requiring consumers
// to inspect or decode its legacy string field.
type LogPayloadMetadata struct {
	SizeBytes     int64  `json:"size_bytes"`
	CapturedBytes int64  `json:"captured_bytes"`
	ContentType   string `json:"content_type,omitempty"`
	Encoding      string `json:"encoding,omitempty"`
	CaptureStatus string `json:"capture_status"` // empty, not_captured, captured, binary, truncated, error, unavailable, metadata_only, or omitted
	RawBase64     string `json:"raw_base64,omitempty"`
	Error         string `json:"error,omitempty"`
}

type LogFileMetadata struct {
	Name          string `json:"name"`
	ContentType   string `json:"content_type,omitempty"`
	SizeBytes     int64  `json:"size_bytes"`
	CapturedBytes int64  `json:"captured_bytes"`
	CaptureStatus string `json:"capture_status"`
}

const (
	MaxRetainedLogEvents = 5000
	MaxRetainedLogBytes  = 32 << 20
)

var nextLogEventID atomic.Uint64

// prepareLogEvent is only used when no EventBus is configured.
func prepareLogEvent(event LogEvent) LogEvent {
	if event.ID == "" {
		event.ID = fmt.Sprintf("log-local-%d", nextLogEventID.Add(1))
	}
	if _, err := time.Parse(time.RFC3339Nano, event.Timestamp); err != nil {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return event
}

func cloneLogEvent(event LogEvent) LogEvent {
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
	event.RequestFiles = append([]LogFileMetadata(nil), event.RequestFiles...)
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

func summaryLogEvent(event LogEvent) LogEvent {
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

type retainedLogEvent struct {
	event LogEvent
	size  int
	seq   uint64
}

type LogHistoryGap struct {
	Reason string `json:"reason"`
	From   string `json:"from_cursor,omitempty"`
	To     string `json:"to_cursor,omitempty"`
}

type LogHistory struct {
	Events       []LogEvent     `json:"events"`
	OldestCursor string         `json:"oldest_cursor,omitempty"`
	LatestCursor string         `json:"latest_cursor,omitempty"`
	NextCursor   string         `json:"next_cursor,omitempty"`
	HasMore      bool           `json:"has_more"`
	Total        int            `json:"total"`
	Expected     int            `json:"expected,omitempty"`
	Complete     bool           `json:"complete"`
	Gap          *LogHistoryGap `json:"gap,omitempty"`
}

// EventBus retains full events and sends lightweight summaries to SSE clients.
type EventBus struct {
	mu             sync.Mutex
	clients        map[chan LogEvent]struct{}
	summaryClients map[chan LogEvent]struct{}
	retained       []retainedLogEvent
	retainedBytes  int
	session        string
	nextSeq        uint64
}

func NewEventBus() *EventBus {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		copy(random[:], []byte(fmt.Sprintf("%012x", time.Now().UnixNano())))
	}
	return &EventBus{clients: make(map[chan LogEvent]struct{}), summaryClients: make(map[chan LogEvent]struct{}), session: hex.EncodeToString(random[:])}
}

func (b *EventBus) Subscribe() chan LogEvent {
	ch := make(chan LogEvent, 64)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe stops broadcasting to ch. The bus never closes subscriber
// channels; owning goroutines should exit on their own signal.
func (b *EventBus) Unsubscribe(ch chan LogEvent) {
	b.mu.Lock()
	delete(b.clients, ch)
	delete(b.summaryClients, ch)
	b.mu.Unlock()
}

func (b *EventBus) Publish(event LogEvent) {
	b.publishWithSummary(event, true)
}

func (b *EventBus) publishWithSummary(event LogEvent, send bool) LogEvent {
	b.mu.Lock()
	b.nextSeq++
	seq := b.nextSeq
	if _, err := time.Parse(time.RFC3339Nano, event.Timestamp); err != nil {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	event.Cursor = b.cursor(seq)
	if event.ID == "" {
		event.ID = fmt.Sprintf("log-%s-%d", b.session, seq)
	}
	stored := cloneLogEvent(event)
	encoded, _ := json.Marshal(stored)
	if len(encoded) > MaxRetainedLogBytes {
		event.NotRetained = true
		stored.NotRetained = true
		encoded, _ = json.Marshal(stored)
	}
	if len(encoded) <= MaxRetainedLogBytes {
		for len(b.retained) > 0 && (len(b.retained) >= MaxRetainedLogEvents || b.retainedBytes+len(encoded) > MaxRetainedLogBytes) {
			b.retainedBytes -= b.retained[0].size
			b.retained[0] = retainedLogEvent{}
			b.retained = b.retained[1:]
		}
		b.retained = append(b.retained, retainedLogEvent{event: stored, size: len(encoded), seq: seq})
		b.retainedBytes += len(encoded)
	}
	if send {
		b.broadcastLocked(summaryLogEvent(event), event)
	}
	b.mu.Unlock()
	return event
}

func (b *EventBus) cursor(seq uint64) string {
	return fmt.Sprintf("%s:%d", b.session, seq)
}

func parseLogCursor(cursor string) (string, uint64, bool) {
	index := strings.LastIndexByte(cursor, ':')
	if index < 1 {
		return "", 0, false
	}
	seq, err := strconv.ParseUint(cursor[index+1:], 10, 64)
	return cursor[:index], seq, err == nil
}

func (b *EventBus) broadcastLocked(summary, detail LogEvent) {
	for ch := range b.clients {
		event := detail
		if _, lightweight := b.summaryClients[ch]; lightweight {
			event = summary
		}
		select {
		case ch <- cloneLogEvent(event):
		default:
			from := event.Cursor
			for {
				select {
				case queued := <-ch:
					if queued.Cursor != "" {
						if from == event.Cursor {
							from = queued.Cursor
						}
					}
				default:
					goto drained
				}
			}
		drained:
			gap := LogEvent{Type: "GAP", Method: "GAP", Path: "", ID: "gap-" + event.ID,
				StreamGap: true, GapReason: "slow_client", GapFromCursor: from, GapToCursor: event.Cursor}
			ch <- gap
			ch <- cloneLogEvent(event)
		}
	}
}

func (b *EventBus) SubscribeAfter(cursor string) (chan LogEvent, []LogEvent, *LogHistoryGap) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan LogEvent, 64)
	b.clients[ch] = struct{}{}
	b.summaryClients[ch] = struct{}{}
	history := b.historyLocked(cursor, "", "", "", "", "", "", "", 0, MaxRetainedLogEvents, 0)
	return ch, history.Events, history.Gap
}

func (b *EventBus) LogSummaries() []LogEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	items := make([]LogEvent, len(b.retained))
	for i, entry := range b.retained {
		items[i] = summaryLogEvent(entry.event)
	}
	return items
}

func (b *EventBus) History(cursor, from, to, channel, method, direction, source, dispatchID string, offset, limit, expected int) LogHistory {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.historyLocked(cursor, from, to, channel, method, direction, source, dispatchID, offset, limit, expected)
}

func (b *EventBus) historyLocked(cursor, from, to, channel, method, direction, source, dispatchID string, offset, limit, expected int) LogHistory {
	result := LogHistory{Events: []LogEvent{}, Complete: true, Expected: expected}
	if limit <= 0 {
		limit = 500
	}
	if limit > MaxRetainedLogEvents {
		limit = MaxRetainedLogEvents
	}
	if offset < 0 {
		offset = 0
	}
	var sinceSession string
	var sinceSeq uint64
	if cursor != "" {
		var valid bool
		sinceSession, sinceSeq, valid = parseLogCursor(cursor)
		if !valid || sinceSession != b.session {
			result.Gap = &LogHistoryGap{Reason: "server_reset", From: cursor, To: b.latestCursorLocked()}
			sinceSeq = 0
		} else if sinceSeq > b.nextSeq {
			result.Gap = &LogHistoryGap{Reason: "cursor_ahead", From: cursor, To: b.latestCursorLocked()}
			sinceSeq = 0
		}
	}
	if len(b.retained) > 0 {
		result.OldestCursor = b.retained[0].event.Cursor
	}
	result.LatestCursor = b.latestCursorLocked()
	if len(b.retained) == 0 && b.nextSeq > 0 && from == "" && to == "" &&
		(cursor != "" || channel == "" && method == "" && direction == "" && source == "" && dispatchID == "") {
		if result.Gap == nil {
			result.Gap = &LogHistoryGap{Reason: "retention_empty", To: result.LatestCursor}
		}
	} else if len(b.retained) > 0 && from == "" && to == "" {
		oldest := b.retained[0].seq
		if cursor == "" && oldest > 1 && channel == "" && method == "" && direction == "" && source == "" && dispatchID == "" && result.Gap == nil {
			result.Gap = &LogHistoryGap{Reason: "retention_window", To: b.cursor(oldest - 1)}
		} else if cursor != "" && sinceSession == b.session && sinceSeq+1 < oldest && result.Gap == nil {
			result.Gap = &LogHistoryGap{Reason: "evicted", From: cursor, To: b.cursor(oldest - 1)}
		}
	}
	var fromSeq, toSeq uint64
	if from != "" {
		session, seq, valid := parseLogCursor(from)
		if !valid || session != b.session {
			result.Gap = &LogHistoryGap{Reason: "server_reset", From: from, To: result.LatestCursor}
		} else {
			fromSeq = seq
		}
	}
	if to != "" {
		session, seq, valid := parseLogCursor(to)
		if !valid || session != b.session {
			result.Gap = &LogHistoryGap{Reason: "server_reset", From: to, To: result.LatestCursor}
		} else {
			toSeq = seq
		}
	}
	// A missing sequence belongs to the complete cursor stream, even if the
	// caller later filters the events to a channel or method.
	if from == "" && to == "" && result.Gap == nil &&
		(cursor != "" && sinceSession == b.session || cursor == "" && channel == "" && method == "" && direction == "" && source == "" && dispatchID == "") {
		last := sinceSeq
		for _, retained := range b.retained {
			if retained.seq <= sinceSeq {
				continue
			}
			if last > 0 && retained.seq > last+1 {
				result.Gap = &LogHistoryGap{Reason: "event_not_retained", From: b.cursor(last + 1), To: b.cursor(retained.seq - 1)}
				break
			}
			last = retained.seq
		}
		if result.Gap == nil && last > 0 && b.nextSeq > last {
			result.Gap = &LogHistoryGap{Reason: "event_not_retained", From: b.cursor(last + 1), To: b.cursor(b.nextSeq)}
		}
	}
	filtered := make([]LogEvent, 0)
	for _, retained := range b.retained {
		if cursor != "" && retained.seq <= sinceSeq {
			continue
		}
		if fromSeq > 0 && retained.seq < fromSeq {
			continue
		}
		if toSeq > 0 && retained.seq > toSeq {
			continue
		}
		if channel != "" && retained.event.Channel != channel && retained.event.Path != channel {
			continue
		}
		if method != "" && retained.event.Method != method {
			continue
		}
		if direction != "" && retained.event.Direction != direction && retained.event.BurstDirection != direction {
			continue
		}
		if source != "" && retained.event.Source != source && retained.event.BurstSource != source {
			continue
		}
		if dispatchID != "" && retained.event.DispatchID != dispatchID {
			continue
		}
		filtered = append(filtered, summaryLogEvent(retained.event))
	}
	result.Total = len(filtered)
	if expected > 0 && result.Total < expected {
		if result.Gap == nil {
			result.Gap = &LogHistoryGap{Reason: "members_expired", From: from, To: to}
		}
	}
	result.Complete = result.Gap == nil
	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	result.Events = filtered[offset:end]
	result.HasMore = end < len(filtered)
	if end > 0 {
		result.NextCursor = filtered[end-1].Cursor
	}
	return result
}

func (b *EventBus) latestCursorLocked() string {
	if b.nextSeq == 0 {
		return ""
	}
	return b.cursor(b.nextSeq)
}

func (b *EventBus) LogDetail(id string) (LogEvent, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.retained) - 1; i >= 0; i-- {
		if b.retained[i].event.ID == id {
			return cloneLogEvent(b.retained[i].event), true
		}
	}
	return LogEvent{}, false
}

// publishLogEvent assigns identity once, then shares that event with stdout and
// the retained/SSE bus. The event bus preserves IDs already assigned here.
func publishLogEvent(jsonMode bool, bus *EventBus, event LogEvent) {
	if bus != nil {
		event = bus.publishWithSummary(event, true)
	} else {
		event = prepareLogEvent(event)
	}
	logRequest(jsonMode, event)
}

// ServerInfo holds metadata shown in the UI footer and connect panel.
type ServerInfo struct {
	Port       int      `json:"port"`
	Target     string   `json:"target"`
	LiveTarget string   `json:"live_target,omitempty"`
	HTTPS      bool     `json:"https"`
	MocksDir   string   `json:"mocks_dir"`
	LocalIPs   []string `json:"local_ips"`
	Version    string   `json:"version"`
}

// RegisterUI sets up the dashboard routes on the given mux.
// If serveUI is true, the embedded static files are served; otherwise only the API is available.
func RegisterUI(mux *http.ServeMux, store *MockStore, bus *EventBus, proxyMgr *ProxyManager, liveTarget func() string, info ServerInfo, serveUI bool) {
	// Serve embedded static files at /__ditto__/ (only when UI is enabled)
	if serveUI {
		webContent, _ := fs.Sub(webFS, "frontend/dist")
		fileServer := http.FileServer(http.FS(webContent))
		mux.Handle("/__ditto__/", http.StripPrefix("/__ditto__/", fileServer))
	}

	// SSE endpoint
	mux.HandleFunc("/__ditto__/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("X-Accel-Buffering", "no")
		since := r.URL.Query().Get("since")
		if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
			since = lastEventID
		}
		ch, replay, gap := bus.SubscribeAfter(since)
		defer bus.Unsubscribe(ch)
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		if gap != nil {
			marker := LogEvent{Type: "GAP", Method: "GAP", StreamGap: true, GapReason: gap.Reason,
				GapFromCursor: gap.From, GapToCursor: gap.To}
			if writeSSELogEvent(w, flusher, marker) != nil {
				return
			}
		}
		for _, event := range replay {
			if writeSSELogEvent(w, flusher, event) != nil || r.Context().Err() != nil {
				return
			}
		}

		ctx := r.Context()
		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			case event := <-ch:
				if writeSSELogEvent(w, flusher, event) != nil {
					return
				}
			}
		}
	})

	// GET log summaries and an individual retained event. Summary rows omit
	// body strings; details remain available while the bounded event is retained.
	mux.HandleFunc("/__ditto__/api/logs/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := r.URL.Query()
		offset, _ := strconv.Atoi(query.Get("offset"))
		limit, _ := strconv.Atoi(query.Get("limit"))
		expected, _ := strconv.Atoi(query.Get("expected"))
		history := bus.History(query.Get("since"), query.Get("from"), query.Get("to"), query.Get("channel"), query.Get("method"), query.Get("direction"), query.Get("source"), query.Get("dispatch_id"), offset, limit, expected)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(history)
	})
	mux.HandleFunc("/__ditto__/api/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(bus.LogSummaries())
	})
	mux.HandleFunc("/__ditto__/api/logs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/__ditto__/api/logs/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		event, ok := bus.LogDetail(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(event)
	})

	// GET mocks list
	mux.HandleFunc("/__ditto__/api/mocks", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Derive actual port from the request (handles port changes at runtime)
			actualPort := info.Port
			if host := r.Host; host != "" {
				if _, portStr, err := net.SplitHostPort(host); err == nil {
					if p, err := strconv.Atoi(portStr); err == nil {
						actualPort = p
					}
				}
			}
			currentLiveTarget := info.LiveTarget
			if liveTarget != nil {
				currentLiveTarget = liveTarget()
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"mocks": store.All(),
				"info": ServerInfo{
					Port:       actualPort,
					Target:     proxyMgr.Target(),
					LiveTarget: currentLiveTarget,
					HTTPS:      info.HTTPS,
					MocksDir:   info.MocksDir,
					LocalIPs:   info.LocalIPs,
					Version:    info.Version,
				},
			})

		case http.MethodPost:
			// Create a new mock
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			var mock Mock
			if err := json.Unmarshal(body, &mock); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			disabled, err := store.Create(mock)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"disabled_duplicates": disabled})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Mock operations: toggle, reload, update, delete
	mux.HandleFunc("/__ditto__/api/mocks/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/__ditto__/api/mocks/")
		parts := strings.Split(path, "/")

		// POST /__ditto__/api/mocks/reload
		if r.Method == http.MethodPost && len(parts) == 1 && parts[0] == "reload" {
			if err := store.Load(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		// Routes that need an index: /{index}/toggle, /{index}, etc.
		if len(parts) < 1 {
			http.NotFound(w, r)
			return
		}

		index, err := strconv.Atoi(parts[0])
		if err != nil {
			http.NotFound(w, r)
			return
		}

		// POST /__ditto__/api/mocks/{index}/toggle
		if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "toggle" {
			ok, disabled := store.Toggle(index)
			if !ok {
				http.Error(w, "mock not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"disabled_duplicates": disabled})
			return
		}

		// POST /__ditto__/api/mocks/{index}/sequence/reset
		if r.Method == http.MethodPost && len(parts) == 3 && parts[1] == "sequence" && parts[2] == "reset" {
			if ok := store.ResetSequence(index); !ok {
				http.Error(w, "mock not found or not a sequence", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		// PUT /__ditto__/api/mocks/{index}
		if r.Method == http.MethodPut && len(parts) == 1 {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			var mock Mock
			if err := json.Unmarshal(body, &mock); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			disabled, err := store.Update(index, mock)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"disabled_duplicates": disabled})
			return
		}

		// DELETE /__ditto__/api/mocks/{index}
		if r.Method == http.MethodDelete && len(parts) == 1 {
			if err := store.Delete(index); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		http.NotFound(w, r)
	})

	// Target URL management
	mux.HandleFunc("/__ditto__/api/target", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"target": proxyMgr.Target(),
			})

		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			var req struct {
				Target string `json:"target"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if req.Target == "" {
				http.Error(w, "target URL is required", http.StatusBadRequest)
				return
			}
			if err := proxyMgr.SetTarget(req.Target); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Update check endpoint
	mux.HandleFunc("/__ditto__/api/update-check", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		latest, downloadURL, err := checkForUpdate()
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{
				"current":   version,
				"latest":    "",
				"available": false,
				"error":     err.Error(),
			})
			return
		}
		available := latest != "" && version != "dev" && isNewerVersion(latest, version)
		json.NewEncoder(w).Encode(map[string]any{
			"current":      version,
			"latest":       latest,
			"available":    available,
			"download_url": downloadURL,
		})
	})

	// QR code endpoint — returns a PNG image
	mux.HandleFunc("/__ditto__/api/qr", func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if info.HTTPS {
			scheme = "https"
		}
		// Use the first local IP for the physical device URL
		ip := "localhost"
		if len(info.LocalIPs) > 0 {
			ip = info.LocalIPs[0]
		}
		dashURL := fmt.Sprintf("%s://%s:%d/__ditto__/", scheme, ip, info.Port)

		png, err := qrcode.Encode(dashURL, qrcode.Medium, 256)
		if err != nil {
			http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Ditto-QR-URL", dashURL)
		w.Write(png)
	})

	// Open in browser endpoint
	mux.HandleFunc("/__ditto__/api/open-browser", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		scheme := "http"
		if info.HTTPS {
			scheme = "https"
		}
		dashURL := fmt.Sprintf("%s://localhost:%d/__ditto__/", scheme, info.Port)
		openBrowser(dashURL)
		w.WriteHeader(http.StatusOK)
	})

	// Open an arbitrary URL in the system browser (used by the desktop app
	// where target="_blank" doesn't escape the Wails webview).
	mux.HandleFunc("/__ditto__/api/open-url", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil || req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		// Only allow https URLs for safety
		if !strings.HasPrefix(req.URL, "https://") && !strings.HasPrefix(req.URL, "http://") {
			http.Error(w, "url must start with http:// or https://", http.StatusBadRequest)
			return
		}
		openBrowser(req.URL)
		w.WriteHeader(http.StatusOK)
	})
}

func writeSSELogEvent(w http.ResponseWriter, flusher http.Flusher, event LogEvent) error {
	data, _ := json.Marshal(event)
	if event.Cursor != "" && !event.StreamGap {
		if _, err := fmt.Fprintf(w, "id: %s\n", event.Cursor); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// RegisterPortRoutes adds port management and config persistence endpoints.
// Called after the server is created so the server reference is available.
func RegisterPortRoutes(mux *http.ServeMux, srv *Server, proxyMgr *ProxyManager, cfgStore *ConfigStore) {
	// Port check — probe if a port is available
	mux.HandleFunc("/__ditto__/api/port/check", func(w http.ResponseWriter, r *http.Request) {
		portStr := r.URL.Query().Get("port")
		port, err := strconv.Atoi(portStr)
		if err != nil {
			http.Error(w, "invalid port", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := CheckPort(port); err != nil {
			json.NewEncoder(w).Encode(map[string]any{
				"port":        port,
				"available":   false,
				"error":       err.Error(),
				"suggestions": SuggestPorts(port),
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"port":      port,
			"available": true,
		})
	})

	// Port get/change
	mux.HandleFunc("/__ditto__/api/port", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"port":        srv.Port(),
				"suggestions": SuggestPorts(srv.Port()),
			})

		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			var req struct {
				Port int `json:"port"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if err := CheckPort(req.Port); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]any{
					"error":       err.Error(),
					"suggestions": SuggestPorts(req.Port),
				})
				return
			}
			// Respond first, then restart async (closing the listener kills in-flight requests)
			if cfgStore != nil {
				cfgStore.SetPort(req.Port)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"port": req.Port,
			})
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			go func() {
				time.Sleep(200 * time.Millisecond)
				if err := srv.Restart(req.Port); err != nil {
					log.Printf("Failed to restart on port %d: %v", req.Port, err)
				}
			}()

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Target change with auto-save (overrides the basic target endpoint)
	mux.HandleFunc("/__ditto__/api/target/save", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		var req struct {
			Target string `json:"target"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := proxyMgr.SetTarget(req.Target); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if cfgStore != nil {
			cfgStore.SetTarget(req.Target)
		}
		w.WriteHeader(http.StatusOK)
	})

	// Config reset
	mux.HandleFunc("/__ditto__/api/config/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfgStore != nil {
			cfgStore.Reset()
		}
		w.WriteHeader(http.StatusOK)
	})
}
