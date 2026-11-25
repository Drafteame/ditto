package eventlog

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type retainedEvent struct {
	event Event
	size  int
	seq   uint64
}

// HistoryGap reports events between two cursors that are no longer
// retained or were dropped for a slow subscriber.
type HistoryGap struct {
	Reason string `json:"reason"`
	From   string `json:"from_cursor,omitempty"`
	To     string `json:"to_cursor,omitempty"`
}

// History is one page of retained events matching a HistoryQuery.
type History struct {
	Events       []Event     `json:"events"`
	OldestCursor string      `json:"oldest_cursor,omitempty"`
	LatestCursor string      `json:"latest_cursor,omitempty"`
	NextCursor   string      `json:"next_cursor,omitempty"`
	HasMore      bool        `json:"has_more"`
	Total        int         `json:"total"`
	Expected     int         `json:"expected,omitempty"`
	Complete     bool        `json:"complete"`
	Gap          *HistoryGap `json:"gap,omitempty"`
}

// Bus retains full events and sends lightweight summaries to SSE clients.
type Bus struct {
	mu             sync.Mutex
	clients        map[chan Event]struct{}
	summaryClients map[chan Event]struct{}
	retained       []retainedEvent
	retainedBytes  int
	session        string
	nextSeq        uint64
	log            *Printer
}

// NewBus returns a bus that also writes every published event to log.
// A nil log disables the request log.
func NewBus(log *Printer) *Bus {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		copy(random[:], []byte(fmt.Sprintf("%012x", time.Now().UnixNano())))
	}
	return &Bus{clients: make(map[chan Event]struct{}), summaryClients: make(map[chan Event]struct{}),
		session: hex.EncodeToString(random[:]), log: log}
}

// Subscribe returns a channel that receives every later event in full.
func (b *Bus) Subscribe() chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe stops broadcasting to ch. The bus never closes subscriber
// channels; owning goroutines should exit on their own signal.
func (b *Bus) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	delete(b.clients, ch)
	delete(b.summaryClients, ch)
	b.mu.Unlock()
}

// Publish assigns the event its ID, timestamp and cursor, retains it, sends
// it to subscribers and writes it to the request log.
func (b *Bus) Publish(event Event) {
	b.log.Log(b.publishWithSummary(event, true))
}

func (b *Bus) publishWithSummary(event Event, send bool) Event {
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
	stored := cloneEvent(event)
	encoded, _ := json.Marshal(stored)
	if len(encoded) > MaxRetainedBytes {
		event.NotRetained = true
		stored.NotRetained = true
		encoded, _ = json.Marshal(stored)
	}
	if len(encoded) <= MaxRetainedBytes {
		for len(b.retained) > 0 && (len(b.retained) >= MaxRetainedEvents || b.retainedBytes+len(encoded) > MaxRetainedBytes) {
			b.retainedBytes -= b.retained[0].size
			b.retained[0] = retainedEvent{}
			b.retained = b.retained[1:]
		}
		b.retained = append(b.retained, retainedEvent{event: stored, size: len(encoded), seq: seq})
		b.retainedBytes += len(encoded)
	}
	if send {
		b.broadcastLocked(summaryEvent(event), event)
	}
	b.mu.Unlock()
	return event
}

func (b *Bus) cursor(seq uint64) string {
	return fmt.Sprintf("%s:%d", b.session, seq)
}

func parseCursor(cursor string) (string, uint64, bool) {
	index := strings.LastIndexByte(cursor, ':')
	if index < 1 {
		return "", 0, false
	}
	seq, err := strconv.ParseUint(cursor[index+1:], 10, 64)
	return cursor[:index], seq, err == nil
}

func (b *Bus) broadcastLocked(summary, detail Event) {
	for ch := range b.clients {
		event := detail
		if _, lightweight := b.summaryClients[ch]; lightweight {
			event = summary
		}
		select {
		case ch <- cloneEvent(event):
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
			gap := Event{Type: "GAP", Method: "GAP", Path: "", ID: "gap-" + event.ID,
				StreamGap: true, GapReason: "slow_client", GapFromCursor: from, GapToCursor: event.Cursor}
			ch <- gap
			ch <- cloneEvent(event)
		}
	}
}

// SubscribeAfter returns a channel of later event summaries, the retained
// summaries after cursor to replay first, and any gap since cursor.
func (b *Bus) SubscribeAfter(cursor string) (chan Event, []Event, *HistoryGap) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan Event, 64)
	b.clients[ch] = struct{}{}
	b.summaryClients[ch] = struct{}{}
	history := b.historyLocked(HistoryQuery{Since: cursor, Limit: MaxRetainedEvents})
	return ch, history.Events, history.Gap
}

