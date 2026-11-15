package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/store"
)

func open(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestRulesRoundTrip checks that a rule written comes back as it was: the point
// of saving them is not having to write them again.
func TestRulesRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := open(t)

	off := false
	specs := []rule.Spec{
		{
			ID: 1, Name: "打码", Kind: "rewrite", Phase: "response",
			Pattern: "*/login", Enabled: &off,
			Actions: []rule.ActionSpec{{Type: "replace-body", Find: "real", Replace: "***"}},
		},
		{
			ID: 2, Name: "假登录", Kind: "mock", Phase: "request",
			Pattern: "*/auth",
			Mock:    &rule.MockSpec{Status: 200, Body: `{"ok":1}`, ContentType: "application/json"},
		},
	}
	if err := db.SaveRules(ctx, specs); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := db.LoadRules(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d rules, want 2", len(got))
	}
	if got[0].Name != "打码" || got[0].Pattern != "*/login" {
		t.Errorf("first = %+v", got[0])
	}
	if got[0].Enabled == nil || *got[0].Enabled {
		t.Error("the disabled rule came back enabled")
	}
	if len(got[0].Actions) != 1 || got[0].Actions[0].Replace != "***" {
		t.Errorf("actions = %+v", got[0].Actions)
	}
	if got[1].Mock == nil || got[1].Mock.Status != 200 {
		t.Errorf("the mock did not survive: %+v", got[1].Mock)
	}

	// Saving again replaces, rather than appending.
	if err := db.SaveRules(ctx, specs[:1]); err != nil {
		t.Fatal(err)
	}
	got, _ = db.LoadRules(ctx)
	if len(got) != 1 {
		t.Errorf("after replacing there are %d rules", len(got))
	}
}

// TestWindowRoundTrip checks the window's shape: a habit that should not be
// retyped every launch.
func TestWindowRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := open(t)

	if _, ok, _ := db.LoadWindow(ctx); ok {
		t.Error("a fresh store reports a saved window")
	}
	want := store.Window{
		Width: 1440, Height: 900, SidebarWidth: 200, MainWidth: 700,
		Detail: 2, Search: "login",
	}
	if err := db.SaveWindow(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.LoadWindow(ctx)
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// Saving twice updates rather than adding a second row.
	want.Width = 1200
	if err := db.SaveWindow(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, _, _ = db.LoadWindow(ctx)
	if got.Width != 1200 {
		t.Errorf("width = %v after the second save", got.Width)
	}
}

// TestSessionsRoundTripAndUpdate checks that sessions come back newest first and
// that a session published twice is stored once.
func TestSessionsRoundTripAndUpdate(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	now := time.Now()

	recs := []store.Record{
		{ID: 1, Started: now.Add(-2 * time.Second), Method: "GET", URL: "https://a.test/1", Host: "a.test", Status: 200, Phase: "complete"},
		{ID: 2, Started: now.Add(-1 * time.Second), Method: "POST", URL: "https://a.test/2", Host: "a.test", Status: 201, Phase: "complete"},
	}
	if err := db.SaveSessions(ctx, recs); err != nil {
		t.Fatal(err)
	}

	got, err := db.LoadSessions(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d", len(got))
	}
	if got[0].ID != 2 || got[1].ID != 1 {
		t.Errorf("order = %d, %d; want newest first", got[0].ID, got[1].ID)
	}

	// The same session, now complete: one row, updated.
	if err := db.SaveSessions(ctx, []store.Record{{
		ID: 2, Started: now.Add(-1 * time.Second), Method: "POST",
		URL: "https://a.test/2", Host: "a.test", Status: 500, Phase: "failed",
	}}); err != nil {
		t.Fatal(err)
	}
	got, _ = db.LoadSessions(ctx, 10)
	if len(got) != 2 {
		t.Fatalf("after the update there are %d rows, want 2", len(got))
	}
	for _, r := range got {
		if r.ID == 2 && (r.Status != 500 || r.Phase != "failed") {
			t.Errorf("the update did not take: %+v", r)
		}
	}
}

// TestPrune checks that the database does not grow without end, by count and by
// age.
func TestPrune(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	now := time.Now()

	var recs []store.Record
	for i := 1; i <= 50; i++ {
		recs = append(recs, store.Record{
			ID: uint64(i), Started: now.Add(-time.Duration(50-i) * time.Hour),
			Method: "GET", URL: "https://a.test/x", Host: "a.test", Status: 200, Phase: "complete",
		})
	}
	if err := db.SaveSessions(ctx, recs); err != nil {
		t.Fatal(err)
	}

	// Keep ten: the newest ten stay.
	if err := db.Prune(ctx, 10, 0); err != nil {
		t.Fatal(err)
	}
	n, _ := db.CountSessions(ctx)
	if n != 10 {
		t.Errorf("after keeping ten there are %d", n)
	}
	kept, _ := db.LoadSessions(ctx, 100)
	if kept[0].ID != 50 {
		t.Errorf("the newest is %d, want 50", kept[0].ID)
	}

	// Keep two hours: what is inside stays, and the newest is 0 hours old, so
	// the rows at 0, 1 and 2 hours are in and 3 onwards are out.
	if err := db.Prune(ctx, 0, 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	n, _ = db.CountSessions(ctx)
	if n != 3 {
		t.Errorf("after keeping two hours there are %d, want 3", n)
	}
}

// TestReopenKeepsWhatWasSaved checks the point of all of this: the data is still
// there after the program ends.
func TestReopenKeepsWhatWasSaved(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.db")

	db1, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db1.SaveRules(ctx, []rule.Spec{{
		ID: 1, Name: "留下", Kind: "abort", Phase: "request", Pattern: "*",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db1.SaveWindow(ctx, store.Window{Width: 1000, Height: 700}); err != nil {
		t.Fatal(err)
	}
	db1.Close()

	db2, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	rules, _ := db2.LoadRules(ctx)
	if len(rules) != 1 || rules[0].Name != "留下" {
		t.Errorf("rules after reopen = %+v", rules)
	}
	w, ok, _ := db2.LoadWindow(ctx)
	if !ok || w.Width != 1000 {
		t.Errorf("window after reopen = %+v", w)
	}
}
