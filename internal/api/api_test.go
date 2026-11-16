package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/api"
	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/mitm"
	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/pipeline"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

// msgReq and msgResp are a sample exchange the session tests store.
var msgReq = msg.Request{Method: "GET", URL: mustURL("https://example.test/path")}

func init() {
	msgReq.Header = http.Header{"Accept": {"*/*"}}
	msgReq.Body = []byte("hi")
	msgReq.Proto = "HTTP/1.1"
	msgResp.Header = http.Header{"Content-Type": {"text/plain"}}
	msgResp.Body = []byte("hello")
	msgResp.Status = 200
	msgResp.Proto = "HTTP/1.1"
}

var msgResp = msg.Response{}

// testServer is an API over a core with no proxy attached: the API's job is to
// steer, and what it steers is exercised here directly.
type testServer struct {
	*httptest.Server
	pipeline *pipeline.Pipeline
	rules    *rule.Engine
	pauses   *breakpoint.Manager
	sessions *session.Store
}

func newServer(t *testing.T, opts api.Options) *testServer {
	t.Helper()
	return newServerWith(t, opts, nil)
}

// newServerWith builds a server and lets a test adjust the core it steers.
func newServerWith(t *testing.T, opts api.Options, adjust func(*api.Core)) *testServer {
	t.Helper()
	ca, err := mitm.Generate("test CA")
	if err != nil {
		t.Fatal(err)
	}
	rules := rule.NewEngine()
	pauses := breakpoint.NewManager(time.Second)
	sessions := session.NewStore(100)
	pol := pipeline.New(pipeline.Options{
		Rules: rules, Breakpoints: pauses, Sessions: sessions,
		Issuer: mitm.NewIssuer(ca),
	})
	ts := &testServer{pipeline: pol, rules: rules, pauses: pauses, sessions: sessions}
	core := api.Core{
		Pipeline:  pol,
		Version:   "test",
		Started:   time.Now(),
		Addr:      func() string { return "127.0.0.1:1234" },
		CaptureOn: pol.Recording,
	}
	if adjust != nil {
		adjust(&core)
	}
	ts.Server = httptest.NewServer(api.New(core, opts))
	t.Cleanup(ts.Close)
	return ts
}

// do sends a request and decodes a JSON response into out.
func (ts *testServer) do(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	} else {
		io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode
}

func TestStatus(t *testing.T) {
	ts := newServer(t, api.Options{})
	var st struct {
		Version   string `json:"version"`
		ProxyAddr string `json:"proxyAddr"`
		APIVer    int    `json:"apiVersion"`
		Rules     int    `json:"rules"`
	}
	if code := ts.do(t, "GET", "/api/status", nil, &st); code != 200 {
		t.Fatalf("status = %d", code)
	}
	if st.Version != "test" || st.ProxyAddr != "127.0.0.1:1234" || st.APIVer != api.APIVersion {
		t.Errorf("status = %+v", st)
	}
}

// TestCaptureToggle checks that turning capture off is reported by the status
// and stops recording, which a GUI shows as a switch.
func TestCaptureToggle(t *testing.T) {
	recording := true
	ts := newServerWith(t, api.Options{}, func(c *api.Core) {
		c.CaptureOn = func() bool { return recording }
		c.SetCapture = func(on bool) { recording = on }
	})

	var st struct {
		Capturing bool `json:"capturing"`
	}
	ts.do(t, "GET", "/api/status", nil, &st)
	if !st.Capturing {
		t.Error("capture should start on")
	}
	ts.do(t, "POST", "/api/capture", map[string]any{"enabled": false}, &st)
	if st.Capturing {
		t.Error("the response still says capturing")
	}
	if recording {
		t.Error("the core was not told to stop")
	}
	ts.do(t, "GET", "/api/status", nil, &st)
	if st.Capturing {
		t.Error("the status still says capturing")
	}
}

