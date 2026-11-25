package eventlog

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEventBusRetainsIdentifiedEventsAndReturnsDetails(t *testing.T) {
	bus := NewBus(nil)
	headers := map[string][]string{"X-Test": {"before"}}
	metadata := &Payload{SizeBytes: 4, ContentType: "text/plain", CaptureStatus: "captured", RawBase64: "dGVzdA=="}
	bus.Publish(Event{Type: "PROXY", Method: "POST", Path: "/item", ResponseBody: "body", RequestHeaders: headers,
		RequestPayload: metadata, Direction: "inbound", ClientID: "client-1"})
	headers["X-Test"][0] = "after"
	metadata.RawBase64 = "mutated"

	summaries := bus.Summaries()
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

	detail, ok := bus.Detail(summary.ID)
	if !ok || detail.ResponseBody != "body" || detail.RequestHeaders["X-Test"][0] != "before" || detail.RequestPayload.RawBase64 != "dGVzdA==" {
		t.Fatalf("detail does not preserve published values: %#v, found=%t", detail, ok)
	}
	if _, ok := bus.Detail("missing"); ok {
		t.Fatal("missing ID unexpectedly resolved")
	}
}

func TestEventBusRetentionBoundsCountAndBytes(t *testing.T) {
	bus := NewBus(nil)
	for i := 0; i <= MaxRetainedEvents; i++ {
		bus.Publish(Event{Method: "GET", Path: "/item"})
	}
	if got := len(bus.Summaries()); got != MaxRetainedEvents {
		t.Fatalf("retained %d events, want max %d", got, MaxRetainedEvents)
	}

	byteBus := NewBus(nil)
	byteBus.Publish(Event{ResponseBody: strings.Repeat("x", MaxRetainedBytes+1)})
	if got := len(byteBus.Summaries()); got != 0 {
		t.Fatalf("oversized event was retained, count=%d", got)
	}
	if byteBus.retainedBytes > MaxRetainedBytes {
		t.Fatalf("retained bytes %d exceed max %d", byteBus.retainedBytes, MaxRetainedBytes)
	}
}

