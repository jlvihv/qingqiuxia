package pipeline_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/pipeline"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

// TestRewriteDoesNotTouchRecordedTraffic pins what a rewrite rule means: it
// changes traffic that comes after it, on the way through, and leaves what was
// already recorded as it was.
//
// A capture tool that could edit its own history would make the list useless as
// evidence: the record has to be what happened.
func TestRewriteDoesNotTouchRecordedTraffic(t *testing.T) {
	rules := rule.NewEngine()
	sessions := session.NewStore(100)
	p := pipeline.New(pipeline.Options{
		Rules:    rules,
		Sessions: sessions,
	})

	// One exchange goes through with no rules at all.
	first := &proxy.Exchange{
		Started: time.Now(),
		Request: newReq(t, "POST", "https://api.test/v1/login", `{"token":"real"}`),
	}
	p.Begin(first)
	if _, err := p.Request(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	resp := &msg.Response{Status: 200}
	resp.Header = http.Header{"Content-Type": {"application/json"}}
	resp.Body = []byte(`{"token":"real"}`)
	if _, err := p.Response(context.Background(), first, resp); err != nil {
		t.Fatal(err)
	}
	p.Complete(first, resp, nil)

	// Its record, as a reader sees it.
	before, ok := sessions.Get(first.ID)
	if !ok {
		t.Fatal("the first exchange was not recorded")
	}
	beforeBody := string(before.Request.Body)
	beforeResp := string(before.Response.Body)
	if beforeBody != `{"token":"real"}` || beforeResp != `{"token":"real"}` {
		t.Fatalf("recorded %q / %q", beforeBody, beforeResp)
	}

	// Now a rule exists that would have changed it.
	if _, err := rules.Add(rule.Spec{
		Name: "打码", Kind: "rewrite", Phase: "response", Pattern: "*",
		Actions: []rule.ActionSpec{
			{Type: "replace-body", Find: `"real"`, Replace: `"***"`},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// The record must not have moved: history is not rewritten.
	after, _ := sessions.Get(first.ID)
	if got := string(after.Request.Body); got != beforeBody {
		t.Errorf("the recorded request changed to %q", got)
	}
	if got := string(after.Response.Body); got != beforeResp {
		t.Errorf("the recorded response changed to %q", got)
	}

	// A later exchange is changed on the way through.
	second := &proxy.Exchange{
		Started: time.Now(),
		Request: newReq(t, "POST", "https://api.test/v1/login", `{"token":"real"}`),
	}
	p.Begin(second)
	if _, err := p.Request(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	resp2 := &msg.Response{Status: 200}
	resp2.Header = http.Header{"Content-Type": {"application/json"}}
	resp2.Body = []byte(`{"token":"real"}`)
	final, err := p.Response(context.Background(), second, resp2)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(final.Body); got != `{"token":"***"}` {
		t.Errorf("the new response is %q, want it rewritten", got)
	}
	// And what is recorded for it is what the rule made.
	p.Complete(second, final, nil)
	rec2, _ := sessions.Get(second.ID)
	if got := string(rec2.Response.Body); got != `{"token":"***"}` {
		t.Errorf("the second record is %q", got)
	}
}

// newReq builds a request for the test.
func newReq(t *testing.T, method, raw, body string) *msg.Request {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := &msg.Request{Method: method, URL: u, Proto: "HTTP/1.1"}
	r.Header = http.Header{"Content-Type": {"application/json"}}
	r.Body = []byte(body)
	return r
}
