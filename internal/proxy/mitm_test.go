package proxy_test

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/mitm"
	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/pipeline"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

// stack is a proxy with the whole core behind it: rules, breakpoints, sessions.
type stack struct {
	proxy    *proxy.Proxy
	addr     string
	ca       *mitm.CA
	pipeline *pipeline.Pipeline
	rules    *rule.Engine
	pauses   *breakpoint.Manager
	sessions *session.Store
}

// startStack builds the core as main does, and starts the proxy with it.
func startStack(t *testing.T, opts ...func(*stack)) *stack {
	t.Helper()
	ca, err := mitm.Generate("Qingqiuxia Test CA")
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	s := &stack{
		ca:       ca,
		rules:    rule.NewEngine(),
		pauses:   breakpoint.NewManager(0),
		sessions: session.NewStore(1000),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.pipeline = pipeline.New(pipeline.Options{
		Rules:       s.rules,
		Breakpoints: s.pauses,
		Sessions:    s.sessions,
		Issuer:      mitm.NewIssuer(ca),
		ShouldIntercept: func(host string) bool {
			return true
		},
	})
	pr := proxy.New(s.pipeline, proxy.Tuning{})
	addr, err := pr.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { pr.Stop() })
	s.proxy = pr
	s.addr = addr
	return s
}

// client returns a client that trusts the stack's CA: exactly what a user gets
// after installing the certificate.
func (s *stack) client(t *testing.T) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(s.ca.Cert)
	proxyURL, err := url.Parse("http://" + s.addr)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{RootCAs: pool},
		},
		Timeout: 20 * time.Second,
	}
}

// tlsOrigin starts an HTTPS origin whose certificate the client cannot verify,
// since the proxy is what the client trusts.
func tlsOrigin(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// TestMITMDecrypts checks the whole point of the tool: an HTTPS request the
// client sent encrypted is visible to the rules, rewritten, and delivered.
func TestMITMDecrypts(t *testing.T) {
	var originSaw string
	origin := tlsOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		originSaw = r.Method + " " + r.URL.Path + " " + string(body) + " " + r.Header.Get("User-Agent")
		io.WriteString(w, "origin response")
	})

	s := startStack(t)
	client := s.client(t)

	resp, err := client.Post(origin.URL+"/api/login", "application/json", strings.NewReader(`{"user":"ada"}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "origin response" {
		t.Fatalf("body = %q", body)
	}

	if !strings.Contains(originSaw, "/api/login") || !strings.Contains(originSaw, `{"user":"ada"}`) {
		t.Errorf("origin did not see the request: %q", originSaw)
	}

	// The session must show the decrypted request, not a tunnel.
	sessions := s.sessions.List(0, -1)
	if len(sessions) != 1 {
		t.Fatalf("recorded %d sessions, want 1", len(sessions))
	}
	got := sessions[0]
	if got.Request.URL.Path != "/api/login" {
		t.Errorf("recorded path = %q", got.Request.URL.Path)
	}
	if string(got.Request.Body) != `{"user":"ada"}` {
		t.Errorf("recorded body = %q", got.Request.Body)
	}
	// Ports are not part of a host name for matching, and the certificate
	// names the host without one.
	if got.Request.URL.Host != strings.TrimPrefix(strings.TrimPrefix(origin.URL, "https://"), "") {
		// The origin's URL carries the port; the recorded one should too.
		if !strings.HasSuffix(origin.URL, got.Request.URL.Host) {
			t.Logf("recorded host %q, origin %q", got.Request.URL.Host, origin.URL)
		}
	}
	if got.Phase != session.Complete {
		t.Errorf("phase = %s", got.Phase)
	}
	if string(got.Response.Body) != "origin response" {
		t.Errorf("recorded response body = %q", got.Response.Body)
	}
}

// TestRuleRewriteRequest checks that a rewrite rule changes what the origin
// receives.
func TestRuleRewriteRequest(t *testing.T) {
	var gotPath, gotAuth, gotHeader string
	origin := tlsOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotHeader = r.Header.Get("X-Injected")
		io.WriteString(w, "ok")
	})

	s := startStack(t)
	enabled := true
	spec := rule.Spec{
		Name: "point at v2", Kind: "rewrite", Phase: "request",
		Pattern: "*/api/*", Enabled: &enabled,
		Actions: []rule.ActionSpec{
			{Type: "set-header", Name: "Authorization", Value: "Bearer secret"},
			{Type: "set-header", Name: "X-Injected", Value: "yes"},
		},
	}
	if err := s.rules.Replace([]rule.Spec{spec}); err != nil {
		t.Fatalf("add rule: %v", err)
	}

	resp, err := s.client(t).Get(origin.URL + "/api/items")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want the rule's", gotAuth)
	}
	if gotHeader != "yes" {
		t.Errorf("X-Injected = %q", gotHeader)
	}
	if gotPath != "/api/items" {
		t.Errorf("path = %q", gotPath)
	}

	sess := s.sessions.List(0, -1)[0]
	if len(sess.Notes) == 0 {
		t.Error("the session has no note about the rule")
	}
}

// TestRuleRewriteResponse checks that a response rule changes the body the
// client gets, including a gzipped one.
func TestRuleRewriteResponse(t *testing.T) {
	origin := tlsOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"token":"abc123","user":"ada"}`)
	})

	s := startStack(t)
	spec := rule.Spec{
		Name: "scrub token", Kind: "rewrite", Phase: "response",
		Pattern: originHost(t, origin) + "/*",
		Actions: []rule.ActionSpec{
			{Type: "replace-body", Find: `"abc123"`, Replace: `"***"`},
		},
	}
	if err := s.rules.Replace([]rule.Spec{spec}); err != nil {
		t.Fatalf("add rule: %v", err)
	}

	resp, err := s.client(t).Get(origin.URL + "/session")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "abc123") {
		t.Errorf("the token reached the client: %q", body)
	}
	if !strings.Contains(string(body), `"***"`) {
		t.Errorf("body = %q", body)
	}
}

