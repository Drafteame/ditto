package main

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"unicode/utf8"
)

func newHTTPLogTestServer(t *testing.T, target string) *Server {
	t.Helper()
	layout, err := EnsureDataLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(ServerConfig{Port: 0, Target: target, MocksDir: layout.MocksDir, Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestHTTPLogCapturesBodiesForEveryMethodAndRequestForm(t *testing.T) {
	srv := newHTTPLogTestServer(t, "")
	cases := []struct {
		method, contentType, body string
		chunked                   bool
	}{
		{http.MethodPost, "application/json", `{"k":"v"}`, false},
		{http.MethodPatch, "text/plain", "plain body", false},
		{http.MethodPut, "application/x-www-form-urlencoded", "tag=one&tag=two", false},
		{http.MethodDelete, "application/json", `{"delete":true}`, false},
		{http.MethodGet, "text/plain", "get body", true},
		{http.MethodOptions, "application/json", `{"options":true}`, false},
	}
	for _, test := range cases {
		if _, err := srv.Store.Create(Mock{Method: test.method, Path: "/capture", Status: http.StatusCreated,
			Body: json.RawMessage(`{"ok":true}`), Headers: map[string]string{"Content-Type": "application/json", "X-Reply": "mock"}}); err != nil {
			t.Fatal(err)
		}
	}

	for _, test := range cases {
		t.Run(test.method, func(t *testing.T) {
			ch := srv.Bus.Subscribe()
			defer srv.Bus.Unsubscribe(ch)
			req := httptest.NewRequest(test.method, "/capture?tag=one&tag=two", strings.NewReader(test.body))
			req.Host = "api.example.test"
			req.RemoteAddr = "192.0.2.4:5123"
			req.Header.Set("Content-Type", test.contentType)
			req.Header.Set("X-Client", "original")
			if test.chunked {
				req.ContentLength = -1
				req.TransferEncoding = []string{"chunked"}
			}
			rec := httptest.NewRecorder()
			srv.Mux.ServeHTTP(rec, req)
			event := <-ch
			if event.Type != "MOCK" || event.Method != test.method || event.Status != http.StatusCreated {
				t.Fatalf("unexpected event: %#v", event)
			}
			if event.RequestBody != test.body || event.RequestPayload.SizeBytes != int64(len(test.body)) || event.RequestPayload.CapturedBytes != int64(len(test.body)) {
				t.Fatalf("request capture mismatch: %#v", event.RequestPayload)
			}
			if event.URL != "http://api.example.test/capture?tag=one&tag=two" || event.Host != "api.example.test" || event.RemoteAddr != "192.0.2.4:5123" || event.Protocol != "HTTP/1.1" {
				t.Fatalf("request context mismatch: %#v", event)
			}
			if event.RequestHeaders["X-Client"][0] != "original" || event.ResponseHeaders["X-Reply"][0] != "mock" {
				t.Fatalf("headers missing: req=%v resp=%v", event.RequestHeaders, event.ResponseHeaders)
			}
			if test.method == http.MethodPut && !bytes.Equal([]byte(strings.Join(event.RequestFormFields["tag"], ",")), []byte("one,two")) {
				t.Fatalf("repeated form fields = %#v", event.RequestFormFields)
			}
		})
	}
}

func TestHTTPLogCapturesMultipartAndFileMetadata(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("name", "first")
	_ = writer.WriteField("name", "second")
	partHeader := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="upload"; filename="note.txt"`}, "Content-Type": {"text/plain"}}
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("file bytes"))
	_ = writer.Close()

	srv := newHTTPLogTestServer(t, "")
	_, err = srv.Store.Create(Mock{Method: http.MethodPost, Path: "/upload", Status: http.StatusOK, Body: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	ch := srv.Bus.Subscribe()
	defer srv.Bus.Unsubscribe(ch)
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	srv.Mux.ServeHTTP(httptest.NewRecorder(), req)
	event := <-ch
	if strings.Join(event.RequestFormFields["name"], ",") != "first,second" {
		t.Fatalf("fields = %#v", event.RequestFormFields)
	}
	if len(event.RequestFiles) != 1 || event.RequestFiles[0].Name != "note.txt" || event.RequestFiles[0].SizeBytes != int64(len("file bytes")) || event.RequestFiles[0].CaptureStatus != "metadata_only" {
		t.Fatalf("files = %#v", event.RequestFiles)
	}
}

func TestURLFormFieldCaptureHasCountLimit(t *testing.T) {
	pairs := make([]string, MaxLogFormFields+1)
	for i := range pairs {
		pairs[i] = fmt.Sprintf("field%d=value", i)
	}
	fields, files, truncated := parseRequestForm([]byte(strings.Join(pairs, "&")), "application/x-www-form-urlencoded", false)
	if !truncated || len(files) != 0 || len(fields) != MaxLogFormFields {
		t.Fatalf("field limit mismatch: fields=%d files=%d truncated=%v", len(fields), len(files), truncated)
	}
}

func TestHTTPProxyAndMissEventsCaptureBodiesHeadersAndTarget(t *testing.T) {
	const reqBody = `{"forward":true}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if string(got) != reqBody {
			t.Errorf("upstream body = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "yes")
		_, _ = w.Write([]byte(`{"from":"upstream"}`))
	}))
	defer upstream.Close()
	srv := newHTTPLogTestServer(t, upstream.URL)
	ch := srv.Bus.Subscribe()
	defer srv.Bus.Unsubscribe(ch)
	req := httptest.NewRequest(http.MethodPatch, "http://client.test/proxied?q=1", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client", "preserved")
	srv.Mux.ServeHTTP(httptest.NewRecorder(), req)
	event := <-ch
	if event.Type != "PROXY" || event.Target != upstream.URL || event.RequestBody != reqBody || event.ResponseBody != `{"from":"upstream"}` {
		t.Fatalf("proxy log missing payload or target: %#v", event)
	}
	if event.URL != "http://client.test/proxied?q=1" || event.Path != "/proxied?q=1" {
		t.Fatalf("original absolute request URL/path changed: url=%q path=%q", event.URL, event.Path)
	}
	if event.RequestHeaders["X-Client"][0] != "preserved" || event.ResponseHeaders["X-Upstream"][0] != "yes" {
		t.Fatalf("proxy headers missing: %#v %#v", event.RequestHeaders, event.ResponseHeaders)
	}

	missServer := newHTTPLogTestServer(t, "")
	missEvents := missServer.Bus.Subscribe()
	defer missServer.Bus.Unsubscribe(missEvents)
	missReq := httptest.NewRequest(http.MethodDelete, "/missing", strings.NewReader("raw"))
	missReq.Header.Set("Content-Type", "text/plain")
	missServer.Mux.ServeHTTP(httptest.NewRecorder(), missReq)
	miss := <-missEvents
	if miss.Type != "MISS" || miss.RequestBody != "raw" || miss.ResponsePayload.CaptureStatus != "captured" || miss.ResponseHeaders["Content-Type"][0] != "application/json" {
		t.Fatalf("miss log missing capture: %#v", miss)
	}
}

type failReadBody struct{ sent bool }

func (b *failReadBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, errors.New("read failed")
	}
	b.sent = true
	return copy(p, "partial"), errors.New("read failed")
}
func (*failReadBody) Close() error { return nil }

func TestHTTPRequestReadErrorReturns400WithoutMatchingPartialBody(t *testing.T) {
	srv := newHTTPLogTestServer(t, "")
	_, err := srv.Store.Create(Mock{Method: http.MethodPost, Path: "/read-error", Status: http.StatusCreated, Body: json.RawMessage(`{"matched":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	ch := srv.Bus.Subscribe()
	defer srv.Bus.Unsubscribe(ch)
	req := httptest.NewRequest(http.MethodPost, "/read-error", nil)
	req.Body = &failReadBody{}
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	srv.Mux.ServeHTTP(rec, req)
	event := <-ch
	if rec.Code != http.StatusBadRequest || event.Status != http.StatusBadRequest || event.ResponseBody == `{"matched":true}` {
		t.Fatalf("partial body matched: status=%d event=%#v", rec.Code, event)
	}
	if event.RequestBody != "partial" || event.RequestPayload.CaptureStatus != "error" || !strings.Contains(event.Error, "read failed") {
		t.Fatalf("read error capture missing: %#v", event)
	}
}

func TestPayloadCaptureDecodesCompressedTextAndPreservesWireBytes(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(`{"gzip":true}`))
	_ = writer.Close()
	display, metadata := captureLogPayload(compressed.Bytes(), int64(compressed.Len()), "application/json", "gzip", nil)
	if display != `{"gzip":true}` || metadata.CaptureStatus != "captured" {
		t.Fatalf("display=%q metadata=%#v", display, metadata)
	}
	decoded, err := base64.StdEncoding.DecodeString(metadata.RawBase64)
	if err != nil || !bytes.Equal(decoded, compressed.Bytes()) {
		t.Fatalf("raw wire bytes changed: %v", err)
	}
}

func TestTruncatedGzipKeepsDecodedPrefixAndDeflateSupportsBothWrappers(t *testing.T) {
	body := make([]byte, MaxLogPayloadCaptureBytes-8)
	copy(body, "visible-prefix:")
	state := uint32(0x12345678)
	alphabet := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for i := len("visible-prefix:"); i < len(body); i++ {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		body[i] = alphabet[state%uint32(len(alphabet))]
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() <= MaxLogPayloadCaptureBytes {
		t.Fatalf("test gzip body compressed to %d bytes; want more than the capture limit", compressed.Len())
	}

	display, metadata := captureLogPayload(compressed.Bytes(), int64(compressed.Len()), "text/plain", "gzip", nil)
	if metadata.CaptureStatus != "truncated" || metadata.Error == "" || !utf8.ValidString(display) || len(display) == 0 {
		t.Fatalf("truncated request gzip lost its useful prefix: status=%s error=%q display-bytes=%d", metadata.CaptureStatus, metadata.Error, len(display))
	}
	if !strings.HasPrefix(display, "visible-prefix:") || metadata.CapturedBytes != MaxLogPayloadCaptureBytes {
		t.Fatalf("unexpected truncated gzip preview: bytes=%d prefix=%q", metadata.CapturedBytes, display[:min(16, len(display))])
	}

	capture := newResponseCapture(httptest.NewRecorder())
	capture.Header().Set("Content-Type", "text/plain")
	capture.Header().Set("Content-Encoding", "gzip")
	capture.WriteHeader(http.StatusOK)
	if _, err := capture.Write(compressed.Bytes()); err != nil {
		t.Fatal(err)
	}
	response, responseMeta := capture.responsePayload()
	if responseMeta.CaptureStatus != "truncated" || responseMeta.Error == "" || !utf8.ValidString(response) || len(response) == 0 {
		t.Fatalf("truncated response gzip lost its useful prefix: status=%s error=%q display-bytes=%d", responseMeta.CaptureStatus, responseMeta.Error, len(response))
	}

	for _, wrapped := range []struct {
		name string
		make func(*bytes.Buffer) (io.WriteCloser, error)
	}{
		{"zlib", func(b *bytes.Buffer) (io.WriteCloser, error) { return zlib.NewWriter(b), nil }},
		{"raw", func(b *bytes.Buffer) (io.WriteCloser, error) { return flate.NewWriter(b, flate.DefaultCompression) }},
	} {
		t.Run(wrapped.name, func(t *testing.T) {
			var encoded bytes.Buffer
			w, err := wrapped.make(&encoded)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("deflate text")); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			got, meta := captureLogPayload(encoded.Bytes(), int64(encoded.Len()), "text/plain", "deflate", nil)
			if got != "deflate text" || meta.CaptureStatus != "captured" {
				t.Fatalf("decoded %s deflate = %q, %#v", wrapped.name, got, meta)
			}
		})
	}
}

func TestResponseCaptureDecodesGzipAndMarksBinaryWithDownloadableBytes(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(`{"from":"gzip"}`))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	capture := newResponseCapture(recorder)
	capture.Header().Set("Content-Type", "application/json")
	capture.Header().Set("Content-Encoding", "gzip")
	capture.WriteHeader(http.StatusOK)
	_, _ = capture.Write(compressed.Bytes())
	display, metadata := capture.responsePayload()
	if display != `{"from":"gzip"}` || metadata.CaptureStatus != "captured" || metadata.Encoding != "gzip" {
		t.Fatalf("gzip response display=%q metadata=%#v", display, metadata)
	}
	raw, err := base64.StdEncoding.DecodeString(metadata.RawBase64)
	if err != nil || !bytes.Equal(raw, compressed.Bytes()) {
		t.Fatalf("gzip raw bytes changed: %v", err)
	}

	binary := newResponseCapture(httptest.NewRecorder())
	binary.Header().Set("Content-Type", "application/octet-stream")
	binary.WriteHeader(http.StatusOK)
	_, _ = binary.Write([]byte{0, 0xff, 1})
	body, metadata := binary.responsePayload()
	if metadata.CaptureStatus != "binary" || !strings.Contains(body, "binary response") || metadata.RawBase64 == "" {
		t.Fatalf("binary payload metadata/body = %#v, %q", metadata, body)
	}
}

func TestCaptureLimitRetainsFullTextRunesAndStreamsWholeResponse(t *testing.T) {
	writer := httptest.NewRecorder()
	capture := newResponseCapture(writer)
	capture.Header().Set("Content-Type", "text/plain; charset=utf-8")
	prefix := bytes.Repeat([]byte("a"), MaxLogPayloadCaptureBytes-2)
	body := append(prefix, []byte("🙂")...)
	n, err := capture.Write(body)
	if err != nil || n != len(body) || !bytes.Equal(writer.Body.Bytes(), body) {
		t.Fatalf("write changed streamed body: n=%d err=%v", n, err)
	}
	display, metadata := capture.responsePayload()
	if len(display) != MaxLogPayloadCaptureBytes-2 || !utf8.ValidString(display) || metadata.CaptureStatus != "truncated" || metadata.SizeBytes != int64(len(body)) || metadata.CapturedBytes != MaxLogPayloadCaptureBytes {
		t.Fatalf("truncated text metadata/display mismatch: status=%s bytes=%d/%d display=%d", metadata.CaptureStatus, metadata.CapturedBytes, metadata.SizeBytes, len(display))
	}
}

type flushErrorWriter struct {
	header   http.Header
	statuses []int
	flushes  int
	flushErr error
	body     bytes.Buffer
}

func (w *flushErrorWriter) Header() http.Header            { return w.header }
func (w *flushErrorWriter) WriteHeader(status int)         { w.statuses = append(w.statuses, status) }
func (w *flushErrorWriter) Write(data []byte) (int, error) { return w.body.Write(data) }
func (w *flushErrorWriter) FlushError() error              { w.flushes++; return w.flushErr }

func TestResponseCaptureFlushAndFinalHeaderSnapshot(t *testing.T) {
	wantErr := errors.New("flush failed")
	writer := &flushErrorWriter{header: make(http.Header), flushErr: wantErr}
	capture := newResponseCapture(writer)
	capture.Header().Set("X-Before", "saved")
	capture.WriteHeader(http.StatusEarlyHints)
	capture.WriteHeader(http.StatusCreated)
	capture.Header().Set("X-Before", "mutated")
	capture.WriteHeader(http.StatusAccepted)
	if err := capture.FlushError(); !errors.Is(err, wantErr) {
		t.Fatalf("FlushError() = %v", err)
	}
	if writer.flushes != 1 || capture.statusCode != http.StatusCreated || capture.responseHeaders().Get("X-Before") != "saved" {
		t.Fatalf("flush/status/header snapshot wrong: %#v", capture)
	}
	if len(writer.statuses) != 2 || writer.statuses[0] != http.StatusEarlyHints || writer.statuses[1] != http.StatusCreated {
		t.Fatalf("statuses = %v", writer.statuses)
	}
}
