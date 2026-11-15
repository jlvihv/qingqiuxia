package ui_test

import (
	"testing"

	"github.com/egoist/mygo/ui"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// TestSelectionFollowsItsRow checks that choosing a row and then letting new
// traffic arrive keeps the choice on the same session, not on the same index:
// a capture tool is used while traffic streams in, and a choice that slides
// down the list every second is unusable.
func TestSelectionFollowsItsRow(t *testing.T) {
	app, store, tt := newTester(t,
		"https://api.test/v1/users",
		"https://api.test/v1/orders",
	)
	app.Selected = 0
	tt.Frame()
	before, _ := app.SelectedSession()
	if before.Path() != "/v1/orders" {
		t.Fatalf("chose %q", before.Path())
	}

	// A new session arrives at the newest end.
	store.Upsert(qqx.Session{ID: 99, Phase: "complete", Method: "GET", Status: 200,
		URL: "https://api.test/v1/new", Host: "api.test"})
	app.InvalidateAppend()
	tt.Frame()

	after, ok := app.SelectedSession()
	if !ok {
		t.Fatal("nothing chosen after the list grew")
	}
	if after.Path() != before.Path() {
		t.Errorf("the choice moved from %q to %q", before.Path(), after.Path())
	}
}

// TestClearEmptiesTheList drives the clear action: nothing chosen, no rows.
func TestClearEmptiesTheList(t *testing.T) {
	app, store, tt := newTester(t, "https://api.test/v1/users")
	app.Selected = 0
	tt.Frame()

	store.Clear()
	app.Invalidate()
	tt.Frame()

	if got := len(app.Visible()); got != 0 {
		t.Errorf("rows = %d after clear", got)
	}
	if _, ok := app.SelectedSession(); ok {
		t.Error("a session is still chosen after the list was cleared")
	}
}

// TestPendingRowShowsThrough checks that a session the core has started but not
// finished draws without a status, which is how a user sees it is in flight.
func TestPendingRowShowsThrough(t *testing.T) {
	s := qqx.NewStore(10)
	s.Upsert(qqx.Session{ID: 1, Phase: "pending", Method: "POST",
		URL: "https://api.test/v1/slow", Host: "api.test"})
	app := qqx.NewApp(s)
	app.ProxyAddr, app.Version = "127.0.0.1:8899", "test"
	tt := ui.NewTester(app.View, 1200, 700)

	if !tt.HasText("/v1/slow") {
		t.Error("the in-flight row is not drawn")
	}
	if tt.HasText("200") {
		t.Error("an in-flight row shows a status")
	}
}
