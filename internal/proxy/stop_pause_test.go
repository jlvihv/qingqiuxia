package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/mitm"
	"github.com/jlvihv/qingqiuxia/internal/pipeline"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

// TestStopEndsAPausedRequest checks that stopping the proxy does not wait for a
// breakpoint nobody is going to answer.
//
// The waiting side is a goroutine blocked on a channel, not on its socket, so
// closing connections did not reach it: Shutdown then waited out its own timeout
// while the handler sat on a five-minute breakpoint timer, and the port stayed
// held for as long as that took.
func TestStopEndsAPausedRequest(t *testing.T) {
	ca, err := mitm.Generate("test CA")
	if err != nil {
		t.Fatal(err)
	}
	breaks := breakpoint.NewManager(5 * time.Minute)
	if err := breaks.Replace([]breakpoint.Spec{{Name: "all", Phase: "request", Pattern: "*"}}); err != nil {
		t.Fatal(err)
	}
	pol := pipeline.New(pipeline.Options{
		Rules:       rule.NewEngine(),
		Breakpoints: breaks,
		Sessions:    session.NewStore(10),
		Issuer:      mitm.NewIssuer(ca),
	})
	p := proxy.New(pol, proxy.Tuning{})
	if _, err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer origin.Close()

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := clientThrough(t, p.Addr()).Get(origin.URL)
		if err == nil {
			resp.Body.Close()
		}
	}()

	// Wait for the exchange to be parked, then stop.
	deadline := time.Now().Add(5 * time.Second)
	for breaks.PauseCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if breaks.PauseCount() == 0 {
		t.Fatal("the request was never paused, so there is nothing to stop")
	}

	start := time.Now()
	if err := p.Stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
	stopped := time.Since(start)
	if stopped > 6*time.Second {
		t.Errorf("Stop took %v; it waited for the breakpoint's timer", stopped)
	}

	select {
	case <-clientDone:
	case <-time.After(3 * time.Second):
		t.Error("the paused handler never returned")
	}
}
