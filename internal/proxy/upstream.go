// Package proxy is Ditto's catch-all HTTP handler: it serves matching
// mocks, proxies everything else to the configured backend, and logs both.
package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	"ditto/internal/capture"
)

// Upstream is the HTTP backend that unmatched requests are proxied to. Its
// target can change at runtime.
type Upstream struct {
	mu        sync.RWMutex
	proxy     *httputil.ReverseProxy
	target    string
	targetURL *url.URL
}

// NewUpstream returns an upstream for target, or with no target when it is
// empty.
func NewUpstream(target string) (*Upstream, error) {
	u := &Upstream{}
	if target != "" {
		if err := u.SetTarget(target); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// SetTarget replaces the target.
func (u *Upstream) SetTarget(target string) error {
	targetURL, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid target URL: %w", err)
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// Keep the client's X-Forwarded-For chain and append its address.
			pr.Out.Header["X-Forwarded-For"] = pr.In.Header["X-Forwarded-For"]
			pr.SetXForwarded()
			route(pr.Out, targetURL)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if cw, ok := w.(*capture.Writer); ok {
				cw.SetProxyError(err.Error())
			}
			http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		},
	}

	u.mu.Lock()
	u.proxy, u.target, u.targetURL = rp, target, targetURL
	u.mu.Unlock()
	return nil
}

// route points out at target, joining target's base path and query with the
// request's, and asks for an uncompressed body: ReverseProxy streams the
// response through untouched, so a compressed body would reach the log and
// "Save as mock" as binary.
func route(out *http.Request, target *url.URL) {
	(&httputil.ProxyRequest{Out: out}).SetURL(target)
	out.Header.Set("Accept-Encoding", "identity")
}

// Router returns a function that points an outgoing request at the current
// target, and that target, taken as one snapshot; the function is nil when
// no target is set. The WebSocket tunnel uses it to dial the target.
func (u *Upstream) Router() (func(out *http.Request), string) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.targetURL == nil || u.target == "" {
		return nil, ""
	}
	targetURL := u.targetURL
	return func(out *http.Request) { route(out, targetURL) }, u.target
}

// Target returns the current target URL, or "".
func (u *Upstream) Target() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.target
}

// Serve proxies r to the target and reports the target it used, taken in
// one snapshot so the request and its log agree when the target changes
// concurrently. It returns false, writing nothing, when no target is set.
func (u *Upstream) Serve(w http.ResponseWriter, r *http.Request) (bool, string) {
	u.mu.RLock()
	rp := u.proxy
	target := u.target
	u.mu.RUnlock()

	if rp == nil {
		return false, ""
	}
	rp.ServeHTTP(w, r)
	return true, target
}