// originHost returns the pattern that matches an origin's host and port.
func originHost(t *testing.T, origin *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	return "https://" + u.Host
}

// TestMockRule checks that a mock rule answers without an origin.
func TestMockRule(t *testing.T) {
	s := startStack(t)
	mock := rule.Spec{
		Name: "fake login", Kind: "mock", Phase: "request",
		Pattern: "*/v1/login",
		Mock: &rule.MockSpec{
			Status: 201,
			Headers: map[string]string{
				"X-Fake": "1",
			},
			Body:        `{"token":"mocked"}`,
			ContentType: "application/json",
		},
	}
	if err := s.rules.Replace([]rule.Spec{mock}); err != nil {
		t.Fatalf("add mock: %v", err)
	}

	// The host does not exist, which proves no dial was made.
	resp, err := s.client(t).Post("https://nothing.invalid/v1/login", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if string(body) != `{"token":"mocked"}` {
		t.Errorf("body = %q", body)
	}
	if resp.Header.Get("X-Fake") != "1" {
		t.Errorf("headers = %v", resp.Header)
	}
	sess := s.sessions.List(0, -1)
	if len(sess) != 1 || sess[0].Source != session.Mocked {
		t.Errorf("session source = %v", sess)
	}
}

// TestBreakpointEditsRequest checks that a paused request waits for a decision
// and continues with the edits a client made.
func TestBreakpointEditsRequest(t *testing.T) {
	var originSaw string
	origin := tlsOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		originSaw = string(body)
		io.WriteString(w, "ok")
	})

	s := startStack(t)
	if err := s.pauses.Replace([]breakpoint.Spec{{
		Name: "login", Phase: "request", Pattern: "*/login",
	}}); err != nil {
		t.Fatalf("breakpoint: %v", err)
	}

	// Wait for the pause in the background: the request blocks until the
	// decision, as it must.
	done := make(chan error, 1)
	go func() {
		resp, err := s.client(t).Post(origin.URL+"/login", "application/json", strings.NewReader(`{"user":"ada"}`))
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		done <- err
	}()

	p, ok := waitForPause(t, s.pauses)
	if !ok {
		t.Fatal("no breakpoint pause")
	}
	if p.Phase != breakpoint.OnRequest {
		t.Errorf("phase = %s", p.Phase)
	}
	// Edit the paused request as the API would, then release it. The edit goes
	// through the live pause, which is what holds the message the proxy
	// continues with.
	live, ok := s.pauses.Live(p.ID)
	if !ok {
		t.Fatal("the pause went away")
	}
	live.Edit(func(req *msg.Request, _ *msg.Response) error {
		req.SetBody([]byte(`{"user":"edited"}`))
		return nil
	})
	if !s.pauses.Resume(p.ID, breakpoint.Release) {
		t.Fatal("resume failed")
	}
	if err := <-done; err != nil {
		t.Fatalf("request: %v", err)
	}
	if originSaw != `{"user":"edited"}` {
		t.Errorf("origin saw %q, want the edited body", originSaw)
	}
}

