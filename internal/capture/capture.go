// Package capture records request and response bodies for the event log
// without buffering whole streams: it keeps a bounded prefix, decodes gzip
// and deflate, and summarises binary payloads.
package capture

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"ditto/internal/eventlog"
)

// MaxBytes is the largest body prefix kept for the log, before and after
// decompression.
const MaxBytes = 1 << 20

// MaxFormFields caps the form fields and multipart parts parsed for the log.
const MaxFormFields = 1000

// Writer wraps a ResponseWriter, streaming everything through while keeping
// the final status, headers and at most MaxBytes of the body for the log.
type Writer struct {
	http.ResponseWriter
	statusCode    int
	headerWritten bool
	headers       http.Header
	body          bytes.Buffer
	totalBytes    int64
	writeErr      error
	proxyError    string
}

func (rc *Writer) WriteHeader(code int) {
	final := code >= http.StatusOK || code == http.StatusSwitchingProtocols
	if final && rc.headerWritten {
		return
	}
	if final {
		rc.statusCode = code
		rc.headerWritten = true
		rc.headers = rc.ResponseWriter.Header().Clone()
	}
	rc.ResponseWriter.WriteHeader(code)
}

func (rc *Writer) Write(b []byte) (int, error) {
	if !rc.headerWritten {
		rc.statusCode, rc.headerWritten = http.StatusOK, true
		rc.headers = rc.ResponseWriter.Header().Clone()
	}
	n, err := rc.ResponseWriter.Write(b)
	if n > 0 {
		rc.totalBytes += int64(n)
		remaining := MaxBytes - rc.body.Len()
		if remaining > 0 {
			if n < remaining {
				remaining = n
			}
			_, _ = rc.body.Write(b[:remaining])
		}
	}
	if err != nil {
		rc.writeErr = err
	}
	return n, err
}

// NewWriter wraps w.
func NewWriter(w http.ResponseWriter) *Writer {
	return &Writer{ResponseWriter: w, statusCode: http.StatusOK}
}

// Unwrap lets http.ResponseController reach optional transport capabilities.
func (rc *Writer) Unwrap() http.ResponseWriter { return rc.ResponseWriter }

func (rc *Writer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(rc.ResponseWriter).Hijack()
}

func (rc *Writer) Flush() {
	_ = rc.FlushError()
}

func (rc *Writer) FlushError() error {
	if !rc.headerWritten {
		rc.statusCode, rc.headerWritten = http.StatusOK, true
		rc.headers = rc.ResponseWriter.Header().Clone()
	}
	err := http.NewResponseController(rc.ResponseWriter).Flush()
	if err != nil {
		rc.writeErr = err
	}
	return err
}

// Status returns the final status code sent, or 200 when none was written.
func (rc *Writer) Status() int { return rc.statusCode }

// WriteErr returns the last error from writing to or flushing the client.
func (rc *Writer) WriteErr() error { return rc.writeErr }

// SetProxyError records the reverse proxy's upstream error for the log.
func (rc *Writer) SetProxyError(msg string) { rc.proxyError = msg }

// ProxyError returns the error recorded by SetProxyError.
func (rc *Writer) ProxyError() string { return rc.proxyError }

// Headers returns the response headers as they were when the final status
// was written.
func (rc *Writer) Headers() http.Header {
	if rc.headers != nil {
		return rc.headers.Clone()
	}
	return rc.ResponseWriter.Header().Clone()
}

// decodedBody returns the captured body as text suitable for the log stream
// and for "Save as mock". Upstreams are asked for identity encoding, but some
// compress anyway, so gzip/deflate are decoded here as a fallback. Anything
// that still isn't valid UTF-8 is binary and is summarised rather than dumped
// as replacement characters.
func (rc *Writer) decodedBody() string {
	body, _, _ := rc.decodedBodyInfo()
	return body
}