func TestEventBusReplayOverflowAndFilteredCursorGaps(t *testing.T) {
	bus := NewBus(nil)
	bus.Publish(Event{Type: "SOCKET", Method: "CONNECT", Path: "/a"})
	first := bus.Summaries()[0].Cursor
	ch, replay, gap := bus.SubscribeAfter(first)
	defer bus.Unsubscribe(ch)
	if gap != nil || len(replay) != 0 {
		t.Fatalf("initial replay = %d events, gap=%#v", len(replay), gap)
	}
	for i := 0; i < 200; i++ {
		bus.Publish(Event{Type: "SOCKET", Method: "FRAME", Path: "/a", Direction: "upstream_to_ditto"})
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
	recovered := bus.History(HistoryQuery{Since: first, Limit: 500})
	if recovered.Total != 200 || !recovered.Complete {
		t.Fatalf("history recovery = total %d complete %t gap=%#v", recovered.Total, recovered.Complete, recovered.Gap)
	}

	gapBus := NewBus(nil)
	gapBus.Publish(Event{Type: "SOCKET", Method: "FRAME", Path: "/wanted", Channel: "/wanted"})
	cursor := gapBus.Summaries()[0].Cursor
	gapBus.Publish(Event{Type: "SOCKET", Method: "FRAME", Path: "/other", ResponseBody: strings.Repeat("x", MaxRetainedBytes+1)})
	gapBus.Publish(Event{Type: "SOCKET", Method: "FRAME", Path: "/wanted", Channel: "/wanted"})
	filtered := gapBus.History(HistoryQuery{Since: cursor, Channel: "/wanted", Limit: 20})
	if filtered.Gap == nil || filtered.Gap.Reason != "event_not_retained" {
		t.Fatalf("filtered history gap = %#v, want missing cursor event", filtered.Gap)
	}
}

func TestBurstHistoryUsesExactGroupAndReportsExpiredMembers(t *testing.T) {
	bus := NewBus(nil)
	var first, last string
	for i := 0; i < 4; i++ {
		direction, source := "ditto_to_client", "hub"
		if i == 1 {
			direction, source = "client_to_upstream", "live-bridge"
		}
		event := bus.publishWithSummary(Event{Type: "SOCKET", Method: "FRAME", Path: "/scores", Channel: "/scores", Direction: direction, Source: source}, false)
		if i == 0 {
			first = event.Cursor
		}
		if i == 3 {
			last = event.Cursor
		}
	}
	history := bus.History(HistoryQuery{From: first, To: last, Channel: "/scores", Method: "FRAME", Direction: "ditto_to_client", Source: "hub", Limit: 20, Expected: 3})
	if len(history.Events) != 3 || !history.Complete {
		t.Fatalf("burst history = %d events complete=%t gap=%#v", len(history.Events), history.Complete, history.Gap)
	}
	history = bus.History(HistoryQuery{From: first, To: last, Channel: "/scores", Method: "FRAME", Direction: "ditto_to_client", Source: "hub", Limit: 20, Expected: 4})
	if history.Complete || history.Gap == nil || history.Gap.Reason != "members_expired" {
		t.Fatalf("incomplete burst = complete %t gap=%#v, want members_expired", history.Complete, history.Gap)
	}
}

func TestConcurrentPublishesKeepCursorAndStdoutIdentityStable(t *testing.T) {
	bus := NewBus(nil)
	const publishers, each = 8, 50
	var wg sync.WaitGroup
	for publisher := 0; publisher < publishers; publisher++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				bus.Publish(Event{Type: "MODE", Method: "CHANGE"})
			}
		}()
	}
	wg.Wait()
	items := bus.Summaries()
	if len(items) != publishers*each {
		t.Fatalf("retained %d events", len(items))
	}
	last := uint64(0)
	for _, item := range items {
		_, seq, ok := parseCursor(item.Cursor)
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
	bus := NewBus(nil)
	events := bus.Subscribe()
	defer bus.Unsubscribe(events)
	pub := NewCoalescer(bus)
	for i := 0; i < CoalesceThresholdPerSecond+5; i++ {
		pub.Publish(Event{Type: "SOCKET", Method: "DISPATCH", Path: "/burst", Status: http.StatusOK,
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
			var burst Event
			for _, event := range bus.Summaries() {
				if event.Method == "DISPATCH_BURST" {
					burst = event
				}
			}
			if burst.BurstCount != CoalesceThresholdPerSecond+5 || burst.BurstStartCursor == "" || burst.BurstEndCursor == "" {
				t.Fatalf("burst summary = %#v", burst)
			}
			members := bus.History(HistoryQuery{From: burst.BurstStartCursor, To: burst.BurstEndCursor, Channel: "/burst", Method: "DISPATCH", Direction: "ditto_to_client", Source: "test", Limit: 100, Expected: burst.BurstCount})
			if !members.Complete || len(members.Events) != burst.BurstCount {
				t.Fatalf("burst members = %d complete=%t gap=%#v", len(members.Events), members.Complete, members.Gap)
			}
			if detail, ok := bus.Detail(members.Events[0].ID); !ok || detail.RequestBody == "" {
				t.Fatalf("suppressed dispatch detail not retained: %#v found=%t", detail, ok)
			}
			return
		}
	}
}

func TestEventBusPublishUnsubscribeRace(t *testing.T) {
	b := NewBus(nil)
	subs := make([]chan Event, 100)
	for i := range subs {
		subs[i] = b.Subscribe()
	}

	done := make(chan struct{})
	var drainWG sync.WaitGroup
	for _, ch := range subs {
		drainWG.Add(1)
		go func(ch chan Event) {
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
			b.Publish(Event{Type: "SOCKET", Method: "DISPATCH"})
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
