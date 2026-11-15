package ui_test

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// countDataRows counts the row nodes of the traffic table, leaving out the
// header: the framework gives the header the row role too, since a screen
// reader reads it as the row of column names.
func countDataRows(tt *ui.Tester) []string {
	var rows []string
	for _, info := range tt.AccessAll() {
		if info.Role != ui.RoleRow {
			continue
		}
		// The header's name is the column titles joined; a data row's begins
		// with its id.
		if len(info.Name) > 0 && info.Name[0] == '#' {
			continue
		}
		rows = append(rows, info.Name)
	}
	return rows
}

// TestNoEmptyRowsAfterUpdates pins the defect a window showed: six sessions, and
// a second band of six rows below them that held nothing and still took the
// pointer and a click.
//
// The core publishes each session twice, once when it starts and once when it
// ends. The window treated both as arrivals and shifted every row by a place,
// putting an empty row at the front each time — so the list doubled.
func TestNoEmptyRowsAfterUpdates(t *testing.T) {
	s := qqx.NewStore(100)
	app := qqx.NewApp(s)
	app.Selected = 0

	// What the stream does: each session starts, and later completes.
	for i := 1; i <= 6; i++ {
		s.Upsert(qqx.Session{ID: uint64(i), Phase: "pending", Method: "GET",
			URL: fmt.Sprintf("https://a.test/r%d", i), Host: "a.test"})
		app.InvalidateAppend(uint64(i))
	}
	for i := 1; i <= 6; i++ {
		s.Upsert(qqx.Session{ID: uint64(i), Phase: "complete", Method: "GET", Status: 200,
			URL: fmt.Sprintf("https://a.test/r%d", i), Host: "a.test"})
		// An update, not an arrival.
		app.Invalidate()
	}

	tt := ui.NewTester(app.View, 1440, 900)
	tt.Frame()

	rows := countDataRows(tt)
	t.Logf("data rows drawn: %d", len(rows))
	for _, name := range rows {
		t.Logf("   %q", name)
	}
	if len(rows) != 6 {
		t.Errorf("the table drew %d rows for 6 sessions", len(rows))
	}
	if got, want := len(app.Visible()), 6; got != want {
		t.Errorf("visible rows = %d, want %d", got, want)
	}
}

// TestEmptyRowsTakeNoPointer checks what a user noticed: the rows below the data
// answered the pointer. With the rows right there is nothing there to answer.
func TestEmptyRowsTakeNoPointer(t *testing.T) {
	s := qqx.NewStore(100)
	app := qqx.NewApp(s)
	app.Selected = 0
	for i := 1; i <= 3; i++ {
		s.Upsert(qqx.Session{ID: uint64(i), Phase: "pending", Method: "GET",
			URL: fmt.Sprintf("https://a.test/r%d", i), Host: "a.test"})
		app.InvalidateAppend(uint64(i))
		s.Upsert(qqx.Session{ID: uint64(i), Phase: "complete", Method: "GET", Status: 200,
			URL: fmt.Sprintf("https://a.test/r%d", i), Host: "a.test"})
		app.Invalidate()
	}
	tt := ui.NewTester(app.View, 1440, 900)
	tt.Frame()

	// Every row the pointer can reach must name a session.
	for _, name := range countDataRows(tt) {
		if name == "" {
			t.Error("a row with no session is drawn and can be hovered")
		}
	}
}
