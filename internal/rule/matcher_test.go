package rule

import "testing"

func TestMatcher(t *testing.T) {
	tests := []struct {
		pattern string
		scheme  string
		host    string
		path    string
		want    bool
	}{
		// A bare host matches itself and its subdomains, over both schemes.
		{"example.com", "https", "example.com", "/", true},
		{"example.com", "http", "example.com", "/", true},
		{"example.com", "https", "api.example.com", "/v1", true},
		{"example.com", "https", "notexample.com", "/", false},
		{"example.com", "https", "example.com.evil.com", "/", false},

		// A scheme narrows the match.
		{"https://example.com", "https", "example.com", "/", true},
		{"https://example.com", "http", "example.com", "/", false},
		{"https://example.com/*", "https", "example.com", "/anything", true},

		// A path pattern matches a path.
		{"example.com/api/*", "https", "example.com", "/api/users", true},
		{"example.com/api/*", "https", "example.com", "/api/", true},
		{"example.com/api/*", "https", "example.com", "/web/users", false},
		// "*" does not cross a path separator, "**" does.
		{"example.com/api/*", "https", "example.com", "/api/a/b", false},
		{"example.com/api/**", "https", "example.com", "/api/a/b", true},

		// A wildcard host.
		{"*.example.com", "https", "api.example.com", "/", true},
		{"*.example.com", "https", "example.com", "/", false},
		{"*", "https", "anything.test", "/x", true},
		{"*/api/*", "https", "anything.test", "/api/v1", true},

		// A port in the pattern is ignored, since a host is what matches.
		{"example.com:8080/*", "http", "example.com", "/x", true},
		// A query is not part of the path pattern.
		{"example.com/x", "https", "example.com", "/x", true},

		// "?" matches one character.
		{"example.com/v?", "https", "example.com", "/v1", true},
		{"example.com/v?", "https", "example.com", "/v12", false},
	}
	for _, tc := range tests {
		m, err := ParseMatcher(tc.pattern)
		if err != nil {
			t.Errorf("ParseMatcher(%q): %v", tc.pattern, err)
			continue
		}
		if got := m.Matches(tc.scheme, tc.host, tc.path, "GET"); got != tc.want {
			t.Errorf("%q vs %s://%s%s = %v, want %v", tc.pattern, tc.scheme, tc.host, tc.path, got, tc.want)
		}
	}
}

func TestMatcherMethods(t *testing.T) {
	m, err := ParseMatcher("example.com")
	if err != nil {
		t.Fatal(err)
	}
	m.SetMethods("post", "PUT")
	if !m.Matches("https", "example.com", "/", "POST") {
		t.Error("POST should match")
	}
	if m.Matches("https", "example.com", "/", "GET") {
		t.Error("GET should not match")
	}
}

func TestMatcherErrors(t *testing.T) {
	for _, bad := range []string{"", "ftp://example.com"} {
		if _, err := ParseMatcher(bad); err == nil {
			t.Errorf("ParseMatcher(%q) should fail", bad)
		}
	}
}
