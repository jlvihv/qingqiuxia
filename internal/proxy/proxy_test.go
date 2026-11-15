package proxy_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
)

// recorder is a Policy that records what it was asked and does nothing else.
type recorder struct {
	begins    atomic.Int64
	completes atomic.Int64
	requests  chan *msg.Request
	decision  proxy.Decision
	// onResponse lets a test rewrite the response.
	onResponse func(*msg.Response) *msg.Response
	errs       chan error
}

func newRecorder() *recorder {
	return &recorder{requests: make(chan *msg.Request, 16), errs: make(chan error, 1)}
}

func (r *recorder) Begin(ex *proxy.Exchange) {
	r.begins.Add(1)
	ex.ID = uint64(r.begins.Load())
}

func (r *recorder) Request(_ context.Context, ex *proxy.Exchange) (proxy.Decision, error) {
	select {
	case r.requests <- ex.Request:
	default:
	}
	return r.decision, nil
}

func (r *recorder) Response(_ context.Context, _ *proxy.Exchange, resp *msg.Response) (*msg.Response, error) {
	if r.onResponse != nil {
		return r.onResponse(resp), nil
	}
	return resp, nil
}

func (r *recorder) Complete(*proxy.Exchange, *msg.Response, error) { r.completes.Add(1) }

func (r *recorder) Intercepts(string) bool { return false }

func (r *recorder) Certificate(string) (*tls.Certificate, error) {
	return nil, fmt.Errorf("not intercepted")
}

// startProxy starts a proxy and returns its address and a stop function.
func startProxy(t *testing.T, policy proxy.Policy) string {
	t.Helper()
	p := proxy.New(policy, proxy.Tuning{})
	addr, err := p.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if err := p.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return addr
}

// clientThrough returns an HTTP client that sends everything through proxyAddr.
func clientThrough(t *testing.T, proxyAddr string) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			DisableKeepAlives: true,
			// The test origins use self-signed certificates; the proxy is what
			// checks them in a real deployment, so let the test skip that.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 15 * time.Second,
	}
}

// TestForwardHTTP checks the plain path: a request the proxy forwards, with
// its method, headers and body intact, and a response that comes back whole.
func TestForwardHTTP(t *testing.T) {
	var gotMethod, gotHeader, gotBody, gotQuery string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Get("X-Test")
		gotQuery = r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("X-Origin", "yes")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer origin.Close()

	policy := newRecorder()
	addr := startProxy(t, policy)
	client := clientThrough(t, addr)

	req, err := http.NewRequest(http.MethodPost, origin.URL+"/path?a=1&b=2", strings.NewReader("hello body"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test", "value")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q", body)
	}
	if resp.Header.Get("X-Origin") != "yes" {
		t.Errorf("response header lost: %v", resp.Header)
	}
	if gotMethod != http.MethodPost || gotHeader != "value" ||
		gotBody != "hello body" || gotQuery != "a=1&b=2" {
		t.Errorf("origin saw %s %q %q %q", gotMethod, gotHeader, gotBody, gotQuery)
	}

	// The policy must have seen the request before it was forwarded.
	select {
	case seen := <-policy.requests:
		if seen.Method != http.MethodPost {
			t.Errorf("policy saw method %s", seen.Method)
		}
		if string(seen.Body) != "hello body" {
			t.Errorf("policy saw body %q", seen.Body)
		}
		if seen.URL.Path != "/path" {
			t.Errorf("policy saw path %q", seen.URL.Path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the policy never saw the request")
	}
	if n := policy.completes.Load(); n != 1 {
		t.Errorf("complete called %d times, want 1", n)
	}
}

// TestHttpsPassthrough checks that a TLS connection the policy does not
// intercept still works: the proxy tunnels it without reading it.
func TestHttpsPassthrough(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "secret")
	}))
	defer origin.Close()

	policy := newRecorder()
	addr := startProxy(t, policy)
	client := clientThrough(t, addr)

	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "secret" {
		t.Errorf("body = %q", body)
	}
}

// TestMock checks that a mock decision answers without contacting the origin.
func TestMock(t *testing.T) {
	var originHit atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHit.Add(1)
	}))
	defer origin.Close()

	policy := newRecorder()
	policy.decision = proxy.Decision{Mock: &msg.Response{
		Status: 200,
		Message: msg.Message{
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   []byte(`{"mocked":true}`),
		},
	}}
	addr := startProxy(t, policy)
	client := clientThrough(t, addr)

	resp, err := client.Get(origin.URL + "/anything")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"mocked":true}` {
		t.Errorf("body = %q", body)
	}
	if n := originHit.Load(); n != 0 {
		t.Errorf("the origin was contacted %d times, want 0", n)
	}
}

// TestAbort checks that an abort decision answers with a status.
func TestAbort(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer origin.Close()

	policy := newRecorder()
	policy.decision = proxy.Decision{Abort: true, Status: http.StatusTeapot}
	addr := startProxy(t, policy)

	resp, err := clientThrough(t, addr).Get(origin.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want 418", resp.StatusCode)
	}
}

// TestResponseRewrite checks that the policy can replace a response.
func TestResponseRewrite(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "original")
	}))
	defer origin.Close()

	policy := newRecorder()
	policy.onResponse = func(resp *msg.Response) *msg.Response {
		edited := resp.Clone()
		edited.SetBody([]byte("rewritten"))
		return edited
	}
	addr := startProxy(t, policy)

	resp, err := clientThrough(t, addr).Get(origin.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "rewritten" {
		t.Errorf("body = %q, want the rewritten one", body)
	}
}

// TestConnectTunnelLargeBody checks that a body larger than the capture limit
// still reaches the origin whole, while the session keeps only the start.
func TestConnectTunnelLargeBody(t *testing.T) {
	const limit = 1024
	payload := strings.Repeat("x", limit*8)
	var gotLen int
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotLen = len(b)
		w.WriteHeader(200)
	}))
	defer origin.Close()

	policy := newRecorder()
	p := proxy.New(policy, proxy.Tuning{MaxBodyBytes: limit})
	if _, err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })

	resp, err := clientThrough(t, p.Addr()).Post(origin.URL, "text/plain", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if gotLen != len(payload) {
		t.Errorf("origin received %d bytes, want %d", gotLen, len(payload))
	}
	select {
	case seen := <-policy.requests:
		if !seen.Truncated {
			t.Error("the captured request should be marked truncated")
		}
		if int64(len(seen.Body)) != limit {
			t.Errorf("captured %d bytes, want %d", len(seen.Body), limit)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the policy never saw the request")
	}
}

// TestManyConcurrentRequests checks that the proxy serves concurrent traffic
// without mixing responses up.
func TestManyConcurrentRequests(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.URL.Path)
	}))
	defer origin.Close()

	addr := startProxy(t, newRecorder())
	client := clientThrough(t, addr)

	const n = 32
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			path := fmt.Sprintf("/req-%d", i)
			resp, err := client.Get(origin.URL + path)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if string(body) != path {
				errs <- fmt.Errorf("path %q got body %q", path, body)
				return
			}
			errs <- nil
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

// TestDialFailure checks that an unreachable origin becomes a 502 the client
// can see, rather than a hang.
func TestDialFailure(t *testing.T) {
	// Grab a port and close it, so nothing listens there.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	policy := newRecorder()
	proxyAddr := startProxy(t, policy)
	resp, err := clientThrough(t, proxyAddr).Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}
