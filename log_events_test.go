package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
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
	bus := NewEventBus(nil)
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
	bus := NewEventBus(nil)
	for i := 0; i <= MaxRetainedLogEvents; i++ {
		bus.Publish(LogEvent{Method: "GET", Path: "/item"})
	}
	if got := len(bus.LogSummaries()); got != MaxRetainedLogEvents {
		t.Fatalf("retained %d events, want max %d", got, MaxRetainedLogEvents)
	}

	byteBus := NewEventBus(nil)
	byteBus.Publish(LogEvent{ResponseBody: strings.Repeat("x", MaxRetainedLogBytes+1)})
	if got := len(byteBus.LogSummaries()); got != 0 {
		t.Fatalf("oversized event was retained, count=%d", got)
	}
	if byteBus.retainedBytes > MaxRetainedLogBytes {
		t.Fatalf("retained bytes %d exceed max %d", byteBus.retainedBytes, MaxRetainedLogBytes)
	}
}

func TestEventBusReplayOverflowAndFilteredCursorGaps(t *testing.T) {
	bus := NewEventBus(nil)
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

	gapBus := NewEventBus(nil)
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
	bus := NewEventBus(nil)
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
	bus := NewEventBus(nil)
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

func TestCoalescingPublisherEmitsBurstSummary(t *testing.T) {
	bus := NewEventBus(nil)
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)
	pub := NewCoalescingPublisher(bus)
	for i := 0; i < SocketLogCoalesceThresholdPerSecond+5; i++ {
		pub.Publish(LogEvent{Type: "SOCKET", Method: "DISPATCH", Path: "/burst", Status: http.StatusOK,
			Direction: "ditto_to_client", Source: "test", RequestBody: fmt.Sprintf(`{"index":%d}`, i)})
	}
	deadline := time.After(1500 * time.Millisecond)
	summaries := 0
	for {
		select {
		case event := <-events:
			if event.Method == "DISPATCH_BURST" {
				summaries++
			}
		case <-deadline:
			if summaries != 1 {
				t.Fatalf("burst summaries = %d, want 1", summaries)
			}
			var burst LogEvent
			for _, event := range bus.LogSummaries() {
				if event.Method == "DISPATCH_BURST" {
					burst = event
				}
			}
			if burst.BurstCount != SocketLogCoalesceThresholdPerSecond+5 || burst.BurstStartCursor == "" || burst.BurstEndCursor == "" {
				t.Fatalf("burst summary = %#v", burst)
			}
			members := bus.History("", burst.BurstStartCursor, burst.BurstEndCursor, "/burst", "DISPATCH", "ditto_to_client", "test", "", 0, 100, burst.BurstCount)
			if !members.Complete || len(members.Events) != burst.BurstCount {
				t.Fatalf("burst members = %d complete=%t gap=%#v", len(members.Events), members.Complete, members.Gap)
			}
			if detail, ok := bus.LogDetail(members.Events[0].ID); !ok || detail.RequestBody == "" {
				t.Fatalf("suppressed dispatch detail not retained: %#v found=%t", detail, ok)
			}
			return
		}
	}
}

func TestEventBusPublishUnsubscribeRace(t *testing.T) {
	b := NewEventBus(nil)
	subs := make([]chan LogEvent, 100)
	for i := range subs {
		subs[i] = b.Subscribe()
	}

	done := make(chan struct{})
	var drainWG sync.WaitGroup
	for _, ch := range subs {
		drainWG.Add(1)
		go func(ch chan LogEvent) {
			defer drainWG.Done()
			for {
				select {
				case <-done:
					return
				case <-ch:
				}
			}
		}(ch)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			b.Publish(LogEvent{Type: "SOCKET", Method: "DISPATCH"})
			runtime.Gosched()
		}
	}()
	go func() {
		defer wg.Done()
		for _, ch := range subs {
			b.Unsubscribe(ch)
			runtime.Gosched()
		}
	}()
	wg.Wait()
	close(done)
	drainWG.Wait()
}
