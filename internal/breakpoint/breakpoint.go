// Package breakpoint pauses traffic so a person can look at it, change it, and
// let it go.
//
// A breakpoint rule names the traffic to stop. When one matches, the proxy
// hands the message to the Manager and waits; a client of the control API sees
// the paused message, edits it, and releases it. The waiting side is a plain
// Go channel, so a proxy goroutine blocked on a breakpoint costs a channel
// receive, not a poll.
package breakpoint

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/events"
	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/rule"
)

// Phase says which side of an exchange a breakpoint stops.
type Phase uint8

const (
	// OnRequest stops the request before it goes to the origin.
	OnRequest Phase = iota
	// OnResponse stops the response on its way back to the client.
	OnResponse
)

// String names the phase for the API.
func (p Phase) String() string {
	switch p {
	case OnRequest:
		return "request"
	case OnResponse:
		return "response"
	}
	return "unknown"
}

// parsed phases, for the specs.
func ParsePhase(s string) (Phase, error) {
	p, err := rule.ParsePhase(s)
	if err != nil {
		return 0, err
	}
	if p == rule.OnResponse {
		return OnResponse, nil
	}
	return OnRequest, nil
}

// Decision is what to do with a paused message.
type Decision uint8

const (
	// Release lets the message continue as it is, with any edits applied.
	Release Decision = iota
	// Abort refuses the message: the client gets Status instead.
	Abort
)

// ErrTimeout reports that nobody released a paused message in time.
var ErrTimeout = errors.New("breakpoint: timed out waiting for a decision")

// Paused is one message waiting for a decision.
//
// Its request and response are the live ones the proxy will continue with, which
// a client of the control API edits. Everything that reads them from outside —
// the API's list, the event stream, the window — goes through Snapshot, so that
// a reader never holds the object a person is editing.
type Paused struct {
	// ID identifies the pause in the API.
	ID uint64
	// SessionID is the traffic record the pause belongs to.
	SessionID uint64
	// Phase is the side that stopped.
	Phase Phase
	// RuleName is the breakpoint rule that matched.
	RuleName string
	// At is when it stopped.
	At time.Time

	// mu guards the message and its edits. The proxy reads the message once,
	// after the decision, so the lock is only ever held for an edit or a copy.
	mu sync.Mutex
	// Request is the request as it stands; for a response breakpoint it is the
	// request the response belongs to.
	Request *msg.Request
	// Response is set for a response breakpoint.
	Response *msg.Response

	// decided wakes the waiting proxy goroutine exactly once.
	decideOnce sync.Once
	decision   chan Decision
}

// Snapshot copies the paused message, so a caller can read it while the proxy
// carries on and a person edits it.
func (p *Paused) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Snapshot{
		ID: p.ID, SessionID: p.SessionID, Phase: p.Phase, RuleName: p.RuleName, At: p.At,
		Request: p.Request.Clone(), Response: p.Response.Clone(),
	}
}

// Edit applies an edit to the paused message under the pause's lock, so an edit
// and a snapshot cannot interleave. It reports whether there was anything to
// edit.
func (p *Paused) Edit(fn func(req *msg.Request, resp *msg.Response) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Request == nil && p.Response == nil {
		return fmt.Errorf("breakpoint: the pause holds no message")
	}
	return fn(p.Request, p.Response)
}

// Send decides what happens to the paused message. It returns false when the
// pause was already decided, by a timeout or by another caller.
func (p *Paused) Send(d Decision) bool {
	sent := false
	p.decideOnce.Do(func() {
		p.decision <- d
		sent = true
	})
	return sent
}

// Snapshot is a paused message as a reader sees it, copied out of the live one.
type Snapshot struct {
	ID        uint64
	SessionID uint64
	Phase     Phase
	RuleName  string
	Request   *msg.Request
	Response  *msg.Response
	At        time.Time
}

// Manager holds the breakpoint rules and the paused messages.
type Manager struct {
	rules atomic.Pointer[[]*breakpointRule]
	mu    sync.Mutex // guards the maps below
	// byID indexes pauses by their own ID, which is what the API names them by.
	byID map[uint64]*Paused
	next uint64
	hub  *events.Hub[Event]
	// timeout bounds how long a message may wait, so a forgotten breakpoint
	// cannot hang a client for ever.
	timeout time.Duration
}

// Event is a pause or resume, published for the API's live view.
//
// Paused carries the live pause, whose message must be read through Snapshot: a
// subscriber that marshalled it directly would be reading the object a person
// edits.
type Event struct {
	Kind   EventKind
	Paused *Paused
}

