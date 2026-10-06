package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type cancelOnFlushRecorder struct {
	*httptest.ResponseRecorder
	cancel  context.CancelFunc
	flushed bool
}

func (w *cancelOnFlushRecorder) Flush() {
	w.ResponseRecorder.Flush()
	if !w.flushed {
		w.flushed = true
		w.cancel()
	}
}

func TestEventBusRetainsIdentifiedEventsAndReturnsDetails(t *testing.T) {
	bus := NewEventBus()
	headers := map[string][]string{"X-Test": {"before"}}
	metadata := &LogPayloadMetadata{SizeBytes: 4, ContentType: "text/plain", CaptureStatus: "captured", RawBase64: "dGVzdA=="}
	bus.Publish(LogEvent{Type: "PROXY", Method: "POST", Path: "/item", ResponseBody: "body", RequestHeaders: headers,
		RequestPayload: metadata, Direction: "inbound", ClientID: "client-1"})
	headers["X-Test"][0] = "after"
	metadata.RawBase64 = "mutated"

	summaries := bus.LogSummaries()
	if len(summaries) != 1 {
		t.Fatalf("got %d summaries, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary.ID == "" {
		t.Fatal("summary has no backend ID")
	}
	if _, err := time.Parse(time.RFC3339Nano, summary.Timestamp); err != nil {
		t.Fatalf("timestamp %q is not RFC3339Nano: %v", summary.Timestamp, err)
	}
	if summary.ResponseBody != "" || summary.RequestHeaders != nil || summary.RequestPayload.RawBase64 != "" {
		t.Fatalf("summary contains detail payload: %#v", summary)
	}
	if summary.Direction != "inbound" || summary.ClientID != "client-1" {
		t.Fatalf("summary lost event context: %#v", summary)
	}

	detail, ok := bus.LogDetail(summary.ID)
	if !ok || detail.ResponseBody != "body" || detail.RequestHeaders["X-Test"][0] != "before" || detail.RequestPayload.RawBase64 != "dGVzdA==" {
		t.Fatalf("detail does not preserve published values: %#v, found=%t", detail, ok)
	}
	if _, ok := bus.LogDetail("missing"); ok {
		t.Fatal("missing ID unexpectedly resolved")
	}
}

func TestEventBusRetentionBoundsCountAndBytes(t *testing.T) {
	bus := NewEventBus()
	for i := 0; i <= MaxRetainedLogEvents; i++ {
		bus.Publish(LogEvent{Method: "GET", Path: "/item"})
	}
	if got := len(bus.LogSummaries()); got != MaxRetainedLogEvents {
		t.Fatalf("retained %d events, want max %d", got, MaxRetainedLogEvents)
	}

	byteBus := NewEventBus()
	byteBus.Publish(LogEvent{ResponseBody: strings.Repeat("x", MaxRetainedLogBytes+1)})
	if got := len(byteBus.LogSummaries()); got != 0 {
		t.Fatalf("oversized event was retained, count=%d", got)
	}
	if byteBus.retainedBytes > MaxRetainedLogBytes {
		t.Fatalf("retained bytes %d exceed max %d", byteBus.retainedBytes, MaxRetainedLogBytes)
	}
}

func TestLogSummaryAndDetailRoutes(t *testing.T) {
	bus := NewEventBus()
	bus.Publish(LogEvent{Type: "MOCK", Method: "GET", Path: "/item", ResponseBody: `{"ok":true}`})
	id := bus.LogSummaries()[0].ID
	mux := http.NewServeMux()
	RegisterUI(mux, nil, bus, nil, nil, ServerInfo{}, false)

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/__ditto__/api/logs", nil))
	var summaries []LogEvent
	if err := json.Unmarshal(list.Body.Bytes(), &summaries); err != nil {
		t.Fatal(err)
	}
	if list.Code != http.StatusOK || len(summaries) != 1 || summaries[0].ID != id || summaries[0].ResponseBody != "" {
		t.Fatalf("unexpected summary response: status=%d body=%s", list.Code, list.Body.String())
	}

	detail := httptest.NewRecorder()
	mux.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/__ditto__/api/logs/"+id, nil))
	var event LogEvent
	if err := json.Unmarshal(detail.Body.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if detail.Code != http.StatusOK || event.ID != id || event.ResponseBody != `{"ok":true}` {
		t.Fatalf("unexpected detail response: status=%d body=%s", detail.Code, detail.Body.String())
	}
}

func TestEventBusReplayOverflowAndFilteredCursorGaps(t *testing.T) {
	bus := NewEventBus()
	bus.Publish(LogEvent{Type: "SOCKET", Method: "CONNECT", Path: "/a"})
	first := bus.LogSummaries()[0].Cursor
	ch, replay, gap := bus.SubscribeAfter(first)
	defer bus.Unsubscribe(ch)
	if gap != nil || len(replay) != 0 {
		t.Fatalf("initial replay = %d events, gap=%#v", len(replay), gap)
	}
	for i := 0; i < 200; i++ {
		bus.Publish(LogEvent{Type: "SOCKET", Method: "FRAME", Path: "/a", Direction: "upstream_to_ditto"})
	}
	var sawGap bool
	for len(ch) > 0 {
		event := <-ch
		if event.StreamGap {
			sawGap = true
		}
	}
	if !sawGap {
		t.Fatal("slow subscriber did not receive a gap marker")
	}
	recovered := bus.History(first, "", "", "", "", "", "", "", 0, 500, 0)
	if recovered.Total != 200 || !recovered.Complete {
		t.Fatalf("history recovery = total %d complete %t gap=%#v", recovered.Total, recovered.Complete, recovered.Gap)
	}

	gapBus := NewEventBus()
	gapBus.Publish(LogEvent{Type: "SOCKET", Method: "FRAME", Path: "/wanted", Channel: "/wanted"})
	cursor := gapBus.LogSummaries()[0].Cursor
	gapBus.Publish(LogEvent{Type: "SOCKET", Method: "FRAME", Path: "/other", ResponseBody: strings.Repeat("x", MaxRetainedLogBytes+1)})
	gapBus.Publish(LogEvent{Type: "SOCKET", Method: "FRAME", Path: "/wanted", Channel: "/wanted"})
	filtered := gapBus.History(cursor, "", "", "/wanted", "", "", "", "", 0, 20, 0)
	if filtered.Gap == nil || filtered.Gap.Reason != "event_not_retained" {
		t.Fatalf("filtered history gap = %#v, want missing cursor event", filtered.Gap)
	}
}

func TestBurstHistoryUsesExactGroupAndReportsExpiredMembers(t *testing.T) {
	bus := NewEventBus()
	var first, last string
	for i := 0; i < 4; i++ {
		direction, source := "ditto_to_client", "hub"
		if i == 1 {
			direction, source = "client_to_upstream", "live-bridge"
		}
		event := bus.publishWithSummary(LogEvent{Type: "SOCKET", Method: "FRAME", Path: "/scores", Channel: "/scores", Direction: direction, Source: source}, false)
		if i == 0 {
			first = event.Cursor
		}
		if i == 3 {
			last = event.Cursor
		}
	}
	history := bus.History("", first, last, "/scores", "FRAME", "ditto_to_client", "hub", "", 0, 20, 3)
	if len(history.Events) != 3 || !history.Complete {
		t.Fatalf("burst history = %d events complete=%t gap=%#v", len(history.Events), history.Complete, history.Gap)
	}
	history = bus.History("", first, last, "/scores", "FRAME", "ditto_to_client", "hub", "", 0, 20, 4)
	if history.Complete || history.Gap == nil || history.Gap.Reason != "members_expired" {
		t.Fatalf("incomplete burst = complete %t gap=%#v, want members_expired", history.Complete, history.Gap)
	}
}

func TestConcurrentPublishesKeepCursorAndStdoutIdentityStable(t *testing.T) {
	bus := NewEventBus()
	const publishers, each = 8, 50
	var wg sync.WaitGroup
	for publisher := 0; publisher < publishers; publisher++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				bus.Publish(LogEvent{Type: "MODE", Method: "CHANGE"})
			}
		}()
	}
	wg.Wait()
	items := bus.LogSummaries()
	if len(items) != publishers*each {
		t.Fatalf("retained %d events", len(items))
	}
	last := uint64(0)
	for _, item := range items {
		_, seq, ok := parseLogCursor(item.Cursor)
		if !ok || seq <= last {
			t.Fatalf("cursor %q is not strictly increasing after %d", item.Cursor, last)
		}
		if !strings.HasPrefix(item.ID, "log-") {
			t.Fatalf("unexpected event ID %q", item.ID)
		}
		last = seq
	}
}

