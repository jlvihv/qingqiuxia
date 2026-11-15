package rule

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// Rule is one compiled rule: what it matches, when it applies, and what it
// does. A rule is immutable once built, so the proxy may hold it across
// goroutines without locking.
type Rule struct {
	// ID is the rule's handle in the API and in a session's notes.
	ID uint64
	// Name is what the user called it.
	Name string
	// Enabled takes part in matching only when true.
	Enabled bool
	// Kind is what the rule does.
	Kind Kind
	// Phase is the side it acts on.
	Phase Phase
	// Matcher decides which exchanges it sees.
	Matcher *Matcher

	// actions are the edits a rewrite makes, in order.
	actions []Action
	// mock is the response a mock rule answers with.
	mock *msg.Response
	// abortStatus is the status an abort rule answers with.
	abortStatus int
	// delay applies before the rule's effect.
	delay time.Duration
	// priority orders rules with the same index: higher first.
	Priority int
}

// Spec is a rule as the API and the rules file describe it.
type Spec struct {
	ID       uint64       `json:"id,omitempty"`
	Name     string       `json:"name"`
	Enabled  *bool        `json:"enabled,omitempty"`
	Kind     string       `json:"kind"`
	Phase    string       `json:"phase"`
	Pattern  string       `json:"pattern"`
	Methods  []string     `json:"methods,omitempty"`
	Actions  []ActionSpec `json:"actions,omitempty"`
	Mock     *MockSpec    `json:"mock,omitempty"`
	Priority int          `json:"priority,omitempty"`
	// Delay adds a wait before the rule's effect, as a negative test knob.
	Delay int `json:"delay,omitempty"`
}

