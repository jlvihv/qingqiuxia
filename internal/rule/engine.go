package rule

import (
	"fmt"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// Outcome is what the rules decided about one exchange.
type Outcome struct {
	// Mock answers the request instead of the origin, when set.
	Mock *msg.Response
	// Abort stops the request; Status is what the client gets.
	Abort  bool
	Status int
	// Delay is how long to wait before the exchange continues.
	Delay time.Duration
	// Applied lists the rules that changed something, for the session's notes.
	Applied []Applied
}

// Applied is one rule that acted on an exchange.
type Applied struct {
	RuleID uint64
	Name   string
	Kind   string
	// Effects describes what changed.
	Effects []string
}

// Request runs every matching request rule against req in order, and returns
// what the pipeline should do with it. req is edited in place.
func (e *Engine) Request(req *msg.Request) (Outcome, error) {
	rs := e.current.Load()
	var out Outcome
	if len(rs.rules) == 0 {
		return out, nil
	}
	var effects []string
	for _, r := range rs.rules {
		if !r.Enabled || r.Phase != OnRequest {
			continue
		}
		if !r.Matcher.Matches(req.URL.Scheme, req.URL.Host, req.URL.Path, req.Method) {
			continue
		}
		if r.delay > 0 && out.Delay < r.delay {
			out.Delay = r.delay
		}
		switch r.Kind {
		case Mock:
			// A mock answers the request, so nothing after it can act on the
			// request any more. The first mock wins, which makes the order of
			// rules the order of precedence.
			if out.Mock == nil && !out.Abort {
				out.Mock = r.mock.Clone()
				out.Applied = append(out.Applied, Applied{
					RuleID: r.ID, Name: r.Name, Kind: r.Kind.String(),
					Effects: r.Describe(),
				})
			}
			return out, nil
		case Abort:
			if !out.Abort {
				out.Abort = true
				out.Status = r.abortStatus
				out.Applied = append(out.Applied, Applied{
					RuleID: r.ID, Name: r.Name, Kind: r.Kind.String(),
					Effects: r.Describe(),
				})
			}
			return out, nil
		case Rewrite:
			effects = effects[:0]
			for _, a := range r.actions {
				changed, err := a.apply(req, nil)
				if err != nil {
					return out, fmt.Errorf("rule %q: %w", r.Name, err)
				}
				if changed {
					effects = append(effects, a.describe())
				}
			}
			if len(effects) > 0 {
				out.Applied = append(out.Applied, Applied{
					RuleID: r.ID, Name: r.Name, Kind: r.Kind.String(),
					Effects: append([]string(nil), effects...),
				})
			}
		}
	}
	return out, nil
}

// Response runs every matching response rule against resp in order. resp is
// edited in place.
func (e *Engine) Response(req *msg.Request, resp *msg.Response) ([]Applied, error) {
	rs := e.current.Load()
	if len(rs.rules) == 0 {
		return nil, nil
	}
	var applied []Applied
	var effects []string
	for _, r := range rs.rules {
		if !r.Enabled || r.Phase != OnResponse || r.Kind != Rewrite {
			continue
		}
		if !r.Matcher.Matches(req.URL.Scheme, req.URL.Host, req.URL.Path, req.Method) {
			continue
		}
		effects = effects[:0]
		for _, a := range r.actions {
			changed, err := a.apply(nil, resp)
			if err != nil {
				return applied, fmt.Errorf("rule %q: %w", r.Name, err)
			}
			if changed {
				effects = append(effects, a.describe())
			}
		}
		if len(effects) > 0 {
			applied = append(applied, Applied{
				RuleID: r.ID, Name: r.Name, Kind: r.Kind.String(),
				Effects: append([]string(nil), effects...),
			})
		}
	}
	return applied, nil
}

// Matches reports whether any enabled rule in the phase applies to the
// exchange, so the proxy can skip work that no rule will touch.
func (e *Engine) Matches(phase Phase, req *msg.Request) bool {
	rs := e.current.Load()
	if len(rs.rules) == 0 {
		return false
	}
	for _, r := range rs.rules {
		if r.Enabled && r.Phase == phase &&
			r.Matcher.Matches(req.URL.Scheme, req.URL.Host, req.URL.Path, req.Method) {
			return true
		}
	}
	return false
}
