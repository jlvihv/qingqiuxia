package ui_test

import (
	"testing"

	"github.com/egoist/mygo/ui"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// newTesterWith is newTester plus a hook to adjust the app before the window is
// built, for the tests that need a pause or a callback.
func newTesterWith(t *testing.T, adjust func(*qqx.App), urls ...string) (*qqx.App, *ui.Tester) {
	t.Helper()
	app, _, _ := newTester(t, urls...)
	if adjust != nil {
		adjust(app)
	}
	return app, ui.NewTester(app.View, 1200, 700)
}

// TestRecordingToggleCallsBack checks that the recording light is a switch: a
// click asks for capture to be turned off, and turning it back on asks again.
func TestRecordingToggleCallsBack(t *testing.T) {
	var calls []bool
	app, tt := newTesterWith(t, func(a *qqx.App) {
		a.OnToggleCapture = func(on bool) { calls = append(calls, on) }
		// The light is a switch only while the proxy listens, so the test
		// starts it.
		a.SetProxyRunning("127.0.0.1:8899")
	}, "https://api.test/v1/x")

	// The button's label is the action it performs, which is what a screen
	// reader announces and what a test clicks.
	if err := tt.Click("停止录制"); err != nil {
		t.Fatalf("click the recording light: %v", err)
	}
	tt.Frame()
	if len(calls) != 1 || calls[0] != false {
		t.Fatalf("calls = %v, want [false]", calls)
	}

	app.SetCapturing(false)
	app.Recording = false
	tt.Frame()
	if err := tt.Click("开始录制"); err != nil {
		t.Fatalf("click again: %v", err)
	}
	tt.Frame()
	if len(calls) != 2 || calls[1] != true {
		t.Errorf("calls = %v, want [false true]", calls)
	}
}

// TestUnreachableCoreCannotToggle checks that the switch does nothing when the
// core is not reachable: the light already says so, and an action that cannot
// land must not pretend to.
func TestUnreachableCoreCannotToggle(t *testing.T) {
	called := false
	app, tt := newTesterWith(t, func(a *qqx.App) {
		a.OnToggleCapture = func(bool) { called = true }
	})
	app.SetCoreDown(errTest{})
	tt.Frame()

	if tt.HasText("录制中") {
		t.Error("an unreachable core still shows a recording light")
	}
	if err := tt.Click("停止录制"); err == nil {
		t.Errorf("the label should be the disabled one, not the action")
	}
	tt.Frame()
	if called {
		t.Error("a click on an unreachable core asked to toggle capture")
	}
}

// TestPauseBannerResumes checks the banner: it shows what stopped and where,
// and its buttons ask for the decision.
func TestPauseBannerResumes(t *testing.T) {
	var resumed, aborted uint64
	_, tt := newTesterWith(t, func(a *qqx.App) {
		a.OnResume = func(id uint64) { resumed = id }
		a.OnAbort = func(id uint64) { aborted = id }
		a.SetPause(&qqx.Pause{
			ID: 7, SessionID: 3, Phase: "request",
			RuleName: "登录打桩", Method: "POST", URL: "https://api.test/v1/login",
		})
	}, "https://api.test/v1/login")

	if !tt.HasText("已暂停") || !tt.HasText("登录打桩") {
		t.Fatal("the banner does not say what stopped")
	}
	if err := tt.Click("放行"); err != nil {
		t.Fatalf("click release: %v", err)
	}
	tt.Frame()
	if resumed != 7 {
		t.Errorf("resumed = %d, want 7", resumed)
	}
	if err := tt.Click("中止"); err != nil {
		t.Fatalf("click abort: %v", err)
	}
	tt.Frame()
	if aborted != 7 {
		t.Errorf("aborted = %d, want 7", aborted)
	}
}

// TestNoBannerWithoutPause checks that the banner only appears while something
// waits, so that it does not become permanent furniture.
func TestNoBannerWithoutPause(t *testing.T) {
	_, _, tt := newTester(t, "https://api.test/v1/x")
	if tt.HasText("已暂停") {
		t.Error("the banner shows with nothing paused")
	}
}

// errTest is a stand-in for a connection error.
type errTest struct{}

func (errTest) Error() string { return "connection refused" }
