// Package httpx holds small HTTP helpers shared by the proxy: hop-by-hop header
// handling, body capture within a limit, and header copying.
package httpx

import (
	"io"
	"net/http"
	"sort"
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

// CopyHeader appends every value of src to dst, replacing what dst had.
func CopyHeader(dst, src http.Header) {
	for k := range dst {
		delete(dst, k)
	}
	for k, vv := range src {
		dst[k] = append([]string(nil), vv...)
	}
}

// SortedNames returns a header's names in a stable order, for the API's JSON
// and for tests.
func SortedNames(h http.Header) []string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// ContentLengthIsKnown reports whether a message announces its body size.
func ContentLengthIsKnown(h http.Header) bool {
	return h.Get("Content-Length") != ""
}

// ReadBody reads a body up to limit bytes. It returns the bytes read and
// whether the body was longer than the limit; in that case the reader is
// drained and closed, since the caller cannot show what follows.
//
// A limit of 0 means no limit.
func ReadBody(r io.ReadCloser, limit int64) (body []byte, truncated bool, err error) {
	return ReadBodyTo(r, limit, nil)
}

// ReadBodyTo is ReadBody, keeping the part that ran past the limit in overflow
// so the caller can forward it untouched. With an overflow writer the reader is
// not closed by this function's caller path for the overflowing part; the
// overflow writer receives the rest.
func ReadBodyTo(r io.ReadCloser, limit int64, overflow io.Writer) (body []byte, truncated bool, err error) {
	if r == nil {
		return nil, false, nil
	}
	if limit <= 0 {
		body, err = io.ReadAll(r)
		return body, false, err
	}
	body, err = io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return body, false, err
	}
	// Peek one byte past the limit to learn whether there is more.
	var one [1]byte
	n, rerr := io.ReadFull(r, one[:])
	if n == 0 {
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			return body, false, nil
		}
		return body, false, rerr
	}
	if overflow != nil {
		if _, werr := overflow.Write(one[:n]); werr != nil {
			return body, true, werr
		}
		if _, werr := io.Copy(overflow, r); werr != nil {
			return body, true, werr
		}
		return body, true, nil
	}
	return body, true, nil
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