// EventKind says what happened.
type EventKind uint8

const (
	// EventPaused is a message that just stopped.
	EventPaused EventKind = iota
	// EventResumed is a message that was released or aborted.
	EventResumed
)

// NewManager returns a manager whose pauses time out after timeout; zero means
// DefaultTimeout.
func NewManager(timeout time.Duration) *Manager {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	m := &Manager{
		byID:    make(map[uint64]*Paused, 8),
		hub:     events.New[Event](64),
		timeout: timeout,
	}
	empty := []*breakpointRule(nil)
	m.rules.Store(&empty)
	return m
}

// DefaultTimeout is how long a paused message waits for a decision.
const DefaultTimeout = 5 * time.Minute

// StatusForAbort is the status a client gets when a paused message is refused.
// It is the policy's decision to make, not the proxy's: an abort is what a
// person chose for traffic they were inspecting.
const StatusForAbort = 403

// Hub exposes the manager's pause and resume stream.
func (m *Manager) Hub() *events.Hub[Event] { return m.hub }

// breakpointRule is one compiled breakpoint rule.
type breakpointRule struct {
	ID      uint64
	Name    string
	Enabled bool
	Phase   Phase
	Matcher *rule.Matcher
	// Update lets the user edit the message before it continues. With it off,
	// the breakpoint only observes.
	Update bool
}

// Spec is a breakpoint rule as the API describes it.
type Spec struct {
	ID      uint64 `json:"id,omitempty"`
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled,omitempty"`
	Phase   string `json:"phase"`
	Pattern string `json:"pattern"`
	// ReadOnly makes the breakpoint observe only: the message is reported and
	// released at once, never waiting.
	ReadOnly bool `json:"readOnly,omitempty"`
}

// Compile validates a spec.
func compile(spec Spec, id uint64) (*breakpointRule, error) {
	if spec.Name == "" {
		return nil, fmt.Errorf("breakpoint: name is required")
	}
	phase, err := ParsePhase(spec.Phase)
	if err != nil {
		return nil, err
	}
	matcher, err := rule.ParseMatcher(spec.Pattern)
	if err != nil {
		return nil, err
	}
	return &breakpointRule{
		ID:      id,
		Name:    spec.Name,
		Enabled: spec.Enabled == nil || *spec.Enabled,
		Phase:   phase,
		Matcher: matcher,
		Update:  !spec.ReadOnly,
	}, nil
}

// Replace swaps the whole breakpoint rule set.
func (m *Manager) Replace(specs []Spec) error {
	// The IDs are handed out and the counter advanced under one hold of the
	// lock: reading the counter, releasing it to compile, and writing it back
	// was a lost update whenever a pause started meanwhile, and a pause that
	// reused a live ID made Resume decide the wrong message.
	m.mu.Lock()
	next := m.next
	rules := make([]*breakpointRule, 0, len(specs))
	seen := make(map[uint64]bool, len(specs))
	for _, spec := range specs {
		id := spec.ID
		if id == 0 {
			next++
			id = next
		} else if id > next {
			next = id
		}
		if seen[id] {
			m.mu.Unlock()
			return fmt.Errorf("breakpoint: duplicate id %d", id)
		}
		seen[id] = true
		r, err := compile(spec, id)
		if err != nil {
			m.mu.Unlock()
			return err
		}
		rules = append(rules, r)
	}
	m.next = next
	m.mu.Unlock()

	// Compiling under the lock is fine: a spec list is small, and the lock is
	// not on the hot path — Intercept reads the atomic pointer.
	m.rules.Store(&rules)
	return nil
}

// Rules returns the current rules.
func (m *Manager) Rules() []Spec {
	rs := *m.rules.Load()
	out := make([]Spec, 0, len(rs))
	for _, r := range rs {
		enabled := r.Enabled
		out = append(out, Spec{
			ID: r.ID, Name: r.Name, Enabled: &enabled,
			Phase: r.Phase.String(), Pattern: r.Matcher.String(),
			ReadOnly: !r.Update,
		})
	}
	return out
}

// Pending returns the messages waiting for a decision, oldest first.
//
// The pauses are copied out: a list is read while a person edits the message a
// pause holds, and handing out the live one is a race with that edit.
func (m *Manager) Pending() []Snapshot {
	m.mu.Lock()
	out := make([]*Paused, 0, len(m.byID))
	for _, p := range m.byID {
		out = append(out, p)
	}
	m.mu.Unlock()
	// The map has no order, so sort by pause ID, which is the order they stopped
	// in. The lock is released first: a copy takes the pause's own lock, and
	// holding the manager's while doing it would put an edit behind the list.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	snaps := make([]Snapshot, 0, len(out))
	for _, p := range out {
		snaps = append(snaps, p.Snapshot())
	}
	return snaps
}

