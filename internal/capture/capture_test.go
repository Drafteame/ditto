package capture

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestURLFormFieldCaptureHasCountLimit(t *testing.T) {
	pairs := make([]string, MaxFormFields+1)
	for i := range pairs {
		pairs[i] = fmt.Sprintf("field%d=value", i)
	}
	fields, files, truncated := Form([]byte(strings.Join(pairs, "&")), "application/x-www-form-urlencoded", false)
	if !truncated || len(files) != 0 || len(fields) != MaxFormFields {
		t.Fatalf("field limit mismatch: fields=%d files=%d truncated=%v", len(fields), len(files), truncated)
	}
}

func TestPayloadCaptureDecodesCompressedTextAndPreservesWireBytes(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(`{"gzip":true}`))
	_ = writer.Close()
	display, metadata := Payload(compressed.Bytes(), int64(compressed.Len()), "application/json", "gzip", nil)
	if display != `{"gzip":true}` || metadata.CaptureStatus != "captured" {
		t.Fatalf("display=%q metadata=%#v", display, metadata)
	}
	decoded, err := base64.StdEncoding.DecodeString(metadata.RawBase64)
	if err != nil || !bytes.Equal(decoded, compressed.Bytes()) {
		t.Fatalf("raw wire bytes changed: %v", err)
	}
}

func TestTruncatedGzipKeepsDecodedPrefixAndDeflateSupportsBothWrappers(t *testing.T) {
	body := make([]byte, MaxBytes-8)
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
	if compressed.Len() <= MaxBytes {
		t.Fatalf("test gzip body compressed to %d bytes; want more than the capture limit", compressed.Len())
	}

	display, metadata := Payload(compressed.Bytes(), int64(compressed.Len()), "text/plain", "gzip", nil)
	if metadata.CaptureStatus != "truncated" || metadata.Error == "" || !utf8.ValidString(display) || len(display) == 0 {
		t.Fatalf("truncated request gzip lost its useful prefix: status=%s error=%q display-bytes=%d", metadata.CaptureStatus, metadata.Error, len(display))
	}
	if !strings.HasPrefix(display, "visible-prefix:") || metadata.CapturedBytes != MaxBytes {
		t.Fatalf("unexpected truncated gzip preview: bytes=%d prefix=%q", metadata.CapturedBytes, display[:min(16, len(display))])
	}

	capture := NewWriter(httptest.NewRecorder())
	capture.Header().Set("Content-Type", "text/plain")
	capture.Header().Set("Content-Encoding", "gzip")
	capture.WriteHeader(http.StatusOK)
	if _, err := capture.Write(compressed.Bytes()); err != nil {
		t.Fatal(err)
	}
	response, responseMeta := capture.Payload()
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
			got, meta := Payload(encoded.Bytes(), int64(encoded.Len()), "text/plain", "deflate", nil)
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
	capture := NewWriter(recorder)
	capture.Header().Set("Content-Type", "application/json")
	capture.Header().Set("Content-Encoding", "gzip")
	capture.WriteHeader(http.StatusOK)
	_, _ = capture.Write(compressed.Bytes())
	display, metadata := capture.Payload()
	if display != `{"from":"gzip"}` || metadata.CaptureStatus != "captured" || metadata.Encoding != "gzip" {
		t.Fatalf("gzip response display=%q metadata=%#v", display, metadata)
	}
	raw, err := base64.StdEncoding.DecodeString(metadata.RawBase64)
	if err != nil || !bytes.Equal(raw, compressed.Bytes()) {
		t.Fatalf("gzip raw bytes changed: %v", err)
	}

	binary := NewWriter(httptest.NewRecorder())
	binary.Header().Set("Content-Type", "application/octet-stream")
	binary.WriteHeader(http.StatusOK)
	_, _ = binary.Write([]byte{0, 0xff, 1})
	body, metadata := binary.Payload()
	if metadata.CaptureStatus != "binary" || !strings.Contains(body, "binary response") || metadata.RawBase64 == "" {
		t.Fatalf("binary payload metadata/body = %#v, %q", metadata, body)
	}
}

