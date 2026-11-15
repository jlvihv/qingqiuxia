package ui_test

import (
	"testing"

	"github.com/egoist/mygo/ui"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// newTester builds the window over a store of the given URLs, with the search
// box as the only thing that can take the keyboard focus.
func newTester(t *testing.T, urls ...string) (*qqx.App, *qqx.Store, *ui.Tester) {
	t.Helper()
	s := qqx.NewStore(100)
	for i, u := range urls {
		s.Upsert(qqx.Session{
			ID: uint64(i + 1), Phase: "complete", Method: "GET", Status: 200,
			URL: u, Host: hostOfTest(u), BytesOut: 100,
		})
	}
	app := qqx.NewApp(s)
	app.ProxyAddr = "127.0.0.1:8899"
	app.Version = "test"
	return app, s, ui.NewTester(app.View, 1200, 700)
}

// TestSearchBoxFiltersTyping drives the real widget: click the box, type, and
// check the list narrows as the characters arrive.
func TestSearchBoxFiltersTyping(t *testing.T) {
	app, store, tt := newTester(t,
		"https://api.test/v1/users",
		"https://api.test/v1/orders",
		"https://cdn.test/app.js",
	)
	if got := len(app.Visible()); got != 3 {
		t.Fatalf("before typing = %d rows, want 3", got)
	}

	// The placeholder is how the box is found before it has text.
	if err := tt.Click("过滤"); err != nil {
		t.Fatalf("click the filter box: %v", err)
	}
	tt.Type("order")
	tt.Frame()

	if app.Search != "order" {
		t.Fatalf("the box holds %q", app.Search)
	}
	if app.Filter.Text != "order" {
		t.Fatalf("the filter is %q, want it to follow the box", app.Filter.Text)
	}
	rows := app.Visible()
	if len(rows) != 1 {
		t.Fatalf("after typing = %d rows, want 1", len(rows))
	}
	sess, ok := store.At(rows[0])
	if !ok || sess.Path() != "/v1/orders" {
		t.Errorf("the row kept is %q", sess.Path())
	}
	if tt.HasText("/v1/users") {
		t.Error("a filtered-out row is still drawn")
	}

	// Backspacing to nothing restores the whole list.
	for i := 0; i < len("order"); i++ {
		tt.Key(0, ui.KeyBackspace)
	}
	tt.Frame()
	if got := len(app.Visible()); got != 3 {
		t.Errorf("after clearing = %d rows, want 3", got)
	}
}
