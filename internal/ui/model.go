package ui

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Message is one side of an exchange as the detail panel shows it: the headers
// as they went out, and the body as text where it can be read as text.
//
// The list never carries these: a thousand sessions with their bodies in memory
// is most of a gigabyte, for detail that one click asks for. They are fetched
// for the chosen session, once, and kept beside it.
type Message struct {
	// Headers are the message's, in the order the carrier kept them.
	Headers []Header
	// ContentType is the media type the panel names the body with.
	ContentType string
	// Body is the body as text, empty when there is none or when it is binary.
	Body string
	// Binary reports a body the panel cannot show as text; Size is what it
	// weighs, so the panel can say so rather than draw nothing.
	Binary bool
	// Base64 is the body when Binary, for a viewer that wants to show the
	// bytes. It is empty for a text body.
	Base64 string
	// Truncated reports that the core kept only the start of the body, because
	// it was longer than the capture limit.
	Truncated bool
	// Streaming reports a body the core passed through without buffering, as
	// server-sent events.
	Streaming bool
	// Size is the body's length as the core measured it on the wire.
	Size int64
}

// Header is one header line, kept as a pair so the panel can show a name twice
// when a message had it twice.
type Header struct {
	Name, Value string
}

// Session is one row of the traffic list, as the API describes it.
type Session struct {
	ID     uint64
	Phase  string // pending, complete, failed
	Source string // proxied, mocked, aborted
	Method string
	URL    string
	Host   string
	Proto  string
	Status int
	Error  string

	DurationMS float64
	TTFBMS     float64
	DNSMS      float64
	ConnectMS  float64
	TLSMS      float64

	BytesIn  int64
	BytesOut int64
	Started  time.Time

	// Notes explain what the pipeline did to this session, which the detail
	// panel shows: a request that looks odd should say why.
	Notes []Note
}

// Note is one thing the pipeline did to a session.
type Note struct {
	At   time.Time
	Rule string
	Kind string
	Text string
}

// Path is the URL's path with its query, which the list shows on its own so
// that a long host does not push the interesting part out of view.
func (s *Session) Path() string {
	if s.URL == "" {
		return ""
	}
	rest := s.URL
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[i:]
	}
	return "/"
}

// Done reports whether the session has an outcome.
func (s *Session) Done() bool { return s.Phase != "pending" }

// Size renders the response size the way the list column does.
func (s *Session) Size() string { return formatBytes(s.BytesOut) }

// Duration renders the whole exchange, or a dash while it is in flight.
func (s *Session) Duration() string {
	if !s.Done() {
		return "—"
	}
	return formatMillis(s.DurationMS)
}

// formatBytes shortens a size to at most four characters and a unit.
func formatBytes(n int64) string {
	switch {
	case n <= 0:
		return "0"
	case n < 1024:
		return fmt.Sprint(n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fk", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1fG", float64(n)/(1024*1024*1024))
}

// formatMillis renders a duration in the list's units: microseconds below a
// millisecond, then milliseconds, then seconds.
func formatMillis(ms float64) string {
	switch {
	case ms < 1:
		return fmt.Sprintf("%.0fµs", ms*1000)
	case ms < 1000:
		return fmt.Sprintf("%.0fms", ms)
	}
	return fmt.Sprintf("%.2fs", ms/1000)
}

// Store holds the sessions the window shows, newest first, and the selection.
//
// The core publishes changes on a stream; this is the window's own copy, so a
// frame never waits on the network.
type Store struct {
	mu       sync.Mutex
	sessions []Session
	byID     map[uint64]int
	limit    int
	// window is the index of the oldest session once the store is full; new
	// sessions overwrite rows from there, so nothing is ever shifted.
	window int
	// total counts what the core holds, which is more than the window keeps.
	total   int
	dropped uint64
}

// NewStore returns a store that keeps at most limit sessions.
func NewStore(limit int) *Store {
	if limit <= 0 {
		limit = 5000
	}
	return &Store{byID: make(map[uint64]int, limit), limit: limit}
}

// Upsert records a session, replacing the row with its ID if it is there.
// A session arrives twice: once when it starts and once when it ends.
//
// New sessions are appended and the window reads the list backwards, so the
// hot path — a request arriving — is an append. Inserting at the front instead
// moved the whole slice on every request, which at a few thousand rows is the
// difference between a list that keeps up and one that stutters.
func (s *Store) Upsert(sess Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.byID[sess.ID]; ok {
		s.sessions[i] = sess
		return
	}
	if len(s.sessions) < s.limit {
		s.byID[sess.ID] = len(s.sessions)
		s.sessions = append(s.sessions, sess)
		return
	}
	// Full: overwrite the oldest row in place and move the window's start.
	// Replacing and advancing is O(1); shifting the slice and rebuilding the
	// index was O(n) on every arriving request once the list was full, which
	// at five thousand rows cost a fifth of a millisecond each.
	delete(s.byID, s.sessions[s.window].ID)
	s.sessions[s.window] = sess
	s.byID[sess.ID] = s.window
	s.window = (s.window + 1) % len(s.sessions)
}

// latestLocked is the index of the newest session.
func (s *Store) latestLocked() int {
	last := len(s.sessions) - 1
	return (s.window + last) % len(s.sessions)
}

// rowLocked is the index of the session at a row of the window's order, newest
// first.
func (s *Store) rowLocked(row int) int {
	// Row 0 is the newest, which sits just before the oldest.
	return (s.window + len(s.sessions) - 1 - row) % len(s.sessions)
}

// At returns the session at a row of the window's order, newest first.
func (s *Store) At(row int) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row < 0 || row >= len(s.sessions) {
		return Session{}, false
	}
	return s.sessions[s.rowLocked(row)], true
}

