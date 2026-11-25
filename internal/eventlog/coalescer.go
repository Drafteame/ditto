package eventlog

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CoalesceThresholdPerSecond is how many DISPATCH or FRAME events per channel
// a Coalescer sends to subscribers each second before summarising the rest.
const CoalesceThresholdPerSecond = 20

// Coalescer publishes socket DISPATCH and FRAME events, but stops sending
// them to subscribers once a channel exceeds CoalesceThresholdPerSecond in a
// one-second window; it then publishes a single *_BURST summary event.
// Every event is still retained and printed.
type Coalescer struct {
	mu      sync.Mutex
	bus     *Bus
	windows map[string]*coalesceWindow
}

type coalesceWindow struct {
	start       time.Time
	count       int
	suppressed  bool
	timer       *time.Timer
	method      string
	path        string
	channel     string
	direction   string
	source      string
	firstCursor string
	lastCursor  string
}

// NewCoalescer returns a Coalescer that publishes to bus.
func NewCoalescer(bus *Bus) *Coalescer {
	return &Coalescer{bus: bus, windows: make(map[string]*coalesceWindow)}
}

// Publish publishes event, coalescing high-rate socket frames.
func (p *Coalescer) Publish(event Event) {
	if event.Type != "SOCKET" || (event.Method != "DISPATCH" && event.Method != "FRAME") {
		p.publish(event)
		return
	}
	key := strings.Join([]string{event.Method, event.Path, event.Channel, event.Direction, event.Source}, "\x00")
	now := time.Now()
	p.mu.Lock()
	window := p.windows[key]
	if window != nil && now.Sub(window.start) >= time.Second {
		p.flushLocked(window)
		delete(p.windows, key)
		window = nil
	}
	if window == nil {
		window = &coalesceWindow{start: now, method: event.Method, path: event.Path, channel: event.Channel, direction: event.Direction, source: event.Source}
		p.windows[key] = window
		// Every window expires, including rates below the summary threshold.
		// Compare the pointer when firing so an old timer cannot delete its replacement.
		window.timer = time.AfterFunc(time.Second, func() { p.flush(key, window) })
	}
	window.count++
	immediate := window.count <= CoalesceThresholdPerSecond
	event = p.bus.publishWithSummary(event, immediate)
	p.bus.log.Log(event)
	if window.firstCursor == "" {
		window.firstCursor = event.Cursor
	}
	window.lastCursor = event.Cursor
	if window.count == CoalesceThresholdPerSecond+1 {
		window.suppressed = true
	}
	p.mu.Unlock()
}

func (p *Coalescer) flush(key string, expected *coalesceWindow) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.windows[key] != expected {
		return
	}
	delete(p.windows, key)
	p.flushLocked(expected)
}

func (p *Coalescer) flushLocked(window *coalesceWindow) {
	if !window.suppressed {
		return
	}
	if window.timer != nil {
		window.timer.Stop()
	}
	burstID := "burst-" + strings.ReplaceAll(window.firstCursor, ":", "-")
	body, _ := json.Marshal(map[string]any{
		"burst_id": burstID, "method": window.method, "total_frames": window.count,
		"window_ms": time.Since(window.start).Milliseconds(), "start_cursor": window.firstCursor,
		"end_cursor": window.lastCursor,
	})
	p.publish(Event{
		Type: "SOCKET", Method: window.method + "_BURST", Path: window.path, Channel: window.channel,
		Status: http.StatusOK, ResponseBody: string(body), BurstID: burstID, BurstMethod: window.method,
		BurstCount: window.count, BurstDirection: window.direction, BurstSource: window.source,
		BurstStartCursor: window.firstCursor, BurstEndCursor: window.lastCursor, BurstWindowMs: time.Since(window.start).Milliseconds(),
	})
}

func (p *Coalescer) publish(event Event) {
	p.bus.Publish(event)
}
