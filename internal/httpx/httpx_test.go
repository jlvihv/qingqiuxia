package httpx

import (
	"net/http"
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

func TestIsUpgrade(t *testing.T) {
	ws := http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}
	if !IsUpgrade(ws) {
		t.Error("a WebSocket upgrade was not recognised")
	}
	if IsUpgrade(http.Header{"Connection": {"keep-alive"}}) {
		t.Error("a keep-alive header looked like an upgrade")
	}
}
