package events

import "testing"

func TestHubFanOut(t *testing.T) {
	h := New[int](4)
	a := h.Subscribe()
	b := h.Subscribe()
	defer a.Close()
	defer b.Close()
	if h.Len() != 2 {
		t.Fatalf("len = %d", h.Len())
	}
	h.Publish(7)
	if got := <-a.C; got != 7 {
		t.Errorf("a got %d", got)
	}
	if got := <-b.C; got != 7 {
		t.Errorf("b got %d", got)
	}
}

func TestSubscriptionClose(t *testing.T) {
	h := New[int](1)
	s := h.Subscribe()
	s.Close()
	s.Close() // closing twice is harmless
	if h.Len() != 0 {
		t.Errorf("len = %d after close", h.Len())
	}
	if _, ok := <-s.C; ok {
		t.Error("the channel is still open")
	}
	// Publishing to no subscribers must not panic.
	h.Publish(1)
}

func TestDropOldest(t *testing.T) {
	h := New[int](2)
	s := h.Subscribe()
	defer s.Close()
	for i := 0; i < 10; i++ {
		h.Publish(i)
	}
	// The queue keeps the last two values published.
	if got := <-s.C; got != 8 {
		t.Errorf("first = %d, want 8", got)
	}
	if got := <-s.C; got != 9 {
		t.Errorf("second = %d, want 9", got)
	}
}

func TestCounter(t *testing.T) {
	var c Counter
	if c.Peek() != 0 {
		t.Errorf("peek = %d", c.Peek())
	}
	if c.Next() != 1 || c.Next() != 2 {
		t.Error("ids do not start at 1")
	}
	if c.Peek() != 2 {
		t.Errorf("peek = %d", c.Peek())
	}
}