// reindexLocked rebuilds the ID index after a trim.
func (s *Store) reindexLocked() {
	clear(s.byID)
	for i := range s.sessions {
		s.byID[s.sessions[i].ID] = i
	}
}

// Has reports whether the store holds a session.
func (s *Store) Has(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byID[id]
	return ok
}

// Remove forgets a session, which the core dropped from its ring.
//
// It rebuilds the slice instead of splicing it. The store is a ring: once it is
// full, new sessions overwrite the oldest in place and window says where the
// oldest one now is. Splicing shifts every index by one but leaves window
// pointing at the same slot, so the rows after the removed one came out in the
// wrong order — the newest row was deleted and the two after it swapped.
func (s *Store) Remove(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return
	}
	// Rewrite in window order (oldest first), keeping every row but this one,
	// then start the ring over at zero. That is O(n) for a rare event, and it
	// makes the invariant plain: sessions are in ring order, window is zero.
	kept := make([]Session, 0, len(s.sessions))
	for i := 0; i < len(s.sessions); i++ {
		sess := s.sessions[(s.window+i)%len(s.sessions)]
		if sess.ID != id {
			kept = append(kept, sess)
		}
	}
	s.sessions = kept
	s.window = 0
	s.reindexLocked()
}

// SetCounts records how many the core holds and how many it dropped.
func (s *Store) SetCounts(total int, dropped uint64) {
	s.mu.Lock()
	s.total, s.dropped = total, dropped
	s.mu.Unlock()
}

// Counts reports the core's total and how many sessions it dropped.
func (s *Store) Counts() (total int, dropped uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total, s.dropped
}

// Len reports how many sessions the window holds.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// Limit reports how many sessions the window holds at most.
func (s *Store) Limit() int { return s.limit }

// Clear empties the window's list.
//
// window is reset with it: leaving it pointing past the end of an empty slice
// meant the next fill started at that offset, and the row that landed there was
// never shown.
func (s *Store) Clear() {
	s.mu.Lock()
	s.sessions = nil
	s.window = 0
	clear(s.byID)
	s.mu.Unlock()
}

// Filter narrows the list. The empty filter matches everything.
type Filter struct {
	// Text is matched against the whole URL, without regard to case.
	Text string
	// Hosts, when not empty, restricts the list to those hosts.
	Hosts []string
}

// Match reports whether a session passes the filter.
func (f Filter) Match(s *Session) bool {
	if f.Text != "" {
		needle := strings.ToLower(f.Text)
		if !strings.Contains(strings.ToLower(s.URL), needle) &&
			!strings.Contains(strings.ToLower(s.Method), needle) &&
			!strings.Contains(fmt.Sprint(s.Status), needle) {
			return false
		}
	}
	if len(f.Hosts) > 0 {
		for _, h := range f.Hosts {
			if strings.EqualFold(h, s.Host) {
				return true
			}
		}
		return false
	}
	return true
}

// HostCount is one entry of the sidebar's host list.
type HostCount struct {
	Host  string
	Count int
}

// Hosts returns the hosts the window has seen, most used first, which is what
// the sidebar lists to narrow the traffic by.
func (s *Store) Hosts() []HostCount {
	s.mu.Lock()
	counts := make(map[string]int, 16)
	for i := range s.sessions {
		if h := s.sessions[i].Host; h != "" {
			counts[h]++
		}
	}
	s.mu.Unlock()

	out := make([]HostCount, 0, len(counts))
	for h, n := range counts {
		out = append(out, HostCount{Host: h, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Host < out[j].Host
	})
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

// Status is what the core says about itself.
type Status struct {
	Version   string
	ProxyAddr string
	Capturing bool
	Sessions  int
	Rules     int
	Paused    int
	MemoryMB  uint64
	// Goroutines is the core's goroutine count, which the status bar shows
	// where an earlier version showed threads.
	Goroutines int
}

// Pause is a message held at a breakpoint.
type Pause struct {
	ID        uint64
	SessionID uint64
	Phase     string
	RuleName  string
	Method    string
	URL       string
}