// MockSpec is the response a mock rule answers with.
type MockSpec struct {
	Status      int               `json:"status"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
}

// Compile turns a spec into a rule, or explains what is wrong with it.
func Compile(spec Spec, id uint64) (*Rule, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return nil, fmt.Errorf("rule: name is required")
	}
	kind, err := ParseKind(spec.Kind)
	if err != nil {
		return nil, err
	}
	phase, err := ParsePhase(spec.Phase)
	if err != nil {
		return nil, err
	}
	matcher, err := ParseMatcher(spec.Pattern)
	if err != nil {
		return nil, err
	}
	matcher.SetMethods(spec.Methods...)

	r := &Rule{
		ID:       id,
		Name:     spec.Name,
		Enabled:  spec.Enabled == nil || *spec.Enabled,
		Kind:     kind,
		Phase:    phase,
		Matcher:  matcher,
		Priority: spec.Priority,
	}
	if spec.Delay > 0 {
		r.delay = time.Duration(spec.Delay) * time.Millisecond
	}

	switch kind {
	case Rewrite:
		if len(spec.Actions) == 0 {
			return nil, fmt.Errorf("rule %q: a rewrite needs at least one action", spec.Name)
		}
		for i, as := range spec.Actions {
			a, err := compileAction(as)
			if err != nil {
				return nil, fmt.Errorf("rule %q: action %d: %w", spec.Name, i+1, err)
			}
			r.actions = append(r.actions, a)
		}
	case Mock:
		if spec.Mock == nil {
			return nil, fmt.Errorf("rule %q: a mock needs a response", spec.Name)
		}
		if phase != OnRequest {
			return nil, fmt.Errorf("rule %q: a mock must be a request rule", spec.Name)
		}
		resp, err := buildMock(spec.Mock)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", spec.Name, err)
		}
		r.mock = resp
	case Abort:
		if phase != OnRequest {
			return nil, fmt.Errorf("rule %q: an abort must be a request rule", spec.Name)
		}
		r.abortStatus = http.StatusForbidden
	}
	return r, nil
}

// buildMock precomputes the response a mock answers with, so answering is a
// copy rather than an allocation per request.
func buildMock(s *MockSpec) (*msg.Response, error) {
	status := s.Status
	if status == 0 {
		status = http.StatusOK
	}
	if status < 200 || status > 599 {
		return nil, fmt.Errorf("mock: status %d is not a response status", status)
	}
	resp := &msg.Response{
		Status:  status,
		Reason:  http.StatusText(status),
		Message: msg.Message{Header: make(http.Header, len(s.Headers)+2)},
	}
	for k, v := range s.Headers {
		resp.SetHeader(k, v)
	}
	body := s.Body
	if body != "" {
		resp.Body = []byte(body)
		if s.ContentType != "" {
			resp.SetHeader("Content-Type", s.ContentType)
		} else if resp.Header.Get("Content-Type") == "" {
			resp.SetHeader("Content-Type", guessContentType(body))
		}
		resp.SetHeader("Content-Length", fmt.Sprint(len(body)))
	}
	return resp, nil
}

// guessContentType picks a type for a body that looks like JSON or HTML.
func guessContentType(body string) string {
	t := strings.TrimSpace(body)
	if t != "" && (t[0] == '{' || t[0] == '[') {
		return "application/json; charset=utf-8"
	}
	if strings.HasPrefix(strings.ToLower(t), "<!doctype html") || strings.HasPrefix(t, "<html") {
		return "text/html; charset=utf-8"
	}
	return "text/plain; charset=utf-8"
}

// Spec returns the rule as the API describes it, so that a caller can read a
// rule, change one field, and write it back: a list of effects is readable but
// cannot be round-tripped.
func (r *Rule) Spec() Spec {
	s := Spec{
		ID:       r.ID,
		Name:     r.Name,
		Kind:     r.Kind.String(),
		Phase:    r.Phase.String(),
		Pattern:  r.Matcher.String(),
		Priority: r.Priority,
	}
	enabled := r.Enabled
	s.Enabled = &enabled
	if r.delay > 0 {
		s.Delay = int(r.delay / time.Millisecond)
	}
	for _, a := range r.actions {
		if spec, ok := a.spec(); ok {
			s.Actions = append(s.Actions, spec)
		}
	}
	if r.mock != nil {
		s.Mock = &MockSpec{
			Status:      r.mock.Status,
			Headers:     map[string]string{},
			Body:        string(r.mock.Body),
			ContentType: r.mock.Header.Get("Content-Type"),
		}
		for k, v := range r.mock.Header {
			if k == "Content-Type" || k == "Content-Length" {
				continue
			}
			if len(v) > 0 {
				s.Mock.Headers[k] = v[0]
			}
		}
	}
	return s
}

// Describe returns the rule's actions as human-readable text, for the API.
func (r *Rule) Describe() []string {
	if len(r.actions) == 0 {
		switch r.Kind {
		case Mock:
			return []string{fmt.Sprintf("mock %d", r.mock.Status)}
		case Abort:
			return []string{fmt.Sprintf("abort %d", r.abortStatus)}
		}
		return nil
	}
	out := make([]string, len(r.actions))
	for i, a := range r.actions {
		out[i] = a.describe()
	}
	return out
}

// Engine holds the rules and matches them against traffic. Rules are replaced
// wholesale when the set changes, which keeps readers lock-free: a request
// takes one atomic pointer and works with an immutable slice.
type Engine struct {
	current atomic.Pointer[ruleSet]
	mu      sync.Mutex // serialises writers
	nextID  uint64
}

// ruleSet is an immutable, ordered snapshot of the rules.
type ruleSet struct {
	rules []*Rule
	// byID indexes rules for updates and deletes.
	byID map[uint64]*Rule
}

// NewEngine returns an engine with no rules.
func NewEngine() *Engine {
	e := &Engine{}
	e.current.Store(&ruleSet{})
	return e
}

// Rules returns the current rules in match order.
func (e *Engine) Rules() []*Rule {
	rs := e.current.Load()
	if len(rs.rules) == 0 {
		return nil
	}
	return append([]*Rule(nil), rs.rules...)
}

// Len reports how many rules are configured.
func (e *Engine) Len() int { return len(e.current.Load().rules) }

// Replace swaps the whole rule set, assigning IDs to specs that have none and
// compiling everything before publishing, so a bad rule leaves the old set in
// place.
func (e *Engine) Replace(specs []Spec) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	rs := &ruleSet{
		rules: make([]*Rule, 0, len(specs)),
		byID:  make(map[uint64]*Rule, len(specs)),
	}
	next := e.nextID
	for _, spec := range specs {
		id := spec.ID
		if id == 0 {
			next++
			id = next
		} else if id > next {
			next = id
		}
		if _, dup := rs.byID[id]; dup {
			return fmt.Errorf("rule: duplicate id %d", id)
		}
		r, err := Compile(spec, id)
		if err != nil {
			return err
		}
		rs.rules = append(rs.rules, r)
		rs.byID[id] = r
	}
	sortRules(rs.rules)
	e.nextID = next
	e.current.Store(rs)
	return nil
}

// Add appends one rule, returning it with its assigned ID.
func (e *Engine) Add(spec Spec) (*Rule, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	next := e.nextID + 1
	if spec.ID > next {
		next = spec.ID
	}
	r, err := Compile(spec, next)
	if err != nil {
		return nil, err
	}
	old := e.current.Load()
	rs := &ruleSet{
		rules: make([]*Rule, 0, len(old.rules)+1),
		byID:  make(map[uint64]*Rule, len(old.rules)+1),
	}
	rs.rules = append(rs.rules, old.rules...)
	for k, v := range old.byID {
		rs.byID[k] = v
	}
	rs.rules = append(rs.rules, r)
	rs.byID[r.ID] = r
	sortRules(rs.rules)
	e.nextID = next
	e.current.Store(rs)
	return r, nil
}

// Update replaces the rule with spec.ID, keeping its position.
func (e *Engine) Update(spec Spec) (*Rule, error) {
	if spec.ID == 0 {
		return nil, fmt.Errorf("rule: update needs an id")
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	r, err := Compile(spec, spec.ID)
	if err != nil {
		return nil, err
	}
	old := e.current.Load()
	if _, ok := old.byID[spec.ID]; !ok {
		return nil, fmt.Errorf("rule: no rule with id %d", spec.ID)
	}
	rs := &ruleSet{
		rules: make([]*Rule, 0, len(old.rules)),
		byID:  make(map[uint64]*Rule, len(old.byID)),
	}
	for _, existing := range old.rules {
		if existing.ID == spec.ID {
			rs.rules = append(rs.rules, r)
		} else {
			rs.rules = append(rs.rules, existing)
		}
		rs.byID[existing.ID] = rs.rules[len(rs.rules)-1]
	}
	sortRules(rs.rules)
	e.current.Store(rs)
	return r, nil
}

// SetEnabled turns one rule on or off.
func (e *Engine) SetEnabled(id uint64, enabled bool) (*Rule, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	old := e.current.Load()
	target, ok := old.byID[id]
	if !ok {
		return nil, fmt.Errorf("rule: no rule with id %d", id)
	}
	// A disabled or enabled copy keeps every compiled part, so nothing is
	// re-parsed.
	replacement := *target
	replacement.Enabled = enabled

	rs := &ruleSet{
		rules: make([]*Rule, 0, len(old.rules)),
		byID:  make(map[uint64]*Rule, len(old.byID)),
	}
	for _, existing := range old.rules {
		if existing.ID == id {
			existing = &replacement
		}
		rs.rules = append(rs.rules, existing)
		rs.byID[existing.ID] = existing
	}
	e.current.Store(rs)
	return &replacement, nil
}

// Delete removes one rule.
func (e *Engine) Delete(id uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	old := e.current.Load()
	if _, ok := old.byID[id]; !ok {
		return fmt.Errorf("rule: no rule with id %d", id)
	}
	rs := &ruleSet{
		rules: make([]*Rule, 0, len(old.rules)),
		byID:  make(map[uint64]*Rule, len(old.byID)),
	}
	for _, existing := range old.rules {
		if existing.ID == id {
			continue
		}
		rs.rules = append(rs.rules, existing)
		rs.byID[existing.ID] = existing
	}
	e.current.Store(rs)
	return nil
}

// sortRules orders rules by priority, highest first, then by ID so that the
// order is stable and a user's ordering is respected.
func sortRules(rules []*Rule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority > rules[j].Priority
		}
		return rules[i].ID < rules[j].ID
	})
}
