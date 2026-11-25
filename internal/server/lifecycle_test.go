package server

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"ditto/internal/appdata"
	"ditto/internal/eventlog"
)

func TestServerRestartSwapsListenerAndShutdownEndsStreams(t *testing.T) {
	layout, err := appdata.Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, second := freePort(t), freePort(t)
	srv, err := New(Config{Port: first, MocksDir: layout.MocksDir, Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	assertServing(t, first, true)

	stream, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/__ditto__/events", first))
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer stream.Body.Close()
	lines := bufio.NewScanner(stream.Body)

	if err := srv.Restart(second); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if srv.Port() != second {
		t.Fatalf("Port() = %d, want %d", srv.Port(), second)
	}
	assertServing(t, second, true)
	assertServing(t, first, false)

	srv.Bus.Publish(eventlog.Event{Type: "MOCK", Method: "GET", Path: "/after-restart"})
	if !scanUntil(lines, "/after-restart") {
		t.Fatal("SSE stream opened before the restart did not receive a later event")
	}

	busy, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", first))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if err := srv.Restart(first); err == nil {
		t.Fatal("Restart() onto a busy port succeeded")
	}
	assertServing(t, second, true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v (SSE stream kept the server busy?)", err)
	}
	assertServing(t, second, false)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func assertServing(t *testing.T, port int, want bool) {
	t.Helper()
	// A fresh connection each time: a pooled keep-alive connection would
	// still reach the old listener's server after a restart.
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/__ditto__/api/mocks", port))
	if err == nil {
		resp.Body.Close()
	}
	if got := err == nil && resp.StatusCode == http.StatusOK; got != want {
		t.Fatalf("serving on %d = %v (err %v), want %v", port, got, err, want)
	}
}

func scanUntil(lines *bufio.Scanner, substr string) bool {
	found := make(chan bool, 1)
	go func() {
		for lines.Scan() {
			if strings.Contains(lines.Text(), substr) {
				found <- true
				return
			}
		}
		found <- false
	}()
	select {
	case ok := <-found:
		return ok
	case <-time.After(2 * time.Second):
		return false
	}
}
