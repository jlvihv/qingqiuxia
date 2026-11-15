// Package session records traffic: what the proxy saw, what was changed, how
// long it took, and how it ended.
//
// The store is the core's memory of every request. It is a bounded ring so
// that a long session cannot exhaust memory, it hands out immutable snapshots
// so the GUI and the API never see a message mutate under them, and it
// publishes each change on its hub for live views.
package session

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/events"
	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// Phase is where a session is in its life.
type Phase uint8

const (
	// Pending is a request the proxy has received and not yet completed: it is
	// waiting on a breakpoint, or in flight.
	Pending Phase = iota
	// Complete is a session whose response the client has.
	Complete
	// Failed is a session that ended without a response: a dial error, a
	// client that went away, an abort rule.
	Failed
)

// String names the phase for the API.
func (p Phase) String() string {
	switch p {
	case Pending:
		return "pending"
	case Complete:
		return "complete"
	case Failed:
		return "failed"
	}
	return "unknown"
}

// Source says how the proxy handled a session.
type Source uint8

const (
	// Proxied is a request the proxy forwarded to the origin.
	Proxied Source = iota
	// Mocked is a response a mock rule answered without touching the network.
	Mocked
	// Aborted is a request a rule refused to forward.
	Aborted
)

// String names the source for the API.
func (s Source) String() string {
	switch s {
	case Proxied:
		return "proxied"
	case Mocked:
		return "mocked"
	case Aborted:
		return "aborted"
	}
	return "unknown"
}

// Timings records where a request's time went, in the style a capture tool
// shows: what the client waited for, and how much of it the proxy added.
type Timings struct {
	// Start is when the proxy began handling the request.
	Start time.Time
	// End is when the response was complete, zero while pending.
	End time.Time
	// DNS, Connect and TLS are the origin connection's setup, zero when a
	// connection was reused.
	DNS, Connect, TLS time.Duration
	// TTFB is from Start to the first byte of the response body.
	TTFB time.Duration
}

// Duration is the whole exchange, or what has passed so far for a pending one.
func (t Timings) Duration() time.Duration {
	if t.End.IsZero() {
		if t.Start.IsZero() {
			return 0
		}
		return time.Since(t.Start)
	}
	return t.End.Sub(t.Start)
}

// Note is one thing the pipeline did to a session, kept so the GUI can explain
// why a request looks the way it does.
type Note struct {
	// At is when the note was added.
	At time.Time
	// Rule is the name of the rule that acted, empty for the proxy's own notes.
	Rule string
	// Kind is the rule kind that acted, or "proxy".
	Kind string
	// Text describes the effect, as "rewrote response body".
	Text string
}

// Snapshot is an immutable copy of a session, safe to hand to other
// goroutines and to keep.
type Snapshot struct {
	ID      uint64
	Phase   Phase
	Source  Source
	Request *msg.Request
	// Response is nil until the client has one.
	Response *msg.Response
	// Err is the failure, for a failed session.
	Err     string
	Timings Timings
	Notes   []Note
	// BytesIn and BytesOut are the body sizes as the proxy moved them.
	BytesIn, BytesOut int64
}

// Session is a traffic record under construction. The proxy owns it while a
// request is in flight; the store copies it into a Snapshot for readers.
type Session struct {
	ID uint64

	mu       sync.Mutex
	phase    Phase
	source   Source
	req      *msg.Request
	resp     *msg.Response
	err      string
	timings  Timings
	notes    []Note
	bytesIn  int64
	bytesOut int64
	// aborted asks the proxy to stop forwarding, set by a rule through the
	// pipeline's view of the session.
	aborted bool
}

// NewSession starts a session record for req.
func NewSession(id uint64, req *msg.Request, now time.Time) *Session {
	return &Session{ID: id, req: req, phase: Pending, timings: Timings{Start: now}}
}

