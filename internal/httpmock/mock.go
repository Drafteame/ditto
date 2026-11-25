// Package httpmock holds Ditto's HTTP mocks: the mock model, request
// matching (method, path wildcards, query, headers, JSON body subsets),
// sequence responses, and the file-backed Store that serves them.
package httpmock

import (
	"encoding/json"
	"reflect"
)

// Match holds optional conditions a request must satisfy beyond method + path.
// All fields are optional; a request matches if every specified condition is met.
type Match struct {
	Query   map[string]string `json:"query,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// IsEmpty reports whether no conditions are defined.
func (m Match) IsEmpty() bool {
	return len(m.Query) == 0 && len(m.Headers) == 0 && len(m.Body) == 0
}

// Specificity returns the number of conditions defined; used to prefer the
// most specific mock when multiple mocks could match the same request.
func (m Match) Specificity() int {
	score := len(m.Query) + len(m.Headers)
	if len(m.Body) > 0 {
		score++
	}
	return score
}

// Equals reports whether two Match definitions describe the same conditions.
func (m Match) Equals(other Match) bool {
	if !reflect.DeepEqual(m.Query, other.Query) {
		return false
	}
	if !equalCaseInsensitive(m.Headers, other.Headers) {
		return false
	}
	return jsonEqual(m.Body, other.Body)
}

// Mock is one canned response for requests matching Method, Path and the
// optional Match conditions. Source is its file name in the store directory.
type Mock struct {
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Status       int               `json:"status"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         json.RawMessage   `json:"body"`
	RawBody      []byte            `json:"-"`
	DelayMs      int               `json:"delay_ms,omitempty"`
	Match        Match             `json:"match,omitempty"`
	Enabled      bool              `json:"enabled"`
	Source       string            `json:"source"`
	ResponseMode string            `json:"response_mode,omitempty"` // "static" (default) | "sequence"
	Sequence     *Sequence         `json:"sequence,omitempty"`
}

// Sequence lets a single mock return different responses on subsequent calls.
// CurrentStep is in-memory only (not persisted to disk); it resets when the
// server restarts. It is exposed over the wire so the UI can render the
// current position, but writeMockFile uses its own struct that excludes it.
type Sequence struct {
	Steps       []Step `json:"steps"`
	OnEnd       string `json:"on_end"` // "loop" | "stay" | "reset" | "proxy"
	CurrentStep int    `json:"current_step,omitempty"`
}

// Step is one response in a Sequence; a zero Status falls back to the
// mock's status.
type Step struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body"`
	DelayMs int               `json:"delay_ms,omitempty"`
}

// IsSequence reports whether this mock should serve a sequence response.
func (m *Mock) IsSequence() bool {
	return m.ResponseMode == "sequence" && m.Sequence != nil && len(m.Sequence.Steps) > 0
}
