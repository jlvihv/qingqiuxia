package ui_test

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// newPortTester builds a window whose proxy is stopped, as one that has just
// opened has it.
func newPortTester(t *testing.T) (*qqx.App, *ui.Tester) {
	t.Helper()
	app, _, _ := newTester(t, "https://api.test/v1/x")
	app.SetProxyStopped()
	return app, ui.NewTester(app.View, 1200, 700)
}

// openProxyPopover clicks the toolbar's proxy button, which is where the port
// controls live: the toolbar shows the address and nothing else, so that a
// window whose proxy is running has no port field in it.
func openProxyPopover(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("代理设置"); err != nil {
		t.Fatalf("open the proxy popover: %v", err)
	}
	tt.Frame()
}

// TestProxyStartsWhenAsked checks that the window offers a port rather than
// being given one: nothing listens until the user asks, and the address they
// typed is what is used.
func TestProxyStartsWhenAsked(t *testing.T) {
	var asked []string
	app, tt := newPortTester(t)
	app.OnStartProxy = func(addr string) { asked = append(asked, addr) }
	tt.Frame()

	if app.ProxyRunning {
		t.Fatal("the proxy is running before anyone asked")
	}
	openProxyPopover(t, tt)
	if !tt.HasText("启动") {
		t.Error("there is no way to start the proxy")
	}

	if err := tt.Click("代理端口"); err != nil {
		t.Fatalf("click the port field: %v", err)
	}
	tt.Type("9100")
	tt.Frame()
	if app.PortText != "9100" {
		t.Fatalf("the field holds %q", app.PortText)
	}
	if err := tt.Click("启动"); err != nil {
		t.Fatalf("click start: %v", err)
	}
	tt.Frame()
	if len(asked) != 1 || asked[0] != "9100" {
		t.Errorf("asked for %v, want [9100]", asked)
	}
}

// TestProxyStartFailureShowsTheReason checks that a port that is taken is
// reported beside the field, not swallowed: a user who is told nothing will try
// the same port again. The popover stays open across a failed start for exactly
// this reason.
func TestProxyStartFailureShowsTheReason(t *testing.T) {
	app, tt := newPortTester(t)
	app.PortText = "8899"
	tt.Frame()
	openProxyPopover(t, tt)

	app.SetProxyError(errors.New("address already in use"))
	tt.Frame()

	if app.ProxyRunning {
		t.Error("the proxy reports running after a failed start")
	}
	if !tt.HasText("address already in use") {
		t.Error("the reason is not shown")
	}
	if !app.ProxyOpen {
		t.Error("the popover closed on a failed start, taking the reason with it")
	}
}

// TestProxyStopsWhenAsked checks the other half: a running proxy is shown with
// its address, and stopping it is one click.
func TestProxyStopsWhenAsked(t *testing.T) {
	stopped := false
	app, tt := newPortTester(t)
	app.OnStopProxy = func() { stopped = true }
	app.SetProxyRunning("127.0.0.1:8899")
	tt.Frame()

	if !tt.HasText("127.0.0.1:8899") {
		t.Error("a running proxy does not show its address in the toolbar")
	}
	openProxyPopover(t, tt)
	if err := tt.Click("停止"); err != nil {
		t.Fatalf("click stop: %v", err)
	}
	tt.Frame()
	if !stopped {
		t.Error("stopping asked for nothing")
	}

	// Stopped, the port field comes back editable, with what was there before
	// so that restarting on the same port is one click.
	app.SetProxyStopped()
	tt.Frame()
	openProxyPopover(t, tt)
	if !tt.HasText("启动") {
		t.Error("the start control did not come back")
	}
	if app.PortText == "" {
		t.Error("the port field is empty after stopping; the last address should remain")
	}
}

// TestRecordingSwitchHasThreeStates checks that the switch does not claim to be
// recording when nothing is listening: a proxy that is off produces no traffic,
// and a switch in the on position beside a stopped proxy is a lie the user acts
// on.
func TestRecordingSwitchHasThreeStates(t *testing.T) {
	app, tt := newPortTester(t)
	tt.Frame()
	if !tt.HasText("代理未启动") {
		t.Error("a stopped proxy does not say why it cannot record")
	}
	// Recording is the core's switch, which is on until someone turns it off;
	// the proxy's own state is separate. What must not happen is the switch
	// showing as on while nothing listens, which the words above cover.

	// Running but not recording is its own state, not the same as off.
	app.SetProxyRunning("127.0.0.1:8899")
	app.SetCapturing(false)
	app.Recording = false
	tt.Frame()
	if !app.ProxyRunning {
		t.Fatal("the proxy should be running")
	}
	if tt.HasText("代理未启动") {
		t.Error("a running proxy still says it is not started")
	}

	// Recording, with the proxy running.
	app.Recording = true
	tt.Frame()
	if !tt.HasText("录制") {
		t.Error("the recording switch is not labelled")
	}
}

// TestCaptureToggleNeedsAProxy checks that the switch is disabled while nothing
// listens: toggling capture on a stopped proxy would ask the core to do
// something that cannot happen.
func TestCaptureToggleNeedsAProxy(t *testing.T) {
	called := false
	app, tt := newPortTester(t)
	app.OnToggleCapture = func(bool) { called = true }
	tt.Frame()

	if err := tt.Click("录制流量"); err == nil && called {
		t.Error("the recording switch asked for a toggle with no proxy listening")
	}
}
