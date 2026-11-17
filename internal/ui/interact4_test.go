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
