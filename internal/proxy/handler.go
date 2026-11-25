package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ditto/internal/capture"
	"ditto/internal/eventlog"
	"ditto/internal/httpmock"
	"ditto/internal/socket"
)

// Handler serves every request outside /__ditto__/: WebSocket upgrades go
// to the socket hub (or are tunnelled to the target), matching mocks are
// answered locally, everything else is proxied to the target, and each
// request is logged with its captured bodies.
type Handler struct {
	Mocks    *httpmock.Store
	Upstream *Upstream
	Bus      *eventlog.Bus
	Hub      *socket.Hub
}

// ServeHTTP serves one request outside the dashboard.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/__ditto__/") {
		return
	}
	if socket.IsUpgrade(r) {
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if socket.WantsProxy(r) {
			if direct, target := h.Upstream.Router(); direct != nil {
				h.Hub.Tunnel(w, r, target, direct)
				return
			}
		}
		h.Hub.ServeHTTP(w, r)
		return
	}

	// Serve favicon at root
	if r.URL.Path == "/favicon.ico" {
		http.Redirect(w, r, "/__ditto__/favicon.png", http.StatusFound)
		return
	}

	start := time.Now()

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	requestHost, remoteAddr, protocol := r.Host, r.RemoteAddr, r.Proto
	requestURI := r.RequestURI
	if requestURI == "" {
		requestURI = r.URL.RequestURI()
	}
	requestPath := requestURI
	requestURL := scheme + "://" + requestHost + requestURI
	if originalURL, err := url.ParseRequestURI(requestURI); err == nil && originalURL.IsAbs() {
		requestURL = originalURL.String()
		requestPath = originalURL.RequestURI()
	}
	reqHeaders := r.Header.Clone()
	var reqBody []byte
	var reqReadErr error
	if r.Body != nil {
		reqBody, reqReadErr = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	requestBody, requestPayload := capture.Payload(reqBody, int64(len(reqBody)), reqHeaders.Get("Content-Type"), reqHeaders.Get("Content-Encoding"), reqReadErr)
	if r.Body == nil {
		requestPayload.CaptureStatus = "not_captured"
	}
	formFields, uploadedFiles, formTruncated := capture.Form(reqBody, reqHeaders.Get("Content-Type"), reqReadErr != nil)
	requestContext := eventlog.Event{URL: requestURL, Host: requestHost, RemoteAddr: remoteAddr, Protocol: protocol,
		RequestBody: requestBody, RequestPayload: requestPayload, RequestHeaders: reqHeaders,
		RequestFormFields: formFields, RequestFiles: uploadedFiles, RequestFormsTruncated: formTruncated}
	if reqReadErr != nil {
		cw := capture.NewWriter(w)
		cw.Header().Set("Content-Type", "application/json")
		cw.WriteHeader(http.StatusBadRequest)
		_, _ = cw.Write([]byte(`{"error":"failed to read request body"}`))
		responseBody, responsePayload := cw.Payload()
		event := requestContext
		event.Type, event.Method, event.Path, event.Status = "MISS", r.Method, requestPath, http.StatusBadRequest
		event.DurationMs, event.ResponseBody, event.ResponsePayload = time.Since(start).Milliseconds(), responseBody, responsePayload
		event.ResponseHeaders, event.Error = cw.Headers(), reqReadErr.Error()
		h.Bus.Publish(event)
		return
	}

	resolved := h.Mocks.Resolve(r, reqBody)
	if resolved != nil {
		if resolved.DelayMs > 0 {
			time.Sleep(time.Duration(resolved.DelayMs) * time.Millisecond)
		}
		cw := capture.NewWriter(w)
		for k, v := range resolved.Headers {
			cw.Header().Set(k, v)
		}
		if cw.Header().Get("Content-Type") == "" {
			cw.Header().Set("Content-Type", "application/json")
		}
		cw.WriteHeader(resolved.Status)
		_, _ = cw.Write(resolved.Body)
		responseBody, responsePayload := cw.Payload()

		event := requestContext
		event.Type, event.Method, event.Path = "MOCK", r.Method, requestPath
		event.Status, event.DurationMs, event.MockID = cw.Status(), time.Since(start).Milliseconds(), resolved.MockID
		event.ResponseBody, event.ResponsePayload, event.ResponseHeaders = responseBody, responsePayload, cw.Headers()
		if cw.WriteErr() != nil {
			event.Error = cw.WriteErr().Error()
		}
		if resolved.IsSequence {
			event.SequenceStep = resolved.SequenceStep
			event.SequenceLen = resolved.SequenceLen
		}
		h.Bus.Publish(event)
		return
	}

	cw := capture.NewWriter(w)
	served, target := h.Upstream.Serve(cw, r)
	if served {
		responseBody, responsePayload := cw.Payload()

		event := requestContext
		event.Type, event.Method, event.Path, event.Status = "PROXY", r.Method, requestPath, cw.Status()
		event.DurationMs, event.ResponseBody, event.ResponsePayload = time.Since(start).Milliseconds(), responseBody, responsePayload
		event.ResponseHeaders, event.Target = cw.Headers(), target
		if cw.ProxyError() != "" {
			event.Error = cw.ProxyError()
		}
		if cw.WriteErr() != nil {
			event.Error = cw.WriteErr().Error()
		}
		h.Bus.Publish(event)
		return
	}

	cw = capture.NewWriter(w)
	cw.Header().Set("Content-Type", "application/json")
	cw.WriteHeader(http.StatusBadGateway)
	_, _ = cw.Write([]byte(`{"error": "no mock found and no target configured"}`))
	responseBody, responsePayload := cw.Payload()

	event := requestContext
	event.Type, event.Method, event.Path, event.Status = "MISS", r.Method, requestPath, http.StatusBadGateway
	event.DurationMs, event.ResponseBody, event.ResponsePayload = time.Since(start).Milliseconds(), responseBody, responsePayload
	event.ResponseHeaders = cw.Headers()
	if cw.WriteErr() != nil {
		event.Error = cw.WriteErr().Error()
	}
	if reqReadErr != nil {
		event.Error = "request body: " + reqReadErr.Error()
	}
	h.Bus.Publish(event)
}
