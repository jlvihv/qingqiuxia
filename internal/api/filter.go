package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/session"
)

// 过滤在服务端做，因为调用方往往是按字节付费的程序：两百行 JSON 就是 AI 大半个
// 上下文，而"有没有 500"这个问题的答案通常只有三行。
//
// 每个过滤条件都是可选的，彼此之间是"且"；带逗号的值是一组备选，任意一个命中
// 即可。

// listFilter 是一次列表查询要了什么。
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

// parseListFilter 解析列表请求的查询参数。
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
	// 要得多就给得多，但有上限：没有上限的列表，是抓包一不小心淹掉别人上下文
	// 的方式。
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

// maxListLimit 是一页的上限。
const maxListLimit = 1000

// parseWindow 接受 "30s"、"5m"、"2h"，或者纯数字（秒）。
func parseWindow(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return 0, &filterError{"since must be a duration such as 30s, 5m or 2h"}
}

// splitList 拆分逗号分隔的查询值。
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

// matches 报告一条会话是否通过过滤。
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

// matchStatus 看状态码的族："5xx" 是 500 到 599 全都算，"404" 是一个具体状态码，
// "2xx,404" 是两个都算。
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

// anyMatch 报告 value 是否命中其中任一模式。没有模式就是全通过；模式里可以用
// * 和 ?。
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

// globMatch 用带 * 和 ? 的模式匹配一个值。
func globMatch(pattern, value string, equalFold bool) bool {
	if equalFold {
		pattern, value = strings.ToLower(pattern), strings.ToLower(value)
	}
	// 纯字符串按子串匹配，这样 url=/login 就能找到
	// https://api.test/v1/login，调用方不必费心写通配符。
	if !strings.ContainsAny(pattern, "*?") {
		return strings.Contains(value, pattern)
	}
	return wildcardMatch(pattern, value)
}

// wildcardMatch 是常见的通配匹配，带回溯。写成迭代而不是递归，这样一串星号
// 也不会把栈压深。
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

// filterError 是参数写错了，列表以 400 回答。
type filterError struct{ msg string }

func (e *filterError) Error() string { return e.msg }

// hostOf 取一条会话的域名。
func hostOf(s *session.Snapshot) string {
	if s.Request != nil && s.Request.URL != nil {
		return s.Request.URL.Host
	}
	return ""
}

// urlOf 取一条会话的 URL。
func urlOf(s *session.Snapshot) string {
	if s.Request != nil && s.Request.URL != nil {
		return s.Request.URL.String()
	}
	return ""
}

// methodOfSession 取一条会话的方法。
func methodOfSession(s *session.Snapshot) string {
	if s.Request != nil {
		return s.Request.Method
	}
	return ""
}
