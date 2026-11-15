package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/session"
)

// The list is filtered on the server because its caller is often a program
// paying for every byte it reads: two hundred rows of JSON is most of an
// agent's context, and the answer to "were there any 500s" is usually three
// rows of it.
//
// Every filter is optional and they combine with AND, except that a value with
// a comma is a list of alternatives.

// listFilter is what a caller asked the list for.
type listFilter struct {
	offset, limit int
	since         time.Time
	host, url     []string
	status        []string
	method        []string
	// fields names the columns to keep; empty means all of them.
	fields []string
	// table asks for rows as arrays under a header, which costs about 40% fewer
	// tokens than an object per row.
	table bool
}

// parseListFilter reads the query of a list request.
func parseListFilter(r *http.Request) (listFilter, error) {
	q := r.URL.Query()
	f := listFilter{
		offset: intParam(r, "offset", 0),
		limit:  intParam(r, "limit", 20),
		table:  q.Get("shape") == "table",
		host:   splitList(q.Get("host")),
		url:    splitList(q.Get("url")),
		status: splitList(q.Get("status")),
		method: splitList(q.Get("method")),
		fields: splitList(q.Get("fields")),
	}
	if f.limit == 0 || f.limit < 0 {
		f.limit = 20
	}
	// A caller that asks for a lot gets a lot, but not without bound: an
	// unbounded list is how a capture fills somebody's context by accident.
	if f.limit > maxListLimit {
		f.limit = maxListLimit
	}
	if s := q.Get("since"); s != "" {
		d, err := parseWindow(s)
		if err != nil {
			return f, err
		}
		f.since = time.Now().Add(-d)
	}
	return f, nil
}

// maxListLimit bounds one page.
const maxListLimit = 1000

// parseWindow reads "30s", "5m", "2h" or a plain number of seconds.
func parseWindow(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return 0, &filterError{"since must be a duration such as 30s, 5m or 2h"}
}

// splitList reads a comma-separated query value.
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// matches reports whether a session passes the filter.
func (f *listFilter) matches(s *session.Snapshot) bool {
	if !f.since.IsZero() && s.Timings.Start.Before(f.since) {
		return false
	}
	if !anyMatch(f.host, hostOf(s), false) {
		return false
	}
	if !anyMatch(f.method, methodOfSession(s), true) {
		return false
	}
	if !anyMatch(f.url, urlOf(s), true) {
		return false
	}
	status := 0
	if s.Response != nil {
		status = s.Response.Status
	}
	return f.matchStatus(status)
}

// matchStatus checks the status family: "5xx" is every status from 500 to 599,
// "404" one code, and "2xx,404" either.
func (f *listFilter) matchStatus(status int) bool {
	if len(f.status) == 0 {
		return true
	}
	for _, p := range f.status {
		// A family is three characters ending in "xx".
		if len(p) == 3 && strings.HasSuffix(strings.ToLower(p), "xx") {
			first := p[0]
			if first >= '1' && first <= '5' && status/100 == int(first-'0') {
				return true
			}
			continue
		}
		if n, err := strconv.Atoi(p); err == nil && n == status {
			return true
		}
	}
	return false
}

// anyMatch reports whether value matches one of the patterns. Without patterns
// everything matches; a pattern may be a glob with * and ?.
func anyMatch(patterns []string, value string, equalFold bool) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if globMatch(p, value, equalFold) {
			return true
		}
	}
	return false
}

// globMatch matches a pattern with * and ? against a value.
func globMatch(pattern, value string, equalFold bool) bool {
	if equalFold {
		pattern, value = strings.ToLower(pattern), strings.ToLower(value)
	}
	// A plain string is a substring test for URLs, so that url=/login finds
	// https://api.test/v1/login without the caller spelling out the wildcards.
	if !strings.ContainsAny(pattern, "*?") {
		return strings.Contains(value, pattern)
	}
	return wildcardMatch(pattern, value)
}

// wildcardMatch is the usual glob walk with backtracking, iterative so that a
// pattern of many stars cannot recurse deeply.
func wildcardMatch(pattern, s string) bool {
	var pi, si, star, mark int
	star = -1
	for si < len(s) {
		switch {
		case pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == s[si]):
			pi++
			si++
		case pi < len(pattern) && pattern[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			mark++
			pi, si = star+1, mark
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// filterError is a bad query value, which the list answers as a 400.
type filterError struct{ msg string }

func (e *filterError) Error() string { return e.msg }

// hostOf returns a snapshot's host.
func hostOf(s *session.Snapshot) string {
	if s.Request != nil && s.Request.URL != nil {
		return s.Request.URL.Host
	}
	return ""
}

// urlOf returns a snapshot's URL.
func urlOf(s *session.Snapshot) string {
	if s.Request != nil && s.Request.URL != nil {
		return s.Request.URL.String()
	}
	return ""
}

// methodOfSession returns a snapshot's method.
func methodOfSession(s *session.Snapshot) string {
	if s.Request != nil {
		return s.Request.Method
	}
	return ""
}
