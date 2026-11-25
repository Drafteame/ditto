package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"ditto/internal/eventlog"
)

// streamLogs sends event summaries as server-sent events, first replaying
// those after the client's last cursor (?since= or Last-Event-ID).
func (a *API) streamLogs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	since := r.URL.Query().Get("since")
	if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
		since = lastEventID
	}
	ch, replay, gap := a.Bus.SubscribeAfter(since)
	defer a.Bus.Unsubscribe(ch)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	if gap != nil {
		marker := eventlog.Event{Type: "GAP", Method: "GAP", StreamGap: true, GapReason: gap.Reason,
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

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
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
}

func writeSSELogEvent(w http.ResponseWriter, flusher http.Flusher, event eventlog.Event) error {
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

// listLogs returns the retained events without their bodies.
func (a *API) listLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Bus.Summaries())
}

// logHistory returns a page of retained events matching the query string.
func (a *API) logHistory(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	offset, _ := strconv.Atoi(query.Get("offset"))
	limit, _ := strconv.Atoi(query.Get("limit"))
	expected, _ := strconv.Atoi(query.Get("expected"))
	writeJSON(w, http.StatusOK, a.Bus.History(eventlog.HistoryQuery{
		Since: query.Get("since"), From: query.Get("from"), To: query.Get("to"),
		Channel: query.Get("channel"), Method: query.Get("method"), Direction: query.Get("direction"),
		Source: query.Get("source"), DispatchID: query.Get("dispatch_id"),
		Offset: offset, Limit: limit, Expected: expected,
	}))
}

// getLog returns one retained event with its bodies.
func (a *API) getLog(w http.ResponseWriter, r *http.Request) {
	event, ok := a.Bus.Detail(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, event)
}
