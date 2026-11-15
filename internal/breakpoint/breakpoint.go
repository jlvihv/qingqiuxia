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
type Paused struct {
	// ID identifies the pause in the API.
	ID uint64
	// SessionID is the traffic record the pause belongs to.
	SessionID uint64
	// Phase is the side that stopped.
	Phase Phase
	// RuleName is the breakpoint rule that matched.
	RuleName string
	// Request is the request as it stands; for a response breakpoint it is the
	// request the response belongs to.
	Request *msg.Request
	// Response is set for a response breakpoint.
	Response *msg.Response
	// At is when it stopped.
	At time.Time

	// decided wakes the waiting proxy goroutine exactly once.
	decideOnce sync.Once
	decision   chan Decision
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

// Manager holds the breakpoint rules and the paused messages.
type Manager struct {
	rules atomic.Pointer[[]*breakpointRule]
	mu    sync.Mutex // guards the maps below
	// byID indexes pauses by their own ID.
	byID map[uint64]*Paused
	// bySession indexes pauses by the session they belong to, so the API can
	// find the pause for a row the user clicked.
	bySession map[uint64]*Paused
	next      uint64
	hub       *events.Hub[Event]
	// timeout bounds how long a message may wait, so a forgotten breakpoint
	// cannot hang a client for ever.
	timeout time.Duration
}

// Event is a pause or resume, published for the API's live view.
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
		byID:      make(map[uint64]*Paused, 8),
		bySession: make(map[uint64]*Paused, 8),
		hub:       events.New[Event](64),
		timeout:   timeout,
	}
	empty := []*breakpointRule(nil)
	m.rules.Store(&empty)
	return m
}

// DefaultTimeout is how long a paused message waits for a decision.
const DefaultTimeout = 5 * time.Minute

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
	m.mu.Lock()
	next := m.next
	m.mu.Unlock()

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
			return fmt.Errorf("breakpoint: duplicate id %d", id)
		}
		seen[id] = true
		r, err := compile(spec, id)
		if err != nil {
			return err
		}
		rules = append(rules, r)
	}

	m.mu.Lock()
	m.next = next
	m.mu.Unlock()
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
func (m *Manager) Pending() []*Paused {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Paused, 0, len(m.byID))
	for _, p := range m.byID {
		out = append(out, p)
	}
	// The map has no order, so sort by pause ID, which is the order they
	// stopped in.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].ID > out[j].ID; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
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
	m.bySession[sessionID] = p
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
	m.resolve(p)

	if d == Abort {
		return Intercept{Matched: true, Abort: true, Status: 403}, nil
	}
	return Intercept{Matched: true, Request: p.Request, Response: p.Response}, nil
}

// resolve removes a pause from the maps and publishes its end.
func (m *Manager) resolve(p *Paused) {
	m.mu.Lock()
	delete(m.byID, p.ID)
	delete(m.bySession, p.SessionID)
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

// ResumeSession decides the pause of a session, for a client that works with
// traffic rows rather than pause IDs.
func (m *Manager) ResumeSession(sessionID uint64, d Decision) bool {
	m.mu.Lock()
	p, ok := m.bySession[sessionID]
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

// PendingFor reports the pause of a session, if it is waiting.
func (m *Manager) PendingFor(sessionID uint64) (*Paused, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.bySession[sessionID]
	return p, ok
}

// InterceptPhase reports whether traffic in this phase could be paused, so the
// proxy can skip the check.
func (m *Manager) InterceptPhase(phase Phase) bool {
	for _, r := range *m.rules.Load() {
		if r.Enabled && r.Phase == phase && r.Update {
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
