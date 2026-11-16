// Package httpx holds small HTTP helpers shared by the proxy: hop-by-hop header
// handling, body capture within a limit, and header copying.
package httpx

import (
	"net/http"
	"strings"
)

// hopHeaders are the headers that belong to one connection and must not be
// forwarded by a proxy (RFC 9110 §7.6.1). Connection's own value names more.
var hopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// RemoveHopHeaders deletes the hop-by-hop headers from h, including those that
// the Connection header names.
func RemoveHopHeaders(h http.Header) {
	for _, name := range h["Connection"] {
		for _, field := range strings.Split(name, ",") {
			if field = strings.TrimSpace(field); field != "" {
				h.Del(field)
			}
		}
	}
	for _, name := range hopHeaders {
		h.Del(name)
	}
}

// CloneWithoutHopHeaders returns a copy of h without the hop-by-hop headers, in
// one pass: the proxy needs this for every request it forwards, so it does not
// build a full copy and then delete from it.
func CloneWithoutHopHeaders(h http.Header) http.Header {
	// The fields Connection names are hop-by-hop too, so collect them first.
	var named map[string]bool
	for _, v := range h["Connection"] {
		for _, field := range strings.Split(v, ",") {
			if field = strings.TrimSpace(field); field != "" {
				if named == nil {
					named = make(map[string]bool, 4)
				}
				named[http.CanonicalHeaderKey(field)] = true
			}
		}
	}
	out := make(http.Header, len(h))
	for k, vv := range h {
		if named[k] || isHopHeader(k) {
			continue
		}
		out[k] = append([]string(nil), vv...)
	}
	return out
}

// isHopHeader reports whether a canonical header name is hop-by-hop.
func isHopHeader(name string) bool {
	switch name {
	case "Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	}
	return false
}

// CopyHeader replaces dst's contents with src's.
//
// The values are appended into dst's existing slices where they fit, so that a
// response written per request does not allocate a header map and a slice per
// header: the profiler showed this as a few percent of the core's garbage.
func CopyHeader(dst, src http.Header) {
	for k := range dst {
		if _, ok := src[k]; !ok {
			delete(dst, k)
		}
	}
	for k, vv := range src {
		existing, ok := dst[k]
		if !ok {
			// No entry to reuse: copy the values so that dst does not alias
			// the message the caller still owns.
			dst[k] = append(make([]string, 0, len(vv)), vv...)
			continue
		}
		existing = existing[:0]
		dst[k] = append(existing, vv...)
	}
}

// IsUpgrade reports whether a request asks to upgrade the connection, as
// WebSocket does.
func IsUpgrade(h http.Header) bool {
	return headerHasToken(h, "Connection", "upgrade") && h.Get("Upgrade") != ""
}

// headerHasToken reports whether a comma-separated header contains token.
func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h[name] {
		for _, field := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(field), token) {
				return true
			}
		}
	}
	return false
}
