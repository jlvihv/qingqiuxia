package ui

import (
	"context"
	"errors"
	"testing"
)

// The window's store is a ring, and these are the invariants that broke: the two
// operations that are not an append, and the two pieces of state a frame
// maintains from row numbers.

func rowIDs(s *Store) []uint64 {
	var out []uint64
	for row := 0; row < s.Len(); row++ {
		if sess, ok := s.At(row); ok {
			out = append(out, sess.ID)
		}
	}
	return out
}

func equalIDs(got, want []uint64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestRemoveKeepsTheRingOrder checks dropping a row out of a full ring. Splicing
// the slice left window pointing where it was, so the rows came back out of
// order.
func TestRemoveKeepsTheRingOrder(t *testing.T) {
	s := NewStore(3)
	for i := 1; i <= 5; i++ {
		s.Upsert(Session{ID: uint64(i)})
	}
	if got, want := rowIDs(s), []uint64{5, 4, 3}; !equalIDs(got, want) {
		t.Fatalf("before remove = %v, want %v", got, want)
	}
	s.Remove(5)
	if got, want := rowIDs(s), []uint64{4, 3}; !equalIDs(got, want) {
		t.Errorf("after removing the newest = %v, want %v", got, want)
	}
	s.Remove(3)
	if got, want := rowIDs(s), []uint64{4}; !equalIDs(got, want) {
		t.Errorf("after removing the oldest = %v, want %v", got, want)
	}
}

// TestClearResetsTheRing checks emptying the store and filling it again. window
// was left pointing past the end, and the row that landed there never showed.
func TestClearResetsTheRing(t *testing.T) {
	s := NewStore(3)
	for i := 1; i <= 5; i++ {
		s.Upsert(Session{ID: uint64(i)})
	}
	s.Clear()
	for i := 100; i < 104; i++ {
		s.Upsert(Session{ID: uint64(i)})
	}
	if got, want := rowIDs(s), []uint64{103, 102, 101}; !equalIDs(got, want) {
		t.Errorf("after clear and refill = %v, want %v", got, want)
	}
}

// TestAppendAtCapacityDoesNotInventARow checks the incremental refresh when the
// store is full. An arrival overwrites the oldest row in place, so the row count
// does not change; shifting the visible indices anyway put one past the end.
func TestAppendAtCapacityDoesNotInventARow(t *testing.T) {
	s := NewStore(3)
	app := NewApp(s)
	for i := 1; i <= 2; i++ {
		s.Upsert(Session{ID: uint64(i)})
	}
	app.Invalidate() // the rows are fresh, so InvalidateAppend can extend them
	for i := 3; i <= 5; i++ {
		s.Upsert(Session{ID: uint64(i)})
		app.InvalidateAppend(uint64(i))
	}
	for _, row := range app.Visible() {
		if row >= s.Len() {
			t.Fatalf("visible index %d is past the store's %d rows", row, s.Len())
		}
	}
}

// TestSelectionFollowsTheSession checks the chosen row after new traffic arrives.
// Selected is a row number and traffic arrives at the front, so the choice used
// to drift to whatever session the new counting left at that number.
func TestSelectionFollowsTheSession(t *testing.T) {
	st := NewStore(50)
	app := NewApp(st)
	st.Upsert(Session{ID: 1})
	st.Upsert(Session{ID: 2})
	app.Invalidate()
	app.Selected = 0
	app.syncSelectionID(app.Visible())
	chosen, ok := app.SelectedSession()
	if !ok || chosen.ID != 2 {
		t.Fatalf("chose %d, want 2", chosen.ID)
	}

	st.Upsert(Session{ID: 3})
	app.InvalidateAppend(3)
	app.syncSelectionID(app.Visible())
	after, ok := app.SelectedSession()
	if !ok || after.ID != 2 {
		t.Errorf("the choice moved to %d, want 2", after.ID)
	}
}

// TestSelectionClearsWhenTheSessionLeaves checks a chosen row that is no longer
// in the list. Keeping the row number would choose whatever took its place.
func TestSelectionClearsWhenTheSessionLeaves(t *testing.T) {
	st := NewStore(10)
	app := NewApp(st)
	st.Upsert(Session{ID: 1})
	app.Invalidate()
	app.Selected = 0
	app.syncSelectionID(app.Visible())

	st.Remove(1)
	app.Invalidate()
	app.syncSelectionID(app.Visible())
	if _, ok := app.SelectedSession(); ok {
		t.Error("a row that no longer exists is still chosen")
	}
}

// failingBackend answers every Detail with an error.
type failingBackend struct{ calls int }

func (f *failingBackend) Status(context.Context) (Status, error) { return Status{}, nil }
func (f *failingBackend) Sessions(context.Context, int) ([]Session, int, uint64, error) {
	return nil, 0, 0, nil
}
func (f *failingBackend) Detail(context.Context, uint64) (Detail, error) {
	f.calls++
	return Detail{}, errors.New("the core is gone")
}
func (f *failingBackend) SetCapture(context.Context, bool) error     { return nil }
func (f *failingBackend) Pauses(context.Context) ([]Pause, error)    { return nil, nil }
func (f *failingBackend) Resume(context.Context, uint64, bool) error { return nil }
func (f *failingBackend) Watch(context.Context, func(Change)) error  { return nil }

// TestAFailedDetailFetchStaysFailed checks remembering a failure. It was
// remembered, but as if it had succeeded: the second frame got no error and no
// content, and the pane said the session had none.
func TestAFailedDetailFetchStaysFailed(t *testing.T) {
	app := NewApp(NewStore(10))
	backend := &failingBackend{}
	app.SetBackend(backend, nil)

	first, err := app.bodiesFor(7)
	if err == nil {
		t.Fatal("the first frame should report the failure")
	}
	_, err = app.bodiesFor(7)
	if err == nil {
		t.Error("the second frame reports success for a body it never read")
	}
	_ = first
	if backend.calls != 1 {
		t.Errorf("the backend was asked %d times; a remembered failure must not retry", backend.calls)
	}
}

// TestInspectorAppearsOnlyWithASelection checks that the detail panel is not
// built while nothing is chosen. It used to stand there empty, taking a third of
// the window's width to say "选一条流量看详情", which the list already implies.
func TestInspectorAppearsOnlyWithASelection(t *testing.T) {
	s := NewStore(10)
	app := NewApp(s)
	s.Upsert(Session{ID: 1, Method: "GET", URL: "https://x.test/a"})
	app.Invalidate()

	if app.hasSelection() {
		t.Error("a fresh window reports a selection")
	}

	app.Selected = 0
	app.syncSelectionID(app.Visible())
	if !app.hasSelection() {
		t.Error("a chosen row should give the inspector something to show")
	}

	// Choosing a row and then losing it must not leave the inspector showing a
	// session that is gone.
	s.Remove(1)
	app.Invalidate()
	app.syncSelectionID(app.Visible())
	if app.hasSelection() {
		t.Error("the inspector would show a session the list no longer holds")
	}
}