func (rc *Writer) decodedBodyInfo() (string, bool, error) {
	headers := rc.Headers()
	wire := rc.body.Bytes()
	if len(wire) == 0 {
		return "", false, nil
	}
	wireTruncated := rc.totalBytes > int64(len(wire))
	raw, supported, decodedTruncated, decodeErr := decodePayload(wire, headers.Get("Content-Encoding"))
	truncated := wireTruncated || decodedTruncated
	if decodeErr != nil && !truncated {
		return "", truncated, decodeErr
	}
	if !supported {
		return fmt.Sprintf("<binary response: %d bytes, content-type=%q, content-encoding=%q>",
			rc.totalBytes, headers.Get("Content-Type"), headers.Get("Content-Encoding")), truncated, nil
	}
	if truncated && isTextContentType(headers.Get("Content-Type")) && !utf8.Valid(raw) {
		if prefix, ok := incompleteUTF8Prefix(raw); ok {
			raw = prefix
		}
	}
	if !utf8.Valid(raw) || !isTextContentType(headers.Get("Content-Type")) {
		return fmt.Sprintf("<binary response: %d bytes, content-type=%q, content-encoding=%q>",
			rc.totalBytes, headers.Get("Content-Type"), headers.Get("Content-Encoding")), truncated, nil
	}
	return string(raw), truncated, decodeErr
}

func decodePayload(raw []byte, encoding string) ([]byte, bool, bool, error) {
	var reader io.ReadCloser
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return raw, true, false, nil
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, true, false, err
		}
		reader = zr
	case "deflate":
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err == nil {
			reader = zr
		} else {
			reader = flate.NewReader(bytes.NewReader(raw))
		}
	default:
		return raw, false, false, nil
	}
	out, readErr := io.ReadAll(io.LimitReader(reader, MaxBytes+1))
	closeErr := reader.Close()
	truncated := len(out) > MaxBytes
	if truncated {
		out = out[:MaxBytes]
	}
	if readErr != nil {
		return out, true, truncated, readErr
	}
	if closeErr != nil {
		return out, true, truncated, closeErr
	}
	return out, true, truncated, nil
}

func incompleteUTF8Prefix(data []byte) ([]byte, bool) {
	start := len(data)
	for start > 0 && len(data)-start < utf8.UTFMax {
		start--
		if utf8.Valid(data[:start]) && !utf8.FullRune(data[start:]) {
			return data[:start], true
		}
	}
	return data, false
}

func isTextContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return contentType == ""
	}
	mediaType = strings.ToLower(mediaType)
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" ||
		strings.HasSuffix(mediaType, "+json") || mediaType == "application/xml" ||
		strings.HasSuffix(mediaType, "+xml") || mediaType == "application/javascript" ||
		mediaType == "application/x-www-form-urlencoded" || mediaType == "multipart/form-data"
}

// Payload returns the captured response body as display text and its
// capture metadata (size, encoding, truncation, raw bytes).
func (rc *Writer) Payload() (string, *eventlog.Payload) {
	captured := rc.body.Bytes()
	body, decodedTruncated, decodeErr := rc.decodedBodyInfo()
	headers := rc.Headers()
	meta := &eventlog.Payload{SizeBytes: rc.totalBytes, CapturedBytes: int64(len(captured)),
		ContentType: headers.Get("Content-Type"), Encoding: headers.Get("Content-Encoding"),
		RawBase64: base64.StdEncoding.EncodeToString(captured)}
	switch {
	case decodeErr != nil && !decodedTruncated && rc.totalBytes <= int64(len(captured)):
		meta.CaptureStatus, meta.Error = "error", decodeErr.Error()
	case rc.writeErr != nil:
		meta.CaptureStatus, meta.Error = "error", rc.writeErr.Error()
	case rc.totalBytes == 0:
		meta.CaptureStatus = "empty"
	case decodedTruncated || rc.totalBytes > int64(len(captured)):
		meta.CaptureStatus = "truncated"
		if decodeErr != nil {
			meta.Error = decodeErr.Error()
		}
	case decodeErr != nil:
		meta.CaptureStatus, meta.Error = "error", decodeErr.Error()
	case strings.HasPrefix(body, "<binary response:"):
		meta.CaptureStatus = "binary"
	default:
		meta.CaptureStatus = "captured"
	}
	return body, meta
}

