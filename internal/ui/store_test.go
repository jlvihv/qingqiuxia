package ui_test

import (
	"testing"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// TestStoreNewestFirst checks that rows read newest first while new sessions
// are appended: the window's order is part of its contract with the table.
func TestStoreNewestFirst(t *testing.T) {
	s := qqx.NewStore(10)
	for i := 1; i <= 3; i++ {
		s.Upsert(qqx.Session{ID: uint64(i)})
	}
	if got := s.Len(); got != 3 {
		t.Fatalf("len = %d", got)
	}
	for row, want := range []uint64{3, 2, 1} {
		got, ok := s.At(row)
		if !ok || got.ID != want {
			t.Errorf("row %d = %d, want %d", row, got.ID, want)
		}
	}
}

// TestStoreUpdateKeepsPlace checks that a session arriving again (it is
// published when it starts and when it ends) updates its row where it is.
func TestStoreUpdateKeepsPlace(t *testing.T) {
	s := qqx.NewStore(10)
	s.Upsert(qqx.Session{ID: 1, Phase: "pending"})
	s.Upsert(qqx.Session{ID: 2, Phase: "pending"})
	s.Upsert(qqx.Session{ID: 1, Phase: "complete", Status: 200})

	if s.Len() != 2 {
		t.Fatalf("len = %d, want 2", s.Len())
	}
	got, _ := s.At(1)
	if got.ID != 1 || got.Phase != "complete" || got.Status != 200 {
		t.Errorf("row 1 = %+v", got)
	}
}

// TestStoreLimit checks that the store keeps the newest rows when it is full.
func TestStoreLimit(t *testing.T) {
	s := qqx.NewStore(3)
	for i := 1; i <= 5; i++ {
		s.Upsert(qqx.Session{ID: uint64(i)})
	}
	if s.Len() != 3 {
		t.Fatalf("len = %d, want 3", s.Len())
	}
	for row, want := range []uint64{5, 4, 3} {
		got, ok := s.At(row)
		if !ok || got.ID != want {
			t.Errorf("row %d = %d, want %d", row, got.ID, want)
		}
	}
	// A dropped session must not be reachable by ID.
	s.Remove(3)
	if s.Len() != 2 {
		t.Errorf("len after remove = %d", s.Len())
	}
}

// TestFilter checks the filter the list applies.
func TestFilter(t *testing.T) {
	sess := qqx.Session{Method: "POST", URL: "https://api.test/v1/login", Host: "api.test", Status: 201}
	for _, tc := range []struct {
		f    qqx.Filter
		want bool
	}{
		{qqx.Filter{}, true},
		{qqx.Filter{Text: "login"}, true},
		{qqx.Filter{Text: "LOGIN"}, true},
		{qqx.Filter{Text: "post"}, true},
		{qqx.Filter{Text: "201"}, true},
		{qqx.Filter{Text: "nope"}, false},
		{qqx.Filter{Hosts: []string{"api.test"}}, true},
		{qqx.Filter{Hosts: []string{"other.test"}}, false},
	} {
		if got := tc.f.Match(&sess); got != tc.want {
			t.Errorf("filter %+v = %v, want %v", tc.f, got, tc.want)
		}
	}
}

// TestInvalidateAppend checks that extending the filtered rows by hand matches
// what a full refilter would produce: the fast path must not change the view.
func TestInvalidateAppend(t *testing.T) {
	s := qqx.NewStore(100)
	app := qqx.NewApp(s)
	for i := 1; i <= 5; i++ {
		s.Upsert(qqx.Session{ID: uint64(i), Method: "GET", URL: "https://a.test/keep", Host: "a.test"})
	}
	app.Filter.Text = "keep"
	full := append([]int(nil), app.Visible()...)

	// One arrives that matches, then one that does not.
	s.Upsert(qqx.Session{ID: 6, Method: "GET", URL: "https://a.test/keep", Host: "a.test"})
	app.InvalidateAppend(6)
	s.Upsert(qqx.Session{ID: 7, Method: "GET", URL: "https://a.test/skip", Host: "a.test"})
	app.InvalidateAppend(7)
	fast := append([]int(nil), app.Visible()...)

	// What a full pass would give.
	app.Invalidate()
	want := app.Visible()

	if len(fast) != len(want) {
		t.Fatalf("fast = %v, want %v (had %v before)", fast, want, full)
	}
	for i := range fast {
		if fast[i] != want[i] {
			t.Errorf("row %d = %d, want %d (fast %v, want %v)", i, fast[i], want[i], fast, want)
			break
		}
	}
}

// TestSearchFiltersLive checks that typing in the filter box narrows the list:
// the text the user types has to reach the filter, which once it did not.
func TestSearchFiltersLive(t *testing.T) {
	s := qqx.NewStore(100)
	app := qqx.NewApp(s)
	for i, u := range []string{
		"https://api.test/v1/users",
		"https://api.test/v1/orders",
		"https://cdn.test/app.js",
	} {
		s.Upsert(qqx.Session{ID: uint64(i + 1), Method: "GET", URL: u, Host: hostOfTest(u)})
	}
	if got := len(app.Visible()); got != 3 {
		t.Fatalf("all rows = %d, want 3", got)
	}

	// What a keystroke does: the view copies the field into the filter.
	app.Search = "order"
	app.Filter.Text = app.Search
	app.Invalidate()
	rows := app.Visible()
	if len(rows) != 1 {
		t.Fatalf("filtered rows = %d, want 1", len(rows))
	}
	sess, ok := app.Store.At(rows[0])
	if !ok || sess.Path() != "/v1/orders" {
		t.Errorf("kept %+v", sess)
	}

	// Clearing the box brings them all back.
	app.Search = ""
	app.Filter.Text = ""
	app.Invalidate()
	if got := len(app.Visible()); got != 3 {
		t.Errorf("after clearing = %d, want 3", got)
	}
}

func hostOfTest(raw string) string {
	s := raw
	for i := 0; i+2 < len(s); i++ {
		if s[i] == ':' && s[i+1] == '/' && s[i+2] == '/' {
			s = s[i+3:]
			break
		}
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i]
		}
	}
	return s
}
