package api_test

import (
	"net/http"
	"testing"

	"github.com/jlvihv/qingqiuxia/internal/api"
)

// fakeProxy is a ProxyController a test can drive: the API's routes are what is
// under test, not the proxy's listening.
type fakeProxy struct {
	running bool
	addr    string
	// startErr is what StartProxy returns, for a port that is taken.
	startErr error
}

func (f *fakeProxy) ProxyStatus() (bool, string) { return f.running, f.addr }
func (f *fakeProxy) StartProxy(addr string) (string, error) {
	if f.startErr != nil {
		return "", f.startErr
	}
	f.running = true
	if addr == "" {
		addr = "127.0.0.1:50000"
	}
	f.addr = addr
	return addr, nil
}
func (f *fakeProxy) StopProxy() error {
	f.running, f.addr = false, ""
	return nil
}

// TestProxyRoutesSteerTheProxy checks the control API's proxy routes: the two
// interfaces are separate, and the proxy's own listener is steered through the
// API rather than being started by the program.
func TestProxyRoutesSteerTheProxy(t *testing.T) {
	fp := &fakeProxy{}
	ts := newServerWith(t, api.Options{}, func(c *api.Core) { c.Proxy = fp })

	var st struct {
		Running bool   `json:"running"`
		Addr    string `json:"addr"`
	}
	ts.do(t, "GET", "/api/proxy", nil, &st)
	if st.Running {
		t.Fatal("the proxy is reported running before it was started")
	}

	// A port the caller chose.
	ts.do(t, "POST", "/api/proxy/start", map[string]any{"addr": "127.0.0.1:8899"}, &st)
	if !st.Running || st.Addr != "127.0.0.1:8899" {
		t.Fatalf("after start: %+v", st)
	}
	if ts.do(t, "GET", "/api/proxy", nil, &st); !st.Running {
		t.Error("the status does not see the started proxy")
	}

	ts.do(t, "POST", "/api/proxy/stop", nil, &st)
	if st.Running {
		t.Error("the proxy still runs after a stop")
	}
	if ts.do(t, "GET", "/api/proxy", nil, &st); st.Running {
		t.Error("the status still says running")
	}
}

// TestProxyStartReportsTheReason checks that a port that is taken is reported:
// a caller who is told only "conflict" will try the same port again.
func TestProxyStartReportsTheReason(t *testing.T) {
	fp := &fakeProxy{startErr: errTest("address already in use")}
	ts := newServerWith(t, api.Options{}, func(c *api.Core) { c.Proxy = fp })

	var errResp struct {
		Error string `json:"error"`
	}
	code := ts.do(t, "POST", "/api/proxy/start", map[string]any{"addr": "8899"}, &errResp)
	if code != http.StatusConflict {
		t.Errorf("code = %d, want 409", code)
	}
	if errResp.Error == "" {
		t.Error("no reason given")
	}
}

// TestProxyRoutesNeedAController checks that a core without a proxy says so
// rather than answering with a zero value that looks like a stopped proxy.
func TestProxyRoutesNeedAController(t *testing.T) {
	ts := newServer(t, api.Options{})
	if code := ts.do(t, "GET", "/api/proxy", nil, nil); code != http.StatusNotImplemented {
		t.Errorf("code = %d, want 501", code)
	}
}

// errTest is a stand-in for a bind failure.
type errTest string

func (e errTest) Error() string { return string(e) }
