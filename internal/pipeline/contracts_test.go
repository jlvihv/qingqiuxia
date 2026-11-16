package pipeline

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

func pipelineRequest() *msg.Request {
	return &msg.Request{
		Method:  "GET",
		URL:     &url.URL{Scheme: "https", Host: "example.test", Path: "/x"},
		Message: msg.Message{Header: map[string][]string{}},
	}
}

// TestResponseBreakpointAbortReplacesTheResponse checks what an abort at a
// response breakpoint means. It produced a note that said "aborted" and a client
// that got the origin's 200 with its body — the decision was recorded and then
// ignored.
func TestResponseBreakpointAbortReplacesTheResponse(t *testing.T) {
	m := breakpoint.NewManager(time.Second)
	if err := m.Replace([]breakpoint.Spec{{Name: "bp", Phase: "response", Pattern: "*"}}); err != nil {
		t.Fatal(err)
	}
	pol := New(Options{Rules: rule.NewEngine(), Breakpoints: m, Sessions: session.NewStore(10)})
	ex := &proxy.Exchange{Started: time.Now(), Request: pipelineRequest()}
	pol.Begin(ex)

	go func() {
		for i := 0; i < 500; i++ {
			if pending := m.Pending(); len(pending) > 0 {
				m.Resume(pending[0].ID, breakpoint.Abort)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	origin := &msg.Response{
		Status:  200,
		Message: msg.Message{Body: []byte("origin body")},
	}
	out, err := pol.Response(context.Background(), ex, origin)
	if err != nil {
		t.Fatalf("response: %v", err)
	}
	if out.Status == 200 {
		t.Error("the client still gets the origin's 200 after an abort")
	}
	if string(out.Body) == "origin body" {
		t.Error("the client still gets the origin's body after an abort")
	}
}

// TestSnapshotDoesNotShareTheLiveRequest checks the copy the store keeps. The
// store used to hold the request the proxy was still editing, so a list call
// could copy a header map while a rule wrote it — a data race and a torn
// snapshot in one.
func TestSnapshotDoesNotShareTheLiveRequest(t *testing.T) {
	pol := New(Options{Sessions: session.NewStore(10)})
	live := pipelineRequest()
	live.Header.Set("X-Before", "1")
	ex := &proxy.Exchange{Started: time.Now(), Request: live}
	pol.Begin(ex)

	snap, ok := pol.Sessions().Get(ex.ID)
	if !ok {
		t.Fatal("no session")
	}
	live.Header.Set("X-After", "1")
	if snap.Request.Header.Get("X-After") != "" {
		t.Error("the snapshot sees a header added to the live request after it was taken")
	}
	// The proxy's copy is its own too, so a rule may edit it.
	live.URL.Path = "/changed"
	if snap.Request.URL.Path != "/x" {
		t.Error("the snapshot shares the live URL")
	}
}