// Summaries returns every retained event without its bodies.
func (b *Bus) Summaries() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	items := make([]Event, len(b.retained))
	for i, entry := range b.retained {
		items[i] = summaryEvent(entry.event)
	}
	return items
}

// HistoryQuery selects retained events. Empty fields do not filter.
type HistoryQuery struct {
	// Since resumes after this cursor; events evicted since then are
	// reported as a gap.
	Since string
	// From and To bound the cursor range, inclusive.
	From, To                                       string
	Channel, Method, Direction, Source, DispatchID string
	Offset, Limit                                  int
	// Expected is the number of matches the caller knows existed (a burst
	// size); fewer matches are reported as a gap.
	Expected int
}

// History returns the retained events that match q.
func (b *Bus) History(q HistoryQuery) History {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.historyLocked(q)
}

func (b *Bus) historyLocked(q HistoryQuery) History {
	cursor, from, to := q.Since, q.From, q.To
	channel, method, direction, source, dispatchID := q.Channel, q.Method, q.Direction, q.Source, q.DispatchID
	offset, limit, expected := q.Offset, q.Limit, q.Expected
	result := History{Events: []Event{}, Complete: true, Expected: expected}
	if limit <= 0 {
		limit = 500
	}
	if limit > MaxRetainedEvents {
		limit = MaxRetainedEvents
	}
	if offset < 0 {
		offset = 0
	}
	var sinceSession string
	var sinceSeq uint64
	if cursor != "" {
		var valid bool
		sinceSession, sinceSeq, valid = parseCursor(cursor)
		if !valid || sinceSession != b.session {
			result.Gap = &HistoryGap{Reason: "server_reset", From: cursor, To: b.latestCursorLocked()}
			sinceSeq = 0
		} else if sinceSeq > b.nextSeq {
			result.Gap = &HistoryGap{Reason: "cursor_ahead", From: cursor, To: b.latestCursorLocked()}
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
			result.Gap = &HistoryGap{Reason: "retention_empty", To: result.LatestCursor}
		}
	} else if len(b.retained) > 0 && from == "" && to == "" {
		oldest := b.retained[0].seq
		if cursor == "" && oldest > 1 && channel == "" && method == "" && direction == "" && source == "" && dispatchID == "" && result.Gap == nil {
			result.Gap = &HistoryGap{Reason: "retention_window", To: b.cursor(oldest - 1)}
		} else if cursor != "" && sinceSession == b.session && sinceSeq+1 < oldest && result.Gap == nil {
			result.Gap = &HistoryGap{Reason: "evicted", From: cursor, To: b.cursor(oldest - 1)}
		}
	}
	var fromSeq, toSeq uint64
	if from != "" {
		session, seq, valid := parseCursor(from)
		if !valid || session != b.session {
			result.Gap = &HistoryGap{Reason: "server_reset", From: from, To: result.LatestCursor}
		} else {
			fromSeq = seq
		}
	}
	if to != "" {
		session, seq, valid := parseCursor(to)
		if !valid || session != b.session {
			result.Gap = &HistoryGap{Reason: "server_reset", From: to, To: result.LatestCursor}
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
				result.Gap = &HistoryGap{Reason: "event_not_retained", From: b.cursor(last + 1), To: b.cursor(retained.seq - 1)}
				break
			}
			last = retained.seq
		}
		if result.Gap == nil && last > 0 && b.nextSeq > last {
			result.Gap = &HistoryGap{Reason: "event_not_retained", From: b.cursor(last + 1), To: b.cursor(b.nextSeq)}
		}
	}
	filtered := make([]Event, 0)
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
		filtered = append(filtered, summaryEvent(retained.event))
	}
	result.Total = len(filtered)
	if expected > 0 && result.Total < expected {
		if result.Gap == nil {
			result.Gap = &HistoryGap{Reason: "members_expired", From: from, To: to}
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

func (b *Bus) latestCursorLocked() string {
	if b.nextSeq == 0 {
		return ""
	}
	return b.cursor(b.nextSeq)
}

// Detail returns the full retained event with the given ID.
func (b *Bus) Detail(id string) (Event, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.retained) - 1; i >= 0; i-- {
		if b.retained[i].event.ID == id {
			return cloneEvent(b.retained[i].event), true
		}
	}
	return Event{}, false
}
