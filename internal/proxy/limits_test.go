package proxy_test

import (
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
)

// The tests here are the ones that were missing, not the ones that broke: every
// case is a body or a protocol the capture limit used to cut in silence.

// TestResponseLargerThanLimitReachesClientWhole checks the response side of the
// capture limit. The limit is what a session keeps for inspection; it is not
// what the client receives.
func TestResponseLargerThanLimitReachesClientWhole(t *testing.T) {
	const limit = 1024
	payload := strings.Repeat("y", limit*8)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", itoa(len(payload)))
		w.Write([]byte(payload))
	}))
	defer origin.Close()

	p := proxy.New(newRecorder(), proxy.Tuning{MaxBodyBytes: limit})
	if _, err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })

	resp, err := clientThrough(t, p.Addr()).Get(origin.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != payload {
		t.Errorf("client got %d bytes, want %d", len(body), len(payload))
	}
}

// TestChunkedRequestLargerThanLimitReachesOriginWhole checks the request side
// when the body has no Content-Length. A chunked upload has no length to compare
// against the limit, and reading it with io.ReadAll cut it.
func TestChunkedRequestLargerThanLimitReachesOriginWhole(t *testing.T) {
	const limit = 1024
	payload := strings.Repeat("x", limit*8)
	var got string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	}))
	defer origin.Close()

	p := proxy.New(newRecorder(), proxy.Tuning{MaxBodyBytes: limit})
	if _, err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })

	// A pipe body has an unknown length, which is what makes it chunked.
	pr, pw := io.Pipe()
	go func() {
		io.Copy(pw, strings.NewReader(payload))
		pw.Close()
	}()
	req, err := http.NewRequest(http.MethodPost, origin.URL, pr)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientThrough(t, p.Addr()).Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if got != payload {
		t.Errorf("origin got %d bytes, want %d", len(got), len(payload))
	}
}

// TestRequestBodyExactlyAtLimitReachesOriginWhole checks the boundary: a body of
// exactly the limit has nothing left over, and must not be marked truncated or
// lose a byte to the peek.
func TestRequestBodyExactlyAtLimitReachesOriginWhole(t *testing.T) {
	const limit = 1024
	payload := strings.Repeat("x", limit)
	var got string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	}))
	defer origin.Close()

	p := proxy.New(newRecorder(), proxy.Tuning{MaxBodyBytes: limit})
	if _, err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })

	resp, err := clientThrough(t, p.Addr()).Post(origin.URL, "text/plain", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if got != payload {
		t.Errorf("origin got %d bytes, want %d", len(got), len(payload))
	}
}

// TestAcceptEncodingIsForwarded checks that the client's own negotiation reaches
// the origin. The transport decodes nothing, so a client that asked for gzip must
// get gzip; dropping the header made every client a plain-text one.
func TestAcceptEncodingIsForwarded(t *testing.T) {
	var sawEncoding string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		io.WriteString(zw, `{"compressed":true}`)
		zw.Close()
	}))
	defer origin.Close()

	p := proxy.New(newRecorder(), proxy.Tuning{})
	if _, err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })

	req, err := http.NewRequest(http.MethodGet, origin.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := clientThrough(t, p.Addr()).Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if sawEncoding != "gzip" {
		t.Errorf("origin saw Accept-Encoding %q, want gzip", sawEncoding)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("client got Content-Encoding %q, want gzip", got)
	}
}

// TestUpgradeTunnelsBothWays checks a WebSocket handshake: the headers reach the
// origin, the 101 comes back, and bytes after it are passed through unchanged.
func TestUpgradeTunnelsBothWays(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		io.Copy(conn, brw)
	}))
	defer origin.Close()

	p := proxy.New(newRecorder(), proxy.Tuning{})
	if _, err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })

	conn, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	host := strings.TrimPrefix(origin.URL, "http://")
	req := "GET " + origin.URL + "/ws HTTP/1.1\r\nHost: " + host +
		"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// The 101 is read off the raw connection: http.ReadResponse would try to
	// consume the upgraded stream as a body.
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("handshake read: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "101") {
		t.Fatalf("origin answered %q, want a 101", buf[:n])
	}
	const frame = "hello-frame"
	if _, err := io.WriteString(conn, frame); err != nil {
		t.Fatal(err)
	}
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("echo read: %v", err)
	}
	if string(buf[:n]) != frame {
		t.Errorf("echo = %q, want %q", buf[:n], frame)
	}
}

// TestReplayClosesStreamingResponse checks that a replayed stream is closed. A
// replay has no client, so nothing else was ever going to close it, and the
// origin kept streaming to a connection with no reader.
func TestReplayClosesStreamingResponse(t *testing.T) {
	originGone := make(chan struct{}, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case <-r.Context().Done():
				originGone <- struct{}{}
				return
			case <-time.After(10 * time.Millisecond):
				if _, err := io.WriteString(w, "data: tick\n\n"); err != nil {
					originGone <- struct{}{}
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer origin.Close()

	u, err := url.Parse(origin.URL + "/sse")
	if err != nil {
		t.Fatal(err)
	}
	p := proxy.New(newRecorder(), proxy.Tuning{})
	done := make(chan error, 1)
	go func() {
		done <- p.Replay(t.Context(), &msg.Request{
			Method: http.MethodGet, URL: u,
			Message: msg.Message{Header: http.Header{}},
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Replay never returned")
	}
	select {
	case <-originGone:
	case <-time.After(2 * time.Second):
		t.Error("the origin is still streaming: the replayed body was never closed")
	}
}

// itoa avoids importing strconv in this file's helpers.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
