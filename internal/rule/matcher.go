package rule

import (
	"fmt"
	"regexp"
	"strings"
)

// Matcher decides whether a rule applies to a request. It is compiled once and
// safe to share between goroutines.
type Matcher struct {
	// raw is the pattern the user wrote, kept for the API.
	raw string
	// schemes and hosts are the alternatives, lowercased; empty means any.
	schemes []string
	hosts   []hostPattern
	// path is the path pattern, "" for any, and pathRE its compiled form.
	path   string
	pathRE *regexp.Regexp
	// methods are the accepted methods, nil when any.
	methods map[string]bool
}

// hostPattern is a host alternative, with wildcards compiled once.
type hostPattern struct {
	literal string
	re      *regexp.Regexp
}

// ParseMatcher compiles a pattern such as
//
//	https://api.example.com/v1/*
//	*/api/*            any scheme, any host, path containing /api/
//	example.com        any scheme, host example.com or its subdomains
//
// The syntax follows what capture tools use: "*" matches any run of
// characters but no "/", "?" matches one character, and "**" matches anything
// including "/". A pattern without a scheme matches both http and https. A
// bare host also matches its subdomains, which is what a user typing
// "example.com" means.
func ParseMatcher(pattern string) (*Matcher, error) {
	raw := strings.TrimSpace(pattern)
	if raw == "" {
		return nil, fmt.Errorf("rule: empty pattern")
	}
	m := &Matcher{raw: raw}

	rest := raw
	// A scheme is only a scheme if a host follows it.
	if i := strings.Index(rest, "://"); i > 0 && !strings.ContainsAny(rest[:i], "*?/") {
		scheme := strings.ToLower(rest[:i])
		if scheme != "http" && scheme != "https" && scheme != "*" {
			return nil, fmt.Errorf("rule: unsupported scheme %q in %q", scheme, raw)
		}
		if scheme != "*" {
			m.schemes = []string{scheme}
		}
		rest = rest[i+3:]
	}

	authority := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		authority, m.path = rest[:i], rest[i:]
	}
	// A leading wildcard is part of the path when the pattern names no host:
	// "*/login" means any host, the path ending in /login, and reading the
	// "*" as a host would drop it and leave "/login" to match exactly, which
	// is a rule that never fires.
	if authority == "*" || authority == "**" {
		m.path = authority + m.path
		authority = ""
	}

	// A bare "example.com" or "*.example.com" also names the host alone.
	for _, alt := range strings.Split(authority, ",") {
		alt = strings.TrimSpace(alt)
		if alt == "" || alt == "*" || alt == "**" {
			continue
		}
		// Drop a port or wildcard port, which does not take part in matching.
		if i := strings.LastIndexByte(alt, ':'); i > 0 && !strings.ContainsAny(alt[i:], "]") {
			alt = alt[:i]
		}
		alt = strings.ToLower(alt)
		hp := hostPattern{literal: alt}
		if strings.ContainsAny(alt, "*?") {
			re, err := compileWildcard(alt)
			if err != nil {
				return nil, err
			}
			hp.re = re
			hp.literal = ""
		}
		m.hosts = append(m.hosts, hp)
	}

	if m.path != "" {
		re, err := compileWildcard(m.path)
		if err != nil {
			return nil, err
		}
		m.pathRE = re
	}
	return m, nil
}

// compileWildcard turns a glob into an anchored regexp.
//
// "*" matches any run of characters, including "/", and "**" is the same thing
// spelled out. Crossing "/" is the choice that matters: a user who writes
// "api.example.com/*" means every path on that host, and a "*" that stopped at
// a separator would match only the first segment, which looks like the rule
// silently not working. mitmproxy's and Charles's wildcards cross "/" too, so
// there is nothing to relearn. "?" matches one character, again including "/",
// for the same reason.
func compileWildcard(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString(`(?s)^`)
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			b.WriteString(`.*`)
			if i+1 < len(glob) && glob[i+1] == '*' {
				// "**" is "*" written twice; consume the pair so the regexp
				// does not carry a redundant ".*.*".
				i++
			}
		case '?':
			b.WriteString(`.`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`$`)
	return regexp.Compile(b.String())
}

// SetMethods restricts the matcher to these methods, upper case.
func (m *Matcher) SetMethods(methods ...string) {
	if len(methods) == 0 {
		m.methods = nil
		return
	}
	m.methods = make(map[string]bool, len(methods))
	for _, method := range methods {
		if method = strings.ToUpper(strings.TrimSpace(method)); method != "" {
			m.methods[method] = true
		}
	}
}

// MethodSet reports the methods the matcher accepts, nil for any.
func (m *Matcher) MethodSet() []string {
	if len(m.methods) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.methods))
	for method := range m.methods {
		out = append(out, method)
	}
	return out
}

// Matches reports whether the matcher applies to a URL and method. The method
// check is skipped when the matcher names no method.
func (m *Matcher) Matches(scheme, host, path, method string) bool {
	if len(m.schemes) > 0 {
		ok := false
		for _, s := range m.schemes {
			if s == strings.ToLower(scheme) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(m.hosts) > 0 {
		h := strings.ToLower(host)
		if i := strings.LastIndexByte(h, ':'); i > 0 {
			h = h[:i]
		}
		ok := false
		for _, hp := range m.hosts {
			if hp.re != nil {
				// A wildcard host also matches its subdomains.
				if hp.re.MatchString(h) || hp.re.MatchString(stripFirstLabel(h)) {
					ok = true
					break
				}
				continue
			}
			if h == hp.literal || strings.HasSuffix(h, "."+hp.literal) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if m.pathRE != nil && !m.pathRE.MatchString(path) {
		return false
	}
	if m.methods != nil && !m.methods[strings.ToUpper(method)] {
		return false
	}
	return true
}

func stripFirstLabel(h string) string {
	if i := strings.IndexByte(h, '.'); i >= 0 {
		return h[i+1:]
	}
	return h
}

// String returns the pattern as the user wrote it.
func (m *Matcher) String() string { return m.raw }