// Snapshot copies the session. The request and response are deep copied, so
// later edits by the proxy do not show up in a snapshot already handed out.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		ID:       s.ID,
		Phase:    s.phase,
		Source:   s.source,
		Request:  s.req.Clone(),
		Response: s.resp.Clone(),
		Err:      s.err,
		Timings:  s.timings,
		BytesIn:  s.bytesIn,
		BytesOut: s.bytesOut,
	}
	if len(s.notes) > 0 {
		snap.Notes = append([]Note(nil), s.notes...)
	}
	return snap
}

// SetSource records how the session is being answered.
func (s *Session) SetSource(src Source) {
	s.mu.Lock()
	s.source = src
	s.mu.Unlock()
}

// SetDNS records the origin name resolution time.
func (s *Session) SetDNS(d time.Duration) {
	s.mu.Lock()
	s.timings.DNS = d
	s.mu.Unlock()
}

// SetConnect records the origin connection time.
func (s *Session) SetConnect(d time.Duration) {
	s.mu.Lock()
	s.timings.Connect = d
	s.mu.Unlock()
}

// SetTLS records the origin TLS handshake time.
func (s *Session) SetTLS(d time.Duration) {
	s.mu.Lock()
	s.timings.TLS = d
	s.mu.Unlock()
}

// SetTTFB records the time to the response's first body byte.
func (s *Session) SetTTFB(d time.Duration) {
	s.mu.Lock()
	s.timings.TTFB = d
	s.mu.Unlock()
}

// SetTimings records where the exchange's time went, in one lock rather than
// one per field.
func (s *Session) SetTimings(t Timings) {
	s.mu.Lock()
	s.timings.DNS = t.DNS
	s.timings.Connect = t.Connect
	s.timings.TLS = t.TLS
	s.timings.TTFB = t.TTFB
	s.mu.Unlock()
}

// SetResponse records the response the client will get.
//
// It is for the proxy while an exchange is in flight, not for changing a
// recorded one: a snapshot a reader already holds does not change, and a
// session that is done is history. A rule that changes future traffic does so
// on the way through, not by editing what was captured.
func (s *Session) SetResponse(resp *msg.Response) {
	s.mu.Lock()
	s.resp = resp
	s.mu.Unlock()
}

// SetRequest replaces the request, after a rule or a breakpoint edited it.
func (s *Session) SetRequest(req *msg.Request) {
	s.mu.Lock()
	s.req = req
	s.mu.Unlock()
}

// AddNote records what the pipeline did.
func (s *Session) AddNote(n Note) {
	s.mu.Lock()
	// Keep the list bounded: a pathological rule that fires per byte would
	// otherwise grow a note per byte.
	const maxNotes = 32
	if len(s.notes) < maxNotes {
		s.notes = append(s.notes, n)
	}
	s.mu.Unlock()
}

// Abort asks the proxy to stop forwarding this session.
func (s *Session) Abort() {
	s.mu.Lock()
	s.aborted = true
	s.mu.Unlock()
}

// Aborted reports whether a rule asked to abort.
func (s *Session) Aborted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.aborted
}

// Complete marks the session done, with resp or an error.
func (s *Session) Complete(resp *msg.Response, err error, now time.Time) {
	s.mu.Lock()
	s.timings.End = now
	if resp != nil {
		s.resp = resp
		s.bytesOut = int64(len(resp.Body))
		if s.req != nil {
			s.bytesIn = int64(len(s.req.Body))
		}
	}
	if err != nil {
		s.phase = Failed
		s.err = err.Error()
	} else {
		s.phase = Complete
	}
	// A session nothing answered is an abort, whatever the rules called it.
	// The check is on the outcome, not on the previous source: a mock that
	// produced no response is still an abort.
	if err == nil && resp == nil && s.source != Mocked {
		s.source = Aborted
	}
	s.mu.Unlock()
}

// Store keeps the recent sessions in a bounded ring and publishes changes.
type Store struct {
	mu      sync.RWMutex
	ring    []*Session
	start   int // index of the oldest session
	count   int
	byID    map[uint64]*Session
	limit   int
	hub     *events.Hub[Event]
	ids     events.Counter
	dropped atomic.Uint64
}

// EventKind says what happened to a session.
type EventKind uint8

