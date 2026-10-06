package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
