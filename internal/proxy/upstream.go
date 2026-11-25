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
	mu     sync.RWMutex
	proxy  *httputil.ReverseProxy
	target string
}

// NewUpstream returns an upstream for target, or with no target when it is
// empty.
func NewUpstream(target string) *Upstream {
	u := &Upstream{}
	if target != "" {
		u.SetTarget(target)
	}
	return u
}

// SetTarget replaces the target. Proxied requests ask the target for
// uncompressed bodies so the log can show them.
func (u *Upstream) SetTarget(target string) error {
	targetURL, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid target URL: %w", err)
	}

	rp := httputil.NewSingleHostReverseProxy(targetURL)
	originalDirector := rp.Director
	rp.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = targetURL.Host
		// Ask upstream for plain bytes. ReverseProxy streams the response
		// through untouched, so a compressed body would reach the log and
		// "Save as mock" as binary garbage.
		req.Header.Set("Accept-Encoding", "identity")
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if cw, ok := w.(*capture.Writer); ok {
			cw.SetProxyError(err.Error())
		}
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
	}

	u.mu.Lock()
	u.proxy = rp
	u.target = target
	u.mu.Unlock()
	return nil
}

// Director returns the current reverse proxy's request director and target
// as one snapshot, or a nil director when no target is configured.
func (u *Upstream) Director() (func(*http.Request), string) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.proxy == nil || u.target == "" {
		return nil, ""
	}
	return u.proxy.Director, u.target
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
