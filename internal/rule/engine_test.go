package rule

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/codec"
	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// req builds a request for a URL.
func req(t *testing.T, method, raw string, headers map[string]string) *msg.Request {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := &msg.Request{Method: method, URL: u}
	r.Header = http.Header{}
	for k, v := range headers {
		r.SetHeader(k, v)
	}
	return r
}

func TestRequestRewrite(t *testing.T) {
	e := NewEngine()
	if err := e.Replace([]Spec{{
		Name: "v2", Kind: "rewrite", Phase: "request", Pattern: "*/api/*",
		Actions: []ActionSpec{
			{Type: "set-url", Value: "/v2/users"},
			{Type: "set-query", Name: "token", Value: "abc"},
			{Type: "set-header", Name: "x-test", Value: "1"},
			{Type: "remove-header", Name: "Cookie"},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	r := req(t, "GET", "https://api.test/api/users?page=2", map[string]string{"Cookie": "a=b"})
	out, err := e.Request(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.URL.Path; got != "/v2/users" {
		t.Errorf("path = %q", got)
	}
	if got := r.URL.Host; got != "api.test" {
		t.Errorf("host = %q, want it kept for a relative set-url", got)
	}
	if got := r.URL.Query().Get("token"); got != "abc" {
		t.Errorf("token = %q", got)
	}
	if got := r.Header.Get("X-Test"); got != "1" {
		t.Errorf("x-test = %q", got)
	}
	if got := r.Header.Get("Cookie"); got != "" {
		t.Errorf("Cookie survived: %q", got)
	}
	if len(out.Applied) != 1 {
		t.Fatalf("applied = %v", out.Applied)
	}
	if len(out.Applied[0].Effects) != 4 {
		t.Errorf("effects = %v", out.Applied[0].Effects)
	}
}

func TestResponseRewriteKeepsEncoding(t *testing.T) {
	e := NewEngine()
	if err := e.Replace([]Spec{{
		Name: "scrub", Kind: "rewrite", Phase: "response", Pattern: "*",
		Actions: []ActionSpec{{Type: "replace-body", Find: `secret-\d+`, Replace: "REDACTED"}},
	}}); err != nil {
		t.Fatal(err)
	}

	// A gzipped body must come back gzipped, with the same header.
	plain := []byte(`{"token":"secret-12345"}`)
	gz, err := codec.Encode(plain, "gzip")
	if err != nil {
		t.Fatal(err)
	}
	resp := &msg.Response{Status: 200}
	resp.Header = http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}}
	resp.Body = gz
	if _, err := e.Response(req(t, "GET", "https://x.test/", nil), resp); err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		t.Fatalf("the rewritten body must still be gzipped: %v", err)
	}
	if strings.Contains(string(decoded), "secret-12345") {
		t.Errorf("the secret survived: %q", decoded)
	}
	if !strings.Contains(string(decoded), "REDACTED") {
		t.Errorf("body = %q", decoded)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q", got)
	}
}

func TestResponseRewriteSkipsBinary(t *testing.T) {
	e := NewEngine()
	if err := e.Replace([]Spec{{
		Name: "r", Kind: "rewrite", Phase: "response", Pattern: "*",
		Actions: []ActionSpec{{Type: "replace-body", Find: "a", Replace: "b"}},
	}}); err != nil {
		t.Fatal(err)
	}
	resp := &msg.Response{Status: 200}
	resp.Header = http.Header{"Content-Type": {"image/png"}}
	resp.Body = []byte("aaa")
	applied, err := e.Response(req(t, "GET", "https://x.test/", nil), resp)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Errorf("a binary body was edited: %v", applied)
	}
	if string(resp.Body) != "aaa" {
		t.Errorf("body changed to %q", resp.Body)
	}
}

func TestMockReplacesEverything(t *testing.T) {
	e := NewEngine()
	e.Replace([]Spec{
		{Name: "mock", Kind: "mock", Phase: "request", Pattern: "*/login",
			Mock: &MockSpec{Status: 200, Body: `{"ok":1}`, ContentType: "application/json"}},
		{Name: "never", Kind: "rewrite", Phase: "request", Pattern: "*",
			Actions: []ActionSpec{{Type: "set-header", Name: "x", Value: "y"}}},
	})
	r := req(t, "POST", "https://x.test/login", nil)
	out, err := e.Request(r)
	if err != nil {
		t.Fatal(err)
	}
	if out.Mock == nil {
		t.Fatal("no mock")
	}
	if got := r.Header.Get("X"); got != "" {
		t.Errorf("a rule after the mock still ran: %q", got)
	}
	if out.Mock.Header.Get("Content-Type") != "application/json" {
		t.Errorf("mock headers = %v", out.Mock.Header)
	}
}

func TestAbort(t *testing.T) {
	e := NewEngine()
	e.Replace([]Spec{{Name: "no", Kind: "abort", Phase: "request", Pattern: "*/admin/*"}})
	out, err := e.Request(req(t, "GET", "https://x.test/admin/1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Abort {
		t.Error("not aborted")
	}
	out, _ = e.Request(req(t, "GET", "https://x.test/public", nil))
	if out.Abort {
		t.Error("aborted an unmatched request")
	}
}

func TestDisabledRuleIsSkipped(t *testing.T) {
	e := NewEngine()
	off := false
	e.Replace([]Spec{{
		Name: "off", Enabled: &off, Kind: "mock", Phase: "request", Pattern: "*",
		Mock: &MockSpec{Status: 200},
	}})
	out, _ := e.Request(req(t, "GET", "https://x.test/", nil))
	if out.Mock != nil {
		t.Error("a disabled rule matched")
	}
}

func TestUpdateKeepPosition(t *testing.T) {
	e := NewEngine()
	e.Replace([]Spec{
		{Name: "a", Kind: "mock", Phase: "request", Pattern: "*/a",
			Mock: &MockSpec{Status: 200, Body: "a"}},
		{Name: "b", Kind: "mock", Phase: "request", Pattern: "*/b",
			Mock: &MockSpec{Status: 200, Body: "b"}},
	})
	rules := e.Rules()
	if len(rules) != 2 {
		t.Fatalf("rules = %d", len(rules))
	}
	first := rules[0].ID
	if _, err := e.Update(Spec{ID: first, Name: "a", Kind: "mock", Phase: "request",
		Pattern: "*/a", Mock: &MockSpec{Status: 200, Body: "changed"}}); err != nil {
		t.Fatal(err)
	}
	rules = e.Rules()
	if rules[0].ID != first {
		t.Errorf("the updated rule moved: %v", rules[0].ID)
	}
	out, _ := e.Request(req(t, "GET", "https://x.test/a", nil))
	if string(out.Mock.Body) != "changed" {
		t.Errorf("body = %q", out.Mock.Body)
	}
}

func TestBadSpecsFail(t *testing.T) {
	bad := []Spec{
		{Name: "", Kind: "mock", Phase: "request", Pattern: "*"},
		{Name: "x", Kind: "nonsense", Phase: "request", Pattern: "*"},
		{Name: "x", Kind: "mock", Phase: "sideways", Pattern: "*"},
		{Name: "x", Kind: "mock", Phase: "request", Pattern: "*"},    // no mock
		{Name: "x", Kind: "rewrite", Phase: "request", Pattern: "*"}, // no actions
		{Name: "x", Kind: "mock", Phase: "response", Pattern: "*", Mock: &MockSpec{}},
		{Name: "x", Kind: "rewrite", Phase: "request", Pattern: "*",
			Actions: []ActionSpec{{Type: "set-status", Status: 99}}},
	}
	for i, spec := range bad {
		if _, err := Compile(spec, 1); err == nil {
			t.Errorf("spec %d should fail: %+v", i, spec)
		}
	}
}

func TestReplaceIsAtomic(t *testing.T) {
	e := NewEngine()
	if err := e.Replace([]Spec{{Name: "keep", Kind: "abort", Phase: "request", Pattern: "*"}}); err != nil {
		t.Fatal(err)
	}
	// A bad set must leave the good one in place.
	if err := e.Replace([]Spec{{Name: "bad", Kind: "nonsense", Phase: "request", Pattern: "*"}}); err == nil {
		t.Fatal("the bad set was accepted")
	}
	if e.Len() != 1 {
		t.Errorf("rules = %d after a failed replace", e.Len())
	}
}

// TestSetBodyKeepsEncoding checks that replacing a body does not leave plain
// text under a Content-Encoding header, which a client cannot decode.
func TestSetBodyKeepsEncoding(t *testing.T) {
	e := NewEngine()
	if err := e.Replace([]Spec{{
		Name: "replace all", Kind: "rewrite", Phase: "response", Pattern: "*",
		Actions: []ActionSpec{{Type: "set-body", Value: `{"replaced":true}`}},
	}}); err != nil {
		t.Fatal(err)
	}
	resp := &msg.Response{Status: 200}
	resp.Header = http.Header{"Content-Encoding": {"gzip"}, "Content-Type": {"application/json"}}
	resp.Body = []byte("not really gzip")
	if _, err := e.Response(req(t, "GET", "https://x.test/", nil), resp); err != nil {
		t.Fatal(err)
	}
	// The body must decode as the header says it is.
	decoded, err := codec.Decode(resp.Body, "gzip")
	if err != nil {
		t.Fatalf("the body does not decode as gzip: %v", err)
	}
	if string(decoded) != `{"replaced":true}` {
		t.Errorf("decoded = %q", decoded)
	}
}

// TestRuleDelay checks that a rule's own delay reaches the outcome, which is
// how a slow network is simulated for one endpoint.
func TestRuleDelay(t *testing.T) {
	e := NewEngine()
	e.Replace([]Spec{{
		Name: "slow", Kind: "rewrite", Phase: "request", Pattern: "*/slow",
		Delay:   250,
		Actions: []ActionSpec{{Type: "set-header", Name: "X-Slow", Value: "1"}},
	}})
	out, err := e.Request(req(t, "GET", "https://x.test/slow", nil))
	if err != nil {
		t.Fatal(err)
	}
	if out.Delay != 250*time.Millisecond {
		t.Errorf("delay = %v", out.Delay)
	}
	// A rule that does not match leaves no delay.
	out, _ = e.Request(req(t, "GET", "https://x.test/fast", nil))
	if out.Delay != 0 {
		t.Errorf("unmatched delay = %v", out.Delay)
	}
}

// TestRedirectAction checks that a redirect drops the body and sets Location.
func TestRedirectAction(t *testing.T) {
	e := NewEngine()
	e.Replace([]Spec{{
		Name: "moved", Kind: "rewrite", Phase: "response", Pattern: "*",
		Actions: []ActionSpec{{Type: "redirect", Value: "https://new.test/", Status: 301}},
	}})
	resp := &msg.Response{Status: 200}
	resp.Header = http.Header{"Content-Type": {"text/html"}}
	resp.Body = []byte("<html>old</html>")
	resp.SetHeader("Content-Length", "16")
	if _, err := e.Response(req(t, "GET", "https://x.test/", nil), resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != 301 {
		t.Errorf("status = %d", resp.Status)
	}
	if got := resp.Header.Get("Location"); got != "https://new.test/" {
		t.Errorf("Location = %q", got)
	}
	if len(resp.Body) != 0 {
		t.Errorf("the body survived: %q", resp.Body)
	}
}
