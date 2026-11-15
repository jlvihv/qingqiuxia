package ui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/mitm"
	"github.com/jlvihv/qingqiuxia/internal/pipeline"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// newLocal builds a core in this process and the window's view of it: what the
// merged command does, without a window or a network.
func newLocal(t *testing.T) (*qqx.Local, *qqx.Store, *qqx.App, *proxy.Proxy) {
	t.Helper()
	ca, err := mitm.Generate("test CA")
	if err != nil {
		t.Fatal(err)
	}
	rules := rule.NewEngine()
	sessions := session.NewStore(100)
	pol := pipeline.New(pipeline.Options{
		Rules:       rules,
		Breakpoints: nil,
		Sessions:    sessions,
		Issuer:      mitm.NewIssuer(ca),
	})
	pr := proxy.New(pol, proxy.Tuning{})
	local := &qqx.Local{Pipeline: pol, Proxy: pr, Version: "test", Started: time.Now()}

	store := qqx.NewStore(100)
	app := qqx.NewApp(store)
	app.SetBackend(local, local)
	return local, store, app, pr
}

// TestLocalSessionsRoundTrip drives real traffic through the proxy and reads it
// back through the window's backend: the same path the merged command takes,
// with no JSON and no socket in between.
func TestLocalSessionsRoundTrip(t *testing.T) {
	local, _, _, pr := newLocal(t)
	addr, err := local.StartProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { pr.Stop() })

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))
	defer origin.Close()

	pu, _ := url.Parse("http://" + addr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := client.Get(origin.URL + "/thing")
	if err != nil {
		t.Fatalf("request through the proxy: %v", err)
	}
	resp.Body.Close()

	ctx := context.Background()
	all, total, dropped, err := local.Sessions(ctx, -1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(all) != 1 {
		t.Fatalf("sessions = %d (total %d), want 1", len(all), total)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d", dropped)
	}
	got := all[0]
	if got.URL != origin.URL+"/thing" {
		t.Errorf("url = %q", got.URL)
	}
	if got.Status != 200 || got.BytesOut != 5 {
		t.Errorf("status = %d, bytes = %d", got.Status, got.BytesOut)
	}
	if got.Phase != "complete" {
		t.Errorf("phase = %q", got.Phase)
	}

	st, err := local.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sessions != 1 || !st.Capturing || st.Version != "test" {
		t.Errorf("status = %+v", st)
	}
}

// TestLocalWatchDeliversChanges checks that the window is told about a session
// as a value, which is what replaces an event stream.
func TestLocalWatchDeliversChanges(t *testing.T) {
	local, _, _, pr := newLocal(t)
	addr, err := local.StartProxy("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Stop() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan qqx.Change, 8)
	go local.Watch(ctx, func(c qqx.Change) { changes <- c })

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer origin.Close()
	pu, _ := url.Parse("http://" + addr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := client.Get(origin.URL + "/watched")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// The session is published when it starts and when it ends.
	deadline := time.After(3 * time.Second)
	seen := 0
	for seen < 2 {
		select {
		case c := <-changes:
			if c.Kind != qqx.ChangeSession || c.Session == nil {
				t.Fatalf("change = %+v", c)
			}
			seen++
		case <-deadline:
			t.Fatalf("saw %d changes, want 2", seen)
		}
	}
}

// TestProxyStartsAndStopsRepeatedly checks that the port can be changed: the
// same proxy is stopped and started elsewhere, which is what a user does when a
// port is taken.
func TestProxyStartsAndStopsRepeatedly(t *testing.T) {
	local, _, app, pr := newLocal(t)
	t.Cleanup(func() { pr.Stop() })

	if _, err := local.StartProxy("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	first := app.PortAddr
	_ = first
	if running, addr := local.ProxyStatus(); !running || addr == "" {
		t.Fatalf("running = %v addr = %q", running, addr)
	}
	firstAddr := local.Proxy.Addr()
	if err := local.StopProxy(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if running, _ := local.ProxyStatus(); running {
		t.Error("still running after stop")
	}

	second, err := local.StartProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start again: %v", err)
	}
	if second == "" || second == firstAddr {
		t.Errorf("second address = %q, first was %q", second, firstAddr)
	}

	// A bare port is a port, not a host.
	if _, err := local.StartProxy("127.0.0.1:0"); err == nil {
		t.Error("starting a running proxy should fail")
	}
	local.StopProxy()
	if addr, err := local.StartProxy("0"); err != nil || addr == "" {
		t.Errorf("a port of 0 should take any free one: %q %v", addr, err)
	}
}
