package session

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/events"
	"github.com/jlvihv/qingqiuxia/internal/msg"
)

func newRequest(t *testing.T, raw string) *msg.Request {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := &msg.Request{Method: "GET", URL: u}
	r.Header = http.Header{}
	return r
}

func TestStoreRing(t *testing.T) {
	s := NewStore(3)
	for i := 0; i < 5; i++ {
		sess := s.Begin(newRequest(t, "https://x.test/"), time.Now())
		sess.Complete(&msg.Response{Status: 200}, nil, time.Now())
	}
	if got := s.Len(); got != 3 {
		t.Errorf("len = %d, want the ring's 3", got)
	}
	if got := s.Dropped(); got != 2 {
		t.Errorf("dropped = %d, want 2", got)
	}
	list := s.List(0, -1)
	if len(list) != 3 {
		t.Fatalf("list = %d", len(list))
	}
	// The oldest kept session is the third one, whose ID is 3.
	if list[0].ID != 3 || list[2].ID != 5 {
		t.Errorf("ids = %d..%d, want 3..5", list[0].ID, list[2].ID)
	}
	// A dropped ID is gone.
	if _, ok := s.Get(1); ok {
		t.Error("a dropped session is still there")
	}
}

func TestStoreListPaging(t *testing.T) {
	s := NewStore(100)
	for i := 0; i < 10; i++ {
		s.Begin(newRequest(t, "https://x.test/"), time.Now())
	}
	page := s.List(3, 2)
	if len(page) != 2 || page[0].ID != 4 || page[1].ID != 5 {
		t.Errorf("page = %d..%d", page[0].ID, page[1].ID)
	}
	if got := s.List(20, 5); len(got) != 0 {
		t.Errorf("past the end returned %d", len(got))
	}
}

func TestSnapshotIsImmutable(t *testing.T) {
	s := NewStore(10)
	req := newRequest(t, "https://x.test/")
	req.Body = []byte("original")
	sess := s.Begin(req, time.Now())
	sess.Complete(&msg.Response{Status: 200, Body: []byte("resp")}, nil, time.Now())

	snap, ok := s.Get(sess.ID)
	if !ok {
		t.Fatal("no session")
	}
	// Editing the live request must not show up in the snapshot taken.
	req.Body = []byte("changed")
	if string(snap.Request.Body) != "original" {
		t.Errorf("snapshot body = %q", snap.Request.Body)
	}
	snap.Request.Header.Set("X-New", "1")
	if req.Header.Get("X-New") != "" {
		t.Error("editing a snapshot changed the live request")
	}
}

func TestClearKeepsIDSpace(t *testing.T) {
	s := NewStore(10)
	a := s.Begin(newRequest(t, "https://x.test/"), time.Now())
	s.Clear()
	if s.Len() != 0 {
		t.Errorf("len = %d after clear", s.Len())
	}
	b := s.Begin(newRequest(t, "https://x.test/"), time.Now())
	if b.ID <= a.ID {
		t.Errorf("ids reused: %d then %d", a.ID, b.ID)
	}
}

func TestHubPublishes(t *testing.T) {
	s := NewStore(10)
	sub := s.Hub().Subscribe()
	defer sub.Close()

	sess := s.Begin(newRequest(t, "https://x.test/"), time.Now())
	sess.Complete(&msg.Response{Status: 200}, nil, time.Now())
	s.Publish(sess)

	var kinds []EventKind
	for len(kinds) < 2 {
		select {
		case ev := <-sub.C:
			kinds = append(kinds, ev.Kind)
		case <-time.After(time.Second):
			t.Fatalf("only got %v", kinds)
		}
	}
	if kinds[0] != EventNew || kinds[1] != EventDone {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestHubSlowSubscriberDoesNotBlock(t *testing.T) {
	h := events.New[int](1)
	sub := h.Subscribe()
	defer sub.Close()
	// Publish more than the queue holds: the publisher must not block, and the
	// subscriber must end up with the newest value.
	for i := 0; i < 100; i++ {
		h.Publish(i)
	}
	if got := <-sub.C; got != 99 {
		t.Errorf("got %d, want the newest 99", got)
	}
}

// TestSnapshotDoesNotWriteThrough checks the other direction of a snapshot's
// isolation. TestSnapshotIsImmutable above checks that editing the live request
// does not change a snapshot; this checks that editing a snapshot does not change
// the store, which the earlier test could not see.
func TestSnapshotDoesNotWriteThrough(t *testing.T) {
	s := NewStore(10)
	req := newRequest(t, "https://x.test/")
	req.Body = []byte("original")
	sess := s.Begin(req, time.Now())
	sess.Complete(&msg.Response{Status: 200, Body: []byte("resp")}, nil, time.Now())

	snap, ok := s.Get(sess.ID)
	if !ok {
		t.Fatal("no session")
	}
	snap.Request.Header.Set("X-New", "1")
	snap.Response.Body[0] = 'X'

	again, ok := s.Get(sess.ID)
	if !ok {
		t.Fatal("no session")
	}
	if got := again.Request.Header.Get("X-New"); got != "" {
		t.Errorf("a snapshot wrote header %q into the store", got)
	}
	if got := string(again.Response.Body); got != "resp" {
		t.Errorf("a snapshot wrote body %q into the store", got)
	}
}
