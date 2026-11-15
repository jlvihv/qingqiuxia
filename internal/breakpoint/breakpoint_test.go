package breakpoint

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/msg"
)

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

func TestInterceptWaitsForDecision(t *testing.T) {
	m := NewManager(0)
	if err := m.Replace([]Spec{{Name: "all", Phase: "request", Pattern: "*"}}); err != nil {
		t.Fatal(err)
	}
	req := request(t, "https://x.test/page")
	type result struct {
		out Intercept
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := m.Intercept(context.Background(), 7, OnRequest, req, nil)
		done <- result{out, err}
	}()

	var pending *Paused
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if list := m.Pending(); len(list) > 0 {
			pending = list[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	if pending == nil {
		t.Fatal("nothing paused")
	}
	if pending.SessionID != 7 {
		t.Errorf("session = %d", pending.SessionID)
	}

	// Edit the paused request, as the API does, then release it.
	pending.Request.SetHeader("X-Edited", "yes")
	if !m.Resume(pending.ID, Release) {
		t.Fatal("resume failed")
	}
	res := <-done
	if res.err != nil {
		t.Fatal(res.err)
	}
	if !res.out.Matched {
		t.Error("not reported as matched")
	}
	if req.Header.Get("X-Edited") != "yes" {
		t.Error("the edit was lost")
	}
	if m.PauseCount() != 0 {
		t.Errorf("still %d paused", m.PauseCount())
	}
}

func TestAbortAtBreakpoint(t *testing.T) {
	m := NewManager(0)
	m.Replace([]Spec{{Name: "all", Phase: "request", Pattern: "*"}})
	done := make(chan Intercept, 1)
	go func() {
		out, _ := m.Intercept(context.Background(), 1, OnRequest, request(t, "https://x.test/"), nil)
		done <- out
	}()
	p := waitPending(t, m)
	m.Resume(p.ID, Abort)
	out := <-done
	if !out.Abort || out.Status != 403 {
		t.Errorf("out = %+v", out)
	}
}

func TestNoMatchPassesThrough(t *testing.T) {
	m := NewManager(0)
	m.Replace([]Spec{{Name: "only api", Phase: "request", Pattern: "x.test/api/*"}})
	out, err := m.Intercept(context.Background(), 1, OnRequest, request(t, "https://x.test/other"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Matched {
		t.Error("an unmatched request matched")
	}
}

func TestReadOnlyBreakpointDoesNotWait(t *testing.T) {
	m := NewManager(0)
	m.Replace([]Spec{{Name: "watch", Phase: "request", Pattern: "*", ReadOnly: true}})
	// A read-only breakpoint must return at once: nothing waits.
	done := make(chan struct{})
	go func() {
		out, err := m.Intercept(context.Background(), 1, OnRequest, request(t, "https://x.test/"), nil)
		if err != nil || !out.Matched {
			t.Errorf("out = %+v err = %v", out, err)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a read-only breakpoint waited")
	}
	if m.PauseCount() != 0 {
		t.Errorf("a read-only breakpoint left %d pending", m.PauseCount())
	}
}

func TestTimeout(t *testing.T) {
	m := NewManager(30 * time.Millisecond)
	m.Replace([]Spec{{Name: "all", Phase: "request", Pattern: "*"}})
	_, err := m.Intercept(context.Background(), 1, OnRequest, request(t, "https://x.test/"), nil)
	if err != ErrTimeout {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
	if m.PauseCount() != 0 {
		t.Error("a timed-out pause stayed pending")
	}
}

func TestContextCancel(t *testing.T) {
	m := NewManager(0)
	m.Replace([]Spec{{Name: "all", Phase: "request", Pattern: "*"}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.Intercept(ctx, 1, OnRequest, request(t, "https://x.test/"), nil)
		done <- err
	}()
	waitPending(t, m)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("err = %v", err)
	}
}

// waitPending waits for a pause to appear.
func waitPending(t *testing.T, m *Manager) *Paused {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if list := m.Pending(); len(list) > 0 {
			return list[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("nothing paused")
	return nil
}