// PendingByID returns a copy of one pause, for a client that has its ID.
func (m *Manager) PendingByID(id uint64) (Snapshot, bool) {
	m.mu.Lock()
	p, ok := m.byID[id]
	m.mu.Unlock()
	if !ok {
		return Snapshot{}, false
	}
	return p.Snapshot(), true
}

// Live returns the pause itself, for a caller that has to edit the message the
// proxy will continue with. Everything that only reads must use Snapshot or
// Pending, which copy.
func (m *Manager) Live(id uint64) (*Paused, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	return p, ok
}

// match returns the first breakpoint rule that applies, or nil.
func (m *Manager) match(phase Phase, req *msg.Request) *breakpointRule {
	for _, r := range *m.rules.Load() {
		if r.Enabled && r.Phase == phase &&
			r.Matcher.Matches(req.URL.Scheme, req.URL.Host, req.URL.Path, req.Method) {
			return r
		}
	}
	return nil
}

// Intercept stops the message if a breakpoint rule applies, and returns once a
// decision is made: the message to continue with (edited by a client), or an
// abort with its status. ok is false when no breakpoint applied.
//
// The request passed in is the live one; edits a client makes are applied to
// the very same value, so the proxy continues with them.
func (m *Manager) Intercept(ctx context.Context, sessionID uint64, phase Phase, req *msg.Request, resp *msg.Response) (out Intercept, err error) {
	r := m.match(phase, req)
	if r == nil {
		return Intercept{}, nil
	}
	if !r.Update {
		// An observing breakpoint reports the message and continues: it never
		// waits, which is what a "watch this URL" breakpoint is for.
		m.hub.Publish(Event{Kind: EventPaused, Paused: &Paused{
			SessionID: sessionID, Phase: phase, RuleName: r.Name,
			Request: req.Clone(), Response: resp.Clone(), At: time.Now(),
		}})
		return Intercept{Matched: true}, nil
	}

	m.mu.Lock()
	m.next++
	p := &Paused{
		ID:        m.next,
		SessionID: sessionID,
		Phase:     phase,
		RuleName:  r.Name,
		Request:   req,
		Response:  resp,
		At:        time.Now(),
		decision:  make(chan Decision, 1),
	}
	m.byID[p.ID] = p
	m.mu.Unlock()

	m.hub.Publish(Event{Kind: EventPaused, Paused: p})

	timer := time.NewTimer(m.timeout)
	defer timer.Stop()

	var d Decision
	select {
	case d = <-p.decision:
	case <-timer.C:
		m.resolve(p)
		return Intercept{}, ErrTimeout
	case <-ctx.Done():
		m.resolve(p)
		return Intercept{}, ctx.Err()
	}
	// The decision has been made, so the client can no longer edit; the message
	// the proxy continues with is the one a client may have edited up to now,
	// read under the pause's lock so an edit cannot be half-applied.
	m.resolve(p)

	if d == Abort {
		return Intercept{Matched: true, Abort: true, Status: StatusForAbort}, nil
	}
	p.mu.Lock()
	edited, editedResp := p.Request, p.Response
	p.mu.Unlock()
	return Intercept{Matched: true, Request: edited, Response: editedResp}, nil
}

// resolve removes a pause from the maps and publishes its end.
func (m *Manager) resolve(p *Paused) {
	m.mu.Lock()
	delete(m.byID, p.ID)
	m.mu.Unlock()
	m.hub.Publish(Event{Kind: EventResumed, Paused: p})
}

// Intercept is the outcome of a possible breakpoint.
type Intercept struct {
	// Matched reports that a breakpoint rule applied, even if the user let the
	// message through unchanged.
	Matched bool
	// Abort asks the proxy to refuse the message; Status is what the client
	// gets.
	Abort  bool
	Status int
	// Request and Response are the message to continue with.
	Request  *msg.Request
	Response *msg.Response
}

// Resume decides a pause by ID. It returns false when no such pause is
// waiting, or when it was already decided.
func (m *Manager) Resume(id uint64, d Decision) bool {
	m.mu.Lock()
	p, ok := m.byID[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	return p.Send(d)
}

// Enabled reports whether any breakpoint rule is on.
func (m *Manager) Enabled() bool {
	for _, r := range *m.rules.Load() {
		if r.Enabled {
			return true
		}
	}
	return false
}

// PauseCount reports how many messages are waiting.
func (m *Manager) PauseCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byID)
}
