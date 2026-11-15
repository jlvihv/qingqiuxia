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
// the same port again.
func TestProxyStartFailureShowsTheReason(t *testing.T) {
	app, tt := newPortTester(t)
	app.PortText = "8899"
	tt.Frame()

	app.SetProxyError(errors.New("address already in use"))
	tt.Frame()

	if app.ProxyRunning {
		t.Error("the proxy reports running after a failed start")
	}
	if !tt.HasText("address already in use") {
		t.Error("the reason is not shown")
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
		t.Error("a running proxy does not show its address")
	}
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
	if !tt.HasText("启动") {
		t.Error("the start control did not come back")
	}
	if app.PortText == "" {
		t.Error("the port field is empty after stopping; the last address should remain")
	}
}

// TestRecordingLightHasThreeStates checks that the light does not claim to be
// recording when nothing is listening: a proxy that is off produces no traffic,
// and a red "recording" beside a stopped proxy is a lie the user acts on.
func TestRecordingLightHasThreeStates(t *testing.T) {
	app, tt := newPortTester(t)
	tt.Frame()
	if tt.HasText("录制中") {
		t.Error("a stopped proxy shows a recording light")
	}
	if !tt.HasText("代理未启动") {
		t.Error("a stopped proxy does not say so")
	}

	// Running but not recording is its own state, not the same as off.
	app.SetProxyRunning("127.0.0.1:8899")
	app.SetCapturing(false)
	app.Recording = false
	tt.Frame()
	if !app.ProxyRunning {
		t.Fatal("the proxy should be running")
	}
	if tt.HasText("录制中") {
		t.Error("a paused capture still shows a recording light")
	}

	// Recording, with the proxy running.
	app.Recording = true
	tt.Frame()
	if !tt.HasText("录制中") {
		t.Error("a recording core does not say so")
	}
}

// TestCaptureToggleNeedsAProxy checks that the light is not a switch while
// nothing listens: toggling capture on a stopped proxy would ask the core to do
// something that cannot happen.
func TestCaptureToggleNeedsAProxy(t *testing.T) {
	called := false
	app, tt := newPortTester(t)
	app.OnToggleCapture = func(bool) { called = true }
	tt.Frame()

	if err := tt.Click("代理未启动"); err == nil {
		// The label is the state; a click must not reach the callback even if
		// the element is hit.
		tt.Frame()
	}
	if called {
		t.Error("a click on a stopped proxy asked to toggle capture")
	}
}