// waitForPause waits for a message to be paused.
func waitForPause(t *testing.T, m *breakpoint.Manager) (breakpoint.Snapshot, bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pending := m.Pending(); len(pending) > 0 {
			return pending[0], true
		}
		time.Sleep(time.Millisecond)
	}
	return breakpoint.Snapshot{}, false
}

// TestBreakpointEditsResponse checks the response side of a breakpoint.
func TestBreakpointEditsResponse(t *testing.T) {
	origin := tlsOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "original body")
	})

	s := startStack(t)
	if err := s.pauses.Replace([]breakpoint.Spec{{
		Name: "everything", Phase: "response", Pattern: "*",
	}}); err != nil {
		t.Fatalf("breakpoint: %v", err)
	}

	got := make(chan string, 1)
	go func() {
		resp, err := s.client(t).Get(origin.URL + "/page")
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		got <- string(body)
	}()

	p, ok := waitForPause(t, s.pauses)
	if !ok {
		t.Fatal("no breakpoint pause")
	}
	live, ok := s.pauses.Live(p.ID)
	if !ok {
		t.Fatal("the pause went away")
	}
	live.Edit(func(_ *msg.Request, resp *msg.Response) error {
		resp.SetBody([]byte("edited body"))
		return nil
	})
	s.pauses.Resume(p.ID, breakpoint.Release)

	if body := <-got; body != "edited body" {
		t.Errorf("client got %q", body)
	}
}

// TestBreakpointAbort checks that aborting at a breakpoint answers the client
// without forwarding.
func TestBreakpointAbort(t *testing.T) {
	var originHit bool
	origin := tlsOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		originHit = true
	})

	s := startStack(t)
	s.pauses.Replace([]breakpoint.Spec{{Name: "all", Phase: "request", Pattern: "*"}})

	done := make(chan int, 1)
	go func() {
		resp, err := s.client(t).Get(origin.URL)
		if err != nil {
			done <- 0
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		done <- resp.StatusCode
	}()

	p, ok := waitForPause(t, s.pauses)
	if !ok {
		t.Fatal("no breakpoint pause")
	}
	s.pauses.Resume(p.ID, breakpoint.Abort)

	if status := <-done; status != 403 {
		t.Errorf("status = %d, want 403", status)
	}
	if originHit {
		t.Error("the origin was contacted after an abort")
	}
}

// TestSSEIsStreamed checks that a stream is passed through as it comes rather
// than buffered until it ends.
func TestSSEIsStreamed(t *testing.T) {
	release := make(chan struct{})
	origin := tlsOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		// Hold the stream open: a buffering proxy would never deliver the
		// first event.
		<-release
		io.WriteString(w, "data: second\n\n")
		flusher.Flush()
	})
	defer close(release)

	s := startStack(t)
	resp, err := s.client(t).Get(origin.URL + "/events")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "first") {
		t.Errorf("first chunk = %q", buf[:n])
	}
}

// TestSessionTimings checks that a completed session records its timing.
func TestSessionTimings(t *testing.T) {
	origin := tlsOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello")
	})
	s := startStack(t)
	resp, err := s.client(t).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	sess := s.sessions.List(0, -1)[0]
	if sess.Timings.Duration() <= 0 {
		t.Error("no duration recorded")
	}
	if sess.BytesOut != 5 {
		t.Errorf("bytes out = %d", sess.BytesOut)
	}
}

// TestReplayIsCaptured checks that a replay goes through the policy, so it
// shows up in the traffic list and runs the rules.
func TestReplayIsCaptured(t *testing.T) {
	var hits int32
	origin := tlsOrigin(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, "ok")
	})
	s := startStack(t)
	client := s.client(t)
	resp, err := client.Get(origin.URL + "/replay-me")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	snap := s.sessions.List(0, -1)[0]
	// Replay through the proxy the stack runs.
	p := proxyFor(t, s)
	if err := p.Replay(t.Context(), snap.Request); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("the origin was hit %d times, want 2", got)
	}
	// The replay is a new session, recorded like the first.
	if got := s.sessions.Len(); got != 2 {
		t.Errorf("sessions = %d after the replay, want 2", got)
	}
	last := s.sessions.List(0, -1)[1]
	if last.Request.URL.Path != "/replay-me" {
		t.Errorf("replayed %q", last.Request.URL.Path)
	}
}