func TestRulesCRUD(t *testing.T) {
	ts := newServer(t, api.Options{})

	var created struct {
		ID      uint64 `json:"id"`
		Effects []string
	}
	code := ts.do(t, "POST", "/api/rules", map[string]any{
		"name": "mock api", "kind": "mock", "phase": "request", "pattern": "*/api/*",
		"mock": map[string]any{"status": 201, "body": `{"ok":true}`},
	}, &created)
	if code != 201 {
		t.Fatalf("create = %d", code)
	}
	if created.ID == 0 {
		t.Fatal("no id assigned")
	}

	var list struct {
		Rules []struct {
			ID      uint64 `json:"id"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		}
	}
	ts.do(t, "GET", "/api/rules", nil, &list)
	if len(list.Rules) != 1 || list.Rules[0].Name != "mock api" || !list.Rules[0].Enabled {
		t.Fatalf("rules = %+v", list.Rules)
	}

	// Disable it.
	ts.do(t, "PATCH", "/api/rules/1", map[string]any{"enabled": false}, &list)
	ts.do(t, "GET", "/api/rules", nil, &list)
	if list.Rules[0].Enabled {
		t.Error("the rule is still enabled")
	}

	if code := ts.do(t, "DELETE", "/api/rules/1", nil, nil); code != 204 {
		t.Errorf("delete = %d", code)
	}
	ts.do(t, "GET", "/api/rules", nil, &list)
	if len(list.Rules) != 0 {
		t.Errorf("rules left: %+v", list.Rules)
	}
}

func TestRuleReplaceOrder(t *testing.T) {
	ts := newServer(t, api.Options{})
	ts.do(t, "PUT", "/api/rules", map[string]any{"rules": []map[string]any{
		{"name": "b", "kind": "abort", "phase": "request", "pattern": "*/b"},
		{"name": "a", "kind": "abort", "phase": "request", "pattern": "*/a"},
	}}, nil)
	var list struct {
		Rules []struct{ Name string } `json:"rules"`
	}
	ts.do(t, "GET", "/api/rules", nil, &list)
	if len(list.Rules) != 2 || list.Rules[0].Name != "b" {
		t.Errorf("order = %+v", list.Rules)
	}
}

func TestBadRuleIsRejected(t *testing.T) {
	ts := newServer(t, api.Options{})
	var errResp struct {
		Error string `json:"error"`
	}
	code := ts.do(t, "POST", "/api/rules", map[string]any{
		"name": "bad", "kind": "mock", "phase": "request", "pattern": "*",
	}, &errResp)
	if code != 400 {
		t.Errorf("code = %d", code)
	}
	if !strings.Contains(errResp.Error, "mock") {
		t.Errorf("error = %q", errResp.Error)
	}
}

func TestSessionsAndDetail(t *testing.T) {
	ts := newServer(t, api.Options{})
	sess := ts.sessions.Begin(&msgReq, time.Now())
	sess.SetResponse(&msgResp)
	sess.Complete(&msgResp, nil, time.Now())
	ts.sessions.Publish(sess)

	var list struct {
		Sessions []struct {
			ID     uint64 `json:"id"`
			Method string
			URL    string
			Status int
		}
		Total int `json:"total"`
	}
	ts.do(t, "GET", "/api/sessions", nil, &list)
	if list.Total != 1 || len(list.Sessions) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if list.Sessions[0].Status != 200 || list.Sessions[0].Method != "GET" {
		t.Errorf("row = %+v", list.Sessions[0])
	}

	var detail struct {
		Request  struct{ Body string } `json:"request"`
		Response struct{ Body string } `json:"response"`
	}
	ts.do(t, "GET", "/api/sessions/1", nil, &detail)
	if detail.Request.Body != "hi" || detail.Response.Body != "hello" {
		t.Errorf("detail = %+v", detail)
	}

	if code := ts.do(t, "DELETE", "/api/sessions", nil, nil); code != 200 {
		t.Errorf("clear = %d", code)
	}
	if ts.sessions.Len() != 0 {
		t.Error("sessions left after clear")
	}
}

func TestBreakpointPauseOverAPI(t *testing.T) {
	ts := newServer(t, api.Options{})
	ts.do(t, "PUT", "/api/breakpoints", map[string]any{"breakpoints": []map[string]any{
		{"name": "all", "phase": "request", "pattern": "*"},
	}}, nil)

	// Pause a request in the background, as the proxy would.
	req := msgReq.Clone()
	done := make(chan breakpoint.Intercept, 1)
	go func() {
		out, _ := ts.pauses.Intercept(t.Context(), 42, breakpoint.OnRequest, req, nil)
		done <- out
	}()

	var pauses struct {
		Pauses []struct {
			ID        uint64 `json:"id"`
			SessionID uint64 `json:"sessionId"`
			Phase     string `json:"phase"`
			Method    string `json:"method"`
		} `json:"pauses"`
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ts.do(t, "GET", "/api/pauses", nil, &pauses)
		if len(pauses.Pauses) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(pauses.Pauses) != 1 {
		t.Fatal("no pause listed")
	}
	p := pauses.Pauses[0]
	if p.SessionID != 42 || p.Phase != "request" || p.Method != "GET" {
		t.Errorf("pause = %+v", p)
	}

	// Edit it, then release it.
	if code := ts.do(t, "PUT", "/api/pauses/"+itoa(p.ID), map[string]any{
		"headers": map[string][]string{"X-Edited": {"yes"}},
		"body":    "edited body",
	}, nil); code != 200 {
		t.Fatalf("edit = %d", code)
	}
	if code := ts.do(t, "POST", "/api/pauses/"+itoa(p.ID)+"/resume", nil, nil); code != 200 {
		t.Fatalf("resume = %d", code)
	}

	out := <-done
	if !out.Matched {
		t.Error("not matched")
	}
	if req.Header.Get("X-Edited") != "yes" {
		t.Errorf("header = %q", req.Header.Get("X-Edited"))
	}
	if string(req.Body) != "edited body" {
		t.Errorf("body = %q", req.Body)
	}
}

func TestPauseAbortOverAPI(t *testing.T) {
	ts := newServer(t, api.Options{})
	ts.pauses.Replace([]breakpoint.Spec{{Name: "all", Phase: "request", Pattern: "*"}})
	done := make(chan breakpoint.Intercept, 1)
	go func() {
		out, _ := ts.pauses.Intercept(t.Context(), 1, breakpoint.OnRequest, msgReq.Clone(), nil)
		done <- out
	}()
	var pending breakpoint.Snapshot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if list := ts.pauses.Pending(); len(list) > 0 {
			pending = list[0]
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pending.ID == 0 {
		t.Fatal("nothing paused")
	}
	if code := ts.do(t, "POST", "/api/pauses/"+itoa(pending.ID)+"/abort", nil, nil); code != 200 {
		t.Fatalf("abort = %d", code)
	}
	if out := <-done; !out.Abort {
		t.Error("not aborted")
	}
}

func TestToken(t *testing.T) {
	ts := newServer(t, api.Options{Token: "s3cret"})

	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("without a token: %d", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/api/status", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("with a token: %d", resp.StatusCode)
	}

	// A query parameter works too, for a browser.
	resp, err = http.Get(ts.URL + "/api/status?token=s3cret")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("token in the query: %d", resp.StatusCode)
	}
}

func TestCertificateDownload(t *testing.T) {
	ts := newServer(t, api.Options{})
	resp, err := http.Get(ts.URL + "/api/certificate?download=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("code = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "BEGIN CERTIFICATE") {
		t.Errorf("body = %q", body)
	}
}

func TestUnknownFieldsRejected(t *testing.T) {
	ts := newServer(t, api.Options{})
	var errResp struct{ Error string }
	code := ts.do(t, "POST", "/api/rules", map[string]any{
		"name": "x", "kind": "abort", "phase": "request", "pattern": "*",
		"typo": true,
	}, &errResp)
	if code != 400 {
		t.Errorf("code = %d", code)
	}
	if !strings.Contains(errResp.Error, "typo") {
		t.Errorf("error = %q", errResp.Error)
	}
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func itoa(n uint64) string {
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
