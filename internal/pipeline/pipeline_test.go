package pipeline_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/pipeline"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

func newPipeline(t *testing.T) (*pipeline.Pipeline, *rule.Engine, *session.Store, *breakpoint.Manager) {
	t.Helper()
	rules := rule.NewEngine()
	pauses := breakpoint.NewManager(time.Second)
	sessions := session.NewStore(100)
	p := pipeline.New(pipeline.Options{Rules: rules, Breakpoints: pauses, Sessions: sessions})
	return p, rules, sessions, pauses
}

func request(t *testing.T, raw string) *msg.Request {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := &msg.Request{Method: "GET", URL: u}
	r.Header = http.Header{}
	return r
}

func TestFullExchange(t *testing.T) {
	p, rules, sessions, _ := newPipeline(t)
	rules.Replace([]rule.Spec{{
		Name: "tag", Kind: "rewrite", Phase: "request", Pattern: "*",
		Actions: []rule.ActionSpec{{Type: "set-header", Name: "X-Tagged", Value: "1"}},
	}})

	ex := &proxy.Exchange{Started: time.Now(), Request: request(t, "https://x.test/a")}
	p.Begin(ex)
	if ex.ID != 1 {
		t.Fatalf("id = %d", ex.ID)
	}
	// The session is visible while the request is in flight, which is what a
	// traffic list shows.
	if sessions.Len() != 1 {
		t.Fatalf("sessions = %d", sessions.Len())
	}

	dec, err := p.Request(context.Background(), ex)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Mock != nil || dec.Abort {
		t.Errorf("decision = %+v", dec)
	}
	if ex.Request.Header.Get("X-Tagged") != "1" {
		t.Error("the rule did not run")
	}

	resp := &msg.Response{Status: 200}
	p.Complete(ex, resp, nil)

	snap, _ := sessions.Get(ex.ID)
	if snap.Phase != session.Complete {
		t.Errorf("phase = %s", snap.Phase)
	}
	if len(snap.Notes) != 1 || snap.Notes[0].Rule != "tag" {
		t.Errorf("notes = %+v", snap.Notes)
	}
}

func TestRuleFailureIsNotFatal(t *testing.T) {
	p, rules, sessions, _ := newPipeline(t)
	// A rule whose action fails at run time: a set-url with an unparseable URL
	// is caught at compile time, so use one that fails on apply.
	rules.Replace([]rule.Spec{{
		Name: "bad", Kind: "rewrite", Phase: "request", Pattern: "*",
		Actions: []rule.ActionSpec{{Type: "set-url", Value: "::not a url::"}},
	}})
	ex := &proxy.Exchange{Started: time.Now(), Request: request(t, "https://x.test/")}
	p.Begin(ex)
	dec, err := p.Request(context.Background(), ex)
	if err != nil {
		t.Fatalf("a failing rule must not fail the request: %v", err)
	}
	if dec.Mock != nil || dec.Abort {
		t.Errorf("decision = %+v", dec)
	}
	snap, _ := sessions.Get(ex.ID)
	if len(snap.Notes) == 0 {
		t.Error("the failure was not noted")
	}
}

func TestMockSession(t *testing.T) {
	p, rules, sessions, _ := newPipeline(t)
	rules.Replace([]rule.Spec{{
		Name: "m", Kind: "mock", Phase: "request", Pattern: "*",
		Mock: &rule.MockSpec{Status: 204},
	}})
	ex := &proxy.Exchange{Started: time.Now(), Request: request(t, "https://x.test/")}
	p.Begin(ex)
	dec, _ := p.Request(context.Background(), ex)
	if dec.Mock == nil {
		t.Fatal("no mock")
	}
	p.Complete(ex, dec.Mock, nil)
	snap, _ := sessions.Get(ex.ID)
	if snap.Source != session.Mocked {
		t.Errorf("source = %s", snap.Source)
	}
}

func TestIntercepts(t *testing.T) {
	p, _, _, _ := newPipeline(t)
	// No issuer and no decision function: nothing is decrypted.
	if p.Intercepts("example.com:443") {
		t.Error("intercepted without an issuer")
	}
	if _, err := p.Certificate("example.com"); err == nil {
		t.Error("minted a certificate without an issuer")
	}
}
