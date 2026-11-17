package ui_test

import (
	"testing"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// TestStatusBarSaysWhenTheCoreIsGone checks that an unreachable core is
// reported where the counts are: the memory figure and the goroutine count are
// stale the moment the core stops answering, and silence about it is worse than
// no number.
func TestStatusBarSaysWhenTheCoreIsGone(t *testing.T) {
	_, tt := newTesterWith(t, func(a *qqx.App) {
		a.SetCoreDown(errTest{})
	})
	tt.Frame()
	if !tt.HasText("未连接") {
		t.Errorf("an unreachable core is not shown in the status bar: %v", tt.Texts())
	}
}
