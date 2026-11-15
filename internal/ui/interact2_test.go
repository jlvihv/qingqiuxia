package ui_test

import (
	"testing"
)

// TestClickingARowSelectsIt drives the table: a click on a row must choose it
// and fill the detail panel.
func TestClickingARowSelectsIt(t *testing.T) {
	app, _, tt := newTester(t,
		"https://api.test/v1/users",
		"https://api.test/v1/orders",
	)
	app.Selected = -1
	tt.Frame()

	// The path is what the row shows; clicking it chooses the row.
	if err := tt.Click("/v1/orders"); err != nil {
		t.Fatalf("click a row: %v", err)
	}
	tt.Frame()

	if app.Selected != 0 {
		t.Errorf("selected = %d, want 0 (the newest row)", app.Selected)
	}
	sess, ok := app.SelectedSession()
	if !ok || sess.Path() != "/v1/orders" {
		t.Errorf("the detail panel shows %q", sess.Path())
	}
	// The detail panel draws the chosen request's URL.
	if !tt.HasText("https://api.test/v1/orders") {
		t.Error("the detail panel did not draw the chosen request")
	}
}

// TestDetailTabsSwitch drives the tab strip: choosing a tab must change the
// pane the panel draws.
func TestDetailTabsSwitch(t *testing.T) {
	app, _, tt := newTester(t, "https://api.test/v1/users")
	app.Selected = 0
	tt.Frame()

	if !tt.HasText("总耗时") {
		t.Fatal("the overview is not the first tab")
	}
	if err := tt.Click("时序"); err != nil {
		t.Fatalf("click the timing tab: %v", err)
	}
	tt.Frame()
	if app.Detail != 4 {
		t.Errorf("detail tab = %d, want 4", app.Detail)
	}
	if tt.HasText("总耗时") {
		t.Error("the overview is still drawn after choosing another tab")
	}
	if !tt.HasText("下载") {
		t.Error("the timing pane is not drawn")
	}
}

// TestSidebarHostFilters checks that a host in the sidebar narrows the list to
// that host, which is what clicking it means.
func TestSidebarHostFilters(t *testing.T) {
	app, _, tt := newTester(t,
		"https://api.test/v1/users",
		"https://cdn.test/app.js",
	)
	if got := len(app.Visible()); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
	if err := tt.Click("cdn.test"); err != nil {
		t.Fatalf("click a host: %v", err)
	}
	tt.Frame()
	if len(app.Filter.Hosts) != 1 || app.Filter.Hosts[0] != "cdn.test" {
		t.Fatalf("filter hosts = %v", app.Filter.Hosts)
	}
	if got := len(app.Visible()); got != 1 {
		t.Errorf("rows = %d, want the one on cdn.test", got)
	}
}
