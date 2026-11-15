// Package events is a fan-out bus: one publisher, many subscribers, each with
// its own buffered queue.
//
// The core publishes traffic and state changes here; the control API turns
// them into server-sent events for the GUI. A slow subscriber must never slow
// the proxy down, so a full queue drops its oldest event and counts the loss
// rather than blocking the publisher.
package events

import "sync"

// Hub broadcasts values of type T to the subscribers present when a value is
// published. The zero Hub is not usable; call New.
type Hub[T any] struct {
	mu     sync.Mutex
	subs   map[uint64]*Subscription[T]
	nextID uint64
	depth  int
}

// Subscription is one subscriber's view of the bus. Close it when done.
type Subscription[T any] struct {
	// C is the queue the subscriber reads. It is closed when the
	// subscription is.
	C  <-chan T
	ch chan T

	hub *Hub[T]
	id  uint64

	// closeMu guards the queue's close, so that Publish and Close cannot race
	// on a channel the other holds.
	closeMu sync.Mutex
	closed  bool
}

// New returns a hub whose subscriptions queue up to depth values each.
func New[T any](depth int) *Hub[T] {
	if depth <= 0 {
		depth = 1
	}
	return &Hub[T]{subs: make(map[uint64]*Subscription[T]), depth: depth}
}

// Subscribe adds a subscriber. The returned subscription's channel must be
// drained or the queue will keep dropping its oldest values.
func (h *Hub[T]) Subscribe() *Subscription[T] {
	ch := make(chan T, h.depth)
	h.mu.Lock()
	h.nextID++
	s := &Subscription[T]{C: ch, ch: ch, hub: h, id: h.nextID}
	h.subs[s.id] = s
	h.mu.Unlock()
	return s
}

// Publish delivers v to every current subscriber. It never blocks.
func (h *Hub[T]) Publish(v T) {
	h.mu.Lock()
	targets := make([]*Subscription[T], 0, len(h.subs))
	for _, s := range h.subs {
		targets = append(targets, s)
	}
	h.mu.Unlock()

	for _, s := range targets {
		s.send(v)
	}
}

// send enqueues v, dropping the oldest queued value when the queue is full.
func (s *Subscription[T]) send(v T) {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return
	}
	for {
		select {
		case s.ch <- v:
			return
		default:
		}
		// The queue is full: make room by discarding the oldest value. The
		// consumer sees a gap rather than the publisher stalling.
		select {
		case <-s.ch:
		default:
			// Another sender emptied it; try to publish again.
		}
	}
}

// Close removes the subscription and closes its channel.
func (s *Subscription[T]) Close() {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return
	}
	s.closed = true
	close(s.ch)
	s.closeMu.Unlock()

	h := s.hub
	h.mu.Lock()
	delete(h.subs, s.id)
	h.mu.Unlock()
}

// Len reports how many subscribers are attached.
func (h *Hub[T]) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
