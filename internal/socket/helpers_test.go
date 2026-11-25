package socket

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"ditto/internal/adapter"
	"ditto/internal/eventlog"
)

// textMessage wraps data as a text socket message.
func textMessage(data []byte) adapter.Message {
	return adapter.Message{Data: data, Kind: websocket.MessageText}
}

// decodeProfileEnvelope splits a profile-wrapped frame into its outer
// envelope and the inner event it carries as a JSON string.
func decodeProfileEnvelope(t *testing.T, data []byte) (struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Event string `json:"event"`
}, struct {
	T       string `json:"t"`
	Channel string `json:"channel,omitempty"`
	E       any    `json:"e"`
}) {
	t.Helper()
	var env struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Event string `json:"event"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("outer JSON error: %v; data=%s", err, data)
	}
	var inner struct {
		T       string `json:"t"`
		Channel string `json:"channel,omitempty"`
		E       any    `json:"e"`
	}
	if err := json.Unmarshal([]byte(env.Event), &inner); err != nil {
		t.Fatalf("inner JSON error: %v; event=%s", err, env.Event)
	}
	return env, inner
}

func waitForBurstSummary(t *testing.T, events <-chan eventlog.Event, channel string) int {
	t.Helper()
	deadline := time.After(1500 * time.Millisecond)
	for {
		select {
		case event := <-events:
			if event.Type != "SOCKET" || event.Method != "DISPATCH_BURST" || event.Path != channel {
				continue
			}
			var body struct {
				TotalFrames int `json:"total_frames"`
			}
			if err := json.Unmarshal([]byte(event.ResponseBody), &body); err != nil {
				t.Fatalf("summary body invalid JSON: %v", err)
			}
			return body.TotalFrames
		case <-deadline:
			t.Fatalf("timed out waiting for DISPATCH_BURST")
		}
	}
}

// writeProto writes a .proto file into dir, creating it, and returns dir.
func writeProto(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
