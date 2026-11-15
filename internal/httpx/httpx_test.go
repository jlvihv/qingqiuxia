package httpx

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRemoveHopHeaders(t *testing.T) {
	h := http.Header{
		"Connection":        {"keep-alive, X-Custom"},
		"X-Custom":          {"drop me"},
		"Keep-Alive":        {"timeout=5"},
		"Transfer-Encoding": {"chunked"},
		"Content-Type":      {"application/json"},
		"X-Keep":            {"yes"},
	}
	RemoveHopHeaders(h)
	for _, name := range []string{"Connection", "X-Custom", "Keep-Alive", "Transfer-Encoding"} {
		if h.Get(name) != "" {
			t.Errorf("%s survived", name)
		}
	}
	if h.Get("Content-Type") == "" || h.Get("X-Keep") == "" {
		t.Error("an end-to-end header was removed")
	}
}

func TestReadBodyWithinLimit(t *testing.T) {
	body, truncated, err := ReadBody(io.NopCloser(strings.NewReader("hello")), 10)
	if err != nil || truncated || string(body) != "hello" {
		t.Errorf("body = %q truncated = %v err = %v", body, truncated, err)
	}
}

func TestReadBodyOverLimit(t *testing.T) {
	body, truncated, err := ReadBody(io.NopCloser(strings.NewReader("1234567890")), 4)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Error("not marked truncated")
	}
	if string(body) != "1234" {
		t.Errorf("body = %q", body)
	}
}

func TestReadBodyOverflow(t *testing.T) {
	var rest bytes.Buffer
	body, truncated, err := ReadBodyTo(io.NopCloser(strings.NewReader("1234567890")), 4, &rest)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || string(body) != "1234" {
		t.Errorf("body = %q truncated = %v", body, truncated)
	}
	// The overflowing part must reach the origin whole.
	if rest.String() != "567890" {
		t.Errorf("overflow = %q", rest.String())
	}
}

func TestIsUpgrade(t *testing.T) {
	ws := http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}
	if !IsUpgrade(ws) {
		t.Error("a WebSocket upgrade was not recognised")
	}
	if IsUpgrade(http.Header{"Connection": {"keep-alive"}}) {
		t.Error("a keep-alive header looked like an upgrade")
	}
}
