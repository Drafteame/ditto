package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const SocketLogCoalesceThresholdPerSecond = 20

type CoalescingPublisher struct {
	mu       sync.Mutex
	bus      *EventBus
	jsonLogs bool
	windows  map[string]*coalesceWindow
}

type coalesceWindow struct {
	start      time.Time
	frames     int
	suppressed bool
	timer      *time.Timer
}

func NewCoalescingPublisher(bus *EventBus, jsonLogs bool) *CoalescingPublisher {
	return &CoalescingPublisher{
		bus:      bus,
		jsonLogs: jsonLogs,
		windows:  make(map[string]*coalesceWindow),
	}
}

func (p *CoalescingPublisher) HasLogSubscribers() bool {
	if p == nil || p.bus == nil {
		return false
	}
	return p.bus.HasSubscribers()
}

func (p *CoalescingPublisher) Publish(event LogEvent) {
	if event.Type != "SOCKET" || event.Method != "DISPATCH" {
		p.publish(event)
		return
	}
	key := event.Type + "\x00" + event.Path
	now := time.Now()
	p.mu.Lock()
	window := p.windows[key]
	if window == nil || now.Sub(window.start) >= time.Second {
		window = &coalesceWindow{start: now}
		p.windows[key] = window
	}
	window.frames++
	frames := window.frames
	if frames == SocketLogCoalesceThresholdPerSecond+1 && window.timer == nil {
		delay := time.Until(window.start.Add(time.Second))
		if delay < 0 {
			delay = 0
		}
		window.suppressed = true
		window.timer = time.AfterFunc(delay, func() { p.flush(key, event.Path) })
	}
	p.mu.Unlock()

	if frames <= SocketLogCoalesceThresholdPerSecond {
		p.publish(event)
	}
}

func (p *CoalescingPublisher) flush(key, path string) {
	p.mu.Lock()
	window := p.windows[key]
	if window == nil {
		p.mu.Unlock()
		return
	}
	delete(p.windows, key)
	frames := window.frames
	suppressed := window.suppressed
	p.mu.Unlock()
	if !suppressed {
		return
	}
	body, _ := json.Marshal(map[string]any{"total_frames": frames, "window_ms": 1000})
	p.publish(LogEvent{
		Timestamp:    time.Now().Format("15:04:05"),
		Type:         "SOCKET",
		Method:       "DISPATCH_BURST",
		Path:         path,
		Status:       http.StatusOK,
		ResponseBody: string(body),
	})
}

func (p *CoalescingPublisher) publish(event LogEvent) {
	logRequest(p.jsonLogs, event)
	if p.bus != nil {
		p.bus.Publish(event)
	}
}
