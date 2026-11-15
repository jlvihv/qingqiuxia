package msg

import (
	"net/http"
	"net/url"
	"testing"
)

func TestSetBodyKeepsLength(t *testing.T) {
	var m Message
	m.Header = http.Header{}
	m.SetBody([]byte("hello"))
	if got := m.Header.Get("Content-Length"); got != "5" {
		t.Errorf("Content-Length = %q", got)
	}
	m.ClearBody()
	if got := m.Header.Get("Content-Length"); got != "" {
		t.Errorf("Content-Length after clear = %q", got)
	}
}

func TestCloneIsDeep(t *testing.T) {
	r := &Request{Method: "POST"}
	r.Header = http.Header{"A": {"1"}}
	r.Body = []byte("body")
	r.URL = mustURL(t, "https://x.test/path?q=1")

	c := r.Clone()
	c.Header.Set("A", "2")
	c.Body[0] = 'B'
	c.URL.Path = "/other"

	if r.Header.Get("A") != "1" {
		t.Error("the header was shared")
	}
	if r.Body[0] != 'b' {
		t.Error("the body was shared")
	}
	if r.URL.Path != "/path" {
		t.Error("the URL was shared")
	}
}

func TestMediaType(t *testing.T) {
	for in, want := range map[string]string{
		"application/json; charset=utf-8": "application/json",
		"TEXT/HTML":                       "text/html",
		"":                                "",
	} {
		if got := MediaType(in); got != want {
			t.Errorf("MediaType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsText(t *testing.T) {
	for _, mt := range []string{"text/html", "application/json", "application/xml",
		"application/problem+json", "image/svg+xml", ""} {
		if !IsText(mt) {
			t.Errorf("%s should be editable as text", mt)
		}
	}
	for _, mt := range []string{"image/png", "application/octet-stream", "video/mp4"} {
		if IsText(mt) {
			t.Errorf("%s should not be text", mt)
		}
	}
}

func TestIsStreaming(t *testing.T) {
	if !IsStreaming("text/event-stream") {
		t.Error("server-sent events must stream")
	}
	if IsStreaming("application/json") {
		t.Error("JSON must not stream")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