func TestSSEStartsImmediatelyAndReplaysSummariesAfterReset(t *testing.T) {
	bus := NewEventBus()
	bus.Publish(LogEvent{Type: "PROXY", Method: "POST", Path: "/secret", RequestBody: "request secret", ResponseBody: "response secret",
		RequestPayload: &LogPayloadMetadata{SizeBytes: 7, CapturedBytes: 7, CaptureStatus: "captured", RawBase64: "c2VjcmV0"}})
	mux := http.NewServeMux()
	RegisterUI(mux, nil, bus, nil, nil, ServerInfo{}, false)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/__ditto__/events", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "old-session:99")
	writer := &cancelOnFlushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	started := time.Now()
	mux.ServeHTTP(writer, req)
	if time.Since(started) > time.Second || !writer.flushed || writer.Code != http.StatusOK {
		t.Fatalf("SSE did not flush headers immediately: elapsed=%s flushed=%t status=%d", time.Since(started), writer.flushed, writer.Code)
	}
	body := writer.Body.String()
	if !strings.Contains(body, `"gap_reason":"server_reset"`) || !strings.Contains(body, `"method":"POST"`) {
		t.Fatalf("reset replay missing gap or event: %s", body)
	}
	if strings.Contains(body, "request secret") || strings.Contains(body, "response secret") || strings.Contains(body, "c2VjcmV0") {
		t.Fatalf("SSE replay contained payload details: %s", body)
	}
	if strings.HasPrefix(body, "id:") {
		t.Fatalf("GAP marker incorrectly advanced SSE ID: %s", body)
	}
}