// Payload describes a request body for the log: raw is the captured prefix
// of a body that was total bytes long, and captureErr is any error reading it.
func Payload(raw []byte, total int64, contentType, encoding string, captureErr error) (string, *eventlog.Payload) {
	captured := raw
	truncated := total > int64(len(captured))
	if len(captured) > MaxBytes {
		captured, truncated = captured[:MaxBytes], true
	}
	displayBytes, supported, decodedTruncated, decodeErr := decodePayload(captured, encoding)
	truncated = truncated || decodedTruncated
	meta := &eventlog.Payload{SizeBytes: total, CapturedBytes: int64(len(captured)), ContentType: contentType,
		Encoding: encoding, RawBase64: base64.StdEncoding.EncodeToString(captured)}
	switch {
	case captureErr != nil:
		meta.CaptureStatus, meta.Error = "error", captureErr.Error()
	case total == 0:
		meta.CaptureStatus = "empty"
	case decodeErr != nil && !truncated && captureErr == nil:
		meta.CaptureStatus, meta.Error = "error", decodeErr.Error()
	case !supported:
		meta.CaptureStatus = "binary"
	case truncated:
		meta.CaptureStatus = "truncated"
	case !utf8.Valid(displayBytes) || !isTextContentType(contentType):
		meta.CaptureStatus = "binary"
	default:
		meta.CaptureStatus = "captured"
	}
	if decodeErr != nil && meta.Error == "" {
		meta.Error = decodeErr.Error()
	}
	if meta.CaptureStatus == "binary" {
		return fmt.Sprintf("<binary request: %d bytes, content-type=%q, content-encoding=%q>", total, contentType, encoding), meta
	}
	if (truncated || captureErr != nil) && isTextContentType(contentType) && !utf8.Valid(displayBytes) {
		if prefix, ok := incompleteUTF8Prefix(displayBytes); ok {
			displayBytes = prefix
		}
	}
	if !utf8.Valid(displayBytes) {
		return "", meta
	}
	return string(displayBytes), meta
}

// Form parses URL-encoded or multipart form bodies into fields and file
// metadata, reporting whether limits or an incomplete body truncated them.
// Other content types return nothing.
func Form(body []byte, contentType string, inputIncomplete bool) (map[string][]string, []eventlog.FileMeta, bool) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, nil, false
	}
	switch mediaType {
	case "application/x-www-form-urlencoded":
		captured := body[:min(len(body), MaxBytes)]
		truncated := inputIncomplete || len(captured) < len(body)
		fieldCount := 1
		for i, b := range captured {
			if b == '&' {
				fieldCount++
				if fieldCount > MaxFormFields {
					captured, truncated = captured[:i], true
					break
				}
			}
		}
		values, err := url.ParseQuery(string(captured))
		if err != nil && !truncated {
			return nil, nil, false
		}
		return map[string][]string(values), nil, truncated
	case "multipart/form-data":
		boundary := params["boundary"]
		if boundary == "" {
			return nil, nil, false
		}
		reader := multipart.NewReader(bytes.NewReader(body), boundary)
		fields := make(map[string][]string)
		fieldBytesRemaining := MaxBytes
		fieldCount := 0
		partCount := 0
		formsTruncated := inputIncomplete
		var files []eventlog.FileMeta
		for {
			part, err := reader.NextPart()
			if err != nil {
				if err != io.EOF {
					formsTruncated = true
				}
				break
			}
			partCount++
			if partCount > MaxFormFields {
				formsTruncated = true
				_ = part.Close()
				break
			}
			if name := part.FileName(); name != "" {
				size, readErr := io.Copy(io.Discard, part)
				status := "metadata_only"
				if readErr != nil {
					status = "error"
				}
				files = append(files, eventlog.FileMeta{Name: name, ContentType: part.Header.Get("Content-Type"),
					SizeBytes: size, CapturedBytes: 0, CaptureStatus: status})
				_ = part.Close()
				continue
			}
			if fieldCount >= MaxFormFields {
				formsTruncated = true
				_ = part.Close()
				break
			}
			if fieldBytesRemaining <= 0 {
				formsTruncated = true
				_ = part.Close()
				continue
			}
			partData, readErr := io.ReadAll(io.LimitReader(part, int64(fieldBytesRemaining)+1))
			_ = part.Close()
			if len(partData) > fieldBytesRemaining {
				partData = partData[:fieldBytesRemaining]
				formsTruncated = true
			}
			if readErr != nil {
				formsTruncated = true
				continue
			}
			fields[part.FormName()] = append(fields[part.FormName()], string(partData))
			fieldCount++
			fieldBytesRemaining -= len(partData)
		}
		if len(fields) == 0 {
			fields = nil
		}
		return fields, files, formsTruncated
	}
	return nil, nil, false
}