func TestCaptureLimitRetainsFullTextRunesAndStreamsWholeResponse(t *testing.T) {
	writer := httptest.NewRecorder()
	capture := NewWriter(writer)
	capture.Header().Set("Content-Type", "text/plain; charset=utf-8")
	prefix := bytes.Repeat([]byte("a"), MaxBytes-2)
	body := append(prefix, []byte("🙂")...)
	n, err := capture.Write(body)
	if err != nil || n != len(body) || !bytes.Equal(writer.Body.Bytes(), body) {
		t.Fatalf("write changed streamed body: n=%d err=%v", n, err)
	}
	display, metadata := capture.Payload()
	if len(display) != MaxBytes-2 || !utf8.ValidString(display) || metadata.CaptureStatus != "truncated" || metadata.SizeBytes != int64(len(body)) || metadata.CapturedBytes != MaxBytes {
		t.Fatalf("truncated text metadata/display mismatch: status=%s bytes=%d/%d display=%d", metadata.CaptureStatus, metadata.CapturedBytes, metadata.SizeBytes, len(display))
	}
}

func TestResponseCaptureFlushAndFinalHeaderSnapshot(t *testing.T) {
	wantErr := errors.New("flush failed")
	writer := &flushErrorWriter{header: make(http.Header), flushErr: wantErr}
	capture := NewWriter(writer)
	capture.Header().Set("X-Before", "saved")
	capture.WriteHeader(http.StatusEarlyHints)
	capture.WriteHeader(http.StatusCreated)
	capture.Header().Set("X-Before", "mutated")
	capture.WriteHeader(http.StatusAccepted)
	if err := capture.FlushError(); !errors.Is(err, wantErr) {
		t.Fatalf("FlushError() = %v", err)
	}
	if writer.flushes != 1 || capture.statusCode != http.StatusCreated || capture.Headers().Get("X-Before") != "saved" {
		t.Fatalf("flush/status/header snapshot wrong: %#v", capture)
	}
	if len(writer.statuses) != 2 || writer.statuses[0] != http.StatusEarlyHints || writer.statuses[1] != http.StatusCreated {
		t.Fatalf("statuses = %v", writer.statuses)
	}
}

func TestDecodedBodyDecompressesGzip(t *testing.T) {
	rec := httptest.NewRecorder()
	capture := &Writer{ResponseWriter: rec, statusCode: 200}
	capture.Header().Set("Content-Type", "application/json")
	capture.Header().Set("Content-Encoding", "gzip")

	gz := gzip.NewWriter(capture)
	gz.Write([]byte(`{"hola":"mundo","n":123}`))
	gz.Close()

	if got := capture.decodedBody(); got != `{"hola":"mundo","n":123}` {
		t.Errorf("decodedBody() = %q, want the decompressed JSON", got)
	}
}

func TestDecodedBodySummarisesBinary(t *testing.T) {
	capture := &Writer{ResponseWriter: httptest.NewRecorder(), statusCode: 200}
	capture.Header().Set("Content-Type", "image/png")
	capture.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe, 0xfd})

	got := capture.decodedBody()
	if !strings.HasPrefix(got, "<binary response:") {
		t.Errorf("decodedBody() = %q, want a binary summary", got)
	}
	if strings.ContainsRune(got, '�') {
		t.Errorf("decodedBody() leaked replacement characters: %q", got)
	}
}

func TestDecodedBodyPassesThroughPlainText(t *testing.T) {
	capture := &Writer{ResponseWriter: httptest.NewRecorder(), statusCode: 200}
	capture.Header().Set("Content-Type", "application/json")
	capture.Write([]byte(`{"acentos":"ñ á é í ó ú"}`))

	if got := capture.decodedBody(); got != `{"acentos":"ñ á é í ó ú"}` {
		t.Errorf("decodedBody() = %q", got)
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
