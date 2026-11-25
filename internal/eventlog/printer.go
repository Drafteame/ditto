package eventlog

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Printer writes one line per log event in the --log-format: a short
// human-readable line, or the event as a JSON object. A nil *Printer
// discards events.
type Printer struct {
	mu   sync.Mutex
	w    io.Writer
	json bool
}

// NewPrinter returns a Printer that writes to w, as JSON lines when json is
// set and as short text lines otherwise.
func NewPrinter(w io.Writer, json bool) *Printer {
	return &Printer{w: w, json: json}
}

// Log writes one line for e.
func (l *Printer) Log(e Event) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.json {
		data, err := json.Marshal(e)
		if err != nil {
			return
		}
		fmt.Fprintln(l.w, string(data))
		return
	}
	fmt.Fprintf(l.w, "%s %s %-6s %s %s → %d (%dms)\n",
		e.Timestamp, e.ID, e.Type, e.Method, e.Path, e.Status, e.DurationMs)
}