const (
	// EventNew is a session the proxy has just started.
	EventNew EventKind = iota
	// EventUpdate is a session whose request, response or timing changed.
	EventUpdate
	// EventDone is a session that reached a final state.
	EventDone
	// EventDropped is a session that left the ring to make room.
	EventDropped
)

// Event is a change to one session.
type Event struct {
	Kind    EventKind
	Session Snapshot
}

// NewStore returns a store that keeps limit sessions; limit must be positive.
func NewStore(limit int) *Store {
	if limit <= 0 {
		limit = 1000
	}
	return &Store{
		ring:  make([]*Session, limit),
		byID:  make(map[uint64]*Session, limit),
		limit: limit,
		hub:   events.New[Event](512),
	}
}

// Hub exposes the store's change stream.
func (s *Store) Hub() *events.Hub[Event] { return s.hub }

// Begin starts a session for req and returns it with its new ID.
func (s *Store) Begin(req *msg.Request, now time.Time) *Session {
	id := s.ids.Next()
	sess := NewSession(id, req, now)

	s.mu.Lock()
	evicted := s.pushLocked(sess)
	s.mu.Unlock()

	// Snapshots are deep copies, and a core with no GUI attached has nobody to
	// publish to: skip the copying, which the profiler showed as most of the
	// garbage under load.
	if s.hub.Len() == 0 {
		return sess
	}
	if evicted != nil {
		s.hub.Publish(Event{Kind: EventDropped, Session: evicted.Snapshot()})
	}
	s.hub.Publish(Event{Kind: EventNew, Session: sess.Snapshot()})
	return sess
}

// pushLocked adds sess to the ring, returning the session it evicted.
func (s *Store) pushLocked(sess *Session) *Session {
	var evicted *Session
	if s.count == s.limit {
		evicted = s.ring[s.start]
		delete(s.byID, evicted.ID)
		s.ring[s.start] = sess
		s.start = (s.start + 1) % s.limit
		s.dropped.Add(1)
	} else {
		s.ring[(s.start+s.count)%s.limit] = sess
		s.count++
	}
	s.byID[sess.ID] = sess
	return evicted
}

// Publish republishes a session's current state after a change. It is a no-op
// when nothing is listening, since the snapshots it would take are the deep
// copies the profiler showed as the core's largest allocation.
func (s *Store) Publish(sess *Session) {
	if s.hub.Len() == 0 {
		return
	}
	// One snapshot, not two: the phase comes out of the same copy that is
	// published.
	snap := sess.Snapshot()
	kind := EventUpdate
	if snap.Phase != Pending {
		kind = EventDone
	}
	s.hub.Publish(Event{Kind: kind, Session: snap})
}

// Get returns a session's snapshot by ID.
func (s *Store) Get(id uint64) (Snapshot, bool) {
	s.mu.RLock()
	sess, ok := s.byID[id]
	s.mu.RUnlock()
	if !ok {
		return Snapshot{}, false
	}
	return sess.Snapshot(), true
}

// List returns up to limit snapshots, oldest first, skipping the first offset.
// A negative limit means all of them.
func (s *Store) List(offset, limit int) []Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if offset < 0 {
		offset = 0
	}
	if offset >= s.count {
		return nil
	}
	n := s.count - offset
	if limit >= 0 && limit < n {
		n = limit
	}
	out := make([]Snapshot, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, s.ring[(s.start+offset+i)%s.limit].Snapshot())
	}
	return out
}

// Len reports how many sessions are held.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.count
}

// Dropped reports how many sessions left the ring.
func (s *Store) Dropped() uint64 { return s.dropped.Load() }

// IDs reports the last session ID handed out.
func (s *Store) LastID() uint64 { return s.ids.Peek() }

// Clear empties the store. The ID sequence keeps counting, so a stale
// reference in a client never matches a new session.
func (s *Store) Clear() int {
	s.mu.Lock()
	n := s.count
	s.ring = make([]*Session, s.limit)
	s.byID = make(map[uint64]*Session, s.limit)
	s.start, s.count = 0, 0
	s.mu.Unlock()
	return n
}
