package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/proxy"
)

// TestStopWithLiveConnection checks that stopping a proxy that is serving a
// keep-alive connection returns.
//
// It did not: Stop closed the connections while holding the lock that each
// connection's own Close takes to unregister itself, so one connection with a
// client attached deadlocked the stop — and with it whoever asked, which in the
// merged program is the window's button and the control API.
func TestStopWithLiveConnection(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer origin.Close()

	p := proxy.New(noopPolicy{}, proxy.Tuning{})
	addr, err := p.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse("http://" + addr)
	// Keep-alive is what keeps a connection registered with the proxy.
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	defer client.CloseIdleConnections()

	done := make(chan error, 1)
	go func() { done <- p.Stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("stop: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop deadlocked with a live connection")
	}

	// The proxy is usable again, on another port.
	second, err := p.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start after a busy stop: %v", err)
	}
	if second == addr {
		t.Errorf("the same address again: %q", second)
	}
	if err := p.Stop(); err != nil {
		t.Errorf("second stop: %v", err)
	}
}
