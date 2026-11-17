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

// TestProxyStartsOnOnePress checks the first press of the proxy button: it
// starts listening, on the conventional port, without a panel to fill in first.
func TestProxyStartsOnOnePress(t *testing.T) {
	var asked []string
	app, tt := newPortTester(t)
	app.OnStartProxy = func(addr string) { asked = append(asked, addr) }
	tt.Frame()

	if app.ProxyRunning {
		t.Fatal("the proxy is running before anyone asked")
	}
	if err := tt.Click("代理设置"); err != nil {
		t.Fatalf("click the proxy button: %v", err)
	}
	tt.Frame()
	if len(asked) != 1 || asked[0] != "8899" {
		t.Errorf("asked for %v, want [8899]", asked)
	}
	// The port tried stays in the field, so the panel opens on it if the start
	// fails and the user has to fix it.
	if app.PortText != "8899" {
		t.Errorf("the port field holds %q, want 8899", app.PortText)
	}
}

// TestProxyStartsOnTheLastPort checks that one press does not throw away the
// port the user chose before: a stopped proxy remembers its last address, and
// starting again is the same one click.
func TestProxyStartsOnTheLastPort(t *testing.T) {
	var asked string
	app, tt := newPortTester(t)
	app.PortText = "9100"
	app.OnStartProxy = func(addr string) { asked = addr }
	tt.Frame()

	if err := tt.Click("代理设置"); err != nil {
		t.Fatalf("click the proxy button: %v", err)
	}
	if asked != "9100" {
		t.Errorf("asked for %q, want the remembered 9100", asked)
	}
}

// TestRunningProxyOpensItsPanel checks the second press: a proxy that is up
// shows its address in the toolbar, and the press asks the command for its
// panel, under the button it was pressed on.
func TestRunningProxyOpensItsPanel(t *testing.T) {
	app, tt := newPortTester(t)
	var anchor ui.Rect
	opened := 0
	app.OnToggleProxyPanel = func(a ui.Rect) {
		opened++
		anchor = a
	}
	app.SetProxyRunning("127.0.0.1:8899")
	tt.Frame()

	if !tt.HasText("127.0.0.1:8899") {
		t.Error("a running proxy does not show its address in the toolbar")
	}
	if err := tt.Click("代理设置"); err != nil {
		t.Fatalf("click the proxy button: %v", err)
	}
	tt.Frame()
	if opened != 1 {
		t.Errorf("the press opened the panel %d times, want 1", opened)
	}
	if anchor.W <= 0 || anchor.H <= 0 {
		t.Errorf("the panel's anchor is %+v, want the button's box", anchor)
	}
}

// TestAStoppedProxyThatFailedOpensItsPanel checks that a start which failed is
// not retried blindly on the next press: the button says so, and the press opens
// the panel where the reason and the field are, rather than asking for the same
// port again.
func TestAStoppedProxyThatFailedOpensItsPanel(t *testing.T) {
	app, tt := newPortTester(t)
	opened, asked := 0, 0
	app.OnToggleProxyPanel = func(ui.Rect) { opened++ }
	app.OnStartProxy = func(string) { asked++ }
	tt.Frame()

	app.SetProxyError(errors.New("address already in use"))
	tt.Frame()
	if !tt.HasText("启动失败") {
		t.Error("the toolbar does not say the start failed")
	}
	if err := tt.Click("代理设置"); err != nil {
		t.Fatalf("click the proxy button: %v", err)
	}
	tt.Frame()
	if opened != 1 || asked != 0 {
		t.Errorf("the press opened %d and started %d, want 1 and 0", opened, asked)
	}
}

// TestProxyPanelOffersThePort checks the stopped panel: a field holding the port
// and the control that starts it. It is a view of its own, because the command
// shows it in a window of the system's.
func TestProxyPanelOffersThePort(t *testing.T) {
	var asked string
	app, _, _ := newTester(t, "https://api.test/v1/x")
	app.SetProxyStopped()
	app.PortText = "9100"
	app.OnStartProxy = func(addr string) { asked = addr }
	tt := ui.NewTester(app.ProxyPanel, 320, 180)
	tt.Frame()

	if !tt.HasText("代理端口") || !tt.HasText("启动") {
		t.Fatalf("the panel does not offer a port to start: %v", tt.Texts())
	}
	if err := tt.Click("启动"); err != nil {
		t.Fatalf("click start: %v", err)
	}
	if asked != "9100" {
		t.Errorf("start asked for %q, want the field's 9100", asked)
	}
}

// TestProxyPanelTakesATypedPort checks the field in the panel, which is what a
// user edits after stopping: typing reaches the app's port, and start uses it.
func TestProxyPanelTakesATypedPort(t *testing.T) {
	var asked string
	app, _, _ := newTester(t, "https://api.test/v1/x")
	app.SetProxyStopped()
	app.OnStartProxy = func(addr string) { asked = addr }
	tt := ui.NewTester(app.ProxyPanel, 320, 180)
	tt.Frame()

	if err := tt.Click("代理端口"); err != nil {
		t.Fatalf("click the port field: %v", err)
	}
	tt.Type("9200")
	tt.Frame()
	if app.PortText != "9200" {
		t.Fatalf("the field holds %q, want 9200", app.PortText)
	}
	if err := tt.Click("启动"); err != nil {
		t.Fatalf("click start: %v", err)
	}
	if asked != "9200" {
		t.Errorf("start asked for %q, want 9200", asked)
	}
}

// TestProxyPanelStopsAndBecomesTheEditor checks the running panel: it shows the
// address and one control to stop, and once the core says it stopped, the same
// panel is the port editor — which is where changing the port starts.
func TestProxyPanelStopsAndBecomesTheEditor(t *testing.T) {
	stopped := false
	app, _, _ := newTester(t, "https://api.test/v1/x")
	app.OnStopProxy = func() { stopped = true }
	app.SetProxyRunning("127.0.0.1:8899")
	tt := ui.NewTester(app.ProxyPanel, 320, 140)
	tt.Frame()

	if !tt.HasText("127.0.0.1:8899") {
		t.Error("the running panel does not show the address")
	}
	if err := tt.Click("停止"); err != nil {
		t.Fatalf("click stop: %v", err)
	}
	if !stopped {
		t.Error("stopping asked for nothing")
	}

	app.SetProxyStopped()
	tt.Frame()
	if !tt.HasText("代理端口") || !tt.HasText("启动") {
		t.Errorf("the panel did not become the port editor after stopping: %v", tt.Texts())
	}
	if app.PortText == "" {
		t.Error("the port field is empty after stopping; the last address should remain")
	}
}

// TestProxyPanelShowsTheFailure checks that a port that is taken is reported
// beside the field: a user who is told nothing will try the same port again.
func TestProxyPanelShowsTheFailure(t *testing.T) {
	app, _, _ := newTester(t, "https://api.test/v1/x")
	app.SetProxyStopped()
	app.PortText = "8899"
	app.SetProxyError(errors.New("address already in use"))
	tt := ui.NewTester(app.ProxyPanel, 320, 205)
	tt.Frame()

	if app.ProxyRunning {
		t.Error("the proxy reports running after a failed start")
	}
	if !tt.HasText("address already in use") {
		t.Error("the reason is not shown")
	}
	if !tt.HasText("启动") {
		t.Error("the panel offers no way to try another port")
	}
}
